// Package plan works out what `fabrica apply` would do, and what stops it.
// With Env.Destroy, it works out what `fabrica destroy` would remove
// instead.
//
// A plan compares what your home repo and its blueprints ask for with what's
// installed, and sorts everything it finds into two kinds:
//
//   - Changes are work for apply: stubs to create, update or delete, and
//     handoffs to issue. Repairing a stub that went stale after a merge is
//     a change too.
//   - Errors are problems apply can't fix: a trigger a runtime doesn't
//     support, a product repo without the Fabrica line, a held lock, and so
//     on. Apply won't start on a factory that has any.
//
// The plan's exit code says which: 0 when there's nothing to do, 2 when
// there are only changes, and 1 when there are errors.
package plan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// Env is everything planning needs from the outside world.
type Env struct {
	Loader *blueprint.Loader
	Git    *gitx.Cache
	// Secret is the server secret from FABRICA_SERVER_SECRET, when it's
	// set. It counts for every server. Without a secret, the plan can't
	// tell whether tokens were rotated, and apply can't run.
	Secret string
	// ServerSecret, when Secret is empty, finds the secret of one server:
	// the one `fabrica auth login` saved for it. It returns "" when there's
	// none.
	ServerSecret func(server string) (string, error)
	// Binary is the absolute path of the fabrica binary, which local tools
	// run as their MCP server and hooks.
	Binary string
	// UserHome is your home folder, where local tools keep their config.
	UserHome string
	// SkipLocal skips local factories (CI has nothing local to set up).
	SkipLocal bool
	// Refresh looks resources up through runtime APIs (read-only) instead
	// of only trusting state. Apply always refreshes once it holds a lock.
	Refresh bool
	// Only limits the plan to these factories. Empty means all.
	Only []string
	// Destroy plans `fabrica destroy`: nothing is wanted, so everything
	// the factories installed becomes a delete, as if their files were
	// gone.
	Destroy bool
	// LockHolder is set by apply once it holds a factory's lock, so the
	// re-plan inside the lock doesn't report our own lock as a problem.
	LockHolder string
	// Now is the clock, replaceable in tests.
	Now func() time.Time
}

// SecretFor finds the secret of a factory's server: Secret when it's set,
// otherwise the server's own. It returns "" when there's none.
func (e *Env) SecretFor(server string) (string, error) {
	if e.Secret != "" || e.ServerSecret == nil || server == "" {
		return e.Secret, nil
	}
	return e.ServerSecret(server)
}

// Plan is the whole plan for a home repo.
type Plan struct {
	Home      *home.Home
	Factories []*Factory
	// Local is the plan for local tools on this machine. Nil when there are
	// no local factories or they were skipped.
	Local *LocalPlan
	// Diags are home-wide problems, like overlapping local factories.
	Diags diag.List
	// Notes are home-wide remarks, like "skipped local factories in CI".
	Notes []string
	// Destroy is true for a plan made for `fabrica destroy`.
	Destroy bool
}

// Factory is the plan for one factory.
type Factory struct {
	Name string
	// Spec is the factory file, or nil when it was deleted and only its
	// state is left.
	Spec      *home.Factory
	State     *home.State
	Blueprint *blueprint.Blueprint
	// Routines are the blueprint's routines the factory deploys: the ones
	// it names, then the ones its wildcard adds.
	Routines []*blueprint.Routine
	// Resolved is the blueprint ref resolved to a commit.
	Resolved gitx.Resolved
	Runtimes []*Runtime
	// Desired are the stubs that should exist.
	Desired []*Resource
	Changes []*Change
	// Pending are handoffs that are still waiting for `fabrica confirm`
	// and still match what's wanted.
	Pending []home.Pending
	// Init is what apply does about the factory's init session, when its
	// blueprint has an init routine that hasn't run yet.
	Init  *Init
	Diags diag.List
	Notes []string
	// Destroy is true when everything the factory installed is to go.
	Destroy bool
}

// HasWork reports whether apply has anything to do for the factory.
func (d *Factory) HasWork() bool { return len(d.Changes) > 0 || d.Init.Start() }

// Local reports whether this is a local factory.
func (d *Factory) Local() bool { return d.Spec != nil && d.Spec.Scope == provider.Local }

// Runtime is one runtime of a factory.
type Runtime struct {
	Name     string
	Provider *provider.Provider
	Mode     provider.ApplyMode
	// Evaluations are the factory's routines checked against this
	// runtime, by routine name.
	Evaluations map[string]*stub.Evaluation
}

// Resource is a stub that should exist: one routine, on one repo, in one
// runtime.
type Resource struct {
	Key         home.Key
	Factory     string
	Runtime     string
	Routine     string
	Repo        string
	Endpoint    string
	Eval        *stub.Evaluation
	Fingerprint string
	Prompt      string
	TokenHash   string
	Mode        provider.ApplyMode
	Provider    *provider.Provider
}

// Action is what a change does.
type Action string

const (
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
	// Adopt records a resource that exists in the runtime but is missing
	// from state, like one made by an apply that crashed before recording.
	Adopt Action = "adopt"
)

// Change is one piece of work for apply.
type Change struct {
	Action   Action
	Key      home.Key
	Routine  string
	Repo     string
	Mode     provider.ApplyMode
	Provider *provider.Provider
	// Desired is what to create or update to.
	Desired *Resource
	// Current is what's there now (for updates, deletes and adoptions).
	Current *home.Resource
	// Reasons say why, in plain English.
	Reasons []string
	// Supersedes is a pending handoff this change replaces.
	Supersedes string
}

// HasErrors reports whether anything in the plan blocks apply.
func (p *Plan) HasErrors() bool {
	if p.Diags.HasErrors() {
		return true
	}
	for _, d := range p.Factories {
		if d.Diags.HasErrors() {
			return true
		}
	}
	return p.Local != nil && p.Local.Diags.HasErrors()
}

// HasChanges reports whether apply has work to do.
func (p *Plan) HasChanges() bool {
	for _, d := range p.Factories {
		if d.HasWork() {
			return true
		}
	}
	return p.Local != nil && p.Local.HasChanges()
}

// ExitCode is 0 when everything is in sync, 2 when there are only
// changes, and 1 when there are errors.
func (p *Plan) ExitCode() int {
	switch {
	case p.HasErrors():
		return 1
	case p.HasChanges():
		return 2
	}
	return 0
}

// Factory finds a factory's plan by name.
func (p *Plan) Factory(name string) *Factory {
	for _, d := range p.Factories {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// Build plans every factory in a home repo.
func Build(ctx context.Context, env *Env, h *home.Home) *Plan {
	p := &Plan{Home: h, Destroy: env.Destroy}
	only := map[string]bool{}
	for _, n := range env.Only {
		only[n] = true
		if h.Factories[n] == nil && !contains(h.StateNames, n) {
			p.Diags.Errorf("plan.unknown-factory", home.FactoriesDir, "there's no factory called %q", n)
		}
	}
	selected := func(name string) bool { return len(only) == 0 || only[name] }

	// Problems found while reading the home repo itself.
	for _, d := range h.Diags {
		p.Diags.Add(d)
	}

	var locals []*Factory
	destroyed := map[string]bool{} // local factories to destroy
	for _, name := range h.Names() {
		d := h.Factories[name]
		if !selected(name) {
			continue
		}
		if d.Scope == provider.Local {
			if env.SkipLocal {
				continue
			}
			if env.Destroy {
				destroyed[name] = true
				continue
			}
			dp := env.planLocalFactory(ctx, d)
			p.Factories = append(p.Factories, dp)
			locals = append(locals, dp)
			continue
		}
		p.Factories = append(p.Factories, env.PlanCloud(ctx, h, name))
	}
	// Factories that were deleted but still have resources to clean up.
	for _, name := range h.StateNames {
		if h.Factories[name] == nil && selected(name) {
			p.Factories = append(p.Factories, env.PlanCloud(ctx, h, name))
		}
	}
	switch {
	case env.SkipLocal && hasLocal(h):
		p.Notes = append(p.Notes, "Skipped local factories: there's nothing local to set up in CI.")
	case h.Personal && env.Destroy:
		// Destroying only cloud factories leaves the local tools alone.
		if len(env.Only) == 0 || len(destroyed) > 0 {
			var notes []string
			p.Local, notes = env.planLocalDestroy(h, destroyed)
			p.Notes = append(p.Notes, notes...)
		}
	case h.Personal && len(env.Only) == 0:
		p.Local = env.planLocalTools(locals, h.Dir)
	}
	sort.Slice(p.Factories, func(i, j int) bool { return p.Factories[i].Name < p.Factories[j].Name })
	return p
}

func hasLocal(h *home.Home) bool {
	for _, d := range h.Factories {
		if d.Scope == provider.Local {
			return true
		}
	}
	return false
}

// resolveProvider finds a runtime's provider: built in, or a folder
// provider shipped in the blueprint.
func resolveProvider(name string, f *blueprint.Blueprint) (*provider.Provider, error) {
	if p, ok := provider.Builtin(name); ok {
		return p, nil
	}
	if f != nil {
		if item := f.Providers[name]; item != nil {
			if item.Provider == nil {
				return nil, fmt.Errorf("runtime %s comes from the blueprint's providers/%s, which has problems (see above)", name, name)
			}
			return item.Provider, nil
		}
	}
	return nil, fmt.Errorf("there's no runtime called %s: it isn't built in (%v), and the blueprint has no providers/%s", name, provider.BuiltinNames(), name)
}

// loadBlueprint resolves a factory's blueprint ref and loads the blueprint.
func (e *Env) loadBlueprint(ctx context.Context, dp *Factory, ref home.BlueprintRef) {
	remote := e.Git.Remote(gitURL(ref.Git), gitx.Full)
	resolved, err := remote.Resolve(ctx, ref.Ref)
	if err != nil {
		dp.Diags.Errorf("blueprint.resolve", "blueprint", "%v", err)
		return
	}
	dp.Resolved = resolved
	f, diags := e.Loader.LoadGit(ctx, ref.Git, resolved.Commit, ref.Path)
	dp.Diags = append(dp.Diags, diags.Only(diag.Error).Prefixed("blueprint "+ref.String())...)
	dp.Blueprint = f
	mode := "frozen: nothing changes until you edit the factory"
	if resolved.Kind.Follows() {
		mode = "follows the branch: merges take effect on the next run, no apply needed"
	}
	dp.Notes = append(dp.Notes, fmt.Sprintf("blueprint %s → %s (%s %s; %s)", ref.String(), gitx.Short(resolved.Commit), resolved.Kind, ref.Ref, mode))
	if f != nil && len(f.Overrides) > 0 {
		drift, err := e.Loader.OverrideDrift(ctx, f)
		if err != nil {
			dp.Notes = append(dp.Notes, fmt.Sprintf("couldn't check overridden items for upstream changes: %v", err))
		}
		for _, d := range drift {
			dp.Notes = append(dp.Notes, fmt.Sprintf("the blueprint overrides %s from import %s, and upstream (%s) changed it since the blueprint's lock (%s → %s); consider `fabrica upgrade` in the blueprint repo",
				d.Ref, d.Alias, d.ImportRef, gitx.Short(d.Locked), gitx.Short(d.Latest)))
		}
	}
}

// checkRoutinesAndParams checks that every routine exists and every param
// is declared and set. It returns the routines the factory deploys, with
// the wildcard expanded, and records them in dp.Routines.
func checkRoutinesAndParams(dp *Factory, d *home.Factory, f *blueprint.Blueprint) []*blueprint.Routine {
	var routines []*blueprint.Routine
	for _, name := range d.Routines {
		r := f.Routines[name]
		if r == nil {
			dp.Diags.Errorf("factory.unknown-routine", d.File, "routine %q isn't in the blueprint", name).
				Hintf("the blueprint has: %v", blueprint.SortedKeys(f.Routines))
			continue
		}
		if len(r.Problems) > 0 {
			continue // its problems are already reported as blueprint errors
		}
		routines = append(routines, r)
	}
	if d.AllRoutines {
		for _, name := range blueprint.SortedKeys(f.Routines) {
			r := f.Routines[name]
			// The init routine runs by itself when the factory is created.
			if d.Names(name) || len(r.Problems) > 0 || r.IsInit() {
				continue
			}
			routines = append(routines, r)
		}
	}
	dp.Routines = routines
	declared := map[string]bool{}
	for _, r := range routines {
		for _, prm := range r.Params {
			declared[prm.Name] = true
		}
	}
	if init := f.Init(); init != nil {
		for _, prm := range init.Params {
			declared[prm.Name] = true
		}
	}
	check := func(params map[string]string, where string) {
		for _, k := range sortedKeys(params) {
			if !declared[k] {
				dp.Diags.Errorf("factory.unknown-param", d.File, "%s sets param %q, but none of the factory's routines declares it", where, k).
					Hintf("check the spelling, or remove it")
			}
		}
	}
	check(d.Params, "params:")
	for _, entry := range d.Targets {
		check(entry.Params, "the params of "+entry.Repo)
	}
	for _, entry := range d.Targets {
		merged := d.ParamsFor(entry.Repo)
		for _, r := range routines {
			for _, prm := range r.Params {
				if _, ok := merged[prm.Name]; prm.Required && !ok {
					dp.Diags.Errorf("factory.missing-param", d.File, "routine %s needs param %q, but it isn't set for %s", r.Ref.Name, prm.Name, entry.Repo).
						Hintf("set it under params: (for every repo) or in the repo's own params:")
				}
			}
		}
	}
	return routines
}

// ParamValues merges a routine's param defaults with the factory's
// values for one repo.
func ParamValues(r *blueprint.Routine, d *home.Factory, repo string) map[string]string {
	values := map[string]string{}
	for _, prm := range r.Params {
		if prm.HasDefault {
			values[prm.Name] = prm.Default
		}
	}
	if d != nil {
		for k, v := range d.ParamsFor(repo) {
			if _, ok := r.Param(k); ok {
				values[k] = v
			}
		}
	}
	return values
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
