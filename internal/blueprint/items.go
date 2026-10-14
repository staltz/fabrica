package blueprint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/provider"
)

// Origin says where an item came from: nowhere (it's defined right here in
// this blueprint) or through a chain of imports, like ["base", "community"]
// for an item that `base` imported from its own `community` import.
type Origin []string

// Local reports whether the item is defined in this blueprint itself.
func (o Origin) Local() bool { return len(o) == 0 }

func (o Origin) String() string {
	if o.Local() {
		return "this blueprint"
	}
	return "import " + strings.Join(o, " › ")
}

// Prepend returns the origin as seen one level up, through import alias.
func (o Origin) Prepend(alias string) Origin {
	return append(Origin{alias}, o...)
}

// ItemInfo is what every item has, whatever its kind.
type ItemInfo struct {
	Ref    names.Ref
	Origin Origin
	// File is the item's path inside the blueprint it was defined in, like
	// "routines/pr-review.md" or "skills/pdf". Used in messages.
	File string
	// Hash is a fingerprint of the item's content, used to tell when
	// upstream changed something (for `fabrica upgrade` and overrides).
	Hash string
	// Problems are errors found while reading the item. A broken item only
	// becomes an error for you if you actually use it: if you leave it out
	// of an import with only: or exclude:, its problems go away too.
	Problems diag.List
}

func (i *ItemInfo) info() *ItemInfo { return i }

// Where is a short description of the item for messages, like
// "routines/pr-review.md (from import base)".
func (i *ItemInfo) Where() string {
	if i.Origin.Local() {
		return i.File
	}
	return i.File + " (from " + i.Origin.String() + ")"
}

// item is implemented by every kind of item.
type item interface {
	info() *ItemInfo
	// withOrigin returns a copy of the item with a new origin. Imported
	// items are shared between everyone who imports them, so we copy
	// instead of changing them in place.
	withOrigin(Origin) item
}

// Skill is a standard Agent Skills folder, completely unchanged.
type Skill struct {
	ItemInfo
	Description string
	// Dir is the skill's folder on disk.
	Dir string
	// Files lists every file in the folder, relative to Dir, sorted.
	Files []SkillFile
	// TextOnly is true when every file is Markdown, plain text or data and
	// none is executable. Only text-only skills work in cloud runtimes in
	// v0.1, because the agent there reads files one at a time over MCP.
	TextOnly bool
	// NotTextOnlyReason says why a skill isn't text-only.
	NotTextOnlyReason string
	// Body is SKILL.md without its frontmatter.
	Body string
}

// SkillFile is one file inside a skill folder.
type SkillFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable,omitempty"`
}

func (s *Skill) withOrigin(o Origin) item { c := *s; c.Origin = o; return &c }

// Trigger is what makes a routine fire.
type Trigger struct {
	// On is the event, like "github.pull_request.opened" or "schedule".
	On string
	// Cron is the schedule, for On == "schedule".
	Cron string
}

func (t Trigger) String() string {
	if t.Cron != "" {
		return t.On + " (" + t.Cron + ")"
	}
	return t.On
}

// SideEffect is something a routine is allowed to do to the outside world,
// like `github.pr_comment` or `slack.notify: {channel: "#eng"}`.
type SideEffect struct {
	// Name is "<connector>.<action>".
	Name string
	// Args are the side effect's settings. Values may contain
	// {{params.x}} placeholders, filled in when the routine is served.
	Args []Arg
}

// Connector returns the connector a side effect goes through.
func (s SideEffect) Connector() string {
	conn, _, _ := strings.Cut(s.Name, ".")
	return conn
}

// Arg is one key/value setting. We keep them in a slice so they keep the
// order they were written in.
type Arg struct {
	Key   string
	Value string
}

// Param is a value a factory can (or must) set for a routine.
type Param struct {
	Name        string
	Required    bool
	Default     string
	HasDefault  bool
	Description string
}

// Routine is something an agent does automatically when a trigger fires.
type Routine struct {
	ItemInfo
	Description string
	Trigger     Trigger
	// Skills are the blueprint skills the routine uses.
	Skills      []string
	Connectors  []names.MaybeName
	SideEffects []SideEffect
	Params      []Param
	// Memory says the routine keeps notes between runs. It's the source of
	// truth for memory on every runtime: the runtime's own memory feature is
	// turned on where the routine needs it and off where it doesn't, and
	// Fabrica keeps the memory itself where the runtime has none.
	Memory bool
	// Models are the models the routine would like to run on, in order of
	// preference: each runtime runs it on the first one it has. Empty means
	// every runtime uses its own default model.
	Models []Model
	// Prompt is the routine's instructions (the Markdown body).
	Prompt string
}

func (r *Routine) withOrigin(o Origin) item { c := *r; c.Origin = o; return &c }

// Param looks up a declared param by name.
func (r *Routine) Param(name string) (Param, bool) {
	for _, p := range r.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Model is one entry of a routine's models: a model and the effort to run
// it at.
type Model struct {
	// Name is the model's ID from its maker, like claude-opus-5-5 or
	// gpt-5.5, the same on every runtime (each runtime's matrix says what
	// it calls the model, if that's different), or provider.DefaultModel
	// for the runtime's own default.
	Name string
	// Effort is one of provider.Efforts, or empty for the model's default
	// effort.
	Effort string
}

// IsDefault reports whether the entry means the runtime's own default.
func (m Model) IsDefault() bool { return m.Name == provider.DefaultModel }

// String writes the entry like "claude-opus-5-5 (high)".
func (m Model) String() string {
	if m.Effort == "" {
		return m.Name
	}
	return m.Name + " (" + m.Effort + ")"
}

// Rule is an always-on instruction, in Cursor's rule format.
type Rule struct {
	ItemInfo
	Description string
	Globs       []string
	AlwaysApply bool
	Body        string
}

func (r *Rule) withOrigin(o Origin) item { c := *r; c.Origin = o; return &c }

// How a rule becomes active, following Cursor's rules.
const (
	ActiveAlways = "always" // alwaysApply: true
	ActiveGlobs  = "globs"  // when the agent touches matching files
	ActiveAgent  = "agent"  // the agent decides, based on the description
	ActiveManual = "manual" // only when asked for by name
)

// Activation says when the rule applies.
func (r *Rule) Activation() string {
	switch {
	case r.AlwaysApply:
		return ActiveAlways
	case len(r.Globs) > 0:
		return ActiveGlobs
	case r.Description != "":
		return ActiveAgent
	default:
		return ActiveManual
	}
}

// Connector kinds.
const (
	NativeConnector = "native"
	MCPConnector    = "mcp"
)

// Connector is an integration the agent needs: a runtime's built-in one
// (like GitHub) or an MCP server.
type Connector struct {
	ItemInfo
	Kind        string
	Description string
	// Server is the MCP server, for Kind == "mcp".
	Server *MCPServer
	// Fallback is an optional MCP server for a native connector, used on
	// runtimes that don't have the built-in integration (the capability
	// matrix marks the connector as emulated there).
	Fallback *MCPServer
}

func (c *Connector) withOrigin(o Origin) item { cp := *c; cp.Origin = o; return &cp }

// MCPServer describes how to reach an MCP server.
type MCPServer struct {
	URL       string   `json:"url,omitempty"`
	Transport string   `json:"transport"`
	Auth      string   `json:"auth"`
	Secret    string   `json:"secret,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
}

// ProviderItem is a folder provider shipped inside a blueprint.
type ProviderItem struct {
	ItemInfo
	Provider *provider.Provider
}

func (p *ProviderItem) withOrigin(o Origin) item { c := *p; c.Origin = o; return &c }

// Validation helpers shared by the parsers.

var (
	triggerRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	sideEffectRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	paramNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	cronFieldRe  = regexp.MustCompile(`^[0-9A-Za-z*/,\-?#LW]+$`)

	// paramRefRe finds {{params.name}} placeholders.
	paramRefRe = regexp.MustCompile(`\{\{\s*params\.([A-Za-z0-9_]+)\s*\}\}`)
	// paramStartRe finds anything that starts like a placeholder, so we can
	// catch broken ones like {{params.x} or {{ params }}.
	paramStartRe = regexp.MustCompile(`\{\{\s*params\b`)
)

// ParamRefs returns the param names used in {{params.x}} placeholders in
// text, and complains about placeholders that are written wrong.
func ParamRefs(text string) ([]string, error) {
	matches := paramRefRe.FindAllStringSubmatch(text, -1)
	if starts := paramStartRe.FindAllStringIndex(text, -1); len(starts) != len(matches) {
		return nil, fmt.Errorf("there's a malformed {{params...}} placeholder; write them like {{params.slack_channel}}")
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

// RenderParams fills in {{params.x}} placeholders. Params without a value
// become empty text.
func RenderParams(text string, values map[string]string) string {
	return paramRefRe.ReplaceAllStringFunc(text, func(m string) string {
		name := paramRefRe.FindStringSubmatch(m)[1]
		return values[name]
	})
}

func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("cron %q should have 5 fields (minute hour day-of-month month day-of-week), but it has %d", expr, len(fields))
	}
	for _, f := range fields {
		if !cronFieldRe.MatchString(f) {
			return fmt.Errorf("cron %q has an invalid field %q", expr, f)
		}
	}
	return nil
}
