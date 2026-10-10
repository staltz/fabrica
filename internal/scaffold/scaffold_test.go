package scaffold

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
)

// The starter template is examples/starter-blueprint, byte for byte, so the
// docs and tests for the example cover what `fabrica new` writes too.
func TestStarterIsTheExample(t *testing.T) {
	dir := t.TempDir()
	written, err := Blueprint(dir, Starter)
	if err != nil {
		t.Fatal(err)
	}
	example := filepath.Join("..", "..", "examples", "starter-blueprint")
	var want []string
	err = filepath.WalkDir(example, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != example {
			// go:embed leaves out dotfiles, like a stray .DS_Store.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(example, p)
		want = append(want, filepath.ToSlash(rel))
		exampleData, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("%s wasn't written: %v", rel, err)
		} else if !bytes.Equal(got, exampleData) {
			t.Errorf("%s differs from the example", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(written, want) {
		t.Errorf("written = %v\nwant %v", written, want)
	}
}

func TestEmptyIsAValidBlueprint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme-blueprint")
	written, err := Blueprint(dir, Empty)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(written, []string{"README.md", "fabrica.yaml"}) {
		t.Errorf("written = %v", written)
	}
	if m, diags := blueprint.ReadManifest(dir); m == nil || len(diags) > 0 {
		t.Errorf("fabrica.yaml should parse: %v", diags)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.HasPrefix(string(readme), "# acme-blueprint\n") {
		t.Errorf("the README should be titled after the folder:\n%s", readme)
	}
}

func TestNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	mine := []byte("---\nalwaysApply: true\n---\nMine.\n")
	writeFile(t, filepath.Join(dir, "rules", "tests-first.mdc"), mine)
	_, err := Blueprint(dir, Starter)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !slices.Equal(conflict.Paths, []string{"rules/tests-first.mdc"}) {
		t.Fatalf("want a conflict on rules/tests-first.mdc, got %v", err)
	}
	if !strings.Contains(err.Error(), "already has rules/tests-first.mdc, so nothing was written; move it out of the way") {
		t.Errorf("message: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "rules", "tests-first.mdc")); !bytes.Equal(got, mine) {
		t.Errorf("the existing rule changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, blueprint.ManifestFile)); err == nil {
		t.Error("nothing should be written when there's a conflict")
	}

	// A file where a folder needs to go is one conflict, not one per file
	// that would go in it.
	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "skills"), nil)
	if _, err := Blueprint(dir, Starter); !errors.As(err, &conflict) || !slices.Equal(conflict.Paths, []string{"skills"}) {
		t.Errorf("want a conflict on skills, got %v", err)
	}

	// An existing blueprint gets a clearer message.
	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, blueprint.ManifestFile), nil)
	if _, err := Blueprint(dir, Empty); err == nil || !strings.Contains(err.Error(), "is already a blueprint") {
		t.Errorf("want an already-a-blueprint error, got %v", err)
	}

	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, nil)
	if _, err := Blueprint(file, Empty); err == nil || !strings.Contains(err.Error(), "is a file, not a folder") {
		t.Errorf("want a not-a-folder error, got %v", err)
	}
}

// Repos made on a git host often start with a README, and possibly a
// license. Neither stops a new blueprint.
func TestKeepsAnExistingReadme(t *testing.T) {
	dir := t.TempDir()
	readme := []byte("# my-blueprint\n")
	writeFile(t, filepath.Join(dir, "README.md"), readme)
	writeFile(t, filepath.Join(dir, "LICENSE"), []byte("MIT\n"))
	written, err := Blueprint(dir, Starter)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(written, "README.md") || !slices.Contains(written, blueprint.ManifestFile) {
		t.Errorf("written = %v", written)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "README.md")); !bytes.Equal(got, readme) {
		t.Errorf("the README changed:\n%s", got)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
