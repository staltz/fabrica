package localtools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Local triggers: the events a local routine can fire on.
const (
	SessionStart = "session.start"
	PromptSubmit = "prompt.submit"
	SessionStop  = "session.stop"
	GitPreCommit = "git.pre_commit"
	GitPrePush   = "git.pre_push"
)

// MCPEntry is an MCP server Fabrica registers in a local tool: the
// `fabrica` server itself, or a `fabrica-<connector>` server that a local
// routine needs.
type MCPEntry struct {
	Name string
	// Transport is "stdio", "http" or "sse".
	Transport string
	Command   string
	Args      []string
	// EnvNames are environment variables passed through to a stdio server.
	EnvNames []string
	URL      string
	// BearerEnv is the environment variable holding a bearer token for an
	// http or sse server.
	BearerEnv string
}

// Hook is a local routine, installed as a tool hook. It can also be the
// session pointer hook (see SessionHook), which belongs to a runtime
// rather than to a routine.
type Hook struct {
	// Binary is the fabrica binary the hook runs.
	Binary      string
	Runtime     string
	Event       string
	Factory     string
	Routine     string
	Fingerprint string
	// Session marks the session pointer hook. It has no Factory, Routine
	// or Fingerprint.
	Session bool
}

// SessionHook is the session pointer hook for a runtime. When a session
// starts, it tells the agent where its own transcript lives, so run_begin
// can record it. There's one per local runtime that has a session-start
// event, whatever routines its factories run.
func SessionHook(binary, runtime string) Hook {
	return Hook{Binary: binary, Runtime: runtime, Event: SessionStart, Session: true}
}

// HasSessionStart reports whether a local runtime has a session-start hook
// event, which the session pointer hook needs.
func HasSessionStart(runtime string) bool {
	var err error
	switch runtime {
	case "claude-code-local", "codex-cli":
		_, _, err = claudeEvent(SessionStart)
	case "copilot-local":
		_, _, err = copilotEvent(SessionStart)
	case "cursor-local":
		_, err = cursorEvent(SessionStart)
	default:
		return false
	}
	return err == nil
}

// Task is a local routine with a schedule trigger, installed as a Claude
// Code desktop scheduled task (one per repo, since a task runs in one
// folder).
type Task struct {
	Factory string
	Routine string
	Repo    string
	// Cron is the routine's schedule, in UTC like everywhere in Fabrica.
	Cron        string
	Fingerprint string
	// Autofix is the task's auto-fix setting for pull requests, "on" or
	// "off": the setting of the native side effect github.pr_autofix (see
	// stub.Shape.SideEffects). Like the schedule, the app keeps it
	// outside SKILL.md, so it's recorded in the description, and changing
	// it is a step in the app (see TaskSetup).
	Autofix string
	// Prompt is what the task tells the agent each time it runs.
	Prompt string
}

// Config is everything Fabrica installs in one local tool.
type Config struct {
	MCP   []MCPEntry
	Hooks []Hook
	// Tasks are scheduled tasks. Only Claude Code has them.
	Tasks []Task
}

// Normalize sorts a config so two configs with the same content compare
// equal.
func (c Config) Normalize() Config {
	mcp := append([]MCPEntry(nil), c.MCP...)
	sort.Slice(mcp, func(i, j int) bool { return mcp[i].Name < mcp[j].Name })
	for i := range mcp {
		if len(mcp[i].Args) == 0 {
			mcp[i].Args = nil
		}
		if len(mcp[i].EnvNames) == 0 {
			mcp[i].EnvNames = nil
		}
	}
	hooks := append([]Hook(nil), c.Hooks...)
	sort.Slice(hooks, func(i, j int) bool { return hookKey(hooks[i]) < hookKey(hooks[j]) })
	tasks := append([]Task(nil), c.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return TaskID(tasks[i]) < TaskID(tasks[j]) })
	return Config{MCP: mcp, Hooks: hooks, Tasks: tasks}
}

func hookKey(h Hook) string {
	return strings.Join([]string{h.Event, h.Factory, h.Routine, h.Fingerprint, h.Runtime, h.Binary}, "\x00")
}

// IsFabricaEntry reports whether an MCP entry name is one Fabrica owns.
func IsFabricaEntry(name string) bool {
	return name == "fabrica" || strings.HasPrefix(name, "fabrica-")
}

// HookCommand is the command line a tool runs for a hook. The command is
// Fabrica's identifying mark: Fabrica finds its own hooks by parsing it.
func HookCommand(h Hook) string {
	if h.Session {
		return strings.Join([]string{shellQuote(h.Binary), "hook", "session", "--runtime", h.Runtime}, " ")
	}
	return strings.Join([]string{
		shellQuote(h.Binary), "hook",
		"--runtime", h.Runtime,
		"--factory", h.Factory,
		"--routine", h.Routine,
		"--stub", h.Fingerprint,
		"--event", h.Event,
	}, " ")
}

// ParseHookCommand recognizes a command line made by HookCommand.
func ParseHookCommand(cmd string) (Hook, bool) {
	args, ok := shellSplit(cmd)
	if !ok || len(args) < 2 || !strings.HasPrefix(filepath.Base(args[0]), "fabrica") || args[1] != "hook" {
		return Hook{}, false
	}
	if len(args) > 2 && args[2] == "session" {
		// fabrica hook session --runtime <runtime>
		if len(args) != 5 || args[3] != "--runtime" || args[4] == "" {
			return Hook{}, false
		}
		return SessionHook(args[0], args[4]), true
	}
	h := Hook{Binary: args[0]}
	for i := 2; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--runtime":
			h.Runtime = args[i+1]
		case "--factory":
			h.Factory = args[i+1]
		case "--routine":
			h.Routine = args[i+1]
		case "--stub":
			h.Fingerprint = args[i+1]
		case "--event":
			h.Event = args[i+1]
		default:
			return Hook{}, false
		}
	}
	if h.Factory == "" || h.Routine == "" || h.Event == "" {
		return Hook{}, false
	}
	return h, true
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSplit splits a command line made of plain words and single-quoted
// words, which is all shellQuote produces.
func shellSplit(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inWord, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inQuote, inWord = true, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inQuote {
		return nil, false
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, true
}

// claudeEvent maps a Fabrica trigger to a Claude Code (or Codex CLI) hook
// event and matcher. Both tools use the same hook format.
func claudeEvent(trigger string) (event, matcher string, err error) {
	switch trigger {
	case SessionStart:
		return "SessionStart", "", nil
	case PromptSubmit:
		return "UserPromptSubmit", "", nil
	case SessionStop:
		return "Stop", "", nil
	case GitPreCommit, GitPrePush:
		return "PreToolUse", "Bash", nil
	}
	return "", "", fmt.Errorf("trigger %s has no hook event", trigger)
}

// copilotEvent maps a Fabrica trigger to a Copilot CLI hook event, and the
// tool its matcher picks out, if any.
func copilotEvent(trigger string) (event, matcher string, err error) {
	switch trigger {
	case SessionStart:
		return "sessionStart", "", nil
	case SessionStop:
		return "agentStop", "", nil
	case GitPreCommit, GitPrePush:
		return "preToolUse", "bash", nil
	}
	return "", "", fmt.Errorf("Copilot CLI has no hook event for trigger %s", trigger)
}

// cursorEvent maps a Fabrica trigger to a Cursor hook event.
func cursorEvent(trigger string) (string, error) {
	switch trigger {
	case SessionStop:
		return "stop", nil
	case GitPreCommit, GitPrePush:
		return "beforeShellExecution", nil
	}
	return "", fmt.Errorf("Cursor has no hook event for trigger %s", trigger)
}
