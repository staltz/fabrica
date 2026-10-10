// Package plan works out what `fabrica apply` would do, and what stops it.
// With Env.Destroy, it works out what `fabrica destroy` would remove
// instead.
//
// A plan compares what your home repo and its factories ask for with what's
// installed, and sorts everything it finds into two kinds:
//
//   - Changes are work for apply: stubs to create, update or delete, and
//     handoffs to issue. Repairing a stub that went stale after a merge is
//     a change too.
//   - Errors are problems apply can't fix: a trigger a runtime doesn't
//     support, a product repo without the Fabrica line, a held lock, and so
//     on. Apply won't start on a deployment that has any.
//
// The plan's exit code says which: 0 when there's nothing to do, 2 when
// there are only changes, and 1 when there are errors.
package plan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// Env is everything planning needs from the outside world.
type Env struct {
	Loader *factory.Loader
	Git    *gitx.Cache
	// Secret is the server secret, when available. Without it the plan
	// can't tell whether tokens were rotated, and apply can't run.
	Secret string
	// Binary is the absolute path of the fabrica binary, which local tools
	// run as their MCP server and hooks.
	Binary string
	// UserHome is your home folder, where local tools keep their config.
	UserHome string
	// SkipLocal skips local deployments (CI has nothing local to set up).
	SkipLocal bool
	// Refresh looks resources up through runtime APIs (read-only) instead
	// of only trusting state. Apply always refreshes once it holds a lock.
	Refresh bool
	// Only limits the plan to these deployments. Empty means all.
	Only []string
	// Destroy plans `fabrica destroy`: nothing is wanted, so everything
	// the deployments installed becomes a delete, as if their files were
	// gone.
	Destroy bool
	// LockHolder is set by apply once it holds a deployment's lock, so the
	// re-plan inside the lock doesn't report our own lock as a problem.
	LockHolder string
	// Now is the clock, replaceable in tests.
	Now func() time.Time
}

// Plan is the whole plan for a home repo.
type Plan struct {
	Home        *home.Home
	Deployments []*Deployment
	// Local is the plan for local tools on this machine. Nil when there are
	// no local deployments or they were skipped.
	Local *LocalPlan
	// Diags are home-wide problems, like overlapping local deployments.
	Diags diag.List
	// Notes are home-wide remarks, like "skipped local deployments in CI".
	Notes []string
	// Destroy is true for a plan made for `fabrica destroy`.
	Destroy bool
}

// Deployment is the plan for one deployment.
type Deployment struct {
	Name string
	// Spec is the deployment file, or nil when it was deleted and only its
	// state is left.
	Spec    *home.Deployment
	State   *home.State
	Factory *factory.Factory
	// Resolved is the factory ref resolved to a commit.
	Resolved gitx.Resolved
	Runtimes []*Runtime
	// Desired are the stubs that should exist.
	Desired []*Resource
	Changes []*Change
	// Pending are handoffs that are still waiting for `fabrica confirm`
	// and still match what's wanted.
	Pending []home.Pending
	Diags   diag.List
	Notes   []string
	// Destroy is true when everything the deployment installed is to go.
	Destroy bool
}

// Local reports whether this is a local deployment.
func (d *Deployment) Local() bool { return d.Spec != nil && d.Spec.Scope == provider.Local }

// Runtime is one runtime of a deployment.
type Runtime struct {
	Name     string
	Provider *provider.Provider
	Mode     provider.ApplyMode
	// Evaluations are the deployment's routines checked against this
	// runtime, by routine name.
	Evaluations map[string]*stub.Evaluation
}

// Resource is a stub that should exist: one routine, on one repo, in one
// runtime.
type Resource struct {
	Key         home.Key
	Deployment  string
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
	for _, d := range p.Deployments {
		if d.Diags.HasErrors() {
			return true
		}
	}
	return p.Local != nil && p.Local.Diags.HasErrors()
}

// HasChanges reports whether apply has work to do.
func (p *Plan) HasChanges() bool {
	for _, d := range p.Deployments {
		if len(d.Changes) > 0 {
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

// Deployment finds a deployment's plan by name.
func (p *Plan) Deployment(name string) *Deployment {
	for _, d := range p.Deployments {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// Build plans every deployment in a home repo.
func Build(ctx context.Context, env *Env, h *home.Home) *Plan {
	p := &Plan{Home: h, Destroy: env.Destroy}
	only := map[string]bool{}
	for _, n := range env.Only {
		only[n] = true
		if h.Deployments[n] == nil && !contains(h.StateNames, n) {
			p.Diags.Errorf("plan.unknown-deployment", home.DeploymentsDir, "there's no deployment called %q", n)
		}
	}
	selected := func(name string) bool { return len(only) == 0 || only[name] }

	// Problems found while reading the home repo itself.
	for _, d := range h.Diags {
		p.Diags.Add(d)
	}

	var locals []*Deployment
	destroyed := map[string]bool{} // local deployments to destroy
	for _, name := range h.Names() {
		d := h.Deployments[name]
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
			dp := env.planLocalDeployment(ctx, d)
			p.Deployments = append(p.Deployments, dp)
			locals = append(locals, dp)
			continue
		}
		p.Deployments = append(p.Deployments, env.PlanCloud(ctx, h, name))
	}
	// Deployments that were deleted but still have resources to clean up.
	for _, name := range h.StateNames {
		if h.Deployments[name] == nil && selected(name) {
			p.Deployments = append(p.Deployments, env.PlanCloud(ctx, h, name))
		}
	}
	switch {
	case env.SkipLocal && hasLocal(h):
		p.Notes = append(p.Notes, "Skipped local deployments: there's nothing local to set up in CI.")
	case h.Personal && env.Destroy:
		// Destroying only cloud deployments leaves the local tools alone.
		if len(env.Only) == 0 || len(destroyed) > 0 {
			var notes []string
			p.Local, notes = env.planLocalDestroy(h, destroyed)
			p.Notes = append(p.Notes, notes...)
		}
	case h.Personal && len(env.Only) == 0:
		p.Local = env.planLocalTools(locals, h.Dir)
	}
	sort.Slice(p.Deployments, func(i, j int) bool { return p.Deployments[i].Name < p.Deployments[j].Name })
	return p
}

func hasLocal(h *home.Home) bool {
	for _, d := range h.Deployments {
		if d.Scope == provider.Local {
			return true
		}
	}
	return false
}

// resolveProvider finds a runtime's provider: built in, or a folder
// provider shipped in the factory.
func resolveProvider(name string, f *factory.Factory) (*provider.Provider, error) {
	if p, ok := provider.Builtin(name); ok {
		return p, nil
	}
	if f != nil {
		if item := f.Providers[name]; item != nil {
			if item.Provider == nil {
				return nil, fmt.Errorf("runtime %s comes from the factory's providers/%s, which has problems (see above)", name, name)
			}
			return item.Provider, nil
		}
	}
	return nil, fmt.Errorf("there's no runtime called %s: it isn't built in (%v), and the factory has no providers/%s", name, provider.BuiltinNames(), name)
}

// loadFactory resolves a deployment's factory ref and loads the factory.
func (e *Env) loadFactory(ctx context.Context, dp *Deployment, ref home.FactoryRef) {
	remote := e.Git.Remote(gitURL(ref.Git), gitx.Full)
	resolved, err := remote.Resolve(ctx, ref.Ref)
	if err != nil {
		dp.Diags.Errorf("factory.resolve", "factory", "%v", err)
		return
	}
	dp.Resolved = resolved
	f, diags := e.Loader.LoadGit(ctx, ref.Git, resolved.Commit, ref.Path)
	dp.Diags = append(dp.Diags, diags.Only(diag.Error).Prefixed("factory "+ref.String())...)
	dp.Factory = f
	mode := "frozen: nothing changes until you edit the deployment"
	if resolved.Kind.Follows() {
		mode = "follows the branch: merges take effect on the next run, no apply needed"
	}
	dp.Notes = append(dp.Notes, fmt.Sprintf("factory %s → %s (%s %s; %s)", ref.String(), gitx.Short(resolved.Commit), resolved.Kind, ref.Ref, mode))
	if f != nil && len(f.Overrides) > 0 {
		drift, err := e.Loader.OverrideDrift(ctx, f)
		if err != nil {
			dp.Notes = append(dp.Notes, fmt.Sprintf("couldn't check overridden items for upstream changes: %v", err))
		}
		for _, d := range drift {
			dp.Notes = append(dp.Notes, fmt.Sprintf("the factory overrides %s from import %s, and upstream (%s) changed it since the factory's lock (%s → %s); consider `fabrica upgrade` in the factory repo",
				d.Ref, d.Alias, d.ImportRef, gitx.Short(d.Locked), gitx.Short(d.Latest)))
		}
	}
}

// checkRoutinesAndParams checks that every routine exists and every param
// is declared and set.
func checkRoutinesAndParams(dp *Deployment, d *home.Deployment, f *factory.Factory) []*factory.Routine {
	var routines []*factory.Routine
	for _, name := range d.Routines {
		r := f.Routines[name]
		if r == nil {
			dp.Diags.Errorf("deployment.unknown-routine", d.File, "routine %q isn't in the factory", name).
				Hintf("the factory has: %v", factory.SortedKeys(f.Routines))
			continue
		}
		if len(r.Problems) > 0 {
			continue // its problems are already reported as factory errors
		}
		routines = append(routines, r)
	}
	declared := map[string]bool{}
	for _, r := range routines {
		for _, prm := range r.Params {
			declared[prm.Name] = true
		}
	}
	check := func(params map[string]string, where string) {
		for _, k := range sortedKeys(params) {
			if !declared[k] {
				dp.Diags.Errorf("deployment.unknown-param", d.File, "%s sets param %q, but none of the deployment's routines declares it", where, k).
					Hintf("check the spelling, or remove it")
			}
		}
	}
	check(d.Params, "params:")
	for _, entry := range d.Repos {
		check(entry.Params, "the params of "+entry.Repo)
	}
	for _, entry := range d.Repos {
		merged := d.ParamsFor(entry.Repo)
		for _, r := range routines {
			for _, prm := range r.Params {
				if _, ok := merged[prm.Name]; prm.Required && !ok {
					dp.Diags.Errorf("deployment.missing-param", d.File, "routine %s needs param %q, but it isn't set for %s", r.Ref.Name, prm.Name, entry.Repo).
						Hintf("set it under params: (for every repo) or in the repo's own params:")
				}
			}
		}
	}
	return routines
}

// ParamValues merges a routine's param defaults with the deployment's
// values for one repo.
func ParamValues(r *factory.Routine, d *home.Deployment, repo string) map[string]string {
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
