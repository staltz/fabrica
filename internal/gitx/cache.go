package gitx

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Cache keeps local copies of remote git repos under one folder, usually
// ~/.cache/fabrica. Each remote gets a bare clone, and each commit we read
// gets unpacked once into a folder that never changes afterwards (a commit's
// content can't change, so neither can its folder).
type Cache struct {
	Root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewCache returns a cache rooted at dir.
func NewCache(dir string) *Cache {
	return &Cache{Root: dir, locks: map[string]*sync.Mutex{}}
}

// Mode says how much of a remote we download.
type Mode int

const (
	// Full downloads everything. Factories use this: we read whole trees.
	Full Mode = iota
	// Partial downloads commits and trees but fetches file contents only
	// when we read them. Product repos use this: we only look at a handful
	// of files, and some product repos are huge.
	Partial
)

// Remote is one remote repo in the cache.
type Remote struct {
	URL   string
	mode  Mode
	cache *Cache
	dir   string // the bare clone

	fetchedOnce bool
}

// Remote returns a handle for the repo at url. Nothing is downloaded until
// you ask for something.
func (c *Cache) Remote(url string, mode Mode) *Remote {
	sum := sha256.Sum256([]byte(url))
	suffix := ""
	if mode == Partial {
		suffix = "-partial"
	}
	name := hex.EncodeToString(sum[:8]) + "-" + safeName(url) + suffix + ".git"
	return &Remote{URL: url, mode: mode, cache: c, dir: filepath.Join(c.Root, "git", name)}
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// safeName turns the end of a URL into something readable for a folder
// name, so `ls ~/.cache/fabrica/git` tells you which repo is which.
func safeName(url string) string {
	base := path.Base(strings.TrimSuffix(strings.TrimRight(url, "/"), ".git"))
	base = unsafeChars.ReplaceAllString(base, "-")
	if len(base) > 40 {
		base = base[:40]
	}
	return base
}

// lockFor returns a mutex for one path, so two goroutines in this process
// never fetch into the same clone at once. Separate processes are kept
// apart by a lock file (see lockFile).
func (c *Cache) lockFor(p string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	m, ok := c.locks[p]
	if !ok {
		m = &sync.Mutex{}
		c.locks[p] = m
	}
	return m
}

// withLock runs fn while holding both the in-process and the cross-process
// lock for the given path.
func (c *Cache) withLock(ctx context.Context, p string, fn func() error) error {
	m := c.lockFor(p)
	m.Lock()
	defer m.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	unlock, err := lockFile(ctx, p+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// ensureClone makes the bare clone if it doesn't exist yet.
func (r *Remote) ensureClone(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(r.dir, "HEAD")); err == nil {
		return nil
	}
	tmp := r.dir + ".tmp"
	_ = os.RemoveAll(tmp)
	args := []string{"clone", "--bare", "--quiet"}
	if r.mode == Partial {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, r.URL, tmp)
	if _, err := run(ctx, "", args...); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("couldn't clone %s: %w", r.URL, err)
	}
	if err := os.Rename(tmp, r.dir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	r.fetchedOnce = true
	return nil
}

// Fetch brings the clone up to date with every branch and tag on the
// remote. The first call clones if needed.
func (r *Remote) Fetch(ctx context.Context) error {
	return r.cache.withLock(ctx, r.dir, func() error {
		fresh := false
		if _, err := os.Stat(filepath.Join(r.dir, "HEAD")); err != nil {
			if err := r.ensureClone(ctx); err != nil {
				return err
			}
			fresh = true
		}
		if fresh {
			return nil
		}
		_, err := run(ctx, r.dir, "fetch", "--quiet", "--prune", "--force", "origin",
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		if err != nil {
			return fmt.Errorf("couldn't fetch %s: %w", r.URL, err)
		}
		r.fetchedOnce = true
		return nil
	})
}

// fetchOnce fetches unless this handle already fetched.
func (r *Remote) fetchOnce(ctx context.Context) error {
	if r.fetchedOnce {
		return nil
	}
	return r.Fetch(ctx)
}

// hasCommit reports whether the clone already has the commit.
func (r *Remote) hasCommit(ctx context.Context, commit string) bool {
	if _, err := os.Stat(filepath.Join(r.dir, "HEAD")); err != nil {
		return false
	}
	_, err := run(ctx, r.dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// EnsureCommit makes sure the clone has the given commit, fetching if it
// doesn't. A commit we already have is never re-fetched, which keeps things
// fast and lets pinned commits work offline once they're cached.
func (r *Remote) EnsureCommit(ctx context.Context, commit string) error {
	if r.hasCommit(ctx, commit) {
		return nil
	}
	if err := r.Fetch(ctx); err != nil {
		return err
	}
	if r.hasCommit(ctx, commit) {
		return nil
	}
	// Branches and tags don't include it. Most hosts still let you fetch a
	// full commit hash directly.
	if len(commit) == 40 {
		_ = r.cache.withLock(ctx, r.dir, func() error {
			_, err := run(ctx, r.dir, "fetch", "--quiet", "origin", commit)
			return err
		})
		if r.hasCommit(ctx, commit) {
			return nil
		}
	}
	return fmt.Errorf("commit %s isn't in %s", Short(commit), r.URL)
}

// RefKind says what kind of thing a ref turned out to be.
type RefKind string

const (
	Branch RefKind = "branch"
	Tag    RefKind = "tag"
	Commit RefKind = "commit"
)

// Follows reports whether a ref of this kind moves over time. Branches do
// (Fabrica calls that "follow"); tags and commits don't ("frozen").
func (k RefKind) Follows() bool { return k == Branch }

// Resolved is a ref resolved to an exact commit.
type Resolved struct {
	Ref    string
	Kind   RefKind
	Commit string
}

// Resolve works out what ref points at right now. A branch means "follow",
// a tag or a commit means "frozen". If a branch and a tag share the same
// name, that's ambiguous, and Fabrica refuses to guess.
func (r *Remote) Resolve(ctx context.Context, ref string) (Resolved, error) {
	if ref == "" {
		return Resolved{}, fmt.Errorf("no ref given for %s; set ref: to a branch, tag or commit", r.URL)
	}
	if err := r.fetchOnce(ctx); err != nil {
		return Resolved{}, err
	}
	branch, branchErr := run(ctx, r.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+ref+"^{commit}")
	tag, tagErr := run(ctx, r.dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+ref+"^{commit}")
	isBranch, isTag := branchErr == nil, tagErr == nil
	switch {
	case isBranch && isTag:
		return Resolved{}, fmt.Errorf("%q is both a branch and a tag in %s, so it's ambiguous; use a name that's only one of them, or a commit hash", ref, r.URL)
	case isBranch:
		return Resolved{Ref: ref, Kind: Branch, Commit: trimOut(branch)}, nil
	case isTag:
		return Resolved{Ref: ref, Kind: Tag, Commit: trimOut(tag)}, nil
	}
	if IsCommitish(ref) {
		if err := r.EnsureCommit(ctx, ref); err == nil {
			full, err := run(ctx, r.dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
			if err == nil {
				return Resolved{Ref: ref, Kind: Commit, Commit: trimOut(full)}, nil
			}
		}
	}
	return Resolved{}, fmt.Errorf("%q isn't a branch, tag or commit in %s", ref, r.URL)
}

// DefaultBranch returns the remote's default branch (what HEAD points at)
// and the commit it's on. Product repo checks read this branch.
func (r *Remote) DefaultBranch(ctx context.Context) (branch, commit string, err error) {
	out, err := run(ctx, "", "ls-remote", "--symref", r.URL, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("couldn't read the default branch of %s: %w", r.URL, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ref: ") && strings.HasSuffix(line, "\tHEAD") {
			ref := strings.TrimSuffix(strings.TrimPrefix(line, "ref: "), "\tHEAD")
			branch = strings.TrimPrefix(ref, "refs/heads/")
		} else if strings.HasSuffix(line, "\tHEAD") {
			commit = strings.TrimSuffix(line, "\tHEAD")
		}
	}
	if branch == "" || commit == "" {
		return "", "", fmt.Errorf("%s has no default branch (is it empty?)", r.URL)
	}
	if err := r.EnsureCommit(ctx, commit); err != nil {
		return "", "", err
	}
	return branch, commit, nil
}

// Tree unpacks the whole tree of a commit into a folder and returns it. The
// folder is shared and must be treated as read-only.
func (r *Remote) Tree(ctx context.Context, commit string) (string, error) {
	if !IsCommitish(commit) || len(commit) != 40 {
		return "", fmt.Errorf("Tree needs a full commit hash, got %q", commit)
	}
	dest := filepath.Join(r.cache.Root, "trees", commit)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	if err := r.EnsureCommit(ctx, commit); err != nil {
		return "", err
	}
	err := r.cache.withLock(ctx, dest, func() error {
		if _, err := os.Stat(dest); err == nil {
			return nil // someone else unpacked it while we waited
		}
		tmp := dest + ".tmp"
		_ = os.RemoveAll(tmp)
		if err := r.archiveTo(ctx, commit, tmp); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		return os.Rename(tmp, dest)
	})
	if err != nil {
		return "", err
	}
	return dest, nil
}

// archiveTo streams `git archive` into dest. Using git's archive keeps file
// modes (like the executable bit on scripts) and symlinks exactly as they
// are in the commit.
func (r *Remote) archiveTo(ctx context.Context, commit, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "archive", "--format=tar", commit)
	cmd.Dir = r.dir
	cmd.Env = gitEnv()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := untar(stdout, dest)
	// Drain whatever is left so git can exit cleanly.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if waitErr != nil {
		return &Error{Args: []string{"archive", commit}, Stderr: stderr.String(), Err: waitErr}
	}
	return extractErr
}

// untar unpacks a tar stream into dest, refusing any entry that would land
// outside of it.
func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		if name == "." || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("refusing archive entry %q outside the tree", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o444)
			if hdr.Mode&0o111 != 0 {
				mode = 0o555
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			// Files are read-only so nobody edits the cache by accident.
			// Folders stay writable so the cache can still be cleaned up.
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Submodules and other special entries don't carry content we
			// can use. Skip them.
		}
	}
}

// ReadFile reads one file at a commit. It returns os.ErrNotExist (wrapped)
// when the file isn't there.
func (r *Remote) ReadFile(ctx context.Context, commit, p string) ([]byte, error) {
	if err := r.EnsureCommit(ctx, commit); err != nil {
		return nil, err
	}
	kind, err := run(ctx, r.dir, "cat-file", "-t", commit+":"+p)
	if err != nil || trimOut(kind) != "blob" {
		return nil, fmt.Errorf("%s at %s: %w", p, Short(commit), os.ErrNotExist)
	}
	return run(ctx, r.dir, "cat-file", "blob", commit+":"+p)
}

// Entry is one item in a folder listing.
type Entry struct {
	Name string
	// Type is "blob" for files, "tree" for folders and "commit" for
	// submodules.
	Type string
	Mode string
}

// ListDir lists one folder at a commit. A missing folder gives an empty
// list, not an error.
func (r *Remote) ListDir(ctx context.Context, commit, dir string) ([]Entry, error) {
	if err := r.EnsureCommit(ctx, commit); err != nil {
		return nil, err
	}
	spec := commit
	if dir != "" && dir != "." {
		spec = commit + ":" + strings.Trim(dir, "/")
		if kind, err := run(ctx, r.dir, "cat-file", "-t", spec); err != nil || trimOut(kind) != "tree" {
			return nil, nil
		}
	}
	out, err := run(ctx, r.dir, "ls-tree", spec)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			continue
		}
		entries = append(entries, Entry{Name: name, Mode: fields[0], Type: fields[1]})
	}
	return entries, nil
}
