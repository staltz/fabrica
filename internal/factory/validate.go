package factory

import (
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/names"
)

// validate checks the merged factory as a whole. Items are only checked
// here, at the top, because only here do we know which items survived
// everyone's only: and exclude: filters.
func (f *Factory) validate() diag.List {
	var diags diag.List

	// Broken items you actually use are errors.
	for _, it := range f.items() {
		for _, p := range it.info().Problems {
			if !it.info().Origin.Local() {
				p.Where = it.info().Where()
			}
			diags.Add(p)
		}
	}

	for _, name := range sortedKeys(f.Routines) {
		r := f.Routines[name]
		if len(r.Problems) > 0 {
			continue // already reported, and its fields may be half-filled
		}
		for _, s := range r.Skills {
			if _, ok := f.Skills[s]; !ok {
				ref := names.Ref{Kind: names.Skills, Name: s}
				diags.Errorf("routine.missing-skill", r.Where(), "the routine lists skill %q in skills:, but this factory has no %s", s, ref).
					Hintf("%s", f.howToGet(ref))
			}
		}
		for _, c := range r.Connectors {
			if _, ok := f.Connectors[c.Name]; !ok {
				ref := names.Ref{Kind: names.Connectors, Name: c.Name}
				diags.Errorf("routine.missing-connector", r.Where(), "the routine needs connector %q, but this factory has no %s", c.Name, ref).
					Hintf("%s", f.howToGet(ref))
			}
		}
	}

	// Each import's expects: list must be covered by ours: our list is the
	// whole contract this factory presents to product repos.
	ours := map[string]bool{}
	for _, e := range f.Manifest.Expects {
		ours[e.Name] = true
	}
	for _, imp := range f.Imports {
		for _, e := range imp.Expects {
			if !ours[e.Name] {
				diags.Warnf("expects.uncovered", ManifestFile, "import %q expects product repos to provide %q, but your expects: doesn't list it", imp.Spec.Alias, e.Name).
					Hintf("add %s to expects: in %s (expects from imports aren't inherited)", e, ManifestFile)
			}
		}
	}
	return diags
}

// howToGet explains how to bring in a missing item, by looking at what
// each import offers before its filter.
func (f *Factory) howToGet(ref names.Ref) string {
	for _, imp := range f.Imports {
		if _, ok := imp.offered[ref]; !ok {
			continue
		}
		if imp.Spec.Only != nil {
			return fmt.Sprintf("import %q has it: add %s to its only: list (only: doesn't pull in what items need on its own)", imp.Spec.Alias, ref)
		}
		if imp.Spec.Excludes(ref) {
			return fmt.Sprintf("import %q has it, but its exclude: list leaves it out; take it out of exclude:, or define your own %s", imp.Spec.Alias, ref)
		}
	}
	return fmt.Sprintf("create %s, or import a factory that has it", fileFor(ref))
}

func fileFor(ref names.Ref) string {
	switch ref.Kind {
	case names.Skills:
		return "skills/" + ref.Name + "/SKILL.md"
	case names.Routines:
		return "routines/" + ref.Name + ".md"
	case names.Rules:
		return "rules/" + ref.Name + ".mdc"
	case names.Connectors:
		return "connectors/" + ref.Name + ".yaml"
	default:
		return "providers/" + ref.Name + "/provider.yaml"
	}
}

// Lint looks for things that are probably mistakes but might not be, and
// warns about them. It never returns errors.
//
//   - A skill's text mentions a skill that one of your imports has, but that
//     you didn't bring in.
//   - A routine's prompt mentions a factory skill it doesn't list in
//     skills:.
//
// Skills mention each other in prose ("use the `test` skill", "/changelog")
// and Fabrica doesn't make you declare those, so these are only warnings.
func (f *Factory) Lint() diag.List {
	var diags diag.List

	// Skills offered by imports that didn't make it into the factory.
	leftOut := map[string]string{} // skill name → import alias
	for _, imp := range f.Imports {
		for ref := range imp.offered {
			if ref.Kind == names.Skills {
				if _, ok := f.Skills[ref.Name]; !ok {
					if _, seen := leftOut[ref.Name]; !seen {
						leftOut[ref.Name] = imp.Spec.Alias
					}
				}
			}
		}
	}
	for _, name := range sortedKeys(f.Skills) {
		s := f.Skills[name]
		for _, other := range sortedKeys(leftOut) {
			if other != name && Mentions(s.Body, other) {
				diags.Warnf("skill.mentions-missing", s.Where(), "mentions %q, a skill in import %q that you didn't bring in", other, leftOut[other]).
					Hintf("if this skill needs it, bring in skills/%s from import %q", other, leftOut[other])
			}
		}
	}
	for _, name := range sortedKeys(f.Routines) {
		r := f.Routines[name]
		listed := map[string]bool{}
		for _, s := range r.Skills {
			listed[s] = true
		}
		for _, skill := range sortedKeys(f.Skills) {
			if !listed[skill] && Mentions(r.Prompt, skill) {
				diags.Warnf("routine.unlisted-skill", r.Where(), "the prompt mentions skill %q, but skills: doesn't list it", skill).
					Hintf("add %s to the routine's skills:", skill)
			}
		}
	}
	return diags
}

var (
	mentionMu    sync.Mutex
	mentionCache = map[string]*regexp.Regexp{}
)

// Mentions reports whether text refers to a skill by name, in one of the
// ways people usually do: `name` in backticks, a /name or $name command, or
// "the name skill".
func Mentions(text, name string) bool {
	mentionMu.Lock()
	defer mentionMu.Unlock()
	re, ok := mentionCache[name]
	if !ok {
		q := regexp.QuoteMeta(name)
		re = regexp.MustCompile("(?i)(`" + q + "`|(^|[\\s(\\[])[/$]" + q + "($|[^a-z0-9-])|\\b" + q + "\\s+skill\\b)")
		mentionCache[name] = re
	}
	return re.MatchString(text)
}

// items lists every item in the factory.
func (f *Factory) items() []item {
	var out []item
	for _, n := range sortedKeys(f.Skills) {
		out = append(out, f.Skills[n])
	}
	for _, n := range sortedKeys(f.Routines) {
		out = append(out, f.Routines[n])
	}
	for _, n := range sortedKeys(f.Rules) {
		out = append(out, f.Rules[n])
	}
	for _, n := range sortedKeys(f.Connectors) {
		out = append(out, f.Connectors[n])
	}
	for _, n := range sortedKeys(f.Providers) {
		out = append(out, f.Providers[n])
	}
	return out
}

// Item returns one item's info by ref, if the factory has it.
func (f *Factory) Item(ref names.Ref) (*ItemInfo, bool) {
	var it item
	var ok bool
	switch ref.Kind {
	case names.Skills:
		it, ok = lookupItem(f.Skills, ref.Name)
	case names.Routines:
		it, ok = lookupItem(f.Routines, ref.Name)
	case names.Rules:
		it, ok = lookupItem(f.Rules, ref.Name)
	case names.Connectors:
		it, ok = lookupItem(f.Connectors, ref.Name)
	case names.Providers:
		it, ok = lookupItem(f.Providers, ref.Name)
	}
	if !ok {
		return nil, false
	}
	return it.info(), true
}

func lookupItem[T item](m map[string]T, name string) (item, bool) {
	v, ok := m[name]
	return v, ok
}

// Usable reports whether the factory loaded without any problem in an item
// it uses. Callers that serve a factory check the load's diagnostics too.
func (f *Factory) Usable() bool {
	for _, it := range f.items() {
		if len(it.info().Problems) > 0 {
			return false
		}
	}
	return true
}

// SkillNames lists the factory's skills, sorted.
func (f *Factory) SkillNames() []string { return sortedKeys(f.Skills) }

// Expects reports whether the factory expects product repos to provide a
// skill with this name.
func (f *Factory) Expects(name string) bool {
	for _, e := range f.Manifest.Expects {
		if e.Name == name {
			return true
		}
	}
	return false
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SortedKeys is sortedKeys for other packages.
func SortedKeys[T any](m map[string]T) []string { return sortedKeys(m) }
