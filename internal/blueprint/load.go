// Package blueprint reads blueprints: the git repos (or folders in one) that
// describe how agents should build software.
//
// A blueprint has a manifest (fabrica.yaml), a lock (fabrica.lock), and items:
// skills, routines, rules, connectors and providers. It can import other
// blueprints, and plain repos full of skills or rules, in whole or in part.
// After imports are resolved, a blueprint is one flat set of names, with a
// separate namespace for each kind of item.
//
// Loading never guesses. Two imports bringing the same item, an import
// that changed since the lock was written, or a routine that needs a skill
// you didn't bring in are all errors, reported together so you can fix
// them in one go.
package blueprint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
)

// maxImportDepth stops runaway import chains.
const maxImportDepth = 8

// Source says where a blueprint came from.
type Source struct {
	// Dir is the folder holding fabrica.yaml.
	Dir string
	// Git is the git: value as written in a factory or import. Empty for
	// a blueprint read straight from a folder (like while you're writing it).
	Git string
	// Commit is the exact commit. Empty for a folder.
	Commit string
	// Path is the blueprint's subfolder inside its repo.
	Path string
}

func (s Source) String() string {
	if s.Git == "" {
		return s.Dir
	}
	out := s.Git
	if s.Path != "" {
		out += "/" + s.Path
	}
	return out + "@" + gitx.Short(s.Commit)
}

// Blueprint is a blueprint with every import resolved into one flat set.
type Blueprint struct {
	Source     Source
	Manifest   *Manifest
	Lock       *Lock
	Skills     map[string]*Skill
	Routines   map[string]*Routine
	Rules      map[string]*Rule
	Connectors map[string]*Connector
	Providers  map[string]*ProviderItem
	// Imports are the blueprint's direct imports, sorted by alias.
	Imports []*Import
	// Overrides are imported items this blueprint excluded and then defined
	// itself, under the same name.
	Overrides []Override
}

// Import is one direct import, as resolved.
type Import struct {
	Spec   ImportSpec
	Commit string
	// IsBlueprint is false for plain skill or rule repos.
	IsBlueprint bool
	// Expects is what the imported blueprint expects from product repos.
	Expects []names.MaybeName
	// offered is everything the import brings, before only: or exclude:.
	offered map[names.Ref]item
}

// Offered lists the refs of everything the import brings before only: or
// exclude:, sorted.
func (i *Import) Offered() []names.Ref {
	return sortedRefs(i.offered)
}

// OfferedHash returns the content hash of one offered item.
func (i *Import) OfferedHash(ref names.Ref) (string, bool) {
	it, ok := i.offered[ref]
	if !ok {
		return "", false
	}
	return it.info().Hash, true
}

// Override is an imported item that this blueprint replaced with its own.
type Override struct {
	Ref   names.Ref
	Alias string
}

// Loader reads blueprints. It remembers imports it already read (a commit's
// content never changes), so loading many factories of the same blueprint
// stays fast. It's safe to use from several goroutines.
type Loader struct {
	Git *gitx.Cache

	mu   sync.Mutex
	memo map[string]*tree
}

// NewLoader returns a loader that reads git repos through cache.
func NewLoader(cache *gitx.Cache) *Loader {
	return &Loader{Git: cache, memo: map[string]*tree{}}
}

// tree is one blueprint (or plain repo) with its own imports resolved, before
// the importer's filters apply.
type tree struct {
	items       map[names.Ref]item
	diags       diag.List
	isBlueprint bool
	manifest    *Manifest
	lock        *Lock
	imports     []*Import
	overrides   []Override
}

// LoadOptions tweak a load.
type LoadOptions struct {
	// Lock, when set, is used instead of the blueprint's fabrica.lock. That's
	// how `fabrica lock` and `fabrica upgrade` check a new lock before
	// writing it.
	Lock *Lock
}

// LoadDir reads the blueprint in a folder, like the one you're writing.
func (l *Loader) LoadDir(ctx context.Context, dir string, opts LoadOptions) (*Blueprint, diag.List) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, diag.AsList(err, "blueprint.unreadable", dir)
	}
	return l.load(ctx, Source{Dir: abs}, opts)
}

// LoadGit reads the blueprint at an exact commit of a git repo.
func (l *Loader) LoadGit(ctx context.Context, gitSpec, commit, subpath string) (*Blueprint, diag.List) {
	remote := l.Git.Remote(repourl.GitURL(gitSpec), gitx.Full)
	root, err := remote.Tree(ctx, commit)
	if err != nil {
		return nil, diag.AsList(err, "blueprint.fetch", gitSpec)
	}
	dir := filepath.Join(root, filepath.FromSlash(subpath))
	return l.load(ctx, Source{Dir: dir, Git: gitSpec, Commit: commit, Path: subpath}, LoadOptions{})
}

func (l *Loader) load(ctx context.Context, src Source, opts LoadOptions) (*Blueprint, diag.List) {
	t := l.build(ctx, src, opts.Lock, nil)
	diags := append(diag.List(nil), t.diags...)
	if t.manifest == nil {
		return nil, diags
	}
	f := &Blueprint{
		Source:     src,
		Manifest:   t.manifest,
		Lock:       t.lock,
		Skills:     map[string]*Skill{},
		Routines:   map[string]*Routine{},
		Rules:      map[string]*Rule{},
		Connectors: map[string]*Connector{},
		Providers:  map[string]*ProviderItem{},
		Imports:    t.imports,
		Overrides:  t.overrides,
	}
	for ref, it := range t.items {
		switch v := it.(type) {
		case *Skill:
			f.Skills[ref.Name] = v
		case *Routine:
			f.Routines[ref.Name] = v
		case *Rule:
			f.Rules[ref.Name] = v
		case *Connector:
			f.Connectors[ref.Name] = v
		case *ProviderItem:
			f.Providers[ref.Name] = v
		}
	}
	diags = append(diags, f.validate()...)
	return f, diags.Dedup()
}

// build reads one blueprint folder and resolves its imports.
func (l *Loader) build(ctx context.Context, src Source, lockOverride *Lock, stack []string) *tree {
	t := &tree{isBlueprint: true, items: map[names.Ref]item{}}
	m, diags := ReadManifest(src.Dir)
	if m == nil {
		if _, err := os.Stat(filepath.Join(src.Dir, "fabrica.yml")); err == nil && len(diags) > 0 {
			diags[0].Hint = "rename fabrica.yml to fabrica.yaml; Fabrica's file names are fixed"
		}
		t.diags = diags
		return t
	}
	t.manifest = m

	lock := lockOverride
	if lock == nil {
		var err error
		if lock, err = ReadLock(src.Dir); err != nil {
			t.diags.Errorf("lock.invalid", LockFile, "%v", err)
		}
	}
	t.lock = lock
	if len(m.Imports) > 0 && lock == nil {
		t.diags.Errorf("lock.missing", LockFile, "this blueprint has imports but no %s", LockFile).
			Hintf("run `fabrica lock` to pin every import to an exact commit")
	}
	if lock != nil {
		for alias := range lock.Imports {
			if _, ok := m.Import(alias); !ok {
				t.diags.Errorf("lock.stale", LockFile, "%s pins import %q, which isn't in %s anymore", LockFile, alias, ManifestFile).
					Hintf("run `fabrica lock` to update %s", LockFile)
			}
		}
	}

	local, localDiags := loadItems(src.Dir)
	t.diags = append(t.diags, localDiags...)

	// Resolve each import and apply its filter.
	filtered := map[string]map[names.Ref]item{}
	for _, spec := range m.Imports {
		pin, ok := LockedImport{}, false
		if lock != nil {
			pin, ok = lock.Imports[spec.Alias]
		}
		if !ok || !pin.Matches(spec) {
			if lock != nil {
				t.diags.Errorf("lock.stale", ManifestFile, "import %q changed since %s was written (or was never locked)", spec.Alias, LockFile).
					Hintf("run `fabrica lock` to pin it")
			}
			continue
		}
		sub := l.loadImport(ctx, spec, pin.Commit, stack)
		t.diags = append(t.diags, sub.diags.Prefixed("import "+spec.Alias)...)
		imp := &Import{Spec: spec, Commit: pin.Commit, IsBlueprint: sub.isBlueprint, offered: sub.items}
		if sub.manifest != nil {
			imp.Expects = sub.manifest.Expects
		}
		t.imports = append(t.imports, imp)

		for _, ref := range append(append([]names.Ref(nil), spec.Only...), spec.Exclude...) {
			if _, ok := sub.items[ref]; !ok && len(sub.diags.Only(diag.Error)) == 0 {
				t.diags.Errorf("import.unknown-item", ManifestFile, "import %q doesn't have %s, but its only:/exclude: lists it", spec.Alias, ref).
					Hintf("check the spelling; the import has %s", summarizeRefs(sub.items, ref.Kind))
			}
		}
		keep := map[names.Ref]item{}
		for ref, it := range sub.items {
			if spec.keeps(ref) {
				it = it.withOrigin(it.info().Origin.Prepend(spec.Alias))
				if r, ok := it.(*Routine); ok {
					it = r.withDefaults(spec.Params)
				}
				keep[ref] = it
			}
		}
		if len(sub.diags.Only(diag.Error)) == 0 {
			t.diags = append(t.diags, checkImportParams(spec, sub.items)...)
		}
		filtered[spec.Alias] = keep
	}

	// Merge: an item may come from only one place.
	offeredBy := map[names.Ref][]string{}
	for _, spec := range m.Imports {
		for ref := range filtered[spec.Alias] {
			offeredBy[ref] = append(offeredBy[ref], spec.Alias)
		}
	}
	for ref, aliases := range offeredBy {
		if len(aliases) > 1 {
			sort.Strings(aliases)
			t.diags.Errorf("import.collision", ManifestFile, "%s comes from more than one import: %s", ref, strings.Join(quoteAll(aliases), " and ")).
				Hintf("keep it from just one of them: add %s to the others' exclude:, or leave it out of their only:", ref)
		}
		t.items[ref] = filtered[aliases[0]][ref]
	}
	for ref, it := range local {
		if aliases := offeredBy[ref]; len(aliases) > 0 {
			t.diags.Errorf("import.collision", it.info().File, "%s is defined here, and import %s brings one too", ref, strings.Join(quoteAll(aliases), " and ")).
				Hintf("to replace the imported one with yours, add %s to the exclude: list of import %s", ref, aliases[0])
		}
		for _, spec := range m.Imports {
			if spec.Excludes(ref) {
				if imp := findImport(t.imports, spec.Alias); imp != nil {
					if _, upstream := imp.offered[ref]; upstream {
						t.overrides = append(t.overrides, Override{Ref: ref, Alias: spec.Alias})
					}
				}
			}
		}
		t.items[ref] = it
	}
	sort.Slice(t.overrides, func(i, j int) bool { return t.overrides[i].Ref.String() < t.overrides[j].Ref.String() })
	return t
}

// checkImportParams makes sure every param an import sets is declared by a
// routine it brings, after only: and exclude:. A typo, or a param of a
// routine you left out, would otherwise do nothing at all.
func checkImportParams(spec ImportSpec, offered map[names.Ref]item) diag.List {
	var diags diag.List
	declared := map[string]bool{}
	leftOut := map[string]names.Ref{} // param → a routine the filter leaves out that declares it
	for _, ref := range sortedRefs(offered) {
		r, ok := offered[ref].(*Routine)
		if !ok {
			continue
		}
		kept := spec.keeps(ref)
		if kept && len(r.Problems) > 0 {
			return nil // its params may be half-read, and its problems are errors anyway
		}
		for _, p := range r.Params {
			if kept {
				declared[p.Name] = true
			} else if _, seen := leftOut[p.Name]; !seen {
				leftOut[p.Name] = ref
			}
		}
	}
	for _, name := range sortedKeys(spec.Params) {
		if declared[name] {
			continue
		}
		var hint string
		switch ref, ok := leftOut[name]; {
		case ok && spec.Excludes(ref):
			hint = fmt.Sprintf("%s declares it, but exclude: leaves it out; if you replaced it with your own, set the default in your %s", ref, fileFor(ref))
		case ok:
			hint = fmt.Sprintf("%s declares it, but it isn't in the import's only: list", ref)
		case len(declared) == 0:
			hint = "the routines this import brings declare no params; remove it"
		default:
			hint = "check the spelling; the routines this import brings declare " + listSome(sortedKeys(declared))
		}
		diags.Errorf("import.unknown-param", ManifestFile, "import %q sets param %q, but none of the routines it brings declares it", spec.Alias, name).
			Hintf("%s", hint)
	}
	return diags
}

// loadImport reads one import at a pinned commit: a blueprint if its folder
// has a fabrica.yaml, a plain repo otherwise.
func (l *Loader) loadImport(ctx context.Context, spec ImportSpec, commit string, stack []string) *tree {
	url := repourl.GitURL(spec.Git)
	key := url + "@" + commit + ":" + spec.Path
	for _, k := range stack {
		if k == key {
			t := &tree{items: map[names.Ref]item{}}
			t.diags.Errorf("import.cycle", "", "importing %s@%s leads back to itself", spec.Git, gitx.Short(commit)).
				Hintf("blueprints can't import each other in a circle; remove one of the imports")
			return t
		}
	}
	if len(stack) >= maxImportDepth {
		t := &tree{items: map[names.Ref]item{}}
		t.diags.Errorf("import.too-deep", "", "imports are nested more than %d levels deep", maxImportDepth)
		return t
	}

	l.mu.Lock()
	if t, ok := l.memo[key]; ok {
		l.mu.Unlock()
		return t
	}
	l.mu.Unlock()

	t := l.readImport(ctx, spec, url, commit, append(append([]string(nil), stack...), key))

	l.mu.Lock()
	if l.memo == nil || len(l.memo) > 512 {
		l.memo = map[string]*tree{} // keep a long-running server's memory in check
	}
	l.memo[key] = t
	l.mu.Unlock()
	return t
}

func (l *Loader) readImport(ctx context.Context, spec ImportSpec, url, commit string, stack []string) *tree {
	root, err := l.Git.Remote(url, gitx.Full).Tree(ctx, commit)
	if err != nil {
		t := &tree{items: map[names.Ref]item{}}
		t.diags.Errorf("import.fetch", "", "%v", err)
		return t
	}
	dir := filepath.Join(root, filepath.FromSlash(spec.Path))
	if info, err := os.Stat(dir); spec.Path != "" && (err != nil || !info.IsDir()) {
		t := &tree{items: map[names.Ref]item{}}
		t.diags.Errorf("import.path", "", "there's no folder %q in %s at %s", spec.Path, spec.Git, gitx.Short(commit))
		return t
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err == nil {
		return l.build(ctx, Source{Dir: dir, Git: spec.Git, Commit: commit, Path: spec.Path}, nil, stack)
	}
	return loadPlain(root, spec.Path)
}

// loadPlain reads a repo that isn't a blueprint: its skills from
// <path>/*/SKILL.md (path defaults to "skills"), its Cursor rules from
// .cursor/rules/*.mdc, and a root AGENTS.md as the always-on rule
// rules/agents-md.
func loadPlain(root, skillsPath string) *tree {
	t := &tree{items: map[names.Ref]item{}}
	if skillsPath == "" {
		skillsPath = "skills"
	}
	skillsDir := filepath.Join(root, filepath.FromSlash(skillsPath))
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(skillsDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			continue
		}
		s := parseSkill(dir, e.Name(), path.Join(skillsPath, e.Name()))
		t.items[s.Ref] = s
	}
	rulesDir := filepath.Join(root, ".cursor", "rules")
	ruleEntries, _ := os.ReadDir(rulesDir)
	for _, e := range ruleEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mdc") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rulesDir, e.Name()))
		if err != nil {
			continue
		}
		r := parseRule(data, strings.TrimSuffix(e.Name(), ".mdc"), ".cursor/rules/"+e.Name())
		t.items[r.Ref] = r
	}
	if data, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err == nil && strings.TrimSpace(string(data)) != "" {
		r := &Rule{
			ItemInfo:    ItemInfo{Ref: names.Ref{Kind: names.Rules, Name: "agents-md"}, File: "AGENTS.md", Hash: hashBytes(data)},
			Description: "The AGENTS.md of an imported repo",
			AlwaysApply: true,
			Body:        strings.TrimSpace(string(data)),
		}
		t.items[r.Ref] = r
	}
	if len(t.items) == 0 {
		t.diags.Errorf("import.empty", "", "this repo has no %s, no skills at %s/*/SKILL.md, no .cursor/rules/*.mdc and no AGENTS.md, so there's nothing to import", ManifestFile, skillsPath).
			Hintf("check the import's git: and path:")
	}
	return t
}

// loadItems reads the items defined directly in a blueprint folder.
func loadItems(dir string) (map[names.Ref]item, diag.List) {
	items := map[names.Ref]item{}
	var diags diag.List
	add := func(it item) { items[it.info().Ref] = it }

	readDir := func(sub string) []fs.DirEntry {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			diags.Errorf("blueprint.unreadable", sub, "%v", err)
		}
		return entries
	}

	for _, e := range readDir("skills") {
		file := "skills/" + e.Name()
		switch {
		case isSymlink(e):
			diags.Errorf("skill.symlink", file, "skill folders can't be symlinks; Fabrica serves skills exactly as they are in git")
		case !e.IsDir():
			diags.Warnf("blueprint.stray-file", file, "isn't a skill folder, so Fabrica ignores it (skills live in skills/<name>/SKILL.md)")
		default:
			add(parseSkill(filepath.Join(dir, "skills", e.Name()), e.Name(), file))
		}
	}
	for _, e := range readDir("routines") {
		file := "routines/" + e.Name()
		switch {
		case e.IsDir() || !strings.HasSuffix(e.Name(), ".md"):
			diags.Warnf("blueprint.stray-file", file, "isn't a routine, so Fabrica ignores it (routines are routines/<name>.md files)")
		default:
			data, err := os.ReadFile(filepath.Join(dir, "routines", e.Name()))
			if err != nil {
				diags.Errorf("blueprint.unreadable", file, "%v", err)
				continue
			}
			add(parseRoutine(data, strings.TrimSuffix(e.Name(), ".md"), file))
		}
	}
	for _, e := range readDir("rules") {
		file := "rules/" + e.Name()
		switch {
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".md"):
			diags.Errorf("rule.extension", file, "rules use Cursor's .mdc extension").Hintf("rename it to rules/%s.mdc", strings.TrimSuffix(e.Name(), ".md"))
		case e.IsDir() || !strings.HasSuffix(e.Name(), ".mdc"):
			diags.Warnf("blueprint.stray-file", file, "isn't a rule, so Fabrica ignores it (rules are rules/<name>.mdc files)")
		default:
			data, err := os.ReadFile(filepath.Join(dir, "rules", e.Name()))
			if err != nil {
				diags.Errorf("blueprint.unreadable", file, "%v", err)
				continue
			}
			add(parseRule(data, strings.TrimSuffix(e.Name(), ".mdc"), file))
		}
	}
	for _, e := range readDir("connectors") {
		file := "connectors/" + e.Name()
		switch {
		case !e.IsDir() && strings.HasSuffix(e.Name(), ".yml"):
			diags.Errorf("connector.extension", file, "connector files use the .yaml extension").Hintf("rename it to connectors/%s.yaml", strings.TrimSuffix(e.Name(), ".yml"))
		case e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml"):
			diags.Warnf("blueprint.stray-file", file, "isn't a connector, so Fabrica ignores it (connectors are connectors/<name>.yaml files)")
		default:
			data, err := os.ReadFile(filepath.Join(dir, "connectors", e.Name()))
			if err != nil {
				diags.Errorf("blueprint.unreadable", file, "%v", err)
				continue
			}
			add(parseConnector(data, strings.TrimSuffix(e.Name(), ".yaml"), file))
		}
	}
	for _, e := range readDir("providers") {
		file := "providers/" + e.Name()
		if !e.IsDir() || isSymlink(e) {
			diags.Warnf("blueprint.stray-file", file, "isn't a provider folder, so Fabrica ignores it (providers live in providers/<name>/provider.yaml)")
			continue
		}
		add(loadProviderItem(filepath.Join(dir, "providers", e.Name()), e.Name(), file))
	}
	if info, err := os.Stat(filepath.Join(dir, "evals")); err == nil && info.IsDir() {
		diags.Warnf("blueprint.evals", "evals/", "evals/ is reserved for skill and routine tests in a future version; v0.1 ignores it")
	}
	return items, diags
}

func loadProviderItem(dir, name, file string) *ProviderItem {
	p := &ProviderItem{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Providers, Name: name}, File: file}}
	if err := names.Validate(name); err != nil {
		p.Problems.Errorf("provider.invalid", file, "the folder name isn't a valid provider name: %v", err)
		return p
	}
	hash, _, err := hashTree(dir)
	if err != nil {
		p.Problems.Errorf("provider.invalid", file, "%v", err)
		return p
	}
	p.Hash = hash
	prov, err := provider.LoadFolder(name, dir)
	if err != nil {
		p.Problems.Errorf("provider.invalid", file, "%v", err)
		return p
	}
	p.Provider = prov
	return p
}

func findImport(imports []*Import, alias string) *Import {
	for _, imp := range imports {
		if imp.Spec.Alias == alias {
			return imp
		}
	}
	return nil
}

func sortedRefs(m map[names.Ref]item) []names.Ref {
	refs := make([]names.Ref, 0, len(m))
	for ref := range m {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}

// summarizeRefs lists the names of one kind, for "did you mean" hints.
func summarizeRefs(m map[names.Ref]item, kind names.Kind) string {
	var out []string
	for _, ref := range sortedRefs(m) {
		if ref.Kind == kind {
			out = append(out, ref.String())
		}
	}
	if len(out) == 0 {
		return "no " + string(kind)
	}
	return listSome(out)
}

// listSome joins up to 8 names, and says how many more there are.
func listSome(list []string) string {
	if len(list) > 8 {
		return strings.Join(list[:8], ", ") + fmt.Sprintf(" and %d more", len(list)-8)
	}
	return strings.Join(list, ", ")
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
