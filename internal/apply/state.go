// Package apply carries out plans: `fabrica apply`, `fabrica confirm` and
// `fabrica unlock`.
//
// Cloud deployments keep their state in the home repo, and every write to
// a state file goes through git. Git only accepts a push on top of the
// latest commit, so a push works like a compare-and-swap: when two people
// (or CI jobs) race, exactly one push gets through, and the other one pulls,
// looks again, and either retries or stops. That's the whole locking
// scheme, with no server needed.
package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
)

// maxAttempts bounds how often we retry after losing a push race.
const maxAttempts = 5

// ErrLocked means another apply holds a deployment's lock.
type ErrLocked struct {
	Deployment string
	Lock       home.Lock
}

func (e *ErrLocked) Error() string {
	return fmt.Sprintf("deployment %s is locked by %q since %s; if that apply crashed, run `fabrica unlock %s`",
		e.Deployment, e.Lock.Holder, e.Lock.Since.Format(time.RFC3339), e.Deployment)
}

// Repo is a home repo checkout that Fabrica commits state to.
type Repo struct {
	Dir string
	wt  gitx.Worktree
}

// OpenRepo checks that dir is a home repo Fabrica can safely commit to: a
// git checkout on a branch that tracks a remote, with no uncommitted
// changes to deployments/ or state/.
func OpenRepo(ctx context.Context, dir string) (*Repo, error) {
	wt, err := gitx.OpenWorktree(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("cloud deployments keep their state in git, but %w", err)
	}
	if _, _, err := wt.Upstream(ctx); err != nil {
		return nil, err
	}
	dirty, err := wt.DirtyPaths(ctx, home.DeploymentsDir, home.StateDir)
	if err != nil {
		return nil, err
	}
	if len(dirty) > 0 {
		return nil, fmt.Errorf("the home repo has uncommitted changes in %s; commit and push them (or stash them) first, because Fabrica applies what's on the remote", strings.Join(dirty, ", "))
	}
	return &Repo{Dir: wt.Dir, wt: wt}, nil
}

// Sync brings the checkout up to date with the remote.
func (r *Repo) Sync(ctx context.Context) error { return r.wt.Sync(ctx) }

// Update changes one deployment's state in a single commit, safely:
//
//  1. bring the checkout up to date,
//  2. read the state and let mutate change it,
//  3. commit and push.
//
// If the push loses a race, the commit is taken back and everything runs
// again on top of the newer remote, up to a few times. mutate must be safe
// to call more than once. When it returns errNoChange, nothing is written.
func (r *Repo) Update(ctx context.Context, name, message string, mutate func(*home.State) error) error {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := r.wt.Sync(ctx); err != nil {
			return err
		}
		state, err := home.ReadState(r.Dir, name)
		if err != nil {
			return err
		}
		if err := mutate(state); err != nil {
			if errors.Is(err, errNoChange) {
				return nil
			}
			return err
		}
		if err := r.write(name, state); err != nil {
			return err
		}
		made, err := r.wt.Commit(ctx, message, home.StatePath(name))
		if err != nil {
			return err
		}
		if !made {
			return nil
		}
		err = r.wt.Push(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, gitx.ErrPushRejected) {
			_ = r.wt.DropLastCommit(ctx)
			return err
		}
		if err := r.wt.DropLastCommit(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("gave up writing %s after %d attempts: the home repo keeps changing under us", home.StatePath(name), maxAttempts)
}

// Release writes the final state (with the lock removed) while we hold the
// lock. Nobody else writes this file while we hold it, so if the push is
// rejected the newer commits must be unrelated (another deployment's
// state, say), and rebasing on top of them is safe.
func (r *Repo) Release(ctx context.Context, name, message string, state *home.State) error {
	state.Lock = nil
	if err := r.write(name, state); err != nil {
		return err
	}
	if _, err := r.wt.Commit(ctx, message, home.StatePath(name)); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = r.wt.Push(ctx)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, gitx.ErrPushRejected) {
			break
		}
		if err := r.wt.PullRebase(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("couldn't push the final state of %s (the lock is still held; fix the problem, then run `fabrica unlock %s`): %w", name, name, lastErr)
}

// write saves a state file, or removes it when a deleted deployment has
// nothing left.
func (r *Repo) write(name string, state *home.State) error {
	if state.Empty() && !deploymentExists(r.Dir, name) {
		err := os.Remove(filepath.Join(r.Dir, home.StatePath(name)))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return home.WriteState(r.Dir, name, state)
}

func deploymentExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, home.DeploymentsDir, name+".yaml"))
	return err == nil
}

var errNoChange = errors.New("no change")

// TakeLock takes a deployment's lock by committing a lock entry to its
// state file and pushing. Losing the race is always safe: nothing remote
// has been touched yet.
func (r *Repo) TakeLock(ctx context.Context, name, holder string, now time.Time) error {
	return r.Update(ctx, name, "fabrica: lock "+name+" for apply", func(s *home.State) error {
		if s.Lock != nil {
			return &ErrLocked{Deployment: name, Lock: *s.Lock}
		}
		s.Lock = &home.Lock{Holder: holder, Since: now.UTC().Truncate(time.Second)}
		return nil
	})
}

// Unlock removes a lock left behind by a crashed apply.
func Unlock(ctx context.Context, homeDir, name string) (*home.Lock, error) {
	r, err := OpenRepo(ctx, homeDir)
	if err != nil {
		return nil, err
	}
	var removed *home.Lock
	err = r.Update(ctx, name, "fabrica: unlock "+name, func(s *home.State) error {
		if s.Lock == nil {
			removed = nil
			return errNoChange
		}
		lock := *s.Lock
		removed = &lock
		s.Lock = nil
		return nil
	})
	return removed, err
}

// Holder describes who's applying, for the lock entry. In GitHub Actions
// it's a link to the run; elsewhere it's user@host.
func Holder() string {
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("GITHUB_RUN_ID") != "" {
		server := strings.TrimPrefix(strings.TrimPrefix(os.Getenv("GITHUB_SERVER_URL"), "https://"), "http://")
		return fmt.Sprintf("ci %s/%s/actions/runs/%s", server, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"))
	}
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	who := fmt.Sprintf("%s@%s (pid %d)", user, host, os.Getpid())
	if os.Getenv("CI") != "" {
		who = "ci " + who
	}
	return who
}
