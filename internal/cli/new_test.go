package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func TestNewCommand(t *testing.T) {
	testkit.NewHost(t) // an isolated git config, with main as the default branch
	t.Setenv("FABRICA_CACHE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()

	out := mustFab(t, 0, work, "new", "my-factory")
	for _, want := range []string{
		"from Fabrica's starter factory",
		"5 skills, 3 routines, 4 rules, 2 connectors",
		"Initialized a new git repo there.",
		"cd my-factory",
		`git add . && git commit -m "Start a factory"`,
		"factory: { git: github.com/you/my-factory, ref: main }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	factoryDir := filepath.Join(work, "my-factory")
	if _, err := os.Stat(filepath.Join(factoryDir, ".git")); err != nil {
		t.Errorf("the factory should be a git repo: %v", err)
	}
	if testkit.Git(t, factoryDir, "status", "--porcelain") == "" {
		t.Error("new shouldn't commit: the files should be waiting to be added")
	}
	if out := mustFab(t, 0, work, "check", "my-factory"); !strings.Contains(out, "No problems found") {
		t.Errorf("the new factory should check clean:\n%s", out)
	}

	out = mustFab(t, 1, work, "new", "my-factory")
	if !strings.Contains(out, "is already a factory") {
		t.Errorf("a second new should refuse:\n%s", out)
	}

	out = mustFab(t, 0, work, "new", "--empty", "--no-git", "blank")
	if !strings.Contains(out, "Created an empty factory") || strings.Contains(out, "git add") {
		t.Errorf("new --empty --no-git:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(work, "blank", ".git")); err == nil {
		t.Error("--no-git shouldn't run git init")
	}
	if out := mustFab(t, 0, work, "check", "blank"); !strings.Contains(out, "0 skills") || !strings.Contains(out, "No problems found") {
		t.Errorf("the empty factory should check clean:\n%s", out)
	}
}

// A factory can live in a subfolder of a repo that's already there, like a
// monorepo, or a repo just cloned from a git host.
func TestNewInsideAnExistingRepo(t *testing.T) {
	host := testkit.NewHost(t)
	t.Setenv("FABRICA_CACHE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	mono := host.Repo("github.com/acme/monorepo")
	mono.Write(map[string]string{"README.md": "# Monorepo\n"}).Commit("init")
	clone := filepath.Join(t.TempDir(), "monorepo")
	testkit.Git(t, filepath.Dir(clone), "clone", "--quiet", mono.URL(), clone)

	out := mustFab(t, 0, clone, "new", "factories/acme")
	if !strings.Contains(out, "It's inside the git repo at") ||
		!strings.Contains(out, "factory: { git: github.com/acme/monorepo, ref: main, path: factories/acme }") {
		t.Errorf("new in a subfolder:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(clone, "factories", "acme", ".git")); err == nil {
		t.Error("there shouldn't be a repo inside the repo")
	}

	// At the root, the README the repo started with stays.
	out = mustFab(t, 0, clone, "new", ".")
	if !strings.Contains(out, "kept the README.md that was already there") ||
		!strings.Contains(out, "It's at the root of a git repo that was already there.") ||
		!strings.Contains(out, "factory: { git: github.com/acme/monorepo, ref: main }") ||
		strings.Contains(out, "cd .") {
		t.Errorf("new at the root of a clone:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(clone, "README.md")); string(got) != "# Monorepo\n" {
		t.Errorf("the README changed:\n%s", got)
	}
}
