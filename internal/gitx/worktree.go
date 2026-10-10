package gitx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrPushRejected means the remote refused our push because someone else
// pushed first. Fabrica's apply lock is built on exactly this: git only
// accepts a push on top of the latest commit, so when two applies race,
// one of them gets this error.
var ErrPushRejected = errors.New("the push was rejected because the remote has newer commits")

// Worktree is a normal checked-out git repo, like your home repo. It's the
// only place Fabrica ever commits to.
type Worktree struct {
	Dir string
}

// OpenWorktree checks that dir is inside a git repo and returns a handle for
// the repo's top folder.
func OpenWorktree(ctx context.Context, dir string) (Worktree, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Worktree{}, fmt.Errorf("%s isn't inside a git repo: %w", dir, err)
	}
	top := trimOut(out)
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	return Worktree{Dir: top}, nil
}

// Head returns the commit HEAD points at.
func (w Worktree) Head(ctx context.Context) (string, error) {
	out, err := run(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return trimOut(out), nil
}

// RemoteURL returns the URL of the named remote, or "" if there's none.
func (w Worktree) RemoteURL(ctx context.Context, remote string) string {
	out, err := run(ctx, w.Dir, "remote", "get-url", remote)
	if err != nil {
		return ""
	}
	return trimOut(out)
}

// Remotes lists the names of the repo's remotes.
func (w Worktree) Remotes(ctx context.Context) []string {
	out, err := run(ctx, w.Dir, "remote")
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// Upstream returns the remote and branch the current branch tracks, like
// ("origin", "main"). Cloud applies need one: the apply lock only works if
// there's a shared remote to push to.
func (w Worktree) Upstream(ctx context.Context) (remote, branch string, err error) {
	cur, err := run(ctx, w.Dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("the home repo at %s isn't on a branch (detached HEAD); check out a branch first", w.Dir)
	}
	local := trimOut(cur)
	remoteOut, err1 := run(ctx, w.Dir, "config", "branch."+local+".remote")
	mergeOut, err2 := run(ctx, w.Dir, "config", "branch."+local+".merge")
	if err1 != nil || err2 != nil {
		return "", "", fmt.Errorf("branch %q of the home repo at %s doesn't track a remote branch; push it with `git push -u` first", local, w.Dir)
	}
	return trimOut(remoteOut), strings.TrimPrefix(trimOut(mergeOut), "refs/heads/"), nil
}

// DirtyPaths lists files under the given paths that have uncommitted
// changes (including new files).
func (w Worktree) DirtyPaths(ctx context.Context, paths ...string) ([]string, error) {
	args := append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, paths...)
	out, err := run(ctx, w.Dir, args...)
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) > 3 {
			dirty = append(dirty, strings.TrimSpace(line[3:]))
		}
	}
	return dirty, nil
}

// Sync fetches the upstream branch and fast-forwards to it, so we work
// against the latest home repo. It refuses when there are local commits
// that haven't been pushed: Fabrica applies what's on the shared remote,
// never what's only on your machine.
func (w Worktree) Sync(ctx context.Context) error {
	remote, branch, err := w.Upstream(ctx)
	if err != nil {
		return err
	}
	if _, err := run(ctx, w.Dir, "fetch", "--quiet", remote, branch); err != nil {
		return fmt.Errorf("couldn't fetch the home repo: %w", err)
	}
	upstream := remote + "/" + branch
	out, err := run(ctx, w.Dir, "rev-list", "--count", upstream+"..HEAD")
	if err != nil {
		return err
	}
	if n, _ := strconv.Atoi(trimOut(out)); n > 0 {
		return fmt.Errorf("the home repo has %d local commit(s) that aren't pushed to %s yet; push them (or open a PR and merge it) first, since Fabrica only applies what's on the remote", n, upstream)
	}
	if _, err := run(ctx, w.Dir, "merge", "--quiet", "--ff-only", upstream); err != nil {
		return fmt.Errorf("couldn't fast-forward the home repo to %s: %w", upstream, err)
	}
	return nil
}

// PullRebase fetches the upstream branch and replays our local commits on
// top of it. We use it after a rejected push of a state commit, when the
// newer remote commits are unrelated (for example, another deployment's
// state file).
func (w Worktree) PullRebase(ctx context.Context) error {
	remote, branch, err := w.Upstream(ctx)
	if err != nil {
		return err
	}
	if _, err := run(ctx, w.Dir, "pull", "--rebase", "--quiet", remote, branch); err != nil {
		_, _ = run(ctx, w.Dir, "rebase", "--abort")
		return fmt.Errorf("couldn't rebase onto the latest home repo: %w", err)
	}
	return nil
}

// DropLastCommit undoes the most recent commit, putting the files it
// touched back the way they were. We only use it to take back our own lock
// commit when the push that would have taken the lock was rejected. Unlike
// a hard reset it never touches other uncommitted work.
func (w Worktree) DropLastCommit(ctx context.Context) error {
	_, err := run(ctx, w.Dir, "reset", "--quiet", "--keep", "HEAD~1")
	return err
}

// Commit stages and commits the given paths with the message. It returns
// false (and makes no commit) when nothing in those paths changed. Other
// changes in the repo are left alone.
func (w Worktree) Commit(ctx context.Context, message string, paths ...string) (bool, error) {
	addArgs := append([]string{"add", "--all", "--"}, paths...)
	if _, err := run(ctx, w.Dir, addArgs...); err != nil {
		return false, err
	}
	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	if _, err := run(ctx, w.Dir, diffArgs...); err == nil {
		return false, nil // nothing staged
	}
	args := w.identityArgs(ctx)
	args = append(args, "commit", "--quiet", "--no-verify", "-m", message, "--")
	args = append(args, paths...)
	if _, err := run(ctx, w.Dir, args...); err != nil {
		return false, err
	}
	return true, nil
}

// identityArgs supplies a committer name and email when git doesn't have
// one configured, which is common on fresh CI machines.
func (w Worktree) identityArgs(ctx context.Context) []string {
	if out, err := run(ctx, w.Dir, "config", "user.email"); err == nil && trimOut(out) != "" {
		return nil
	}
	return []string{"-c", "user.name=Fabrica", "-c", "user.email=fabrica@localhost"}
}

// Push pushes the current branch to its upstream. It returns
// ErrPushRejected when the remote has commits we don't have yet.
func (w Worktree) Push(ctx context.Context) error {
	remote, branch, err := w.Upstream(ctx)
	if err != nil {
		return err
	}
	out, err := run(ctx, w.Dir, "push", "--porcelain", remote, "HEAD:refs/heads/"+branch)
	if err == nil {
		return nil
	}
	var gitErr *Error
	if errors.As(err, &gitErr) {
		text := string(out) + gitErr.Stderr
		if strings.Contains(text, "[rejected]") || strings.Contains(text, "non-fast-forward") ||
			strings.Contains(text, "fetch first") {
			return ErrPushRejected
		}
	}
	return fmt.Errorf("couldn't push the home repo: %w", err)
}
