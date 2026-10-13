package auth

import (
	"context"
	"io"
	"os/exec"
)

// GitHubCLI is the GitHub CLI, gh. It can sign you in to GitHub in your
// browser, and let git use that sign-in, which is the easiest way to give
// git access to private repos on github.com. Fabrica doesn't keep git
// credentials itself: git and gh do.
type GitHubCLI struct {
	Path string
}

// FindGitHubCLI returns the GitHub CLI on your PATH, or nil.
func FindGitHubCLI() *GitHubCLI {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil
	}
	return &GitHubCLI{Path: path}
}

// SignedIn reports whether gh is signed in to github.com.
func (g *GitHubCLI) SignedIn(ctx context.Context) bool {
	return exec.CommandContext(ctx, g.Path, "auth", "status", "--hostname", "github.com").Run() == nil
}

// Login signs in to github.com in your browser. It's interactive, so it
// talks to you directly.
func (g *GitHubCLI) Login(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	return g.run(ctx, in, out, errOut, "auth", "login", "--hostname", "github.com", "--web", "--git-protocol", "https")
}

// SetupGit makes git use gh's sign-in for github.com, by adding gh as a
// credential helper in your global git config.
func (g *GitHubCLI) SetupGit(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	return g.run(ctx, in, out, errOut, "auth", "setup-git", "--hostname", "github.com")
}

func (g *GitHubCLI) run(ctx context.Context, in io.Reader, out, errOut io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, g.Path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
	return cmd.Run()
}
