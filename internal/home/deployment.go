// Package home reads home repos: the git repos that say which factory runs
// on which product repos, in which runtimes.
//
// A home repo has two folders:
//
//	deployments/<name>.yaml   what to deploy where (you write these)
//	state/<name>.yaml         what's installed (only Fabrica writes these)
//
// The file name is the deployment's ID. A deployment with cloud runtimes
// (Claude routines, Cursor automations, Codex) is a cloud deployment; one
// with local runtimes (Claude Code, Cursor IDE, Codex CLI) is a local
// deployment.
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

// Wildcard is the repo entry that matches every repo not matched by
// another local deployment.
const Wildcard = "*"

// FactoryRef says which factory a deployment uses.
type FactoryRef struct {
	Git  string `yaml:"git"`
	Ref  string `yaml:"ref"`
	Path string `yaml:"path,omitempty"`
}

func (f FactoryRef) String() string {
	s := f.Git
	if f.Path != "" {
		s += "/" + f.Path
	}
	return s + "@" + f.Ref
}

// RepoEntry is one entry under repos:.
type RepoEntry struct {
	// Repo is the canonical repo name, or Wildcard.
	Repo string
	// Params override the deployment's params for this repo.
	Params map[string]string
}

// Deployment is one deployments/<name>.yaml.
type Deployment struct {
	Name     string
	File     string
	Factory  FactoryRef
	Server   string
	Runtimes []string
	Routines []string
	Repos    []RepoEntry
	Params   map[string]string
	// Scope is cloud or local, worked out from the runtimes.
	Scope provider.Scope
}

// Endpoint is the deployment's URL on its Fabrica server, like
// https://fabrica.acme.dev/pr-review. Fabrica builds it from server: and
// the deployment's name, so nobody types it by hand.
func (d *Deployment) Endpoint() string {
	if d.Server == "" {
		return ""
	}
	return strings.TrimRight(d.Server, "/") + "/" + d.Name
}

// IsWildcard reports whether the deployment is `repos: ["*"]`.
func (d *Deployment) IsWildcard() bool {
	return len(d.Repos) == 1 && d.Repos[0].Repo == Wildcard
}

// Covers reports whether the deployment lists repo (ignoring wildcards).
func (d *Deployment) Covers(repo string) bool {
	_, ok := d.repoEntry(repo)
	return ok
}

func (d *Deployment) repoEntry(repo string) (RepoEntry, bool) {
	for _, r := range d.Repos {
		if r.Repo == repo {
			return r, true
		}
	}
	return RepoEntry{}, false
}

// HasRuntime reports whether the deployment runs on a runtime.
func (d *Deployment) HasRuntime(rt string) bool { return contains(d.Runtimes, rt) }

// HasRoutine reports whether the deployment includes a routine.
func (d *Deployment) HasRoutine(r string) bool { return contains(d.Routines, r) }

// ParamsFor merges the deployment's params with one repo's overrides.
func (d *Deployment) ParamsFor(repo string) map[string]string {
	out := map[string]string{}
	for k, v := range d.Params {
		out[k] = v
	}
	entry, ok := d.repoEntry(repo)
	if !ok && d.IsWildcard() {
		entry = d.Repos[0]
	}
	for k, v := range entry.Params {
		out[k] = v
	}
	return out
}

// SpecificRepos lists the repos the deployment names (no wildcard).
func (d *Deployment) SpecificRepos() []string {
	var out []string
	for _, r := range d.Repos {
		if r.Repo != Wildcard {
			out = append(out, r.Repo)
		}
	}
	return out
}

type deploymentFile struct {
	Factory  *FactoryRef          `yaml:"factory"`
	Server   string               `yaml:"server"`
	Runtimes []string             `yaml:"runtimes"`
	Routines []string             `yaml:"routines"`
	Repos    []yaml.Node          `yaml:"repos"`
	Params   map[string]yaml.Node `yaml:"params"`
}

type repoFile struct {
	Repo   string               `yaml:"repo"`
	Params map[string]yaml.Node `yaml:"params"`
}

// ParseDeployment reads one deployment file. personal says whether it's in
// your personal home repo, where local deployments are allowed.
func ParseDeployment(name, file string, data []byte, personal bool) (*Deployment, diag.List) {
	var diags diag.List
	fail := func(format string, args ...any) (*Deployment, diag.List) {
		diags.Errorf("deployment.invalid", file, format, args...)
		return nil, diags
	}
	failHint := func(hint, format string, args ...any) (*Deployment, diag.List) {
		diags.Errorf("deployment.invalid", file, format, args...).Hintf("%s", hint)
		return nil, diags
	}
	if err := names.Validate(name); err != nil {
		return fail("the file name isn't a valid deployment name: %v", err)
	}
	var f deploymentFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return fail("%v", err)
	}
	d := &Deployment{Name: name, File: file, Server: strings.TrimSpace(f.Server)}

	if f.Factory == nil || strings.TrimSpace(f.Factory.Git) == "" || strings.TrimSpace(f.Factory.Ref) == "" {
		return fail("factory: needs both git and ref, like `factory: { git: github.com/acme/factory, ref: main }`")
	}
	d.Factory = FactoryRef{Git: strings.TrimSpace(f.Factory.Git), Ref: strings.TrimSpace(f.Factory.Ref), Path: strings.Trim(strings.TrimSpace(f.Factory.Path), "/")}

	if len(f.Runtimes) == 0 {
		return fail("runtimes: is empty; list where the factory should run, like [claude-code-cloud, cursor-cloud] or [claude-code-local, cursor-local]")
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
		// Unknown runtimes are folder providers from the factory, which
		// are always cloud in v0.1. Whether they exist is checked once the
		// factory is loaded.
		scope := provider.Cloud
		if p, ok := provider.Builtin(rt); ok {
			scope = p.Matrix.Scope
		}
		scopes[scope] = append(scopes[scope], rt)
	}
	if len(scopes) > 1 {
		return fail("runtimes: mixes cloud runtimes (%s) with local ones (%s); a deployment is either cloud or local, so split it into two files",
			strings.Join(scopes[provider.Cloud], ", "), strings.Join(scopes[provider.Local], ", "))
	}
	for scope := range scopes {
		d.Scope = scope
	}

	seen = map[string]bool{}
	for _, r := range f.Routines {
		if err := names.Validate(r); err != nil {
			return fail("routines: %v", err)
		}
		if seen[r] {
			return fail("routines: lists %q twice", r)
		}
		seen[r] = true
		d.Routines = append(d.Routines, r)
	}

	var err error
	if d.Params, err = scalarMap(f.Params); err != nil {
		return fail("params: %v", err)
	}
	if len(f.Repos) == 0 {
		return fail("repos: is empty; list the product repos this deployment covers, like [github.com/acme/app]")
	}
	seen = map[string]bool{}
	for _, node := range f.Repos {
		entry, err := parseRepoEntry(&node)
		if err != nil {
			return fail("repos: %v", err)
		}
		if seen[entry.Repo] {
			return fail("repos: lists %s twice", entry.Repo)
		}
		seen[entry.Repo] = true
		d.Repos = append(d.Repos, entry)
	}
	if seen[Wildcard] && len(d.Repos) > 1 {
		return fail(`repos: the wildcard "*" already matches every repo, so it must be the only entry`)
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
			return fail(`repos: the wildcard "*" is only allowed for local runtimes; cloud deployments must name each repo`)
		}
		if len(d.Routines) == 0 {
			return fail("routines: is empty; a cloud deployment exists to run routines, so list at least one")
		}
	case provider.Local:
		if !personal {
			return fail("local deployments only live in your personal home repo; team home repos are cloud-only in v0.1")
		}
		if d.Server != "" {
			return fail("server: is only for cloud deployments; local runtimes talk to `fabrica mcp` on your machine")
		}
	}
	return d, diags
}

// remoteServerDocs explains how to run a Fabrica server.
const remoteServerDocs = "https://github.com/staltz/fabrica/blob/main/docs/mcp-server.md#running-the-remote-server"

// missingServerHint says, step by step, how to fix a cloud deployment
// without server:.
func missingServerHint(file string, personal bool) string {
	steps := []string{
		fmt.Sprintf("if you already run a Fabrica server, add its URL to %s as a line like `server: https://fabrica.acme.dev`, then run this command again.", file),
		fmt.Sprintf("If you don't have one yet, start one with `fabrica mcp --http --home <this home repo's git URL>` (it needs FABRICA_SERVER_SECRET set), put it behind HTTPS, and use that URL as server:. Instructions: %s", remoteServerDocs),
	}
	if personal {
		steps = append(steps, "Or, to run the routines on this machine instead, change runtimes: to local ones, like [claude-code-local, cursor-local]; local deployments don't need a server.")
	}
	// Indent the later lines so they line up under "hint:".
	return strings.Join(steps, "\n  ")
}

func parseRepoEntry(node *yaml.Node) (RepoEntry, error) {
	var raw string
	var params map[string]string
	switch node.Kind {
	case yaml.ScalarNode:
		raw = node.Value
	case yaml.MappingNode:
		var rf repoFile
		if err := node.Decode(&rf); err != nil {
			return RepoEntry{}, err
		}
		// Decode isn't strict, so check the keys ourselves.
		for i := 0; i < len(node.Content); i += 2 {
			if k := node.Content[i].Value; k != "repo" && k != "params" {
				return RepoEntry{}, fmt.Errorf("line %d: unknown key %q (a repo entry has repo and params)", node.Content[i].Line, k)
			}
		}
		raw = rf.Repo
		var err error
		if params, err = scalarMap(rf.Params); err != nil {
			return RepoEntry{}, fmt.Errorf("params of %s: %v", raw, err)
		}
		if raw == "" {
			return RepoEntry{}, fmt.Errorf("line %d: a repo entry needs a repo key", node.Line)
		}
	default:
		return RepoEntry{}, fmt.Errorf("line %d: each repo is a name like github.com/acme/app, or `{ repo: …, params: … }`", node.Line)
	}
	if raw == Wildcard {
		return RepoEntry{Repo: Wildcard, Params: params}, nil
	}
	repo, err := repourl.Normalize(raw)
	if err != nil {
		return RepoEntry{}, err
	}
	return RepoEntry{Repo: repo, Params: params}, nil
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

// sortedNames returns the keys of a map of deployments, sorted.
func sortedNames(m map[string]*Deployment) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
