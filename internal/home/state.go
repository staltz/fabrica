package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/yamlx"
)

// State is state/<deployment>.yaml: everything one cloud deployment owns
// in its runtimes. Only Fabrica writes it, and it's committed to the home
// repo, so it's versioned and shared like everything else.
type State struct {
	// Lock is only present while an apply (or confirm) is running.
	Lock *Lock `yaml:"lock,omitempty"`
	// Factory is the factory the last apply used. Fabrica needs it to clean
	// up after a deleted deployment whose runtime came from a folder
	// provider in that factory.
	Factory *StateFactory `yaml:"factory,omitempty"`
	// Resources are the stubs installed in runtimes.
	Resources []Resource `yaml:"resources"`
	// Pending are handoffs waiting for `fabrica confirm`.
	Pending []Pending `yaml:"pending"`
}

// Lock says who's applying this deployment right now.
type Lock struct {
	Holder string    `yaml:"holder"`
	Since  time.Time `yaml:"since"`
}

// StateFactory records a factory at an exact commit.
type StateFactory struct {
	Git    string `yaml:"git"`
	Path   string `yaml:"path,omitempty"`
	Commit string `yaml:"commit"`
}

// Shape is the part of a stub's shape that state records: enough to show
// what's installed and its fingerprint.
type Shape struct {
	Trigger     string   `yaml:"trigger"`
	Cron        string   `yaml:"cron,omitempty"`
	Connectors  []string `yaml:"connectors"`
	Memory      string   `yaml:"memory,omitempty"`
	Fingerprint string   `yaml:"fingerprint"`
}

// Resource is one stub installed in one runtime.
type Resource struct {
	ID      string `yaml:"id"`
	Runtime string `yaml:"runtime"`
	Routine string `yaml:"routine"`
	Repo    string `yaml:"repo"`
	// RemoteID is the runtime's own ID for the resource. Empty means the
	// resource is unverifiable: it was confirmed after a handoff without an
	// ID, so Fabrica can't check on it or delete it automatically.
	RemoteID      string `yaml:"remote_id,omitempty"`
	Mode          string `yaml:"mode"`
	FactoryCommit string `yaml:"factory_commit"`
	// TokenHash fingerprints the token baked into the resource, so `plan`
	// notices a rotated server secret.
	TokenHash string    `yaml:"token_hash,omitempty"`
	Shape     Shape     `yaml:"shape"`
	AppliedAt time.Time `yaml:"applied_at"`
	// RenamedFrom is the runtime's old name, when an earlier Fabrica release
	// recorded the resource under it. It isn't saved.
	RenamedFrom string `yaml:"-"`
}

// Unverifiable reports whether Fabrica can't check on the resource.
func (r Resource) Unverifiable() bool { return r.RemoteID == "" }

// Pending is a handoff that was issued but not confirmed yet.
type Pending struct {
	ID       string `yaml:"id"`
	Action   string `yaml:"action"`
	Resource string `yaml:"resource"`
	Runtime  string `yaml:"runtime"`
	Routine  string `yaml:"routine"`
	Repo     string `yaml:"repo"`
	// RemoteID is the resource's ID for updates and deletes, when known.
	RemoteID string `yaml:"remote_id,omitempty"`
	// Shape is what the handoff installs (creates and updates).
	Shape         *Shape    `yaml:"shape,omitempty"`
	TokenHash     string    `yaml:"token_hash,omitempty"`
	FactoryCommit string    `yaml:"factory_commit,omitempty"`
	IssuedAt      time.Time `yaml:"issued_at"`
}

// Key identifies a resource within a deployment: the same identity can
// exist once per runtime.
type Key struct {
	Runtime string
	ID      string
}

// Key returns the resource's key.
func (r Resource) Key() Key { return Key{r.Runtime, r.ID} }

// Key returns the key of the resource a pending handoff is about.
func (p Pending) Key() Key { return Key{p.Runtime, p.Resource} }

// StatePath is the path of a deployment's state file inside a home repo.
func StatePath(name string) string { return filepath.Join(StateDir, name+".yaml") }

const stateHeader = "# Written by Fabrica. Don't edit by hand.\n# It records what this deployment installed in each runtime.\n"

// ReadState reads a deployment's state. A missing file is an empty state.
func ReadState(homeDir, name string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(homeDir, StatePath(name)))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := yamlx.DecodeStrict(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %v (only Fabrica should write this file; if someone edited it by hand, restore it from git)", StatePath(name), err)
	}
	moveRenamedRuntimes(&s)
	return &s, nil
}

// moveRenamedRuntimes moves what earlier Fabrica releases recorded under
// runtimes' old names over to their current names. The stubs it installed still name the
// old runtime in their prompts, so their fingerprints are dropped: that
// makes plan update them, even if this state gets saved before an apply.
func moveRenamedRuntimes(s *State) {
	for i := range s.Resources {
		r := &s.Resources[i]
		if current, ok := provider.Renamed(r.Runtime); ok {
			r.RenamedFrom, r.Runtime, r.Shape.Fingerprint = r.Runtime, current, ""
		}
	}
	for i := range s.Pending {
		p := &s.Pending[i]
		if current, ok := provider.Renamed(p.Runtime); ok {
			p.Runtime = current
			if p.Shape != nil {
				p.Shape.Fingerprint = ""
			}
		}
	}
}

// WriteState writes a deployment's state, sorted so diffs stay small.
func WriteState(homeDir, name string, s *State) error {
	sort.Slice(s.Resources, func(i, j int) bool { return keyLess(s.Resources[i].Key(), s.Resources[j].Key()) })
	sort.Slice(s.Pending, func(i, j int) bool { return keyLess(s.Pending[i].Key(), s.Pending[j].Key()) })
	if s.Resources == nil {
		s.Resources = []Resource{}
	}
	if s.Pending == nil {
		s.Pending = []Pending{}
	}
	data, err := yamlx.Marshal(s)
	if err != nil {
		return err
	}
	p := filepath.Join(homeDir, StatePath(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(stateHeader), data...), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func keyLess(a, b Key) bool {
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	return a.Runtime < b.Runtime
}

// Empty reports whether the state records nothing at all.
func (s *State) Empty() bool {
	return s.Lock == nil && len(s.Resources) == 0 && len(s.Pending) == 0
}

// FindPending looks up a pending handoff by ID.
func (s *State) FindPending(id string) (int, bool) {
	for i, p := range s.Pending {
		if p.ID == id {
			return i, true
		}
	}
	return -1, false
}

// FindResource looks up a resource by key.
func (s *State) FindResource(k Key) (int, bool) {
	for i, r := range s.Resources {
		if r.Key() == k {
			return i, true
		}
	}
	return -1, false
}
