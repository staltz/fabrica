package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchCheckout is a private checkout of one branch of a remote, for files
// that only Fabrica writes, like the Agent SDK projects it deploys from (the
// Agent SDK only deploys code that's pushed to git). It's not a Worktree:
// nobody works in it, so Reset throws away whatever is in it and starts
// again from the remote.
type BranchCheckout struct {
	Dir    string
	URL    string
	Branch string
}

// OpenBranch returns a checkout of branch on the remote at url, in dir,
// making the folder a git repo first if it isn't one yet. Call Reset before
// writing to it.
func OpenBranch(ctx context.Context, dir, url, branch string) (*BranchCheckout, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := &BranchCheckout{Dir: dir, URL: url, Branch: branch}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := run(ctx, dir, "init", "--quiet"); err != nil {
			return nil, err
		}
		if _, err := run(ctx, dir, "remote", "add", "origin", url); err != nil {
			return nil, err
		}
	} else if _, err := run(ctx, dir, "remote", "set-url", "origin", url); err != nil {
		return nil, err
	}
	return b, nil
}

// Reset makes the checkout exactly the branch as it is on the remote, or an
// empty branch with no commits when the remote doesn't have it yet. It
// reports whether the remote has it.
func (b *BranchCheckout) Reset(ctx context.Context) (bool, error) {
	ref := "refs/heads/" + b.Branch
	_, err := run(ctx, b.Dir, "ls-remote", "--exit-code", "--heads", "origin", ref)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		if _, err := run(ctx, b.Dir, "fetch", "--quiet", "--no-tags", "origin", "+"+ref+":refs/remotes/origin/"+b.Branch); err != nil {
			return false, fmt.Errorf("couldn't fetch branch %s of %s: %w", b.Branch, b.URL, err)
		}
		if _, err := run(ctx, b.Dir, "checkout", "--quiet", "--force", "-B", b.Branch, "refs/remotes/origin/"+b.Branch); err != nil {
			return false, err
		}
		_, err = run(ctx, b.Dir, "clean", "--quiet", "-ffdx")
		return true, err
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 2:
		// The remote is there, without the branch: start it from nothing.
		if _, err := run(ctx, b.Dir, "symbolic-ref", "HEAD", ref); err != nil {
			return false, err
		}
		_, _ = run(ctx, b.Dir, "update-ref", "-d", ref)
		_, _ = run(ctx, b.Dir, "rm", "-r", "--quiet", "--cached", "--ignore-unmatch", ".")
		_, err = run(ctx, b.Dir, "clean", "--quiet", "-ffdx")
		return false, err
	default:
		return false, fmt.Errorf("couldn't read %s: %w", b.URL, err)
	}
}

// Commit commits every change in the checkout and returns the commit HEAD
// is at afterwards. made is false when nothing changed.
func (b *BranchCheckout) Commit(ctx context.Context, message string) (commit string, made bool, err error) {
	if _, err := run(ctx, b.Dir, "add", "--all"); err != nil {
		return "", false, err
	}
	if _, err := run(ctx, b.Dir, "diff", "--cached", "--quiet"); err != nil {
		args := Worktree{Dir: b.Dir}.identityArgs(ctx)
		args = append(args, "commit", "--quiet", "--no-verify", "-m", message)
		if _, err := run(ctx, b.Dir, args...); err != nil {
			return "", false, err
		}
		made = true
	}
	out, err := run(ctx, b.Dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", made, nil // an empty branch with nothing ever committed
	}
	return trimOut(out), made, nil
}

// Push pushes the branch. It returns ErrPushRejected when the remote has
// commits the checkout doesn't, so the caller can Reset and try again.
func (b *BranchCheckout) Push(ctx context.Context) error {
	out, err := run(ctx, b.Dir, "push", "--porcelain", "origin", "HEAD:refs/heads/"+b.Branch)
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
	return fmt.Errorf("couldn't push branch %s to %s: %w", b.Branch, b.URL, err)
}
