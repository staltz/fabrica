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
	// FindByIdentity says the runtime's API can list resources and read
	// their Fabrica identity back, so Fabrica can find (and never
	// duplicate) what it made, even after a crash.
	FindByIdentity bool
	// FindByRemoteID says the runtime's API can't list resources, but can
	// read one back by its remote ID. Fabrica then finds what it made
	// through the IDs in state, and records each new resource in state
	// right after creating it. The api mode needs this or FindByIdentity.
	FindByRemoteID bool
	SkillTransport SkillTransport
	// MCPAuth says how the runtime can pass a token to an MCP server:
	// TokenInHeader and/or TokenInURL. Only cloud runtimes need it.
	MCPAuth     map[string]Support
	Triggers    map[string]Support
	Connectors  map[string]Support
	SideEffects map[string]Support
	// Memory says how the runtime lets a routine keep notes between runs:
	// Native means it has its own memory feature, which a stub turns on or
	// off to match the routine; Emulated means Fabrica keeps the memory
	// itself and serves it over MCP. Not listed means unsupported.
	Memory Support
}

// matrixFile is the on-disk shape of provider.yaml.
type matrixFile struct {
	Kind           string             `yaml:"kind"`
	Description    string             `yaml:"description"`
	ApplyModes     []string           `yaml:"apply_modes"`
	FindByIdentity bool               `yaml:"find_by_identity"`
	FindByRemoteID bool               `yaml:"find_by_remote_id"`
	SkillTransport string             `yaml:"skill_transport"`
	MCPAuth        map[string]Support `yaml:"mcp_auth"`
	Triggers       map[string]Support `yaml:"triggers"`
	Connectors     map[string]Support `yaml:"connectors"`
	SideEffects    map[string]Support `yaml:"side_effects"`
	Memory         Support            `yaml:"memory"`
}

var (
	triggerKeyRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	connectorKeyRe  = regexp.MustCompile(`^([a-z0-9]+(-[a-z0-9]+)*|mcp\.(http|sse|stdio))$`)
	sideEffectKeyRe = regexp.MustCompile(`^(\*|[a-z0-9]+(-[a-z0-9]+)*\.(\*|[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*))$`)
)

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

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return m, nil
}

func (s Support) valid() bool { return s == Native || s == Emulated || s == Unsupported }

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

// TokenPlacement picks how to pass the factory token: in a header when
// the runtime allows it (tokens in URLs can leak through logs and
// screenshots), otherwise in the URL.
func (m *Matrix) TokenPlacement() string {
	if m.MCPAuth[TokenInHeader] == Native {
		return TokenInHeader
	}
	return TokenInURL
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
