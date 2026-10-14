// Package provider describes the runtimes Fabrica can deploy to.
//
// A provider maps Fabrica's runtime-independent ideas (triggers,
// connectors, side effects, skills) onto one specific runtime, like Claude
// routines or Cursor IDE. The heart of a provider is its capability matrix:
// plain data that says, for each thing a routine might need, whether the
// runtime supports it natively, through emulation, or not at all.
//
// Because the matrix is plain data, `fabrica check` can tell you a routine
// won't work on a runtime without running any provider code. And published
// together, the matrices form a public table of what each runtime supports.
package provider

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/yamlx"
)

// Support says how a runtime handles one capability.
type Support string

const (
	// Native means the runtime has the capability built in.
	Native Support = "native"
	// Emulated means Fabrica fakes it: for a side effect, by turning it into
	// prompt instructions plus the connector it needs; for a connector, by
	// registering its MCP fallback server instead of a built-in integration.
	Emulated Support = "emulated"
	// Unsupported means the runtime can't do it. Using it is an error.
	Unsupported Support = "unsupported"
)

// Scope says whether a runtime runs in the cloud or on your machine.
type Scope string

const (
	Cloud Scope = "cloud"
	Local Scope = "local"
)

// ApplyMode is how Fabrica gets a stub into a runtime.
type ApplyMode string

const (
	// API means Fabrica creates, updates and deletes resources itself,
	// through the runtime's API (or, for local runtimes, by writing the
	// tool's config files).
	API ApplyMode = "api"
	// Handoff means Fabrica writes step-by-step instructions that a person,
	// or an AI with computer use, follows. Fabrica never does computer use
	// itself.
	Handoff ApplyMode = "handoff"
)

// SkillTransport is how a runtime gets at a skill's files.
type SkillTransport string

const (
	// Directory means the skill's whole folder is unpacked on disk. Only
	// local runtimes can do this in v0.1.
	Directory SkillTransport = "directory"
	// Text means the agent reads a skill's files one at a time over MCP,
	// which only works for text-only skills.
	Text SkillTransport = "text"
)

// Token placements: how a runtime passes the factory's secret token to
// the Fabrica MCP server.
const (
	TokenInHeader = "header" // Authorization: Bearer …
	TokenInURL    = "url"    // …/<factory>/t/<token>
)

// Matrix is a provider's capability matrix.
type Matrix struct {
	Name        string
	Scope       Scope
	Description string
	// ApplyModes are tried in order; the first one that works is used.
	ApplyModes []ApplyMode
	// LaunchModes are how Fabrica can start a one-off session here, for an
	// init routine: API means it starts the session itself (through the
	// runtime's API, or a local tool's command line), Handoff means it
	// writes instructions for a person to start it. Empty means init
	// routines can't run here.
	LaunchModes []ApplyMode
	// FindByIdentity says the runtime's API can list resources and read
	// their Fabrica identity back, so Fabrica can find (and never
	// duplicate) what it made, even after a crash.
	FindByIdentity bool
	// FindByRemoteID says the runtime's API can't list resources, but can
	// read one back by its remote ID. Fabrica then finds what it made
	// through the IDs in state. It records that a create is about to
	// happen before it calls the API, and records the new ID right after.
	// A create whose ID was never learned is not retried: that would risk
	// a duplicate. The api mode needs this or FindByIdentity.
	FindByRemoteID bool
	SkillTransport SkillTransport
	// MCPAuth says how the runtime can pass a token to an MCP server:
	// TokenInHeader and/or TokenInURL. Cloud runtimes need it, and so does
	// a local runtime that starts init sessions for cloud factories.
	MCPAuth     map[string]Support
	Triggers    map[string]Support
	Connectors  map[string]Support
	SideEffects map[string]Support
	// Memory says how the runtime lets a routine keep notes between runs:
	// Native means it has its own memory feature, which a stub turns on or
	// off to match the routine; Emulated means Fabrica keeps the memory
	// itself and serves it over MCP. Not listed means unsupported.
	Memory Support
	// Models are the models a stub can ask the runtime to run its routine
	// on, each with the efforts it can run at (empty when the runtime has
	// no effort setting for it). A routine runs on the first of its models:
	// that is listed here. Not listed means the runtime picks the model
	// itself, so only a routine's `default` entry matches. Models go by the
	// names blueprints use, their makers' model IDs, whatever the runtime
	// calls them.
	Models map[string][]string
	// FastModels are the models from Models that the runtime can run in a
	// fast mode (a faster variant of the model, usually at a higher price),
	// by the names blueprints use. A stub asks for fast mode on these
	// models, or turns it off; elsewhere, only entries that don't ask for
	// fast mode match.
	FastModels map[string]bool
	// ModelIDs are the runtime's own IDs for the models it calls something
	// else, by the names blueprints use. ModelID translates.
	ModelIDs map[string]string
	// Credentials are what the runtime's API needs, like an API key, sorted
	// by variable name. Without every one of them, api mode is unavailable
	// and the runtime falls back to its next apply mode.
	Credentials []Credential
}

// Credential is something a cloud runtime's API needs, like an API key.
// Fabrica finds it in its environment variable, or else where `fabrica
// auth login` saved it, and hands it to the API.
type Credential struct {
	// Env is the environment variable, like CURSOR_API_KEY.
	Env string
	// Aliases are other environment variables that work too, like
	// CURSOR_TOKEN. Env wins when both are set.
	Aliases []string
	// Description says what it is, for people, in a few words.
	Description string
	// URL is the page where you make one. `fabrica auth login` opens it.
	URL string
	// Instructions say what to do on that page.
	Instructions string
}

// Names are the credential's environment variables, Env first.
func (c Credential) Names() []string { return append([]string{c.Env}, c.Aliases...) }

// matrixFile is the on-disk shape of provider.yaml.
type matrixFile struct {
	Kind           string                    `yaml:"kind"`
	Description    string                    `yaml:"description"`
	ApplyModes     []string                  `yaml:"apply_modes"`
	LaunchModes    []string                  `yaml:"launch_modes"`
	FindByIdentity bool                      `yaml:"find_by_identity"`
	FindByRemoteID bool                      `yaml:"find_by_remote_id"`
	SkillTransport string                    `yaml:"skill_transport"`
	MCPAuth        map[string]Support        `yaml:"mcp_auth"`
	Triggers       map[string]Support        `yaml:"triggers"`
	Connectors     map[string]Support        `yaml:"connectors"`
	SideEffects    map[string]Support        `yaml:"side_effects"`
	Memory         Support                   `yaml:"memory"`
	Models         map[string][]string       `yaml:"models"`
	FastModels     []string                  `yaml:"fast_models"`
	ModelIDs       map[string]string         `yaml:"model_ids"`
	Credentials    map[string]credentialFile `yaml:"credentials"`
}

// credentialFile is one entry under credentials:, keyed by its variable.
type credentialFile struct {
	Description  string   `yaml:"description"`
	URL          string   `yaml:"url"`
	Instructions string   `yaml:"instructions"`
	Aliases      []string `yaml:"aliases"`
}

var (
	triggerKeyRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	connectorKeyRe  = regexp.MustCompile(`^([a-z0-9]+(-[a-z0-9]+)*|mcp\.(http|sse|stdio))$`)
	sideEffectKeyRe = regexp.MustCompile(`^(\*|[a-z0-9]+(-[a-z0-9]+)*\.(\*|[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*))$`)
	envNameRe       = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	modelNameRe     = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
)

// DefaultModel is the entry in a routine's models: that matches every
// runtime and means "the runtime's own default model". No model can have
// this name.
const DefaultModel = "default"

// Efforts are the effort levels, from least to most. Runtimes name some of
// them differently (like "extra-high" for xhigh), and their providers
// translate, so blueprints and matrices use one set of words.
var Efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// IsEffort reports whether s is one of Efforts.
func IsEffort(s string) bool {
	for _, e := range Efforts {
		if s == e {
			return true
		}
	}
	return false
}

// CheckModelName says what's wrong with a model name, if anything.
func CheckModelName(name string) error {
	switch {
	case name == DefaultModel:
		return fmt.Errorf("%q isn't a model, it means the runtime's own default", name)
	case !modelNameRe.MatchString(name):
		return fmt.Errorf("%q isn't a valid model name; use the model's ID from its maker, in lowercase letters, digits, dots, hyphens and underscores (like claude-opus-5-5 or gpt-5.5)", name)
	}
	return nil
}

// ParseMatrix reads a provider.yaml. Unknown keys and unknown values are
// errors, like everywhere else in Fabrica.
func ParseMatrix(name string, data []byte) (*Matrix, error) {
	var f matrixFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return nil, err
	}
	m := &Matrix{
		Name:           name,
		Scope:          Scope(f.Kind),
		Description:    strings.TrimSpace(f.Description),
		FindByIdentity: f.FindByIdentity,
		FindByRemoteID: f.FindByRemoteID,
		SkillTransport: SkillTransport(f.SkillTransport),
		MCPAuth:        orEmpty(f.MCPAuth),
		Triggers:       orEmpty(f.Triggers),
		Connectors:     orEmpty(f.Connectors),
		SideEffects:    orEmpty(f.SideEffects),
		Memory:         f.Memory,
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	switch m.Scope {
	case Cloud, Local:
	case "":
		add("kind: is missing; set it to cloud or local")
	default:
		add("kind: is %q; use cloud or local", f.Kind)
	}

	if len(f.ApplyModes) == 0 {
		add("apply_modes: is empty; list at least one of api or handoff")
	}
	seenMode := map[ApplyMode]bool{}
	for _, raw := range f.ApplyModes {
		mode := ApplyMode(raw)
		switch mode {
		case API, Handoff:
		default:
			add("apply_modes: has %q; use api or handoff", raw)
			continue
		}
		if seenMode[mode] {
			add("apply_modes: lists %q twice", raw)
		}
		seenMode[mode] = true
		m.ApplyModes = append(m.ApplyModes, mode)
	}
	seenLaunch := map[ApplyMode]bool{}
	for _, raw := range f.LaunchModes {
		mode := ApplyMode(raw)
		switch {
		case mode != API && mode != Handoff:
			add("launch_modes: has %q; use api or handoff", raw)
			continue
		case seenLaunch[mode]:
			add("launch_modes: lists %q twice", raw)
		}
		seenLaunch[mode] = true
		m.LaunchModes = append(m.LaunchModes, mode)
	}
	if seenMode[API] && !m.FindByIdentity && !m.FindByRemoteID {
		add("apply_modes: has api, but neither find_by_identity nor find_by_remote_id is true; api mode needs an API that can list resources and read their identity back, or at least read one back by its ID, so Fabrica never makes duplicates")
	}

	switch m.SkillTransport {
	case Directory, Text:
	case "":
		add("skill_transport: is missing; set it to directory or text")
	default:
		add("skill_transport: is %q; use directory or text", f.SkillTransport)
	}
	if m.Scope == Cloud && m.SkillTransport == Directory {
		add("skill_transport: a cloud runtime can't use directory in v0.1, because the Fabrica server can't put files on a cloud runtime's disk; use text")
	}

	for key, s := range m.MCPAuth {
		if key != TokenInHeader && key != TokenInURL {
			add("mcp_auth: has %q; use header or url", key)
		}
		if s != Native && s != Unsupported {
			add("mcp_auth.%s: is %q; use native or unsupported", key, s)
		}
	}
	if m.Scope == Cloud && m.MCPAuth[TokenInHeader] != Native && m.MCPAuth[TokenInURL] != Native {
		add("mcp_auth: a cloud runtime must support passing the token in a header or in the URL, or it can't reach the Fabrica server at all")
	}

	for key, s := range m.Triggers {
		if !triggerKeyRe.MatchString(key) {
			add("triggers: %q isn't a valid trigger name (like github.pull_request.opened)", key)
		}
		if s == Emulated {
			add("triggers.%s: triggers can't be emulated; use native or unsupported", key)
		} else if s != Native && s != Unsupported {
			add("triggers.%s: is %q; use native or unsupported", key, s)
		}
	}
	for key, s := range m.Connectors {
		if !connectorKeyRe.MatchString(key) {
			add("connectors: %q isn't valid; use a connector name (like github) or mcp.http, mcp.sse or mcp.stdio", key)
		}
		if strings.HasPrefix(key, "mcp.") && s == Emulated {
			add("connectors.%s: MCP transports can't be emulated; use native or unsupported", key)
		}
		if !s.valid() {
			add("connectors.%s: is %q; use native, emulated or unsupported", key, s)
		}
	}
	for key, s := range m.SideEffects {
		if !sideEffectKeyRe.MatchString(key) {
			add("side_effects: %q isn't valid; use connector.action (like github.pr_comment), connector.* or *", key)
		}
		if !s.valid() {
			add("side_effects.%s: is %q; use native, emulated or unsupported", key, s)
		}
	}

	if m.Memory == "" {
		m.Memory = Unsupported
	} else if !m.Memory.valid() {
		add("memory: is %q; use native, emulated or unsupported", f.Memory)
	}

	m.Models = parseModels(f.Models, add)
	m.FastModels = parseFastModels(f.FastModels, m.Models, add)
	m.ModelIDs = parseModelIDs(f.ModelIDs, m.Models, add)

	m.Credentials = parseCredentials(f.Credentials, add)
	if len(m.Credentials) > 0 && (m.Scope != Cloud || !seenMode[API] && !seenLaunch[API]) {
		add("credentials: are only for a cloud runtime's API, so they need kind: cloud and api in apply_modes or launch_modes")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return m, nil
}

func (s Support) valid() bool { return s == Native || s == Emulated || s == Unsupported }

// parseModels checks the models: entries.
func parseModels(in map[string][]string, add func(string, ...any)) map[string][]string {
	out := map[string][]string{}
	for name, efforts := range in {
		if err := CheckModelName(name); err != nil {
			add("models: %v", err)
		}
		seen := map[string]bool{}
		for _, e := range efforts {
			switch {
			case !IsEffort(e):
				add("models.%s: effort %q isn't one Fabrica knows; use %s", name, e, strings.Join(Efforts, ", "))
			case seen[e]:
				add("models.%s: lists effort %q twice", name, e)
			}
			seen[e] = true
		}
		out[name] = efforts
	}
	return out
}

// parseFastModels checks the fast_models: entries: each one is a model
// from models:, listed once.
func parseFastModels(in []string, models map[string][]string, add func(string, ...any)) map[string]bool {
	out := map[string]bool{}
	for _, name := range in {
		if _, ok := models[name]; !ok {
			add("fast_models: %s isn't in models:", name)
		}
		if out[name] {
			add("fast_models: lists %s twice", name)
		}
		out[name] = true
	}
	return out
}

// parseModelIDs checks the model_ids: entries: each one renames a model
// from models:, to an ID no other model has.
func parseModelIDs(in map[string]string, models map[string][]string, add func(string, ...any)) map[string]string {
	out := map[string]string{}
	owner := map[string]string{} // runtime ID → the model it belongs to
	for name, id := range in {
		if _, ok := models[name]; !ok {
			add("model_ids.%s: isn't in models:", name)
		}
		switch err := CheckModelName(id); {
		case err != nil:
			add("model_ids.%s: %v", name, err)
		case id == name:
			add("model_ids.%s: is the model's own name, so it doesn't need an entry", name)
		case owner[id] != "":
			add("model_ids: %s and %s are both %q", owner[id], name, id)
		}
		if _, ok := models[id]; ok && id != name {
			add("model_ids.%s: is %q, which models: lists as another model", name, id)
		}
		owner[id] = name
		out[name] = id
	}
	return out
}

// parseCredentials checks the credentials: entries and sorts them.
func parseCredentials(in map[string]credentialFile, add func(string, ...any)) []Credential {
	var out []Credential
	seen := map[string]string{} // variable → the credential it belongs to
	claim := func(name, owner string) {
		switch {
		case !envNameRe.MatchString(name):
			add("credentials: %q isn't an environment variable name (like ACME_API_KEY: uppercase letters, digits and underscores)", name)
		case strings.HasPrefix(name, "FABRICA_"):
			add("credentials: %s starts with FABRICA_, which is for Fabrica's own settings; pick another name", name)
		case seen[name] != "":
			add("credentials: %s is listed twice (in %s and %s)", name, seen[name], owner)
		}
		seen[name] = owner
	}
	for name, f := range in {
		claim(name, name)
		for _, alias := range f.Aliases {
			claim(alias, name)
		}
		c := Credential{Env: name, Aliases: f.Aliases, Description: strings.TrimSpace(f.Description), URL: strings.TrimSpace(f.URL), Instructions: strings.TrimSpace(f.Instructions)}
		if c.Description == "" {
			add("credentials.%s: description: is missing; say what it is in a few words, like \"an Acme API key\"", name)
		}
		if c.URL != "" {
			if u, err := url.Parse(c.URL); err != nil || u.Scheme != "https" || u.Host == "" {
				add("credentials.%s: url: %q must be an https URL: the page where you make one", name, c.URL)
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	return out
}

func orEmpty(m map[string]Support) map[string]Support {
	if m == nil {
		return map[string]Support{}
	}
	return m
}

// Trigger says how the runtime handles a trigger. Anything not listed is
// unsupported.
func (m *Matrix) Trigger(on string) Support { return lookup(m.Triggers, on) }

// Connector says how the runtime handles a connector key: a native
// connector's name (like "github"), or "mcp.<transport>" for MCP servers.
func (m *Matrix) Connector(key string) Support { return lookup(m.Connectors, key) }

// SideEffect says how the runtime handles a side effect, looking for the
// exact name first, then "<connector>.*", then "*".
func (m *Matrix) SideEffect(name string) Support {
	if s, ok := m.SideEffects[name]; ok {
		return s
	}
	if conn, _, ok := strings.Cut(name, "."); ok {
		if s, ok := m.SideEffects[conn+".*"]; ok {
			return s
		}
	}
	return lookup(m.SideEffects, "*")
}

func lookup(m map[string]Support, key string) Support {
	if s, ok := m[key]; ok {
		return s
	}
	return Unsupported
}

// HasModel reports whether the runtime can run a stub on model at effort,
// and in its fast mode if fast. An empty effort means the model's default
// effort, which every listed model has.
func (m *Matrix) HasModel(model, effort string, fast bool) bool {
	efforts, ok := m.Models[model]
	if !ok || fast && !m.FastModels[model] {
		return false
	}
	if effort == "" {
		return true
	}
	for _, e := range efforts {
		if e == effort {
			return true
		}
	}
	return false
}

// ModelID is what the runtime calls a model: its entry in ModelIDs, or else
// its name.
func (m *Matrix) ModelID(name string) string {
	if id, ok := m.ModelIDs[name]; ok {
		return id
	}
	return name
}

// ModelWithID is the name of the model the runtime calls id, when that
// isn't the model's name, and empty otherwise.
func (m *Matrix) ModelWithID(id string) string {
	for name, mid := range m.ModelIDs {
		if mid == id {
			return name
		}
	}
	return ""
}

// ModelNames lists the runtime's models, sorted.
func (m *Matrix) ModelNames() []string {
	out := make([]string, 0, len(m.Models))
	for name := range m.Models {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TokenPlacement picks how to pass the factory token: in a header when
// the runtime allows it (tokens in URLs can leak through logs and
// screenshots), otherwise in the URL.
func (m *Matrix) TokenPlacement() string {
	if m.MCPAuth[TokenInHeader] == Native {
		return TokenInHeader
	}
	return TokenInURL
}

// CanLaunch reports whether Fabrica can start an init session here at
// all, one way or another.
func (m *Matrix) CanLaunch() bool { return len(m.LaunchModes) > 0 }

// HasLaunchMode reports whether the provider supports a launch mode.
func (m *Matrix) HasLaunchMode(mode ApplyMode) bool {
	for _, have := range m.LaunchModes {
		if have == mode {
			return true
		}
	}
	return false
}

// HasMode reports whether the provider supports an apply mode.
func (m *Matrix) HasMode(mode ApplyMode) bool {
	for _, have := range m.ApplyModes {
		if have == mode {
			return true
		}
	}
	return false
}
