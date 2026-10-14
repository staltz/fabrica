// Package stub works out what Fabrica installs in a runtime for one
// routine: the stub.
//
// Fabrica never installs a routine's real content into a runtime. It
// installs a tiny stub: the trigger, the connectors, and a short prompt that
// says "load your instructions from Fabrica". Prompts, skills, rules, params
// and side effects are all served live over MCP, so editing them never
// needs a re-apply.
//
// What a stub bakes into the runtime is its shape: the trigger, plus the
// connectors and their definitions (including connectors that emulated side
// effects need), plus the runtime's memory setting where it has its own
// memory feature, plus its auto-fix setting for pull requests where it has
// one, plus the model the routine runs on where the runtime lets a stub
// pick one, plus the address of the Fabrica server. A short fingerprint of
// the shape goes into the stub, and the server compares it on every run to
// catch stubs that went stale.
package stub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/provider"
)

// Target is where a routine is being deployed: one runtime of one
// factory.
type Target struct {
	Factory string
	Runtime *provider.Provider
	// Endpoint is the factory's Fabrica server URL, like
	// https://fabrica.acme.dev/pr-review. Empty for local runtimes, which
	// talk to `fabrica mcp` over stdio.
	Endpoint string
}

// Shape is everything a stub bakes into a runtime.
type Shape struct {
	Trigger    string           `json:"trigger"`
	Cron       string           `json:"cron,omitempty"`
	Connectors []ShapeConnector `json:"connectors"`
	// Memory is the runtime's own memory setting for the stub, "on" or
	// "off", mirroring the routine's memory: key. It's only set on runtimes
	// with a native memory feature: elsewhere Fabrica keeps the memory, and
	// nothing about it is baked into the runtime.
	Memory string `json:"memory,omitempty"`
	// Autofix is the runtime's auto-fix setting for the stub, "on" or
	// "off", mirroring the routine's autofix: key: whether the runtime keeps
	// watching the pull requests a run opens and pushes fixes. It's only set
	// on runtimes with an auto-fix setting (autofix: native), and not for
	// local hooks, which run inside a session you already have open.
	Autofix string `json:"autofix,omitempty"`
	// Model and Effort are the model the runtime runs the routine on, the
	// first of the routine's models: that the runtime has, and the effort
	// to run it at. Model is the runtime's own ID for it, which isn't
	// always the name the routine uses (see provider.Matrix.ModelID). Both
	// are empty when the runtime picks (the routine lists no models, or the
	// runtime matched its default entry); Effort alone is empty for the
	// model's default effort.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Fast is the model's fast mode, "on" or "off", on runtimes that have
	// one for it (see provider.Matrix.FastModels): on when the routine's
	// entry says fast: true, off otherwise, so the runtime's own default
	// never decides. It's empty for models without a fast mode.
	Fast string `json:"fast,omitempty"`
	// Endpoint and TokenPlacement describe the connection to the Fabrica
	// server (cloud only).
	Endpoint       string `json:"endpoint,omitempty"`
	TokenPlacement string `json:"token_placement,omitempty"`
}

// ShapeConnector is one connector as it's actually installed in the
// runtime.
type ShapeConnector struct {
	Name string `json:"name"`
	// Kind is "native" (the runtime's built-in integration) or "mcp" (an
	// MCP server is registered, either because the connector is an MCP
	// server or because it's a native connector emulated through its
	// fallback).
	Kind   string               `json:"kind"`
	Server *blueprint.MCPServer `json:"server,omitempty"`
}

// Memory settings in a shape.
const (
	MemoryOn  = "on"
	MemoryOff = "off"
)

// Auto-fix settings in a shape.
const (
	AutofixOn  = "on"
	AutofixOff = "off"
)

// Fast mode settings in a shape.
const (
	FastOn  = "on"
	FastOff = "off"
)

// Fingerprint is a short hash of the shape. The same shape always gives
// the same fingerprint.
func (s Shape) Fingerprint() string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err) // Shape only holds plain data
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// ConnectorNames lists the shape's connector names.
func (s Shape) ConnectorNames() []string {
	out := make([]string, len(s.Connectors))
	for i, c := range s.Connectors {
		out[i] = c.Name
	}
	return out
}

// Use says how a runtime handles one thing a routine needs.
type Use struct {
	Name    string
	Support provider.Support
	// Dropped is true for optional connectors the runtime can't provide,
	// and for side effects that go through them.
	Dropped bool
}

// Evaluation is a routine checked against one runtime.
type Evaluation struct {
	Routine     *blueprint.Routine
	Trigger     Use
	Connectors  []Use
	SideEffects []Use
	// Memory is how the runtime gives the routine its memory. Its Support
	// is empty when the routine doesn't use memory.
	Memory Use
	// Autofix is how the runtime auto-fixes the routine's pull requests.
	// Its Support is empty when the routine doesn't ask for auto-fix.
	Autofix Use
	// Model is the entry of the routine's models: the runtime runs it on.
	// Its Name is empty when the routine lists no models, and
	// provider.DefaultModel when the runtime picks one itself.
	Model blueprint.Model
	Shape Shape
	// Diags holds an error for everything the runtime can't do.
	Diags diag.List
}

// OK reports whether the routine can run on the runtime.
func (e *Evaluation) OK() bool { return !e.Diags.HasErrors() }

// SideEffect returns how a side effect is handled, by name.
func (e *Evaluation) SideEffect(name string) (Use, bool) {
	for _, u := range e.SideEffects {
		if u.Name == name {
			return u, true
		}
	}
	return Use{}, false
}

// Evaluate checks a routine against a runtime's capability matrix and works
// out its shape. Problems are reported in Diags, but the shape is filled in
// as far as possible either way.
func Evaluate(f *blueprint.Blueprint, r *blueprint.Routine, t Target) *Evaluation {
	m := t.Runtime.Matrix
	e := &Evaluation{Routine: r}
	where := r.Where()
	rt := t.Runtime.Name()

	e.Trigger = Use{Name: r.Trigger.On, Support: m.Trigger(r.Trigger.On)}
	if e.Trigger.Support != provider.Native {
		e.Diags.Errorf("capability.trigger", where, "runtime %s doesn't support trigger %s", rt, r.Trigger.On).
			Hintf("triggers can't be emulated; deploy this routine to a runtime that has %s, or change its trigger", r.Trigger.On)
	}

	shape := Shape{Trigger: r.Trigger.On, Cron: r.Trigger.Cron}
	if t.Endpoint != "" {
		shape.Endpoint = t.Endpoint
		shape.TokenPlacement = m.TokenPlacement()
	}

	dropped := map[string]bool{}
	for _, mc := range r.Connectors {
		conn := f.Connectors[mc.Name]
		if conn == nil {
			continue // the blueprint loader already reported it
		}
		use := Use{Name: mc.Name}
		var installed *ShapeConnector
		switch conn.Kind {
		case blueprint.NativeConnector:
			use.Support = m.Connector(mc.Name)
			switch use.Support {
			case provider.Native:
				installed = &ShapeConnector{Name: mc.Name, Kind: blueprint.NativeConnector}
			case provider.Emulated:
				if conn.Fallback == nil {
					use.Support = provider.Unsupported
				} else {
					installed = &ShapeConnector{Name: mc.Name, Kind: blueprint.MCPConnector, Server: conn.Fallback}
				}
			}
		case blueprint.MCPConnector:
			use.Support = m.Connector("mcp." + conn.Server.Transport)
			if use.Support == provider.Native {
				installed = &ShapeConnector{Name: mc.Name, Kind: blueprint.MCPConnector, Server: conn.Server}
			}
		}
		if installed == nil {
			use.Support = provider.Unsupported
			if mc.Optional {
				use.Dropped = true
				dropped[mc.Name] = true
			} else {
				e.Diags.Errorf("capability.connector", where, "runtime %s can't provide connector %s", rt, connectorDesc(conn)).
					Hintf("%s", connectorHint(conn, m))
			}
		} else {
			shape.Connectors = append(shape.Connectors, *installed)
		}
		e.Connectors = append(e.Connectors, use)
	}

	for _, se := range r.SideEffects {
		use := Use{Name: se.Name, Support: m.SideEffect(se.Name)}
		switch {
		case dropped[se.Connector()]:
			// Its optional connector isn't available here, so neither is
			// the side effect.
			use.Dropped = true
		case use.Support == provider.Unsupported:
			e.Diags.Errorf("capability.side-effect", where, "runtime %s doesn't support side effect %s", rt, se.Name)
		}
		e.SideEffects = append(e.SideEffects, use)
	}

	if m.Memory == provider.Native {
		shape.Memory = MemoryOff
		if r.Memory {
			shape.Memory = MemoryOn
		}
	}
	if r.Memory {
		e.Memory = Use{Name: "memory", Support: m.Memory}
		if m.Memory != provider.Native && m.Memory != provider.Emulated {
			e.Diags.Errorf("capability.memory", where, "runtime %s can't give routine %s the memory it asks for", rt, r.Ref.Name).
				Hintf("drop memory: true from the routine if it can run without remembering anything, or deploy it to a runtime whose capability matrix has memory: native or emulated")
		}
	}

	// A local hook runs inside a session you already have open, so the
	// session's own auto-fix setting decides, not the stub.
	hook := m.Scope == provider.Local && r.Trigger.On != "schedule"
	if m.Autofix == provider.Native && !hook {
		shape.Autofix = AutofixOff
		if r.Autofix {
			shape.Autofix = AutofixOn
		}
	}
	if r.Autofix {
		e.Autofix = Use{Name: "autofix", Support: m.Autofix}
		switch {
		case m.Autofix != provider.Native:
			e.Diags.Errorf("capability.autofix", where, "runtime %s can't auto-fix the pull requests routine %s opens", rt, r.Ref.Name).
				Hintf("drop autofix: true from the routine if it can do without, or deploy it to a runtime whose capability matrix has autofix: native")
		case hook:
			e.Autofix.Support = provider.Unsupported
			e.Diags.Errorf("capability.autofix", where, "routine %s runs as a hook inside the session you have open in %s, so it has no auto-fix setting of its own", r.Ref.Name, rt).
				Hintf("drop autofix: true from the routine, and turn on auto-fix in that session when you want it; on %s, only schedule routines get their own auto-fix setting", rt)
		}
	}

	if len(r.Models) > 0 {
		if pick, ok := PickModel(r.Models, m); ok {
			e.Model = pick
			if !pick.IsDefault() {
				shape.Model, shape.Effort = m.ModelID(pick.Name), pick.Effort
				if m.FastModels[pick.Name] {
					shape.Fast = FastOff
					if pick.Fast {
						shape.Fast = FastOn
					}
				}
			}
		} else {
			e.Diags.Errorf("capability.model", where, "runtime %s has none of the models routine %s lists (%s)", rt, r.Ref.Name, describeModels(r.Models)).
				Hintf("%s", modelHint(r.Models, m))
		}
	}

	if m.SkillTransport == provider.Text {
		for _, name := range r.Skills {
			s := f.Skills[name]
			if s != nil && !s.TextOnly {
				e.Diags.Errorf("capability.skill", where, "skill %s isn't text-only (%s), so cloud runtime %s can't use it", name, s.NotTextOnlyReason, rt).
					Hintf("in v0.1 cloud runtimes can only use skills made of Markdown, plain text and data files; run this routine locally, or split the scripts out of the skill")
			}
		}
	}

	e.Shape = shape
	return e
}

// PickModel picks the first of a routine's models that the runtime has:
// the model at that effort (and in its fast mode, if the entry asks for
// it), or default, which every runtime has.
func PickModel(models []blueprint.Model, m *provider.Matrix) (blueprint.Model, bool) {
	for _, want := range models {
		if want.IsDefault() || m.HasModel(want.Name, want.Effort, want.Fast) {
			return want, true
		}
	}
	return blueprint.Model{}, false
}

func describeModels(models []blueprint.Model) string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.String()
	}
	return strings.Join(out, ", ")
}

// modelHint says what the runtime has instead, when none of a routine's
// models match.
func modelHint(models []blueprint.Model, m *provider.Matrix) string {
	const end = "or end models: with default, to run on the runtime's own default model where none of the others are there"
	for _, want := range models {
		if name := m.ModelWithID(want.Name); name != "" {
			return fmt.Sprintf("%s is what the runtime calls %s; blueprints name models the same way on every runtime, so use %s, %s", want.Name, name, name, end)
		}
		if efforts, ok := m.Models[want.Name]; ok {
			if want.Fast && !m.FastModels[want.Name] {
				return fmt.Sprintf("the runtime has %s, but no fast mode for it; drop fast: true from it, %s", want.Name, end)
			}
			if len(efforts) == 0 {
				return fmt.Sprintf("the runtime has %s, but no effort setting for it; list it without an effort, %s", want.Name, end)
			}
			return fmt.Sprintf("the runtime has %s, at effort %s; pick one of those, %s", want.Name, strings.Join(efforts, ", "), end)
		}
	}
	names := m.ModelNames()
	switch {
	case len(names) == 0:
		return "this runtime picks its model itself; end models: with default to let it"
	case len(names) > 8:
		names = append(names[:8:8], fmt.Sprintf("and %d more in its capability matrix", len(names)-8))
	}
	return fmt.Sprintf("add one it has (%s), %s", strings.Join(names, ", "), end)
}

func connectorDesc(c *blueprint.Connector) string {
	if c.Kind == blueprint.MCPConnector {
		return fmt.Sprintf("%s (an MCP server over %s)", c.Ref.Name, c.Server.Transport)
	}
	return c.Ref.Name
}

func connectorHint(c *blueprint.Connector, m *provider.Matrix) string {
	switch {
	case c.Kind == blueprint.NativeConnector && m.Connector(c.Ref.Name) == provider.Emulated:
		return fmt.Sprintf("this runtime has no built-in %s integration, but could use an MCP server instead; add a fallback: MCP server to connectors/%s.yaml", c.Ref.Name, c.Ref.Name)
	case c.Kind == blueprint.MCPConnector:
		return fmt.Sprintf("this runtime doesn't support MCP servers over %s; use another transport, or mark the connector optional (%s?) in the routine", c.Server.Transport, c.Ref.Name)
	default:
		return fmt.Sprintf("mark it optional (%s?) in the routine if it can run without it, or deploy to a runtime that has it", c.Ref.Name)
	}
}

// Identity builds a resource's stable identity from its endpoint, routine
// and repo, like "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app".
// Nothing random goes in, so the same resource always gets the same
// identity, and two home repos (which never share an endpoint) never claim
// each other's resources.
func Identity(endpoint, routine, repo string) string {
	return EndpointID(endpoint) + "#" + routine + "@" + repo
}

// EndpointID is an endpoint URL without its scheme or trailing slash.
func EndpointID(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return strings.TrimRight(endpoint, "/")
	}
	return u.Host + strings.TrimRight(u.Path, "/")
}

// Prompt is the text of a cloud stub. It tells the agent who it is and how
// to load its real instructions.
func Prompt(identity, routine, runtime, repo, fingerprint string) string {
	return fmt.Sprintf("You are routine `%s` (`%s`). Call `run_begin` with routine `%s`, runtime `%s`, repo `%s` and stub `%s`, and an intent that sums up what triggered you in at most 20 words. Then load the routine with `routine_get` from the Fabrica MCP server and follow it.",
		routine, identity, routine, runtime, repo, fingerprint)
}

// LocalPrompt is what a local routine's hook tells the agent.
func LocalPrompt(routine, factory, runtime, repo, fingerprint string) string {
	return fmt.Sprintf("Fabrica routine `%s` (from your local factory `%s`) should run now. Call `run_begin` with routine `%s`, runtime `%s`, repo `%s` and stub `%s`, and an intent that sums up what you're about to do in at most 20 words. Then load the routine with `routine_get` from the Fabrica MCP server and follow it.",
		routine, factory, routine, runtime, repo, fingerprint)
}

// DescribeTrigger turns a trigger into words for handoff instructions.
func DescribeTrigger(tr blueprint.Trigger, repo string) string {
	if tr.On == "schedule" {
		return fmt.Sprintf("on a schedule, cron `%s` (UTC)", tr.Cron)
	}
	if rest, ok := strings.CutPrefix(tr.On, "github."); ok {
		event, action, _ := strings.Cut(rest, ".")
		event = strings.ReplaceAll(event, "_", " ")
		if action != "" {
			return fmt.Sprintf("GitHub event `%s` (when a %s is %s) on `%s`", tr.On, event, strings.ReplaceAll(action, "_", " "), repo)
		}
		return fmt.Sprintf("GitHub event `%s` on `%s`", tr.On, repo)
	}
	return fmt.Sprintf("event `%s` on `%s`", tr.On, repo)
}

// DescribeMemory says what to do with the runtime's memory feature, for
// handoff instructions. needed is the routine's memory: key, and support is
// how the runtime provides memory.
func DescribeMemory(needed bool, support provider.Support) string {
	switch {
	case support == provider.Native && needed:
		return "turn the runtime's own memory **on**: this routine keeps notes between runs"
	case support == provider.Native:
		return "turn the runtime's own memory **off**: this routine doesn't use memory, so nothing from earlier runs should steer it"
	case needed:
		return "leave the runtime's own memory **off**, if it has one: Fabrica keeps this routine's memory and serves it over MCP"
	default:
		return "leave the runtime's own memory **off**, if it has one: this routine doesn't use memory"
	}
}

// DescribeAutofix says what to do with the runtime's auto-fix setting, for
// handoff instructions, given the shape's Autofix. It's empty for runtimes
// without one.
func DescribeAutofix(setting string) string {
	switch setting {
	case AutofixOn:
		return "turn auto-fix for pull requests **on**: after a run opens a pull request, the runtime keeps watching its CI and review comments, and pushes fixes"
	case AutofixOff:
		return "turn auto-fix for pull requests **off**: this routine doesn't ask for it, so nothing should push to its pull requests after a run ends"
	}
	return ""
}

// DescribeModel says which model to pick, for handoff instructions.
func DescribeModel(shape Shape) string {
	if shape.Model == "" {
		return "leave it at the runtime's default"
	}
	out := fmt.Sprintf("pick model `%s`, with effort `%s`", shape.Model, shape.Effort)
	if shape.Effort == "" {
		out = fmt.Sprintf("pick model `%s`, and leave its effort at the default", shape.Model)
	}
	switch shape.Fast {
	case FastOn:
		out += ", with fast mode **on**"
	case FastOff:
		out += ", with fast mode **off**"
	}
	return out
}

// DescribeConnector says how to set up one installed connector, for
// handoff instructions.
func DescribeConnector(c ShapeConnector) string {
	if c.Kind == blueprint.NativeConnector {
		return fmt.Sprintf("the runtime's built-in %s integration (connect it if it isn't connected yet)", c.Name)
	}
	s := c.Server
	var parts []string
	if s.URL != "" {
		parts = append(parts, fmt.Sprintf("MCP server at `%s` (%s)", s.URL, s.Transport))
	} else {
		parts = append(parts, fmt.Sprintf("local MCP server `%s %s`", s.Command, strings.Join(s.Args, " ")))
	}
	switch s.Auth {
	case "oauth":
		parts = append(parts, "signing in with OAuth when asked")
	case "bearer":
		parts = append(parts, fmt.Sprintf("with header `Authorization: Bearer <the %s secret>`", s.Secret))
	}
	if len(s.Env) > 0 {
		parts = append(parts, "with environment variables "+strings.Join(s.Env, ", "))
	}
	return strings.Join(parts, ", ")
}
