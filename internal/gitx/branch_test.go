package gitx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/testkit"
)

func TestBranchCheckoutStartsBranchAndPushes(t *testing.T) {
	host := testkit.NewHost(t)
	home := host.Home("github.com/acme/home")
	url := "https://github.com/acme/home.git"
	ctx := context.Background()

	b, err := gitx.OpenBranch(ctx, t.TempDir(), url, "fabrica/agents")
	if err != nil {
		t.Fatal(err)
	}
	exists, err := b.Reset(ctx)
	if err != nil || exists {
		t.Fatalf("Reset on a new branch = %v, %v; want false, nil", exists, err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "README.md")); err == nil {
		t.Fatal("a new branch should start empty, not from the default branch")
	}
	testkit.WriteFiles(t, b.Dir, map[string]string{"a/one.txt": "one"})
	commit, made, err := b.Commit(ctx, "add one")
	if err != nil || !made || commit == "" {
		t.Fatalf("Commit = %q, %v, %v", commit, made, err)
	}
	if err := b.Push(ctx); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(testkit.Git(t, home.Bare, "rev-parse", "refs/heads/fabrica/agents"))
	if got != commit {
		t.Errorf("the remote branch is at %s, want %s", got, commit)
	}
	// The default branch is untouched.
	if log := testkit.Git(t, home.Bare, "log", "--format=%s", "main"); strings.TrimSpace(log) != "init" {
		t.Errorf("main changed: %q", log)
	}

	// Nothing changed: no commit, same HEAD.
	again, made, err := b.Commit(ctx, "nothing")
	if err != nil || made || again != commit {
		t.Errorf("Commit with no changes = %q, %v, %v", again, made, err)
	}
}

func TestBranchCheckoutResetFollowsTheRemoteAndPushRejects(t *testing.T) {
	host := testkit.NewHost(t)
	host.Home("github.com/acme/home")
	url := "https://github.com/acme/home.git"
	ctx := context.Background()

	first, _ := gitx.OpenBranch(ctx, t.TempDir(), url, "agents")
	second, _ := gitx.OpenBranch(ctx, t.TempDir(), url, "agents")
	if _, err := first.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	testkit.WriteFiles(t, first.Dir, map[string]string{"first.txt": "1"})
	if _, _, err := first.Commit(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := first.Push(ctx); err != nil {
		t.Fatal(err)
	}

	testkit.WriteFiles(t, second.Dir, map[string]string{"second.txt": "2"})
	if _, _, err := second.Commit(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if err := second.Push(ctx); !errors.Is(err, gitx.ErrPushRejected) {
		t.Fatalf("pushing behind the remote = %v, want ErrPushRejected", err)
	}

	// Reset throws the local commit away and picks up the remote's.
	exists, err := second.Reset(ctx)
	if err != nil || !exists {
		t.Fatalf("Reset = %v, %v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(second.Dir, "second.txt")); err == nil {
		t.Error("Reset kept a file that was never pushed")
	}
	if data, err := os.ReadFile(filepath.Join(second.Dir, "first.txt")); err != nil || string(data) != "1" {
		t.Errorf("Reset didn't bring in the remote's file: %q, %v", data, err)
	}
	testkit.WriteFiles(t, second.Dir, map[string]string{"second.txt": "2"})
	if _, _, err := second.Commit(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if err := second.Push(ctx); err != nil {
		t.Fatal(err)
	}
}
