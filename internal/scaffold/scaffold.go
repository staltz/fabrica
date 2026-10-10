// Package scaffold writes out new factories, for `fabrica new`.
//
// It only writes files. Running git is up to the caller.
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/staltz/fabrica/examples"
	"github.com/staltz/fabrica/internal/factory"
)

// Template is what a new factory starts with.
type Template int

const (
	// Starter is Fabrica's starter factory, examples/starter-factory: a
	// working factory with skills, routines, rules and connectors.
	Starter Template = iota
	// Empty is a factory with no items yet: a fabrica.yaml with every key
	// commented out, and a README.
	Empty
)

//go:embed empty
var empty embed.FS

const readme = "README.md"

// ConflictError means the folder already has files the new factory would
// write. Nothing was written.
type ConflictError struct {
	Dir string
	// Paths are slash-separated and relative to Dir, sorted.
	Paths []string
}

func (e *ConflictError) Error() string {
	for _, p := range e.Paths {
		if p == factory.ManifestFile {
			return fmt.Sprintf("%s is already a factory: it has a %s", e.Dir, factory.ManifestFile)
		}
	}
	shown, more, them := e.Paths, "", "them"
	if len(shown) > 5 {
		shown, more = shown[:5], fmt.Sprintf(" and %d more", len(e.Paths)-5)
	}
	if len(e.Paths) == 1 {
		them = "it"
	}
	return fmt.Sprintf("%s already has %s%s, so nothing was written; move %s out of the way, or pick another folder",
		e.Dir, strings.Join(shown, ", "), more, them)
}

// Factory writes a new factory from a template into dir, creating the
// folder if needed. It never overwrites anything: when any file it would
// write is already there, it writes nothing and returns a *ConflictError.
// The one exception is a README.md, which it leaves as it is, since a
// repo made on a git host often starts with one. It returns the files it
// wrote, slash-separated and relative to dir, sorted.
func Factory(dir string, tmpl Template) ([]string, error) {
	files, err := templateFiles(dir, tmpl)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("%s is a file, not a folder", dir)
	}
	if info, err := os.Stat(filepath.Join(dir, readme)); err == nil && info.Mode().IsRegular() {
		delete(files, readme)
	}
	written := make([]string, 0, len(files))
	for p := range files {
		written = append(written, p)
	}
	sort.Strings(written)
	if conflicts := conflicts(dir, written); len(conflicts) > 0 {
		return nil, &ConflictError{Dir: dir, Paths: conflicts}
	}
	for _, p := range written {
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, files[p], 0o644); err != nil {
			return nil, err
		}
	}
	return written, nil
}

// templateFiles reads a template's files, keyed by slash-separated path.
func templateFiles(dir string, tmpl Template) (map[string][]byte, error) {
	var src fs.FS
	switch tmpl {
	case Starter:
		src = examples.StarterFactory()
	case Empty:
		sub, err := fs.Sub(empty, "empty")
		if err != nil {
			return nil, err
		}
		src = sub
	default:
		return nil, fmt.Errorf("unknown template %d", tmpl)
	}
	files := map[string][]byte{}
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		files[p] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if tmpl == Empty {
		name := filepath.Base(dir)
		files[readme] = []byte(strings.ReplaceAll(string(files[readme]), "{{name}}", name))
	}
	return files, nil
}

// conflicts lists the files that are already in dir, and the folders the
// new files need that are already there as something other than a folder.
func conflicts(dir string, files []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range files {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			add(p)
		}
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(parent))); err == nil && !info.IsDir() {
				add(parent)
			}
		}
	}
	sort.Strings(out)
	return out
}
