// Package localtools installs Fabrica into the AI coding tools on your
// machine: Claude Code, Cursor IDE and Codex CLI.
//
// `fabrica apply` writes three kinds of things, all at user level and never
// into a product repo:
//
//   - MCP servers: the `fabrica` server itself (which picks the right
//     factory each time a session starts), plus a `fabrica-<connector>`
//     server for each connector a local routine needs.
//   - Hooks: one per local routine, running `fabrica hook …`.
//   - Scheduled tasks: one per repo for each local routine with a
//     schedule trigger (Claude Code only).
//
// Everything Fabrica installs carries an identifying mark (the `fabrica`
// name prefix, or the `fabrica hook` command), so Fabrica finds its own
// installs just by reading the tool's config (or, for scheduled tasks,
// the task's folder name and description). It leaves everything else in
// those files exactly as it was.
package localtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tool is one local AI tool's configuration.
type Tool interface {
	// Runtime is the runtime name, like "claude-code-local".
	Runtime() string
	// Files lists the config files the tool reads and writes.
	Files() []string
	// Read returns what Fabrica currently has installed in the tool.
	Read() (Config, error)
	// Write replaces everything Fabrica installed with c. It leaves the
	// rest of the files alone, and doesn't touch files that wouldn't change.
	Write(c Config) error
}

// For returns the tool for a local runtime. homeDir is your home folder
// (where ~/.claude.json and friends live).
func For(runtime, homeDir string) (Tool, error) {
	switch runtime {
	case "claude-code-local":
		return &claudeCode{home: homeDir}, nil
	case "cursor-ide":
		return &cursorIDE{home: homeDir}, nil
	case "codex-cli":
		return &codexCLI{home: homeDir}, nil
	}
	return nil, fmt.Errorf("Fabrica doesn't know how to configure local runtime %q", runtime)
}

// Runtimes lists the local runtimes this package can configure.
func Runtimes() []string { return []string{"claude-code-local", "codex-cli", "cursor-ide"} }

// hookTimeoutSeconds is how long a tool should wait for `fabrica hook`.
const hookTimeoutSeconds = 30

// Claude Code: MCP servers in ~/.claude.json, hooks in
// ~/.claude/settings.json, scheduled tasks in ~/.claude/scheduled-tasks.

type claudeCode struct{ home string }

func (c *claudeCode) Runtime() string { return "claude-code-local" }

func (c *claudeCode) mcpFile() string   { return filepath.Join(c.home, ".claude.json") }
func (c *claudeCode) hooksFile() string { return filepath.Join(c.home, ".claude", "settings.json") }

func (c *claudeCode) Files() []string { return []string{c.mcpFile(), c.hooksFile(), c.tasksDir()} }

func (c *claudeCode) Read() (Config, error) {
	var cfg Config
	var err error
	if cfg.MCP, err = readMCPServers(c.mcpFile(), claudeEntryFromJSON); err != nil {
		return cfg, err
	}
	if cfg.Hooks, err = readClaudeHooks(c.hooksFile()); err != nil {
		return cfg, err
	}
	cfg.Tasks, err = readClaudeTasks(c.tasksDir())
	return cfg, err
}

func (c *claudeCode) Write(cfg Config) error {
	if err := writeMCPServers(c.mcpFile(), cfg.MCP, claudeEntryToJSON); err != nil {
		return err
	}
	if err := writeClaudeHooks(c.hooksFile(), cfg.Hooks); err != nil {
		return err
	}
	return writeClaudeTasks(c.tasksDir(), cfg.Tasks)
}

func claudeEntryToJSON(e MCPEntry) (*object, error) {
	o := newObject()
	switch e.Transport {
	case "stdio":
		_ = o.setValue("type", "stdio")
		_ = o.setValue("command", e.Command)
		_ = o.setValue("args", nonNil(e.Args))
		if len(e.EnvNames) > 0 {
			env := newObject()
			for _, name := range e.EnvNames {
				_ = env.setValue(name, "${"+name+"}")
			}
			raw, _ := env.MarshalJSON()
			o.set("env", raw)
		}
	default:
		_ = o.setValue("type", e.Transport)
		_ = o.setValue("url", e.URL)
		if e.BearerEnv != "" {
			_ = o.setValue("headers", map[string]string{"Authorization": "Bearer ${" + e.BearerEnv + "}"})
		}
	}
	return o, nil
}

func claudeEntryFromJSON(name string, raw json.RawMessage) (MCPEntry, error) {
	var v struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return MCPEntry{}, err
	}
	e := MCPEntry{Name: name, Transport: v.Type, Command: v.Command, Args: v.Args, URL: v.URL}
	if e.Transport == "" {
		e.Transport = "stdio"
		if v.URL != "" {
			e.Transport = "http"
		}
	}
	e.EnvNames = sortedKeys(v.Env)
	e.BearerEnv = bearerEnv(v.Headers["Authorization"], "${", "}")
	return e, nil
}

// Cursor: MCP servers in ~/.cursor/mcp.json, hooks in ~/.cursor/hooks.json.

type cursorIDE struct{ home string }

func (c *cursorIDE) Runtime() string { return "cursor-ide" }

func (c *cursorIDE) mcpFile() string   { return filepath.Join(c.home, ".cursor", "mcp.json") }
func (c *cursorIDE) hooksFile() string { return filepath.Join(c.home, ".cursor", "hooks.json") }

func (c *cursorIDE) Files() []string { return []string{c.mcpFile(), c.hooksFile()} }

func (c *cursorIDE) Read() (Config, error) {
	var cfg Config
	var err error
	if cfg.MCP, err = readMCPServers(c.mcpFile(), cursorEntryFromJSON); err != nil {
		return cfg, err
	}
	cfg.Hooks, err = readCursorHooks(c.hooksFile())
	return cfg, err
}

func (c *cursorIDE) Write(cfg Config) error {
	if err := writeMCPServers(c.mcpFile(), cfg.MCP, cursorEntryToJSON); err != nil {
		return err
	}
	return writeCursorHooks(c.hooksFile(), cfg.Hooks)
}

func cursorEntryToJSON(e MCPEntry) (*object, error) {
	o := newObject()
	switch e.Transport {
	case "stdio":
		_ = o.setValue("type", "stdio")
		_ = o.setValue("command", e.Command)
		_ = o.setValue("args", nonNil(e.Args))
		if len(e.EnvNames) > 0 {
			env := newObject()
			for _, name := range e.EnvNames {
				_ = env.setValue(name, "${env:"+name+"}")
			}
			raw, _ := env.MarshalJSON()
			o.set("env", raw)
		}
	default:
		_ = o.setValue("url", e.URL)
		if e.BearerEnv != "" {
			_ = o.setValue("headers", map[string]string{"Authorization": "Bearer ${env:" + e.BearerEnv + "}"})
		}
		if e.Transport == "sse" {
			_ = o.setValue("type", "sse")
		}
	}
	return o, nil
}

func cursorEntryFromJSON(name string, raw json.RawMessage) (MCPEntry, error) {
	var v struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return MCPEntry{}, err
	}
	e := MCPEntry{Name: name, Command: v.Command, Args: v.Args, URL: v.URL, Transport: v.Type}
	switch {
	case v.URL == "":
		e.Transport = "stdio"
	case e.Transport == "":
		e.Transport = "http"
	}
	e.EnvNames = sortedKeys(v.Env)
	e.BearerEnv = bearerEnv(v.Headers["Authorization"], "${env:", "}")
	return e, nil
}

// Codex CLI: MCP servers in ~/.codex/config.toml, hooks in
// ~/.codex/hooks.json (the same hook format as Claude Code).

type codexCLI struct{ home string }

func (c *codexCLI) Runtime() string { return "codex-cli" }

func (c *codexCLI) configFile() string { return filepath.Join(c.home, ".codex", "config.toml") }
func (c *codexCLI) hooksFile() string  { return filepath.Join(c.home, ".codex", "hooks.json") }

func (c *codexCLI) Files() []string { return []string{c.configFile(), c.hooksFile()} }

func (c *codexCLI) Read() (Config, error) {
	var cfg Config
	data, err := readOptional(c.configFile())
	if err != nil {
		return cfg, err
	}
	if cfg.MCP, err = readCodexServers(data); err != nil {
		return cfg, fmt.Errorf("%s: %w", c.configFile(), err)
	}
	cfg.Hooks, err = readClaudeHooks(c.hooksFile())
	return cfg, err
}

func (c *codexCLI) Write(cfg Config) error {
	data, err := readOptional(c.configFile())
	if err != nil {
		return err
	}
	if err := writeIfChanged(c.configFile(), data, writeCodexServers(data, cfg.MCP)); err != nil {
		return err
	}
	return writeClaudeHooks(c.hooksFile(), cfg.Hooks)
}

// Shared helpers for JSON files with an "mcpServers" object.

type entryFromJSON func(name string, raw json.RawMessage) (MCPEntry, error)
type entryToJSON func(MCPEntry) (*object, error)

func readMCPServers(file string, decode entryFromJSON) ([]MCPEntry, error) {
	root, _, err := readJSONFile(file)
	if err != nil {
		return nil, err
	}
	servers, err := root.child("mcpServers")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var out []MCPEntry
	for _, name := range servers.keys {
		if !IsFabricaEntry(name) {
			continue
		}
		e, err := decode(name, servers.vals[name])
		if err != nil {
			return nil, fmt.Errorf("%s: mcpServers.%s: %w", file, name, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func writeMCPServers(file string, entries []MCPEntry, encode entryToJSON) error {
	root, before, err := readJSONFile(file)
	if err != nil {
		return err
	}
	servers, err := root.child("mcpServers")
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	for _, name := range append([]string(nil), servers.keys...) {
		if IsFabricaEntry(name) {
			servers.delete(name)
		}
	}
	sorted := append([]MCPEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, e := range sorted {
		o, err := encode(e)
		if err != nil {
			return err
		}
		raw, _ := o.MarshalJSON()
		servers.set(e.Name, raw)
	}
	if _, had := root.get("mcpServers"); had || servers.len() > 0 {
		raw, _ := servers.MarshalJSON()
		root.set("mcpServers", raw)
	}
	return writeJSONIfChanged(file, before, root)
}

// Claude-style hooks: {"hooks": {"Event": [{"matcher": …, "hooks": [{"type": "command", "command": …}]}]}}.
// Codex CLI uses the same format.

func readClaudeHooks(file string) ([]Hook, error) {
	root, _, err := readJSONFile(file)
	if err != nil {
		return nil, err
	}
	hooks, err := root.child("hooks")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var out []Hook
	for _, event := range hooks.keys {
		groups, err := rawArray(hooks.vals[event])
		if err != nil {
			return nil, fmt.Errorf("%s: hooks.%s: %w", file, event, err)
		}
		for _, g := range groups {
			var group struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(g, &group); err != nil {
				continue // not something we wrote; leave it be
			}
			for _, h := range group.Hooks {
				if hook, ok := ParseHookCommand(h.Command); ok {
					out = append(out, hook)
				}
			}
		}
	}
	return out, nil
}

func writeClaudeHooks(file string, desired []Hook) error {
	root, before, err := readJSONFile(file)
	if err != nil {
		return err
	}
	hooks, err := root.child("hooks")
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	// Take out every hook Fabrica installed, dropping groups and events
	// that end up empty.
	for _, event := range append([]string(nil), hooks.keys...) {
		groups, err := rawArray(hooks.vals[event])
		if err != nil {
			return fmt.Errorf("%s: hooks.%s: %w", file, event, err)
		}
		var kept []json.RawMessage
		for _, g := range groups {
			group, err := parseObject(g)
			if err != nil {
				kept = append(kept, g)
				continue
			}
			inner, err := rawArray(group.vals["hooks"])
			if err != nil {
				kept = append(kept, g)
				continue
			}
			var keptInner []json.RawMessage
			for _, h := range inner {
				var v struct {
					Command string `json:"command"`
				}
				if json.Unmarshal(h, &v) == nil {
					if _, ours := ParseHookCommand(v.Command); ours {
						continue
					}
				}
				keptInner = append(keptInner, h)
			}
			if len(keptInner) == 0 {
				continue
			}
			if len(keptInner) != len(inner) {
				_ = group.setValue("hooks", keptInner)
				g, _ = group.MarshalJSON()
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			hooks.delete(event)
		} else {
			_ = hooks.setValue(event, kept)
		}
	}
	// Add the hooks we want, one group each.
	for _, h := range sortHooks(desired) {
		event, matcher, err := claudeEvent(h.Event)
		if err != nil {
			return err
		}
		group := newObject()
		if matcher != "" {
			_ = group.setValue("matcher", matcher)
		}
		_ = group.setValue("hooks", []map[string]any{{"type": "command", "command": HookCommand(h), "timeout": hookTimeoutSeconds}})
		raw, _ := group.MarshalJSON()
		groups, _ := rawArray(hooks.vals[event])
		_ = hooks.setValue(event, append(groups, raw))
	}
	if _, had := root.get("hooks"); had || hooks.len() > 0 {
		raw, _ := hooks.MarshalJSON()
		root.set("hooks", raw)
	}
	return writeJSONIfChanged(file, before, root)
}

// Cursor hooks: {"version": 1, "hooks": {"event": [{"command": …}]}}.

func readCursorHooks(file string) ([]Hook, error) {
	root, _, err := readJSONFile(file)
	if err != nil {
		return nil, err
	}
	hooks, err := root.child("hooks")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var out []Hook
	for _, event := range hooks.keys {
		entries, err := rawArray(hooks.vals[event])
		if err != nil {
			return nil, fmt.Errorf("%s: hooks.%s: %w", file, event, err)
		}
		for _, e := range entries {
			var v struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(e, &v) == nil {
				if h, ok := ParseHookCommand(v.Command); ok {
					out = append(out, h)
				}
			}
		}
	}
	return out, nil
}

func writeCursorHooks(file string, desired []Hook) error {
	root, before, err := readJSONFile(file)
	if err != nil {
		return err
	}
	hooks, err := root.child("hooks")
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	for _, event := range append([]string(nil), hooks.keys...) {
		entries, err := rawArray(hooks.vals[event])
		if err != nil {
			return fmt.Errorf("%s: hooks.%s: %w", file, event, err)
		}
		var kept []json.RawMessage
		for _, e := range entries {
			var v struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(e, &v) == nil {
				if _, ours := ParseHookCommand(v.Command); ours {
					continue
				}
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			hooks.delete(event)
		} else {
			_ = hooks.setValue(event, kept)
		}
	}
	for _, h := range sortHooks(desired) {
		event, err := cursorEvent(h.Event)
		if err != nil {
			return err
		}
		entry, _ := json.Marshal(map[string]any{"command": HookCommand(h), "timeout": hookTimeoutSeconds})
		entries, _ := rawArray(hooks.vals[event])
		_ = hooks.setValue(event, append(entries, entry))
	}
	_, hadHooks := root.get("hooks")
	if hadHooks || hooks.len() > 0 {
		if _, ok := root.get("version"); !ok {
			_ = root.setValue("version", 1)
		}
		raw, _ := hooks.MarshalJSON()
		root.set("hooks", raw)
	}
	return writeJSONIfChanged(file, before, root)
}

func sortHooks(hooks []Hook) []Hook {
	return Config{Hooks: hooks}.Normalize().Hooks
}

// File helpers.

func readOptional(file string) ([]byte, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func readJSONFile(file string) (*object, []byte, error) {
	data, err := readOptional(file)
	if err != nil {
		return nil, nil, err
	}
	o, err := parseObject(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s isn't valid JSON (%v); fix it by hand before running fabrica apply", file, err)
	}
	return o, data, nil
}

func writeJSONIfChanged(file string, before []byte, root *object) error {
	if root.len() == 0 && before == nil {
		return nil // nothing to write, and no file to begin with
	}
	after, err := root.pretty()
	if err != nil {
		return err
	}
	if before != nil {
		// Compare meaning, not formatting: if only whitespace would change,
		// leave the file exactly as the tool wrote it.
		var a, b bytes.Buffer
		if json.Compact(&a, before) == nil && json.Compact(&b, after) == nil && bytes.Equal(a.Bytes(), b.Bytes()) {
			return nil
		}
	}
	return writeIfChanged(file, before, after)
}

// writeIfChanged writes after to file unless it equals before. It keeps a
// backup of the previous content next to the file (file.fabrica.bak), keeps
// the file's permissions, and writes atomically so a crash never leaves a
// half-written config behind.
func writeIfChanged(file string, before, after []byte) error {
	if bytes.Equal(before, after) {
		return nil
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
		if err := os.WriteFile(file+".fabrica.bak", before, mode); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(after); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bearerEnv pulls NAME out of "Bearer ${NAME}" (or "Bearer ${env:NAME}").
func bearerEnv(header, open, close string) string {
	rest, ok := strings.CutPrefix(header, "Bearer "+open)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(rest, close)
}
