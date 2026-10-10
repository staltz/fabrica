// Package names handles the names of factory items and the way you refer to
// them.
//
// Every item in a factory (a skill, a routine, a rule, a connector or a
// provider) gets its name from its file or folder name. You refer to an item
// by kind and name, exactly like its path: `skills/pr-review`,
// `routines/pr-review`, `rules/tests-first`. Each kind has its own namespace,
// so a skill and a routine can both be called `pr-review`.
package names

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind is one of the kinds of item a factory can contain.
type Kind string

const (
	Skills     Kind = "skills"
	Routines   Kind = "routines"
	Rules      Kind = "rules"
	Connectors Kind = "connectors"
	Providers  Kind = "providers"
)

// Kinds lists every kind, in the order we like to print them.
var Kinds = []Kind{Skills, Routines, Rules, Connectors, Providers}

// Singular returns "skill" for Skills, "routine" for Routines and so on.
func (k Kind) Singular() string { return strings.TrimSuffix(string(k), "s") }

// Valid reports whether k is a kind we know.
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// Ref points at one item, like `skills/pr-review`.
type Ref struct {
	Kind Kind
	Name string
}

func (r Ref) String() string { return string(r.Kind) + "/" + r.Name }

// ParseRef parses `skills/pr-review` into a Ref.
func ParseRef(s string) (Ref, error) {
	kind, name, ok := strings.Cut(s, "/")
	if !ok || name == "" {
		return Ref{}, fmt.Errorf("%q isn't an item reference; write it as kind/name, like skills/pr-review", s)
	}
	k := Kind(kind)
	if !k.Valid() {
		return Ref{}, fmt.Errorf("%q has an unknown kind %q; use one of skills, routines, rules, connectors or providers", s, kind)
	}
	if strings.Contains(name, "/") {
		return Ref{}, fmt.Errorf("%q has too many slashes; write it as kind/name, like skills/pr-review", s)
	}
	return Ref{Kind: k, Name: name}, nil
}

// nameRe is the naming rule from the Agent Skills spec: lowercase letters,
// digits and single hyphens, not starting or ending with a hyphen. We use the
// same rule for every kind of item so there's only one rule to remember.
var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// MaxLen is the longest name we accept (also from the Agent Skills spec).
const MaxLen = 64

// Validate checks that name follows the naming rule.
func Validate(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("the name is empty")
	case len(name) > MaxLen:
		return fmt.Errorf("the name %q is longer than %d characters", name, MaxLen)
	case !nameRe.MatchString(name):
		return fmt.Errorf("the name %q isn't valid; use lowercase letters, digits and single hyphens (like pr-review)", name)
	}
	return nil
}

// Optional splits the trailing `?` off names like `slack?`, which is how
// lists such as `connectors:` and `expects:` mark something as optional.
func Optional(s string) (name string, optional bool) {
	if strings.HasSuffix(s, "?") {
		return strings.TrimSuffix(s, "?"), true
	}
	return s, false
}

// MaybeName is an entry in a list that allows optional items, like
// `connectors: [github, slack?]`.
type MaybeName struct {
	Name     string
	Optional bool
}

func (m MaybeName) String() string {
	if m.Optional {
		return m.Name + "?"
	}
	return m.Name
}

// ParseMaybeNames parses a list like [github, slack?]. It complains about
// invalid names and about the same name showing up twice.
func ParseMaybeNames(list []string) ([]MaybeName, error) {
	seen := map[string]bool{}
	out := make([]MaybeName, 0, len(list))
	for _, raw := range list {
		name, opt := Optional(strings.TrimSpace(raw))
		if err := Validate(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("%q is listed twice", name)
		}
		seen[name] = true
		out = append(out, MaybeName{Name: name, Optional: opt})
	}
	return out, nil
}
