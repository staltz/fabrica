// Package home reads home repos: the git repos that say which blueprint runs
// on which product repos, in which runtimes.
//
// A home repo has two folders:
//
//	factories/<name>.yaml   what to deploy where (you write these)
//	state/<name>.yaml         what's installed (only Fabrica writes these)
//
// The file name is the factory's ID. A factory with cloud runtimes
// (Claude routines, Cursor automations, Codex, Copilot automations) is a
// cloud factory; one with local runtimes (Claude Code, Cursor IDE, Codex
// CLI, Copilot CLI) is a local factory.
package home

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/yamlx"
)

// Wildcard is the target that matches every repo not matched by
// another local factory.
const Wildcard = "*"

// BlueprintRef says which blueprint a factory uses.
type BlueprintRef struct {
	Git  string `yaml:"git"`
	Ref  string `yaml:"ref"`
	Path string `yaml:"path,omitempty"`
}

func (f BlueprintRef) String() string {
	s := f.Git
	if f.Path != "" {
		s += "/" + f.Path
	}
	return s + "@" + f.Ref
}

// Target is one entry under targets:.
type Target struct {
	// Repo is the canonical repo name, or Wildcard.
	Repo string
	// Params override the factory's params for this target.
	Params map[string]string
}

// Factory is one factories/<name>.yaml.
type Factory struct {
	Name      string
	File      string
	Blueprint BlueprintRef
	Server    string
	Runtimes  []string
	Routines  []string
	// RoutineRuntimes narrows routines to some of the factory's
	// runtimes. A routine that isn't in it runs on all of them.
	RoutineRuntimes map[string][]string
	Targets         []Target
	Params          map[string]string
	// NoInit is `init: false`: the factory never runs its blueprint's init
	// routine.
	NoInit bool
	// Scope is cloud or local, worked out from the runtimes.
	Scope provider.Scope
}

// Endpoint is the factory's URL on its Fabrica server, like
// https://fabrica.acme.dev/pr-review. Fabrica builds it from server: and
// the factory's name, so nobody types it by hand.
func (d *Factory) Endpoint() string {
	if d.Server == "" {
		return ""
	}
	return strings.TrimRight(d.Server, "/") + "/" + d.Name
}

// IsWildcard reports whether the factory is `targets: ["*"]`.
func (d *Factory) IsWildcard() bool {
	return len(d.Targets) == 1 && d.Targets[0].Repo == Wildcard
}

// Covers reports whether the factory lists repo (ignoring wildcards).
func (d *Factory) Covers(repo string) bool {
	_, ok := d.target(repo)
	return ok
}

func (d *Factory) target(repo string) (Target, bool) {
	for _, r := range d.Targets {
		if r.Repo == repo {
			return r, true
		}
	}
	return Target{}, false
}

// HasRuntime reports whether the factory runs on a runtime.
func (d *Factory) HasRuntime(rt string) bool { return contains(d.Runtimes, rt) }

// HasRoutine reports whether the factory includes a routine.
func (d *Factory) HasRoutine(r string) bool { return contains(d.Routines, r) }

// Runs reports whether the factory runs a routine on a runtime: both are
// in the factory, and the routine isn't narrowed to other runtimes.
func (d *Factory) Runs(routine, runtime string) bool {
	if !d.HasRoutine(routine) || !d.HasRuntime(runtime) {
		return false
	}
	only, ok := d.RoutineRuntimes[routine]
	return !ok || contains(only, runtime)
}

// RoutinesOn lists the routines the factory runs on a runtime.
func (d *Factory) RoutinesOn(runtime string) []string {
	var out []string
	for _, r := range d.Routines {
		if d.Runs(r, runtime) {
			out = append(out, r)
		}
	}
	return out
}

// ParamsFor merges the factory's params with one target's overrides.
func (d *Factory) ParamsFor(repo string) map[string]string {
	out := map[string]string{}
	for k, v := range d.Params {
		out[k] = v
	}
	entry, ok := d.target(repo)
	if !ok && d.IsWildcard() {
		entry = d.Targets[0]
	}
	for k, v := range entry.Params {
		out[k] = v
	}
	return out
}

// SpecificTargets lists the repos the factory targets by name (no wildcard).
func (d *Factory) SpecificTargets() []string {
	var out []string
	for _, r := range d.Targets {
		if r.Repo != Wildcard {
			out = append(out, r.Repo)
		}
	}
	return out
}

type factoryFile struct {
	Blueprint *BlueprintRef        `yaml:"blueprint"`
	Server    string               `yaml:"server"`
	Runtimes  []string             `yaml:"runtimes"`
	Routines  []yaml.Node          `yaml:"routines"`
	Targets   []yaml.Node          `yaml:"targets"`
	Params    map[string]yaml.Node `yaml:"params"`
	Init      *bool                `yaml:"init"`
}

type routineFile struct {
	Routine  string   `yaml:"routine"`
	Runtimes []string `yaml:"runtimes"`
}

type targetFile struct {
	Repo   string               `yaml:"repo"`
	Params map[string]yaml.Node `yaml:"params"`
}

// ParseFactory reads one factory file. personal says whether it's in
// your personal home repo, where local factories are allowed.
func ParseFactory(name, file string, data []byte, personal bool) (*Factory, diag.List) {
	var diags diag.List
	fail := func(format string, args ...any) (*Factory, diag.List) {
		diags.Errorf("factory.invalid", file, format, args...)
		return nil, diags
	}
	failHint := func(hint, format string, args ...any) (*Factory, diag.List) {
		diags.Errorf("factory.invalid", file, format, args...).Hintf("%s", hint)
		return nil, diags
	}
	if err := names.Validate(name); err != nil {
		return fail("the file name isn't a valid factory name: %v", err)
	}
	var f factoryFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return fail("%v", err)
	}
	d := &Factory{Name: name, File: file, Server: strings.TrimSpace(f.Server), NoInit: f.Init != nil && !*f.Init}

	if f.Blueprint == nil || strings.TrimSpace(f.Blueprint.Git) == "" || strings.TrimSpace(f.Blueprint.Ref) == "" {
		return fail("blueprint: needs both git and ref, like `blueprint: { git: github.com/acme/blueprint, ref: main }`")
	}
	d.Blueprint = BlueprintRef{Git: strings.TrimSpace(f.Blueprint.Git), Ref: strings.TrimSpace(f.Blueprint.Ref), Path: strings.Trim(strings.TrimSpace(f.Blueprint.Path), "/")}

	if len(f.Runtimes) == 0 {
		return fail("runtimes: is empty; list where the blueprint should run, like [claude-code-cloud, cursor-cloud] or [claude-code-local, cursor-local]")
	}
	seen := map[string]bool{}
	scopes := map[provider.Scope][]string{}
	for _, rt := range f.Runtimes {
		if err := names.Validate(rt); err != nil {
			return fail("runtimes: %q isn't a valid runtime name", rt)
		}
		if current, ok := provider.Renamed(rt); ok {
			return fail("runtimes: %s was renamed to %s; use the new name", rt, current)
		}
		if seen[rt] {
			return fail("runtimes: lists %q twice", rt)
		}
		seen[rt] = true
		d.Runtimes = append(d.Runtimes, rt)
		// Unknown runtimes are folder providers from the blueprint, which
		// are always cloud in v0.1. Whether they exist is checked once the
		// blueprint is loaded.
		scope := provider.Cloud
		if p, ok := provider.Builtin(rt); ok {
			scope = p.Matrix.Scope
		}
		scopes[scope] = append(scopes[scope], rt)
	}
	if len(scopes) > 1 {
		return fail("runtimes: mixes cloud runtimes (%s) with local ones (%s); a factory is either cloud or local, so split it into two files",
			strings.Join(scopes[provider.Cloud], ", "), strings.Join(scopes[provider.Local], ", "))
	}
	for scope := range scopes {
		d.Scope = scope
	}

	seen = map[string]bool{}
	for _, node := range f.Routines {
		r, only, err := parseRoutineEntry(&node, d.Runtimes)
		if err != nil {
			return fail("routines: %v", err)
		}
		if seen[r] {
			return fail("routines: lists %q twice", r)
		}
		seen[r] = true
		d.Routines = append(d.Routines, r)
		if only != nil {
			if d.RoutineRuntimes == nil {
				d.RoutineRuntimes = map[string][]string{}
			}
			d.RoutineRuntimes[r] = only
		}
	}

	var err error
	if d.Params, err = scalarMap(f.Params); err != nil {
		return fail("params: %v", err)
	}
	if len(f.Targets) == 0 {
		return fail("targets: is empty; list the product repos this factory covers, like [github.com/acme/app]")
	}
	seen = map[string]bool{}
	for _, node := range f.Targets {
		entry, err := parseTarget(&node)
		if err != nil {
			return fail("targets: %v", err)
		}
		if seen[entry.Repo] {
			return fail("targets: lists %s twice", entry.Repo)
		}
		seen[entry.Repo] = true
		d.Targets = append(d.Targets, entry)
	}
	if seen[Wildcard] && len(d.Targets) > 1 {
		return fail(`targets: the wildcard "*" already matches every repo, so it must be the only entry`)
	}

	switch d.Scope {
	case provider.Cloud:
		if d.Server == "" {
			return failHint(missingServerHint(file, personal),
				"server: is missing; runtimes: [%s] run in the cloud, and cloud runtimes reach Fabrica through a Fabrica server that you host",
				strings.Join(d.Runtimes, ", "))
		}
		u, err := url.Parse(d.Server)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return fail("server: %q must be an https URL (plain http is only allowed for localhost)", d.Server)
		}
		if seen[Wildcard] {
			return fail(`targets: the wildcard "*" is only allowed for local runtimes; cloud factories must name each repo`)
		}
		if len(d.Routines) == 0 {
			return fail("routines: is empty; a cloud factory exists to run routines, so list at least one")
		}
		for _, rt := range d.Runtimes {
			if len(d.RoutinesOn(rt)) == 0 {
				return fail("runtimes: lists %s, but none of the routines run on it; remove it, or add it to a routine's runtimes", rt)
			}
		}
	case provider.Local:
		if !personal {
			return fail("local factories only live in your personal home repo; team home repos are cloud-only in v0.1")
		}
		if d.Server != "" {
			return fail("server: is only for cloud factories; local runtimes talk to `fabrica mcp` on your machine")
		}
		if f.Init != nil {
			return fail("init: is only for cloud factories; local factories don't run their blueprint's init routine")
		}
	}
	return d, diags
}

// remoteServerDocs explains how to run a Fabrica server.
const remoteServerDocs = "https://github.com/staltz/fabrica/blob/main/docs/mcp-server.md#running-the-remote-server"

// missingServerHint says, step by step, how to fix a cloud factory
// without server:.
func missingServerHint(file string, personal bool) string {
	steps := []string{
		fmt.Sprintf("if you already run a Fabrica server, add its URL to %s as a line like `server: https://fabrica.acme.dev`, then run this command again.", file),
		fmt.Sprintf("If you don't have one yet, start one with `fabrica mcp --http --home <this home repo's git URL>` (it needs FABRICA_SERVER_SECRET set), put it behind HTTPS, and use that URL as server:. Instructions: %s", remoteServerDocs),
	}
	if personal {
		steps = append(steps, "Or, to run the routines on this machine instead, change runtimes: to local ones, like [claude-code-local, cursor-local]; local factories don't need a server.")
	}
	// Indent the later lines so they line up under "hint:".
	return strings.Join(steps, "\n  ")
}

// parseRoutineEntry reads one entry under routines:, which is a name, or
// `{ routine, runtimes }` to run it on only some of the factory's
// runtimes. only is nil when the routine runs on all of them.
func parseRoutineEntry(node *yaml.Node, runtimes []string) (name string, only []string, err error) {
	switch node.Kind {
	case yaml.ScalarNode:
		name = node.Value
	case yaml.MappingNode:
		var rf routineFile
		if err := node.Decode(&rf); err != nil {
			return "", nil, err
		}
		// Decode isn't strict, so check the keys ourselves.
		hasRuntimes := false
		for i := 0; i < len(node.Content); i += 2 {
			switch k := node.Content[i].Value; k {
			case "routine":
			case "runtimes":
				hasRuntimes = true
			default:
				return "", nil, fmt.Errorf("line %d: unknown key %q (a routine entry has routine and runtimes)", node.Content[i].Line, k)
			}
		}
		name = rf.Routine
		if name == "" {
			return "", nil, fmt.Errorf("line %d: a routine entry needs a routine key", node.Line)
		}
		if hasRuntimes {
			if len(rf.Runtimes) == 0 {
				return "", nil, fmt.Errorf("runtimes of %s is empty; list where it should run, or write just its name to run it on every runtime", name)
			}
			only = []string{}
			for _, rt := range rf.Runtimes {
				if current, ok := provider.Renamed(rt); ok {
					return "", nil, fmt.Errorf("runtimes of %s: %s was renamed to %s; use the new name", name, rt, current)
				}
				if !contains(runtimes, rt) {
					return "", nil, fmt.Errorf("runtimes of %s: %s isn't in the factory's runtimes: [%s]", name, rt, strings.Join(runtimes, ", "))
				}
				if contains(only, rt) {
					return "", nil, fmt.Errorf("runtimes of %s lists %q twice", name, rt)
				}
				only = append(only, rt)
			}
		}
	default:
		return "", nil, fmt.Errorf("line %d: each routine is a name like pr-review, or `{ routine: …, runtimes: […] }`", node.Line)
	}
	if err := names.Validate(name); err != nil {
		return "", nil, err
	}
	return name, only, nil
}

func parseTarget(node *yaml.Node) (Target, error) {
	var raw string
	var params map[string]string
	switch node.Kind {
	case yaml.ScalarNode:
		raw = node.Value
	case yaml.MappingNode:
		var rf targetFile
		if err := node.Decode(&rf); err != nil {
			return Target{}, err
		}
		// Decode isn't strict, so check the keys ourselves.
		for i := 0; i < len(node.Content); i += 2 {
			if k := node.Content[i].Value; k != "repo" && k != "params" {
				return Target{}, fmt.Errorf("line %d: unknown key %q (a target has repo and params)", node.Content[i].Line, k)
			}
		}
		raw = rf.Repo
		var err error
		if params, err = scalarMap(rf.Params); err != nil {
			return Target{}, fmt.Errorf("params of %s: %v", raw, err)
		}
		if raw == "" {
			return Target{}, fmt.Errorf("line %d: a target needs a repo key", node.Line)
		}
	default:
		return Target{}, fmt.Errorf("line %d: each repo is a name like github.com/acme/app, or `{ repo: …, params: … }`", node.Line)
	}
	if raw == Wildcard {
		return Target{Repo: Wildcard, Params: params}, nil
	}
	repo, err := repourl.Normalize(raw)
	if err != nil {
		return Target{}, err
	}
	return Target{Repo: repo, Params: params}, nil
}

// scalarMap turns params into plain strings. Params can be text, numbers or
// booleans, but not lists or maps.
func scalarMap(m map[string]yaml.Node) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m {
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s must be a single value, not a list or map", k)
		}
		out[k] = v.Value
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sortedNames returns the keys of a map of factories, sorted.
func sortedNames(m map[string]*Factory) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
