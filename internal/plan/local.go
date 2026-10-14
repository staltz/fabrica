package plan

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/localtools"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// LocalPlan is what apply would change in the local tools on this machine.
type LocalPlan struct {
	Runtimes []*LocalRuntime
	Diags    diag.List
}

// HasChanges reports whether any local tool needs changing.
func (l *LocalPlan) HasChanges() bool {
	for _, rt := range l.Runtimes {
		if len(rt.Changes) > 0 {
			return true
		}
	}
	return false
}

// LocalRuntime is one local tool.
type LocalRuntime struct {
	Name    string
	Tool    localtools.Tool
	Current localtools.Config
	Desired localtools.Config
	// Changes describe the differences, one line each.
	Changes []string
}

// planLocalFactory checks one local factory. Choosing a blueprint
// needs no apply (`fabrica mcp` matches the workspace each time a session
// starts), so the only installs are the shared MCP entry plus hooks and
// scheduled tasks for local routines; those are worked out across
// factories in planLocalTools.
func (e *Env) planLocalFactory(ctx context.Context, d *home.Factory) *Factory {
	dp := &Factory{Name: d.Name, Spec: d}
	e.loadBlueprint(ctx, dp, d.Blueprint)
	f := dp.Blueprint
	if f == nil {
		return dp
	}
	for _, rt := range d.Runtimes {
		p, err := resolveProvider(rt, f)
		if err != nil {
			dp.Diags.Errorf("factory.unknown-runtime", d.File, "%v", err)
			continue
		}
		if p.Matrix.Scope != d.Scope {
			dp.Diags.Errorf("factory.runtime-scope", d.File, "runtime %s is a %s runtime, but this is a %s factory", rt, p.Matrix.Scope, d.Scope)
			continue
		}
		dp.Runtimes = append(dp.Runtimes, &Runtime{Name: rt, Provider: p, Mode: "api", Evaluations: map[string]*stub.Evaluation{}})
	}
	routines := checkRoutinesAndParams(dp, d, f)
	if init := f.Init(); init != nil {
		dp.Notes = append(dp.Notes, fmt.Sprintf("init routine %s doesn't run here: only cloud factories run their blueprint's init, since they keep state to remember they did", init.Ref.Name))
	}
	if d.IsWildcard() {
		for _, r := range routines {
			if r.Trigger.On == "schedule" {
				dp.Diags.Errorf("factory.schedule-wildcard", d.File, "routine %s runs on a schedule, so it needs a repo to run in, but this factory has targets: [\"*\"]", r.Ref.Name).
					Hintf("list the targets it should run in, or move the routine to a factory that does")
			}
		}
	}
	for _, rt := range dp.Runtimes {
		for _, r := range routines {
			if !d.Runs(r.Ref.Name, rt.Name) {
				continue
			}
			ev := stub.Evaluate(f, r, stub.Target{Factory: d.Name, Runtime: rt.Provider})
			rt.Evaluations[r.Ref.Name] = ev
			dp.Diags = append(dp.Diags, ev.Diags...)
		}
	}
	e.checkProductRepos(ctx, dp, d, f)
	if d.IsWildcard() {
		dp.Notes = append(dp.Notes, `targets: ["*"] matches every repo not covered by another local factory; each repo still needs the Fabrica line, which is checked when a session starts`)
	}
	return dp
}

// planLocalTools works out the local tool configs on this machine.
// personalHome is passed to `fabrica mcp` so the tools find your home repo
// even when they don't see your FABRICA_HOME.
func (e *Env) planLocalTools(locals []*Factory, personalHome string) *LocalPlan {
	lp := &LocalPlan{}
	used := map[string][]*Factory{} // runtime → factories using it
	for _, dp := range locals {
		for _, rt := range dp.Spec.Runtimes {
			used[rt] = append(used[rt], dp)
		}
	}
	for _, rt := range localtools.Runtimes() {
		tool, err := localtools.For(rt, e.UserHome)
		if err != nil {
			lp.Diags.Errorf("local.tool", rt, "%v", err)
			continue
		}
		current, err := tool.Read()
		if err != nil {
			lp.Diags.Errorf("local.config", rt, "%v", err).Hintf("fix the file by hand, then plan again")
			continue
		}
		lr := &LocalRuntime{Name: rt, Tool: tool, Current: current.Normalize()}
		desired, diags := e.desiredLocal(rt, used[rt], current, personalHome)
		lp.Diags = append(lp.Diags, diags...)
		lr.Desired = desired.Normalize()
		lr.Changes = describeLocalChanges(lr.Current, lr.Desired)
		if len(used[rt]) > 0 || len(lr.Changes) > 0 {
			lp.Runtimes = append(lp.Runtimes, lr)
		}
	}
	if len(lp.Runtimes) == 0 && len(lp.Diags) == 0 {
		return nil
	}
	return lp
}

// planLocalDestroy works out what `fabrica destroy` removes from the local
// tools on this machine. A tool that no other local factory uses loses
// everything Fabrica installed in it. A tool that others still use only
// loses the destroyed factories' hooks and scheduled tasks: the fabrica
// MCP server is shared, and so may be connector servers, so they stay.
func (e *Env) planLocalDestroy(h *home.Home, destroyed map[string]bool) (*LocalPlan, []string) {
	lp := &LocalPlan{}
	kept := map[string]bool{} // runtimes that other local factories still use
	for _, name := range h.Names() {
		if d := h.Factories[name]; d.Scope == provider.Local && !destroyed[name] {
			for _, rt := range d.Runtimes {
				kept[rt] = true
			}
		}
	}
	var notes []string
	for _, name := range sortedNames(destroyed) {
		for _, rt := range h.Factories[name].Runtimes {
			if kept[rt] {
				notes = append(notes, fmt.Sprintf("Other local factories keep the fabrica MCP server installed, so it still serves %s's blueprint in its targets; delete %s/%s.yaml to stop that.", name, home.FactoriesDir, name))
				break
			}
		}
	}
	for _, rt := range localtools.Runtimes() {
		tool, err := localtools.For(rt, e.UserHome)
		if err != nil {
			lp.Diags.Errorf("local.tool", rt, "%v", err)
			continue
		}
		current, err := tool.Read()
		if err != nil {
			lp.Diags.Errorf("local.config", rt, "%v", err).Hintf("fix the file by hand, then plan again")
			continue
		}
		var desired localtools.Config
		if kept[rt] {
			desired.MCP = current.MCP
			for _, hk := range current.Hooks {
				if !destroyed[hk.Factory] {
					desired.Hooks = append(desired.Hooks, hk)
				}
			}
			for _, t := range current.Tasks {
				if !destroyed[t.Factory] {
					desired.Tasks = append(desired.Tasks, t)
				}
			}
		}
		lr := &LocalRuntime{Name: rt, Tool: tool, Current: current.Normalize(), Desired: desired.Normalize()}
		lr.Changes = describeLocalChanges(lr.Current, lr.Desired)
		if len(lr.Changes) > 0 {
			lp.Runtimes = append(lp.Runtimes, lr)
		}
	}
	if len(lp.Runtimes) == 0 && len(lp.Diags) == 0 {
		return nil, notes
	}
	return lp, notes
}

// desiredLocal is what one local tool should have installed.
func (e *Env) desiredLocal(rt string, factories []*Factory, current localtools.Config, personalHome string) (localtools.Config, diag.List) {
	var cfg localtools.Config
	var diags diag.List
	if len(factories) == 0 {
		return cfg, nil // nothing uses this tool: remove whatever Fabrica left there
	}
	args := []string{"mcp", "--runtime", rt}
	if personalHome != "" {
		args = append(args, "--home", personalHome)
	}
	cfg.MCP = append(cfg.MCP, localtools.MCPEntry{Name: "fabrica", Transport: "stdio", Command: e.Binary, Args: args})

	connectors := map[string]localtools.MCPEntry{}
	connectorFrom := map[string]string{}
	var broken []*Factory
	for _, dp := range factories {
		if dp.Diags.HasErrors() || dp.Blueprint == nil {
			broken = append(broken, dp)
			continue
		}
		var rtPlan *Runtime
		for _, r := range dp.Runtimes {
			if r.Name == rt {
				rtPlan = r
			}
		}
		if rtPlan == nil {
			continue
		}
		for _, routine := range dp.Spec.RoutinesOn(rt) {
			ev := rtPlan.Evaluations[routine]
			if ev == nil || !ev.OK() {
				continue
			}
			if tr := ev.Routine.Trigger; tr.On == "schedule" {
				for _, repo := range dp.Spec.SpecificTargets() {
					cfg.Tasks = append(cfg.Tasks, localtools.Task{
						Factory: dp.Name, Routine: routine, Repo: repo, Cron: tr.Cron, Fingerprint: ev.Shape.Fingerprint(),
						Prompt: stub.LocalPrompt(routine, dp.Name, rt, repo, ev.Shape.Fingerprint()),
					})
				}
			} else {
				cfg.Hooks = append(cfg.Hooks, localtools.Hook{
					Binary: e.Binary, Runtime: rt, Event: tr.On,
					Factory: dp.Name, Routine: routine, Fingerprint: ev.Shape.Fingerprint(),
				})
			}
			for _, c := range ev.Shape.Connectors {
				if c.Server == nil {
					continue
				}
				entry := entryFor(c)
				if prev, ok := connectors[entry.Name]; ok && !reflect.DeepEqual(prev, entry) {
					diags.Errorf("local.connector-conflict", rt, "factories %s and %s both need a connector called %s on %s, but define it differently", connectorFrom[entry.Name], dp.Name, c.Name, rt).
						Hintf("local connectors are installed once per tool, so they have to match; align the two blueprints' connectors/%s.yaml", c.Name)
					continue
				}
				connectors[entry.Name] = entry
				connectorFrom[entry.Name] = dp.Name
			}
		}
	}
	// An error blocks the whole factory: keep whatever it has installed
	// now and add nothing new. We can't tell which connector servers belong
	// to it, so keep the installed ones nobody else asked for.
	for _, dp := range broken {
		for _, h := range current.Hooks {
			if h.Factory == dp.Name && h.Runtime == rt {
				cfg.Hooks = append(cfg.Hooks, h)
			}
		}
		for _, t := range current.Tasks {
			if t.Factory == dp.Name {
				cfg.Tasks = append(cfg.Tasks, t)
			}
		}
		for _, m := range current.MCP {
			if _, taken := connectors[m.Name]; m.Name != "fabrica" && !taken {
				connectors[m.Name] = m
			}
		}
	}
	for _, name := range sortedEntryNames(connectors) {
		cfg.MCP = append(cfg.MCP, connectors[name])
	}
	return cfg, diags
}

func entryFor(c stub.ShapeConnector) localtools.MCPEntry {
	s := c.Server
	e := localtools.MCPEntry{Name: "fabrica-" + c.Name, Transport: s.Transport}
	switch s.Transport {
	case "stdio":
		e.Command, e.Args, e.EnvNames = s.Command, s.Args, append([]string(nil), s.Env...)
		sort.Strings(e.EnvNames)
	default:
		e.URL = s.URL
		if s.Auth == "bearer" {
			e.BearerEnv = s.Secret
		}
	}
	return e
}

func describeLocalChanges(cur, want localtools.Config) []string {
	var out []string
	curMCP := map[string]localtools.MCPEntry{}
	for _, m := range cur.MCP {
		curMCP[m.Name] = m
	}
	wantMCP := map[string]bool{}
	for _, m := range want.MCP {
		wantMCP[m.Name] = true
		old, ok := curMCP[m.Name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("+ add MCP server %s", m.Name))
		case !reflect.DeepEqual(old, m):
			out = append(out, fmt.Sprintf("~ update MCP server %s", m.Name))
		}
	}
	for _, m := range cur.MCP {
		if !wantMCP[m.Name] {
			out = append(out, fmt.Sprintf("- remove MCP server %s", m.Name))
		}
	}
	type hookID struct{ factory, routine string }
	curHooks := map[hookID]localtools.Hook{}
	for _, h := range cur.Hooks {
		curHooks[hookID{h.Factory, h.Routine}] = h
	}
	wantHooks := map[hookID]bool{}
	for _, h := range want.Hooks {
		id := hookID{h.Factory, h.Routine}
		wantHooks[id] = true
		old, ok := curHooks[id]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("+ add hook for routine %s/%s on %s", h.Factory, h.Routine, h.Event))
		case old != h:
			out = append(out, fmt.Sprintf("~ update hook for routine %s/%s (%s, stub %s)", h.Factory, h.Routine, h.Event, h.Fingerprint))
		}
	}
	for _, h := range cur.Hooks {
		if !wantHooks[hookID{h.Factory, h.Routine}] {
			out = append(out, fmt.Sprintf("- remove hook for routine %s/%s", h.Factory, h.Routine))
		}
	}
	curTasks := map[string]localtools.Task{}
	for _, t := range cur.Tasks {
		curTasks[localtools.TaskID(t)] = t
	}
	wantTasks := map[string]bool{}
	for _, t := range want.Tasks {
		id := localtools.TaskID(t)
		wantTasks[id] = true
		old, ok := curTasks[id]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("+ add scheduled task %s for routine %s/%s on %s (cron `%s` UTC)", id, t.Factory, t.Routine, t.Repo, t.Cron))
		case old != t:
			out = append(out, fmt.Sprintf("~ update scheduled task %s for routine %s/%s (cron `%s` UTC, stub %s)", id, t.Factory, t.Routine, t.Cron, t.Fingerprint))
		}
	}
	for _, t := range cur.Tasks {
		if id := localtools.TaskID(t); !wantTasks[id] {
			out = append(out, fmt.Sprintf("- remove scheduled task %s for routine %s/%s", id, t.Factory, t.Routine))
		}
	}
	return out
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedEntryNames(m map[string]localtools.MCPEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
