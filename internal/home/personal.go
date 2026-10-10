package home

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/paths"
)

// personalGitignore keeps local run history (runs.db, and SQLite's -wal and
// -shm files next to it) out of a new personal home repo.
const personalGitignore = "runs.db*\n"

// EnsurePersonal creates your personal home repo ($FABRICA_HOME, or
// ~/.fabrica) unless it's there already, and returns its folder. Every
// command calls it first, so there's always a home repo to add factories
// to and to keep local run history in.
//
// A new home repo is a git repo with an empty factories/ folder and a
// .gitignore that keeps runs.db out of git. Nothing is committed. A folder
// that's a git repo already, like a home repo you cloned there, is left
// exactly as it is. Calling it again is always safe: it finishes a job a
// previous call left half done, and never overwrites a file.
func EnsurePersonal(ctx context.Context) (dir string, created bool, err error) {
	dir, err = paths.PersonalHome()
	if err != nil {
		return "", false, err
	}
	// .git is the last thing we make, so its being there means done.
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return dir, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return dir, false, err
	}
	if err := os.MkdirAll(filepath.Join(dir, FactoriesDir), 0o755); err != nil {
		return dir, false, err
	}
	if err := writeNew(filepath.Join(dir, ".gitignore"), personalGitignore); err != nil {
		return dir, false, err
	}
	if _, err := gitx.Init(ctx, dir); err != nil {
		return dir, false, err
	}
	return dir, true, nil
}

// writeNew writes a file only if there's no file there yet.
func writeNew(file, content string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
