package factory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/repourl"
)

// PinChange is one import whose pinned commit changed (or got pinned for
// the first time).
type PinChange struct {
	Alias string
	Ref   string
	// Old is the previous commit, or "" for a new import.
	Old string
	New string
	// Diff lists what changed in the import between the two commits.
	Diff ItemDiff
}

// ItemDiff is what changed in an import between two commits.
type ItemDiff struct {
	Added   []names.Ref
	Removed []names.Ref
	Changed []names.Ref
	// Overridden are changed items that you replaced with your own. You
	// probably want to look at what upstream changed and decide whether
	// your version needs the same change.
	Overridden []names.Ref
	// Excluded are changed items you don't use anyway.
	Excluded []names.Ref
	// Providers are added or changed folder providers. Providers can run
	// code with your runtime credentials during apply, so new provider
	// code deserves a careful look.
	Providers []names.Ref
}

// Empty reports whether nothing changed.
func (d ItemDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// Lock pins every import of the factory in dir to an exact commit.
//
// Imports that are already pinned (and whose git, ref and path haven't
// changed) keep their commit: locking never moves a pin on its own. Use
// Upgrade for that. The returned lock is checked by loading the factory
// with it; the caller should only write it when there are no errors.
func (l *Loader) Lock(ctx context.Context, dir string) (*Lock, []PinChange, *Factory, diag.List) {
	m, diags := ReadManifest(dir)
	if m == nil {
		return nil, nil, nil, diags
	}
	old, err := ReadLock(dir)
	if err != nil {
		diags.Warnf("lock.invalid", LockFile, "%v; re-pinning every import", err)
		old = nil
	}
	newLock := &Lock{Imports: map[string]LockedImport{}}
	var changes []PinChange
	for _, spec := range m.Imports {
		if old != nil {
			if pin, ok := old.Imports[spec.Alias]; ok && pin.Matches(spec) {
				newLock.Imports[spec.Alias] = pin
				continue
			}
		}
		resolved, err := l.Git.Remote(repourl.GitURL(spec.Git), gitx.Full).Resolve(ctx, spec.Ref)
		if err != nil {
			diags.Errorf("lock.resolve", ManifestFile, "import %q: %v", spec.Alias, err)
			continue
		}
		newLock.Imports[spec.Alias] = LockedImport{Git: spec.Git, Ref: spec.Ref, Path: spec.Path, Commit: resolved.Commit}
		change := PinChange{Alias: spec.Alias, Ref: spec.Ref, New: resolved.Commit}
		if old != nil {
			if pin, ok := old.Imports[spec.Alias]; ok {
				change.Old = pin.Commit
			}
		}
		changes = append(changes, change)
	}
	if diags.HasErrors() {
		return nil, changes, nil, diags
	}
	f, loadDiags := l.LoadDir(ctx, dir, LoadOptions{Lock: newLock})
	diags = append(diags, loadDiags...)
	if f != nil {
		diags = append(diags, f.Lint()...)
		l.fillDiffs(ctx, m, f, changes)
	}
	return newLock, changes, f, diags.Dedup()
}

// Upgrade moves imports to the latest commit of their ref (all imports, or
// only the given aliases) and reports what changed in each. Like Lock, it
// returns the new lock for the caller to write once it's happy.
func (l *Loader) Upgrade(ctx context.Context, dir string, aliases []string) (*Lock, []PinChange, *Factory, diag.List) {
	m, diags := ReadManifest(dir)
	if m == nil {
		return nil, nil, nil, diags
	}
	old, err := ReadLock(dir)
	if err != nil {
		diags.Errorf("lock.invalid", LockFile, "%v", err).Hintf("run `fabrica lock` first")
		return nil, nil, nil, diags
	}
	if old == nil && len(m.Imports) > 0 {
		diags.Errorf("lock.missing", LockFile, "there's no %s yet", LockFile).Hintf("run `fabrica lock` first")
		return nil, nil, nil, diags
	}
	selected := map[string]bool{}
	for _, a := range aliases {
		if _, ok := m.Import(a); !ok {
			diags.Errorf("upgrade.unknown-import", ManifestFile, "there's no import called %q", a)
		}
		selected[a] = true
	}
	if diags.HasErrors() {
		return nil, nil, nil, diags
	}
	newLock := &Lock{Imports: map[string]LockedImport{}}
	var changes []PinChange
	for _, spec := range m.Imports {
		pin, ok := LockedImport{}, false
		if old != nil {
			pin, ok = old.Imports[spec.Alias]
		}
		if !ok || !pin.Matches(spec) {
			diags.Errorf("lock.stale", ManifestFile, "import %q changed since %s was written", spec.Alias, LockFile).
				Hintf("run `fabrica lock` first, then upgrade")
			continue
		}
		if len(selected) > 0 && !selected[spec.Alias] {
			newLock.Imports[spec.Alias] = pin
			continue
		}
		resolved, err := l.Git.Remote(repourl.GitURL(spec.Git), gitx.Full).Resolve(ctx, spec.Ref)
		if err != nil {
			diags.Errorf("lock.resolve", ManifestFile, "import %q: %v", spec.Alias, err)
			continue
		}
		pin.Commit = resolved.Commit
		newLock.Imports[spec.Alias] = pin
		if resolved.Commit != old.Imports[spec.Alias].Commit {
			changes = append(changes, PinChange{Alias: spec.Alias, Ref: spec.Ref, Old: old.Imports[spec.Alias].Commit, New: resolved.Commit})
		}
	}
	if diags.HasErrors() {
		return nil, changes, nil, diags
	}
	f, loadDiags := l.LoadDir(ctx, dir, LoadOptions{Lock: newLock})
	diags = append(diags, loadDiags...)
	if f != nil {
		diags = append(diags, f.Lint()...)
		l.fillDiffs(ctx, m, f, changes)
	}
	return newLock, changes, f, diags.Dedup()
}

// fillDiffs works out the item diff of every pin change.
func (l *Loader) fillDiffs(ctx context.Context, m *Manifest, f *Factory, changes []PinChange) {
	for i := range changes {
		c := &changes[i]
		spec, _ := m.Import(c.Alias)
		var before map[names.Ref]item
		if c.Old != "" {
			before = l.loadImport(ctx, spec, c.Old, nil).items
		}
		after := l.loadImport(ctx, spec, c.New, nil).items
		c.Diff = diffItems(before, after, spec, f)
	}
}

func diffItems(before, after map[names.Ref]item, spec ImportSpec, f *Factory) ItemDiff {
	var d ItemDiff
	overridden := map[names.Ref]bool{}
	for _, o := range f.Overrides {
		if o.Alias == spec.Alias {
			overridden[o.Ref] = true
		}
	}
	for _, ref := range sortedRefs(after) {
		prev, existed := before[ref]
		switch {
		case !existed:
			d.Added = append(d.Added, ref)
		case prev.info().Hash != after[ref].info().Hash:
			d.Changed = append(d.Changed, ref)
			switch {
			case overridden[ref]:
				d.Overridden = append(d.Overridden, ref)
			case !spec.keeps(ref):
				d.Excluded = append(d.Excluded, ref)
			}
		default:
			continue
		}
		if ref.Kind == names.Providers && spec.keeps(ref) {
			d.Providers = append(d.Providers, ref)
		}
	}
	for _, ref := range sortedRefs(before) {
		if _, ok := after[ref]; !ok {
			d.Removed = append(d.Removed, ref)
		}
	}
	return d
}

// FormatChanges renders pin changes for people to read, like:
//
//	base: v1.2 9c1e4b2 → 1f0a3d7
//	  + skills/changelog
//	  ~ skills/pr-review   (you override this; check what upstream changed)
//	  - rules/old-rule
func FormatChanges(changes []PinChange) string {
	if len(changes) == 0 {
		return "No import changed.\n"
	}
	var b strings.Builder
	for _, c := range changes {
		from := "new import"
		if c.Old != "" {
			from = gitx.Short(c.Old)
		}
		fmt.Fprintf(&b, "%s (%s): %s → %s\n", c.Alias, c.Ref, from, gitx.Short(c.New))
		if c.Diff.Empty() {
			b.WriteString("  (no item changed)\n")
			continue
		}
		notes := map[names.Ref]string{}
		for _, r := range c.Diff.Overridden {
			notes[r] = "you override this; check whether your version needs the same change"
		}
		for _, r := range c.Diff.Excluded {
			notes[r] = "you exclude this"
		}
		for _, r := range c.Diff.Providers {
			notes[r] = "provider code can run with your runtime credentials during apply; review it"
		}
		lines := map[string]names.Ref{}
		var keys []string
		add := func(sym string, refs []names.Ref) {
			for _, r := range refs {
				k := r.String() + sym
				lines[k] = r
				keys = append(keys, k)
			}
		}
		add("+", c.Diff.Added)
		add("~", c.Diff.Changed)
		add("-", c.Diff.Removed)
		sort.Strings(keys)
		for _, k := range keys {
			r := lines[k]
			sym := k[len(k)-1:]
			if note := notes[r]; note != "" {
				fmt.Fprintf(&b, "  %s %s   (%s)\n", sym, r, note)
			} else {
				fmt.Fprintf(&b, "  %s %s\n", sym, r)
			}
		}
	}
	return b.String()
}

// Drift is an override whose upstream version changed since the lock was
// written.
type Drift struct {
	Override
	// ImportRef is the import's ref, like "main" or "v1".
	ImportRef string
	Locked    string
	Latest    string
}

// OverrideDrift checks, for every item this factory overrides, whether the
// import's ref has moved on and changed that item upstream. `fabrica plan`
// uses it to remind you about overrides that may need attention.
func (l *Loader) OverrideDrift(ctx context.Context, f *Factory) ([]Drift, error) {
	var out []Drift
	for _, o := range f.Overrides {
		imp := findImport(f.Imports, o.Alias)
		if imp == nil {
			continue
		}
		resolved, err := l.Git.Remote(repourl.GitURL(imp.Spec.Git), gitx.Full).Resolve(ctx, imp.Spec.Ref)
		if err != nil {
			return nil, err
		}
		if resolved.Commit == imp.Commit {
			continue
		}
		latest := l.loadImport(ctx, imp.Spec, resolved.Commit, nil).items
		now, okNow := latest[o.Ref]
		then, okThen := imp.offered[o.Ref]
		if okNow != okThen || okNow && now.info().Hash != then.info().Hash {
			out = append(out, Drift{Override: o, ImportRef: imp.Spec.Ref, Locked: imp.Commit, Latest: resolved.Commit})
		}
	}
	return out, nil
}
