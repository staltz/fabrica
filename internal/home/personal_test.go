package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func readFile(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnsurePersonalCreatesHomeRepo(t *testing.T) {
	testkit.NewHost(t) // isolated git config
	dir := filepath.Join(t.TempDir(), "nested", ".fabrica")
	t.Setenv("FABRICA_HOME", dir)

	got, created, err := EnsurePersonal(context.Background())
	if err != nil || !created || got != dir {
		t.Fatalf("EnsurePersonal = %q, %v, %v; want %q, true, nil", got, created, err, dir)
	}
	for _, sub := range []string{".git", FactoriesDir} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("want a %s folder: %v", sub, err)
		}
	}
	if s := readFile(t, filepath.Join(dir, ".gitignore")); s != "runs.db*\n" {
		t.Errorf(".gitignore = %q", s)
	}
	h, err := Open(dir)
	if err != nil || !h.Personal || len(h.Factories) != 0 || len(h.Diags) != 0 {
		t.Errorf("the new home repo should open clean: %+v, %v", h, err)
	}

	// Again: nothing to do, and nothing changes.
	testkit.WriteFiles(t, dir, map[string]string{".gitignore": "mine\n"})
	if _, created, err := EnsurePersonal(context.Background()); err != nil || created {
		t.Errorf("second EnsurePersonal = %v, %v; want false, nil", created, err)
	}
	if s := readFile(t, filepath.Join(dir, ".gitignore")); s != "mine\n" {
		t.Errorf(".gitignore = %q, want it left alone", s)
	}
}

func TestEnsurePersonalFinishesWhatsThere(t *testing.T) {
	testkit.NewHost(t)
	// A folder that isn't a git repo yet, say with run history from before.
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, map[string]string{".gitignore": "*.db\n", "runs.db": "history"})
	t.Setenv("FABRICA_HOME", dir)
	if _, created, err := EnsurePersonal(context.Background()); err != nil || !created {
		t.Fatalf("EnsurePersonal = %v, %v; want true, nil", created, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("want a git repo: %v", err)
	}
	if s := readFile(t, filepath.Join(dir, ".gitignore")); s != "*.db\n" {
		t.Errorf(".gitignore = %q, want it left alone", s)
	}
	if s := readFile(t, filepath.Join(dir, "runs.db")); s != "history" {
		t.Errorf("runs.db = %q, want it left alone", s)
	}
}

func TestEnsurePersonalLeavesGitReposAlone(t *testing.T) {
	testkit.NewHost(t)
	// Like a home repo you cloned to ~/.fabrica.
	dir := t.TempDir()
	testkit.Git(t, dir, "init", "--quiet")
	t.Setenv("FABRICA_HOME", dir)
	if _, created, err := EnsurePersonal(context.Background()); err != nil || created {
		t.Fatalf("EnsurePersonal = %v, %v; want false, nil", created, err)
	}
	for _, name := range []string{FactoriesDir, ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s shouldn't be added to a git repo that's already there", name)
		}
	}
}
