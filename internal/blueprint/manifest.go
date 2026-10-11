package blueprint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/yamlx"
)

// File names at the root of a blueprint.
const (
	ManifestFile = "fabrica.yaml"
	LockFile     = "fabrica.lock"
)

// Manifest is fabrica.yaml.
type Manifest struct {
	Description string
	// Imports, sorted by alias.
	Imports []ImportSpec
	// Expects are skills the blueprint hopes the product repo provides.
	Expects []names.MaybeName
}

// Import looks up an import by alias.
func (m *Manifest) Import(alias string) (ImportSpec, bool) {
	for _, imp := range m.Imports {
		if imp.Alias == alias {
			return imp, true
		}
	}
	return ImportSpec{}, false
}

// ImportSpec is one entry under imports: in fabrica.yaml.
type ImportSpec struct {
	// Alias is the name you picked for the import. It only matters inside
	// your manifest and in messages.
	Alias string
	Git   string
	Ref   string
	// Path is the subfolder the blueprint (or, for plain repos, the skills)
	// lives in. Empty means the default.
	Path string
	// Only and Exclude filter what comes in. At most one of them is set.
	Only    []names.Ref
	Exclude []names.Ref
}

// Excludes reports whether the import's exclude: list has ref.
func (s ImportSpec) Excludes(ref names.Ref) bool {
	for _, r := range s.Exclude {
		if r == ref {
			return true
		}
	}
	return false
}

// keeps reports whether ref survives the import's filter.
func (s ImportSpec) keeps(ref names.Ref) bool {
	if s.Only != nil {
		for _, r := range s.Only {
			if r == ref {
				return true
			}
		}
		return false
	}
	return !s.Excludes(ref)
}

type manifestFile struct {
	Description string                `yaml:"description"`
	Imports     map[string]importFile `yaml:"imports"`
	Expects     []string              `yaml:"expects"`
}

type importFile struct {
	Git     string    `yaml:"git"`
	Ref     string    `yaml:"ref"`
	Path    string    `yaml:"path"`
	Only    *[]string `yaml:"only"`
	Exclude *[]string `yaml:"exclude"`
}

// ReadManifest reads fabrica.yaml from a blueprint folder.
func ReadManifest(dir string) (*Manifest, diag.List) {
	var diags diag.List
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			diags.Errorf("manifest.missing", dir, "there's no %s here, so this isn't a blueprint", ManifestFile).
				Hintf("create %s at the root of your blueprint (it can be empty to start with)", ManifestFile)
		} else {
			diags.Errorf("manifest.unreadable", ManifestFile, "%v", err)
		}
		return nil, diags
	}
	m, err := parseManifest(data)
	if err != nil {
		diags.Errorf("manifest.invalid", ManifestFile, "%v", err)
		return nil, diags
	}
	return m, nil
}

func parseManifest(data []byte) (*Manifest, error) {
	var f manifestFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return nil, err
	}
	m := &Manifest{Description: strings.TrimSpace(f.Description)}
	var err error
	m.Expects, err = names.ParseMaybeNames(f.Expects)
	if err != nil {
		return nil, fmt.Errorf("expects: %v", err)
	}
	for alias, imp := range f.Imports {
		if err := names.Validate(alias); err != nil {
			return nil, fmt.Errorf("imports: alias %q isn't valid: %v", alias, err)
		}
		spec := ImportSpec{Alias: alias, Git: strings.TrimSpace(imp.Git), Ref: strings.TrimSpace(imp.Ref)}
		if spec.Git == "" {
			return nil, fmt.Errorf("imports.%s: git is missing; say where the import lives, like `git: github.com/acme/base-blueprint`", alias)
		}
		if spec.Ref == "" {
			return nil, fmt.Errorf("imports.%s: ref is missing; pin a branch, tag or commit (fabrica.lock records the exact commit)", alias)
		}
		if imp.Path != "" {
			clean := path.Clean(strings.Trim(imp.Path, "/"))
			if path.IsAbs(imp.Path) || clean == ".." || strings.HasPrefix(clean, "../") {
				return nil, fmt.Errorf("imports.%s: path %q must be a folder inside the imported repo", alias, imp.Path)
			}
			spec.Path = clean
		}
		if imp.Only != nil && imp.Exclude != nil {
			return nil, fmt.Errorf("imports.%s: use either only: or exclude:, not both", alias)
		}
		if imp.Only != nil {
			if len(*imp.Only) == 0 {
				return nil, fmt.Errorf("imports.%s: only: is empty, which would import nothing; drop the import instead", alias)
			}
			if spec.Only, err = parseRefs(*imp.Only); err != nil {
				return nil, fmt.Errorf("imports.%s.only: %v", alias, err)
			}
		}
		if imp.Exclude != nil {
			if spec.Exclude, err = parseRefs(*imp.Exclude); err != nil {
				return nil, fmt.Errorf("imports.%s.exclude: %v", alias, err)
			}
			if spec.Exclude == nil {
				spec.Exclude = []names.Ref{}
			}
		}
		m.Imports = append(m.Imports, spec)
	}
	sort.Slice(m.Imports, func(i, j int) bool { return m.Imports[i].Alias < m.Imports[j].Alias })
	return m, nil
}

func parseRefs(list []string) ([]names.Ref, error) {
	seen := map[names.Ref]bool{}
	var out []names.Ref
	for _, s := range list {
		ref, err := names.ParseRef(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
		if seen[ref] {
			return nil, fmt.Errorf("%s is listed twice", ref)
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out, nil
}

// Lock is fabrica.lock: the exact commit of every import.
type Lock struct {
	Imports map[string]LockedImport
}

// LockedImport is one pinned import.
type LockedImport struct {
	Git    string `yaml:"git"`
	Ref    string `yaml:"ref"`
	Path   string `yaml:"path,omitempty"`
	Commit string `yaml:"commit"`
}

// Matches reports whether a lock entry was made for this import spec. If
// you change an import's git, ref or path, its old pin no longer counts.
// Changing only: or exclude: doesn't need a new pin.
func (l LockedImport) Matches(spec ImportSpec) bool {
	return l.Git == spec.Git && l.Ref == spec.Ref && l.Path == spec.Path
}

type lockFile struct {
	Imports map[string]LockedImport `yaml:"imports"`
}

// ReadLock reads fabrica.lock. A missing file gives (nil, nil).
func ReadLock(dir string) (*Lock, error) {
	data, err := os.ReadFile(filepath.Join(dir, LockFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f lockFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %v", LockFile, err)
	}
	l := &Lock{Imports: f.Imports}
	if l.Imports == nil {
		l.Imports = map[string]LockedImport{}
	}
	for alias, imp := range l.Imports {
		if len(imp.Commit) != 40 {
			return nil, fmt.Errorf("%s: imports.%s.commit should be a full 40-character commit hash; run `fabrica lock` to rewrite the file", LockFile, alias)
		}
	}
	return l, nil
}

const lockHeader = "# Written by `fabrica lock`. Don't edit by hand.\n# It pins every import to an exact commit; `fabrica upgrade` moves the pins.\n"

// WriteLock writes fabrica.lock into dir.
func WriteLock(dir string, l *Lock) error {
	data, err := yamlx.Marshal(lockFile{Imports: l.Imports})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, LockFile), append([]byte(lockHeader), data...))
}

// writeFileAtomic writes a file by writing a temp file next to it and
// renaming it into place, so a crash never leaves a half-written file.
func writeFileAtomic(p string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
