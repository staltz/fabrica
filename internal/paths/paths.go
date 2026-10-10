// Package paths knows where Fabrica keeps things on your machine.
//
//   - Your personal home repo: $FABRICA_HOME, or ~/.fabrica by default.
//   - Local run history: runs.db inside your personal home repo.
//   - The cache (git clones, unpacked commits and skills): $FABRICA_CACHE,
//     or $XDG_CACHE_HOME/fabrica, or ~/.cache/fabrica.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// PersonalHome returns the folder of your personal home repo.
func PersonalHome() (string, error) {
	if dir := os.Getenv("FABRICA_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("couldn't find your home folder; set FABRICA_HOME to where your home repo lives: %w", err)
	}
	return filepath.Join(home, ".fabrica"), nil
}

// Cache returns Fabrica's cache folder.
func Cache() (string, error) {
	if dir := os.Getenv("FABRICA_CACHE"); dir != "" {
		return filepath.Abs(dir)
	}
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "fabrica"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("couldn't find your home folder; set FABRICA_CACHE to a folder Fabrica can use as a cache: %w", err)
	}
	return filepath.Join(home, ".cache", "fabrica"), nil
}

// LocalRunsDB returns the path of the local run-history database.
func LocalRunsDB() (string, error) {
	home, err := PersonalHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runs.db"), nil
}

// Same reports whether two paths point at the same folder, after making
// them absolute and resolving symlinks.
func Same(a, b string) bool {
	return Canonical(a) == Canonical(b)
}

// Canonical makes a path absolute, cleans it and resolves symlinks where it
// can. Paths that don't exist yet are just made absolute and cleaned.
func Canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
