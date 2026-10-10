// Package testkit helps tests build little git worlds: factory repos,
// product repos and home repos, all on the local disk.
//
// The trick is git's own `url.<base>.insteadOf` setting. Tests point
// `https://` at a temp folder, so when Fabrica asks git for
// `https://github.com/acme/app.git` it quietly reads
// `<temp>/hosts/github.com/acme/app.git` instead. Fabrica itself has no
// test-only code paths: it runs real git against real repos.
package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Host is a fake git host living in a temp folder.
type Host struct {
	t    *testing.T
	Root string
}

// NewHost sets up an isolated git configuration for the test (so the
// developer's own ~/.gitconfig can't change the outcome) and returns an
// empty fake host.
func NewHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	hosts := filepath.Join(root, "hosts")
	if err := os.MkdirAll(hosts, 0o755); err != nil {
		t.Fatal(err)
	}
	gitconfig := filepath.Join(root, "gitconfig")
	cfg := "[url \"file://" + hosts + "/\"]\n\tinsteadOf = https://\n" +
		"[user]\n\tname = Test\n\temail = test@example.com\n" +
		"[init]\n\tdefaultBranch = main\n" +
		"[protocol \"file\"]\n\tallow = always\n" +
		"[uploadpack]\n\tallowFilter = true\n\tallowAnySHA1InWant = true\n" +
		"[advice]\n\tdetachedHead = false\n"
	if err := os.WriteFile(gitconfig, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_DATE", "")
	return &Host{t: t, Root: root}
}

// Repo is a repo on the fake host. It's a normal (non-bare) repo, so tests
// can write files and commit straight into it, and Fabrica can still clone
// and fetch from it.
type Repo struct {
	t *testing.T
	// Name is the canonical name, like github.com/acme/app.
	Name string
	// Dir is the folder holding the repo.
	Dir string
}

// URL is the address Fabrica should use for this repo.
func (r *Repo) URL() string { return "https://" + r.Name + ".git" }

// Repo creates (or returns) the repo with the given canonical name.
func (h *Host) Repo(name string) *Repo {
	h.t.Helper()
	dir := filepath.Join(h.Root, "hosts", filepath.FromSlash(name)+".git")
	r := &Repo{t: h.t, Name: name, Dir: dir}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return r
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	Git(h.t, dir, "init", "--quiet", "-b", "main")
	return r
}

// Write writes files into the repo's working tree without committing. A
// path ending in "!x" is written as an executable (the "!x" is dropped).
// An empty string content with a path ending in "!rm" deletes the file.
func (r *Repo) Write(files map[string]string) *Repo {
	r.t.Helper()
	WriteFiles(r.t, r.Dir, files)
	return r
}

// Commit stages everything and commits it, returning the new commit hash.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	Git(r.t, r.Dir, "add", "--all")
	Git(r.t, r.Dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	return strings.TrimSpace(Git(r.t, r.Dir, "rev-parse", "HEAD"))
}

// Tag tags the current commit.
func (r *Repo) Tag(name string) *Repo {
	r.t.Helper()
	Git(r.t, r.Dir, "tag", name)
	return r
}

// Branch creates a branch at the current commit, without switching to it.
func (r *Repo) Branch(name string) *Repo {
	r.t.Helper()
	Git(r.t, r.Dir, "branch", "--force", name)
	return r
}

// HomeRepo is a home repo: a bare repo on the fake host plus a clone of it
// in a separate folder, set up to track the bare repo like a real checkout.
type HomeRepo struct {
	t *testing.T
	// Dir is the clone, where `fabrica` commands run.
	Dir string
	// Bare is the shared remote.
	Bare string
}

// Home creates a home repo with an initial commit, pushed and tracking.
func (h *Host) Home(name string) *HomeRepo {
	h.t.Helper()
	bare := filepath.Join(h.Root, "hosts", filepath.FromSlash(name)+".git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		h.t.Fatal(err)
	}
	Git(h.t, bare, "init", "--quiet", "--bare", "-b", "main")
	dir := filepath.Join(h.Root, "clones", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		h.t.Fatal(err)
	}
	Git(h.t, "", "clone", "--quiet", bare, dir)
	WriteFiles(h.t, dir, map[string]string{"README.md": "home repo\n"})
	Git(h.t, dir, "add", "--all")
	Git(h.t, dir, "commit", "--quiet", "-m", "init")
	Git(h.t, dir, "push", "--quiet", "-u", "origin", "main")
	return &HomeRepo{t: h.t, Dir: dir, Bare: bare}
}

// Clone makes another checkout of the same home repo, like a second person
// (or a CI job) working on it.
func (hr *HomeRepo) Clone(dir string) *HomeRepo {
	hr.t.Helper()
	Git(hr.t, "", "clone", "--quiet", hr.Bare, dir)
	return &HomeRepo{t: hr.t, Dir: dir, Bare: hr.Bare}
}

// Write writes files into the clone.
func (hr *HomeRepo) Write(files map[string]string) *HomeRepo {
	hr.t.Helper()
	WriteFiles(hr.t, hr.Dir, files)
	return hr
}

// CommitPush commits everything in the clone and pushes it.
func (hr *HomeRepo) CommitPush(msg string) {
	hr.t.Helper()
	Git(hr.t, hr.Dir, "add", "--all")
	Git(hr.t, hr.Dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	Git(hr.t, hr.Dir, "push", "--quiet")
}

// Pull pulls the latest commits into the clone.
func (hr *HomeRepo) Pull() {
	hr.t.Helper()
	Git(hr.t, hr.Dir, "pull", "--quiet", "--rebase")
}

// Git runs a git command and fails the test if it fails.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// WriteFiles writes files under dir, creating folders as needed. See
// Repo.Write for the "!x" and "!rm" suffixes.
func WriteFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		mode := os.FileMode(0o644)
		switch {
		case strings.HasSuffix(name, "!x"):
			name, mode = strings.TrimSuffix(name, "!x"), 0o755
		case strings.HasSuffix(name, "!rm"):
			if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(name, "!rm")))); err != nil {
				t.Fatal(err)
			}
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}
