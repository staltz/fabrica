package provider

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// Provider is one runtime Fabrica can deploy to: its capability matrix plus
// whatever it needs to apply changes (handoff instructions, and for folder
// providers, an optional `api` program).
type Provider struct {
	Matrix *Matrix
	// Builtin is true for providers compiled into the fabrica binary.
	Builtin bool
	// Dir is where a folder provider lives on disk. Empty for built-ins.
	Dir string
	// APIPath is a folder provider's `api` program, if it has one.
	APIPath string
	// handoff is the parsed handoff.md template, if the provider has one.
	handoff *template.Template
}

// Name is the runtime's name, like "claude-code-cloud" or "cursor-local".
func (p *Provider) Name() string { return p.Matrix.Name }

// IsLocal reports whether this runtime runs on your machine.
func (p *Provider) IsLocal() bool { return p.Matrix.Scope == Local }

//go:embed builtin
var builtinFS embed.FS

var builtins = mustLoadBuiltins()

func mustLoadBuiltins() map[string]*Provider {
	out := map[string]*Provider{}
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		name := e.Name()
		data, err := fs.ReadFile(builtinFS, path.Join("builtin", name, "provider.yaml"))
		if err != nil {
			panic(err)
		}
		m, err := ParseMatrix(name, data)
		if err != nil {
			panic(fmt.Sprintf("built-in provider %s: %v", name, err))
		}
		p := &Provider{Matrix: m, Builtin: true}
		if tmpl, err := fs.ReadFile(builtinFS, path.Join("builtin", name, "handoff.md")); err == nil {
			p.handoff, err = parseHandoff(name, string(tmpl))
			if err != nil {
				panic(err)
			}
		}
		out[name] = p
	}
	return out
}

// Builtin returns the built-in provider for a runtime, if there is one.
func Builtin(name string) (*Provider, bool) {
	p, ok := builtins[name]
	return p, ok
}

// BuiltinNames lists the built-in runtimes, sorted.
func BuiltinNames() []string {
	var names []string
	for name := range builtins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsBuiltin reports whether name is a built-in runtime.
func IsBuiltin(name string) bool {
	_, ok := builtins[name]
	return ok
}

// renamed maps built-in runtimes' names in earlier Fabrica v0.1 releases to
// their current names. The old names don't work anymore: Fabrica only
// recognizes them to say what to use instead, and to move old state over.
var renamed = map[string]string{
	"claude":      "claude-code-cloud", // until v0.1.1
	"claude-code": "claude-code-local", // until v0.1.1
	"cursor":      "cursor-cloud",      // until v0.1.3
	"cursor-ide":  "cursor-local",      // until v0.1.3
}

// Renamed returns the current name of the built-in runtime that an earlier
// Fabrica release called name.
func Renamed(name string) (string, bool) {
	current, ok := renamed[name]
	return current, ok
}

// LoadFolder loads a folder provider from providers/<name>/ in a factory.
//
// A folder provider must have a provider.yaml. If its apply modes include
// api it needs an executable `api` program next to it, and if they include
// handoff it needs a handoff.md template. In v0.1 folder providers can only
// describe cloud runtimes, and can't replace a built-in one.
func LoadFolder(name, dir string) (*Provider, error) {
	if IsBuiltin(name) {
		return nil, fmt.Errorf("%q is a built-in provider, and in v0.1 a folder provider can't replace a built-in one; pick another name", name)
	}
	if current, ok := Renamed(name); ok {
		return nil, fmt.Errorf("%q is what built-in provider %s used to be called, so it's reserved; pick another name", name, current)
	}
	data, err := os.ReadFile(filepath.Join(dir, "provider.yaml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("providers/%s has no provider.yaml", name)
		}
		return nil, err
	}
	m, err := ParseMatrix(name, data)
	if err != nil {
		return nil, fmt.Errorf("providers/%s/provider.yaml: %w", name, err)
	}
	if m.Scope == Local {
		return nil, fmt.Errorf("providers/%s/provider.yaml: folder providers for local runtimes aren't supported in v0.1; use kind: cloud", name)
	}
	p := &Provider{Matrix: m, Dir: dir}
	if m.HasMode(API) {
		apiPath := filepath.Join(dir, "api")
		info, err := os.Stat(apiPath)
		switch {
		case err != nil:
			return nil, fmt.Errorf("providers/%s lists api in apply_modes but has no api program next to provider.yaml", name)
		case info.IsDir() || info.Mode().Perm()&0o111 == 0:
			return nil, fmt.Errorf("providers/%s/api isn't an executable file; mark it executable (chmod +x) and commit that", name)
		}
		p.APIPath = apiPath
	}
	if m.HasMode(Handoff) {
		tmpl, err := os.ReadFile(filepath.Join(dir, "handoff.md"))
		if err != nil {
			return nil, fmt.Errorf("providers/%s lists handoff in apply_modes but has no handoff.md", name)
		}
		p.handoff, err = parseHandoff(name, string(tmpl))
		if err != nil {
			return nil, fmt.Errorf("providers/%s/handoff.md: %w", name, err)
		}
	}
	return p, nil
}

// HandoffData is what a handoff.md template gets to work with.
type HandoffData struct {
	// ID is the handoff's ID, used with `fabrica confirm`.
	ID string
	// Action is "create", "update" or "delete".
	Action     string
	Runtime    string
	Deployment string
	// Identity is the resource's stable identity. Templates should ask for
	// it to be used as the resource's name, so Fabrica can recognize it.
	Identity string
	Routine  string
	Repo     string
	// RemoteID is the runtime's own ID for the resource, when Fabrica knows
	// it (for updates and deletes).
	RemoteID    string
	Trigger     HandoffTrigger
	Connectors  []HandoffConnector
	Memory      HandoffMemory
	Fabrica     HandoffFabrica
	Prompt      string
	Fingerprint string
	// ConfirmCommand is the exact `fabrica confirm …` line to run at the end.
	ConfirmCommand string
}

// HandoffTrigger describes the trigger to set up.
type HandoffTrigger struct {
	On          string
	Cron        string
	Description string
}

// HandoffConnector describes one connector to set up.
type HandoffConnector struct {
	Name string
	// Setup is a one-line description of how to set it up, like "the
	// built-in GitHub integration" or "MCP server at https://…".
	Setup string
}

// HandoffMemory describes what to do with the runtime's own memory
// feature, mirroring the routine's memory: key.
type HandoffMemory struct {
	// Needed is the routine's memory: key: it keeps notes between runs.
	Needed bool
	// Support is how the runtime provides memory: "native" (its own memory
	// feature, to turn on when Needed and off otherwise), "emulated"
	// (Fabrica keeps the memory, so the runtime's own stays off) or
	// "unsupported".
	Support string
	// Setup is a one-line description of what to set, like "turn the
	// runtime's own memory **off**: …".
	Setup string
}

// HandoffFabrica describes the connection to the Fabrica server.
type HandoffFabrica struct {
	// URL is the URL to configure. When the token goes in the URL, it's
	// already included here.
	URL string
	// Header is the Authorization header value ("Bearer …"), when the token
	// goes in a header. Empty otherwise.
	Header string
}

var handoffFuncs = template.FuncMap{
	// indent indents every line of s by n spaces, which keeps a pasted
	// prompt inside a Markdown list item's code block.
	"indent": func(n int, s string) string {
		pad := strings.Repeat(" ", n)
		lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
		for i, l := range lines {
			if l != "" {
				lines[i] = pad + l
			}
		}
		return strings.Join(lines, "\n")
	},
}

func parseHandoff(name, text string) (*template.Template, error) {
	return template.New(name).Funcs(handoffFuncs).Option("missingkey=error").Parse(text)
}

// RenderHandoff fills in the provider's handoff template.
func (p *Provider) RenderHandoff(data HandoffData) (string, error) {
	if p.handoff == nil {
		return "", fmt.Errorf("provider %s has no handoff template", p.Name())
	}
	var buf bytes.Buffer
	if err := p.handoff.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("provider %s: filling in handoff.md: %w", p.Name(), err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}

// Mode picks the apply mode to use: the first listed mode that this
// provider can actually do. For api that means it has a way to talk to the
// runtime (local built-ins write config files; folder providers need their
// api program).
func (p *Provider) Mode() (ApplyMode, error) {
	for _, mode := range p.Matrix.ApplyModes {
		switch mode {
		case API:
			if p.IsLocal() || p.APIPath != "" {
				return API, nil
			}
		case Handoff:
			if p.handoff != nil {
				return Handoff, nil
			}
		}
	}
	return "", fmt.Errorf("runtime %s has no apply mode Fabrica can use (it supports %v)", p.Name(), p.Matrix.ApplyModes)
}
