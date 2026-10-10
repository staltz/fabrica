// Package gitx is Fabrica's thin wrapper around the `git` command.
//
// Fabrica sits beside git and never wraps it for you, but it still needs git
// to do its job: to read factories at exact commits, to read product repos'
// default branches, and to commit and push state in your home repo. Instead
// of re-implementing git, we run the real `git` binary. That way your
// credential helpers, SSH keys and `url.<base>.insteadOf` rewrites all just
// work.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Error is what a failed git command returns. It keeps git's own stderr,
// because that's usually where the useful explanation is.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// run runs git in dir and returns its stdout.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runInput(ctx, dir, nil, args...)
}

func runInput(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("Fabrica needs the git command, but it isn't installed or isn't on your PATH")
		}
		// Hand back stdout too: some commands (like `git push --porcelain`)
		// explain what went wrong there rather than on stderr.
		return stdout.Bytes(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// gitEnv is the environment for git commands. We switch off interactive
// password prompts: Fabrica often runs where nobody can type a password (CI,
// or as an MCP server inside an editor), and a prompt there would just hang
// forever. Set up a credential helper or SSH keys instead.
func gitEnv() []string {
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return env
}

func trimOut(b []byte) string { return strings.TrimSpace(string(b)) }

// IsCommitish reports whether s looks like a commit hash (7 to 40 hex
// characters). Refs like `v1.2` or `main` don't.
func IsCommitish(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Short returns the first 7 characters of a commit hash, the way git
// usually prints them.
func Short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
