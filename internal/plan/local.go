package plan

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"sort"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/localtools"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// LocalPlan is what apply would change in the local tools on this machine.
type LocalPlan struct {
	Runtimes []*LocalRuntime
	// Heartbeat is the job that runs `fabrica heartbeat`, for routines with
	// a pace trigger. Nil when there's none and none is wanted, or the
	// system has no scheduler Fabrica can use.
	Heartbeat *LocalHeartbeat
	Diags     diag.List
}

// HasChanges reports whether any local tool needs changing.
func (l *LocalPlan) HasChanges() bool {
	for _, rt := range l.Runtimes {
		if len(rt.Changes) > 0 {
			return true
		}
	}
	return l.Heartbeat != nil && len(l.Heartbeat.Changes) > 0
}

// LocalHeartbeat is the heartbeat job: installed in the system's per-user
// scheduler, it runs `fabrica heartbeat` every minute, which starts the
// local routines whose pace says they're due.
type LocalHeartbeat struct {
	Scheduler *localtools.Scheduler
	// Job is what to install, or nil to have none.
	Job              *localtools.HeartbeatJob
	Current, Desired map[string][]byte
	// Changes describe what apply would do, in one line, or nothing.
	Changes []string
}

// planHeartbeat works out the heartbeat job: one is wanted when a local
// factory runs a routine on its pace. A factory with errors keeps what's
// installed, like it keeps its hooks, and so does kept (a destroy that
// leaves other local factories with a pace).
func (e *Env) planHeartbeat(lp *LocalPlan, locals []*Factory, personalHome string, kept bool) {
	want, broken := false, false
	for _, dp := range locals {
		if dp.Diags.HasErrors() || dp.Blueprint == nil {
			broken = broken || dp.Spec.Pace > 0
			continue
		}
		for _, rt := range dp.Runtimes {
			for _, ev := range rt.Evaluations {
				for _, st := range ev.Stubs {
					want = want || ev.OK() && st.Shape.HasTrigger(blueprint.PaceTrigger)
				}
			}
		}
	}
	goos := e.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	sched, err := localtools.SchedulerFor(goos, e.UserHome, e.CacheDir)
	if err != nil {
		if want {
			lp.Diags.Warnf("local.heartbeat", "heartbeat", "a local factory runs routines on its pace, but %v", err).
				Hintf("keep `fabrica heartbeat --watch` running instead: it starts those routines when they're due")
		}
		return
	}
	if e.RunScheduler != nil {
		sched.Run = e.RunScheduler
	}
	current, err := sched.Read()
	if err != nil {
		lp.Diags.Errorf("local.heartbeat", "heartbeat", "couldn't read the heartbeat job: %v", err)
		return
	}
	hb := &LocalHeartbeat{Scheduler: sched, Current: current}
	switch {
	case want:
		hb.Job = &localtools.HeartbeatJob{Binary: e.Binary, Home: personalHome, Env: localtools.HeartbeatEnv(e.Binary)}
		hb.Desired = sched.Render(hb.Job)
	case broken || kept:
		hb.Desired = current // keep it until the factory is fixed, or while others still need it
	default:
		hb.Desired = map[string][]byte{}
	}
	what := sched.Describe() + ", that runs `fabrica heartbeat` every minute"
	switch {
	case localtools.SameFiles(current, hb.Desired):
		if len(current) == 0 {
			return
		}
	case len(current) == 0:
		hb.Changes = []string{"+ install the heartbeat: " + what}
	case len(hb.Desired) == 0:
		hb.Changes = []string{"- remove the heartbeat: no local factory runs routines on its pace anymore"}
	default:
		hb.Changes = []string{"~ update the heartbeat: " + what}
	}
	lp.Heartbeat = hb
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
// starts), so the only installs are the shared MCP entry and session
// pointer hook, plus hooks and scheduled tasks for local routines; those
// are worked out across factories in planLocalTools.
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
		dp.Details = append(dp.Details, fmt.Sprintf("init routine %s doesn't run here: only cloud factories run their blueprint's init, since they keep state to remember they did", init.Ref.Name))
	}
	if d.IsWildcard() {
		for _, r := range routines {
			if r.HasTrigger(blueprint.ScheduleTrigger) || r.HasPace() {
				dp.Diags.Errorf("factory.schedule-wildcard", d.File, "routine %s runs on a schedule or a pace, so it needs a repo to run in, but this factory has targets: [\"*\"]", r.Ref.Name).
					Hintf("list the targets it should run in, or move the routine to a factory that does")
			}
		}
	}
	for _, rt := range dp.Runtimes {
		for _, r := range routines {
			if !d.Runs(r.Ref.Name, rt.Name) {
				continue
			}
			ev := stub.Evaluate(f, r, stub.Target{Factory: d.Name, Runtime: rt.Provider, Pace: d.Pace})
			rt.Evaluations[r.Ref.Name] = ev
			dp.Diags = append(dp.Diags, ev.Diags...)
		}
	}
	e.checkProductRepos(ctx, dp, d, f)
	if d.IsWildcard() {
		dp.Details = append(dp.Details, `targets: ["*"] matches every repo not covered by another local factory; each repo still needs the Fabrica line, which is checked when a session starts`)
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
	e.planHeartbeat(lp, locals, personalHome, false)
	if len(lp.Runtimes) == 0 && len(lp.Diags) == 0 && lp.Heartbeat == nil {
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
	// The heartbeat job stays while another local factory has a pace.
	paced := false
	for _, name := range h.Names() {
		if d := h.Factories[name]; d.Scope == provider.Local && !destroyed[name] && d.Pace > 0 {
			paced = true
		}
	}
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
	e.planHeartbeat(lp, nil, h.Dir, paced)
	if len(lp.Runtimes) == 0 && len(lp.Diags) == 0 && lp.Heartbeat == nil {
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
	if localtools.HasSessionStart(rt) {
		cfg.Hooks = append(cfg.Hooks, localtools.SessionHook(e.Binary, rt))
	}

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
		for _, r := range dp.Routines {
			routine := r.Ref.Name
			ev := rtPlan.Evaluations[routine] // only there when it runs on rt
			if ev == nil || !ev.OK() {
				continue
			}
			// Local runtimes take one trigger per stub: a hook for each
			// event, and a scheduled task for a schedule. A pace needs
			// nothing in the tool: the heartbeat job starts its sessions.
			for _, st := range ev.Stubs {
				tr, fp := st.Triggers[0], st.Shape.Fingerprint()
				if tr.On == blueprint.PaceTrigger {
					continue
				}
				if tr.On == "schedule" {
					for _, repo := range dp.Spec.SpecificTargets() {
						cfg.Tasks = append(cfg.Tasks, localtools.Task{
							Factory: dp.Name, Routine: routine, Repo: repo, Cron: tr.Cron, Fingerprint: fp, Autofix: st.Shape.SideEffects[provider.PRAutofix],
							Prompt: stub.LocalPrompt(routine, dp.Name, rt, repo, fp),
						})
					}
				} else {
					cfg.Hooks = append(cfg.Hooks, localtools.Hook{
						Binary: e.Binary, Runtime: rt, Event: tr.On,
						Factory: dp.Name, Routine: routine, Fingerprint: fp,
					})
				}
			}
			// Every stub of a routine has the same connectors.
			for _, c := range ev.Stubs[0].Shape.Connectors {
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
	type hookID struct{ factory, routine, event string }
	curHooks := map[hookID]localtools.Hook{}
	for _, h := range cur.Hooks {
		curHooks[hookID{h.Factory, h.Routine, h.Event}] = h
	}
	wantHooks := map[hookID]bool{}
	for _, h := range want.Hooks {
		id := hookID{h.Factory, h.Routine, h.Event}
		wantHooks[id] = true
		old, ok := curHooks[id]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("+ add %s on %s", hookName(h), h.Event))
		case old != h && h.Session:
			out = append(out, "~ update session pointer hook")
		case old != h:
			out = append(out, fmt.Sprintf("~ update hook for routine %s/%s (%s, stub %s)", h.Factory, h.Routine, h.Event, h.Fingerprint))
		}
	}
	for _, h := range cur.Hooks {
		if !wantHooks[hookID{h.Factory, h.Routine, h.Event}] {
			out = append(out, fmt.Sprintf("- remove %s on %s", hookName(h), h.Event))
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
			out = append(out, fmt.Sprintf("+ add scheduled task %s for routine %s/%s on %s (cron `%s` UTC%s)", id, t.Factory, t.Routine, t.Repo, t.Cron, taskAutofix(t)))
		case old != t:
			out = append(out, fmt.Sprintf("~ update scheduled task %s for routine %s/%s (cron `%s` UTC%s, stub %s)", id, t.Factory, t.Routine, t.Cron, taskAutofix(t), t.Fingerprint))
		}
	}
	for _, t := range cur.Tasks {
		if id := localtools.TaskID(t); !wantTasks[id] {
			out = append(out, fmt.Sprintf("- remove scheduled task %s for routine %s/%s", id, t.Factory, t.Routine))
		}
	}
	return out
}

// hookName says which hook a change is about. The session pointer hook has
// no factory or routine, so it's the only one with that ID.
func hookName(h localtools.Hook) string {
	if h.Session {
		return "session pointer hook"
	}
	return fmt.Sprintf("hook for routine %s/%s", h.Factory, h.Routine)
}

// taskAutofix describes a scheduled task's auto-fix setting, if it has one.
func taskAutofix(t localtools.Task) string {
	if t.Autofix == "" {
		return ""
	}
	return ", auto-fix " + t.Autofix
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
