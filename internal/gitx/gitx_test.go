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

func TestResolveBranchTagAndCommit(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/blueprint")
	first := repo.Write(map[string]string{"a.txt": "one"}).Commit("first")
	repo.Tag("v1")
	second := repo.Write(map[string]string{"a.txt": "two"}).Commit("second")

	ctx := context.Background()
	cache := gitx.NewCache(t.TempDir())
	remote := cache.Remote(repo.URL(), gitx.Full)

	got, err := remote.Resolve(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != gitx.Branch || got.Commit != second || !got.Kind.Follows() {
		t.Errorf("main resolved to %+v, want branch at %s", got, second)
	}

	got, err = remote.Resolve(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != gitx.Tag || got.Commit != first || got.Kind.Follows() {
		t.Errorf("v1 resolved to %+v, want tag at %s", got, first)
	}

	got, err = remote.Resolve(ctx, first[:9])
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != gitx.Commit || got.Commit != first {
		t.Errorf("short hash resolved to %+v, want commit %s", got, first)
	}

	if _, err := remote.Resolve(ctx, "nope"); err == nil {
		t.Error("resolving a missing ref should fail")
	}
}

func TestResolveRefusesAmbiguousRef(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/blueprint")
	repo.Write(map[string]string{"a.txt": "one"}).Commit("first")
	repo.Tag("stable").Branch("stable")

	remote := gitx.NewCache(t.TempDir()).Remote(repo.URL(), gitx.Full)
	_, err := remote.Resolve(context.Background(), "stable")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}
}

func TestTreeKeepsModesAndIsReadOnly(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/blueprint")
	commit := repo.Write(map[string]string{
		"skills/pdf/SKILL.md":             "---\nname: pdf\n---\n",
		"skills/pdf/scripts/extract.sh!x": "#!/bin/sh\necho hi\n",
	}).Commit("skills")

	ctx := context.Background()
	remote := gitx.NewCache(t.TempDir()).Remote(repo.URL(), gitx.Full)
	dir, err := remote.Tree(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "skills/pdf/scripts/extract.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("script lost its executable bit: %v", info.Mode())
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf("cached files should be read-only, got %v", info.Mode())
	}
	// A second call reuses the same folder.
	again, err := remote.Tree(ctx, commit)
	if err != nil || again != dir {
		t.Errorf("second Tree call = %q, %v; want %q", again, err, dir)
	}
}

func TestPartialReadAndList(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/app")
	repo.Write(map[string]string{
		"AGENTS.md":                    "hello\n",
		".claude/skills/test/SKILL.md": "x",
	}).Commit("init")

	ctx := context.Background()
	remote := gitx.NewCache(t.TempDir()).Remote(repo.URL(), gitx.Partial)
	branch, commit, err := remote.DefaultBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Errorf("default branch = %q, want main", branch)
	}
	data, err := remote.ReadFile(ctx, commit, "AGENTS.md")
	if err != nil || string(data) != "hello\n" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if _, err := remote.ReadFile(ctx, commit, "CLAUDE.md"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file should give ErrNotExist, got %v", err)
	}
	entries, err := remote.ListDir(ctx, commit, ".claude/skills")
	if err != nil || len(entries) != 1 || entries[0].Name != "test" || entries[0].Type != "tree" {
		t.Errorf("ListDir = %+v, %v", entries, err)
	}
	if entries, err := remote.ListDir(ctx, commit, ".agents/skills"); err != nil || len(entries) != 0 {
		t.Errorf("missing folder should list empty, got %+v, %v", entries, err)
	}
}

func TestWorktreeCommitPushAndRejection(t *testing.T) {
	host := testkit.NewHost(t)
	home := host.Home("github.com/acme/home")
	other := home.Clone(filepath.Join(t.TempDir(), "other"))
	ctx := context.Background()

	wt, err := gitx.OpenWorktree(ctx, home.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	// Someone else pushes first.
	other.Write(map[string]string{"state/b.yaml": "b\n"}).CommitPush("other")

	home.Write(map[string]string{"state/a.yaml": "a\n"})
	made, err := wt.Commit(ctx, "lock a", "state/a.yaml")
	if err != nil || !made {
		t.Fatalf("Commit = %v, %v", made, err)
	}
	if err := wt.Push(ctx); !errors.Is(err, gitx.ErrPushRejected) {
		t.Fatalf("want ErrPushRejected, got %v", err)
	}
	if err := wt.PullRebase(ctx); err != nil {
		t.Fatal(err)
	}
	if err := wt.Push(ctx); err != nil {
		t.Fatalf("push after rebase: %v", err)
	}

	// Nothing changed, so no commit.
	made, err = wt.Commit(ctx, "noop", "state/a.yaml")
	if err != nil || made {
		t.Errorf("empty Commit = %v, %v; want false, nil", made, err)
	}
}

func TestSyncRefusesUnpushedCommits(t *testing.T) {
	host := testkit.NewHost(t)
	home := host.Home("github.com/acme/home")
	ctx := context.Background()
	home.Write(map[string]string{"factories/x.yaml": "x\n"})
	testkit.Git(t, home.Dir, "add", "--all")
	testkit.Git(t, home.Dir, "commit", "--quiet", "-m", "local only")

	wt, _ := gitx.OpenWorktree(ctx, home.Dir)
	err := wt.Sync(ctx)
	if err == nil || !strings.Contains(err.Error(), "aren't pushed") {
		t.Fatalf("want an unpushed-commits error, got %v", err)
	}
}

func TestDropLastCommitKeepsOtherWork(t *testing.T) {
	host := testkit.NewHost(t)
	home := host.Home("github.com/acme/home")
	ctx := context.Background()
	wt, _ := gitx.OpenWorktree(ctx, home.Dir)

	home.Write(map[string]string{"notes.txt": "unrelated work in progress\n"})
	home.Write(map[string]string{"state/a.yaml": "lock\n"})
	if _, err := wt.Commit(ctx, "lock", "state/a.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := wt.DropLastCommit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home.Dir, "state/a.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state/a.yaml should be gone after dropping the commit, got %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(home.Dir, "notes.txt")); err != nil || !strings.Contains(string(data), "unrelated") {
		t.Errorf("unrelated work was lost: %q, %v", data, err)
	}
}

func TestIsCommitish(t *testing.T) {
	for s, want := range map[string]bool{
		"3f2a1c9": true, "9c1e4b2aa": true, "main": false, "v1.2": false, "abc": false,
		"3F2A1C9": false, strings.Repeat("a", 41): false,
	} {
		if got := gitx.IsCommitish(s); got != want {
			t.Errorf("IsCommitish(%q) = %v, want %v", s, got, want)
		}
	}
}
