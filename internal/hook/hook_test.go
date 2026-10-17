package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func TestRunsGit(t *testing.T) {
	for cmd, want := range map[string]bool{
		"git push origin main":       true,
		"cd app && git -C . push":    true,
		"git --no-pager push":        true,
		"/usr/bin/git push":          true,
		"git log --grep push":        false,
		"git commit -m 'push it'":    false,
		"echo git push":              false,
		"npm test; git push --force": true,
		"gitpush":                    false,
	} {
		if got := RunsGit(cmd, "push"); got != want {
			t.Errorf("RunsGit(%q, push) = %v, want %v", cmd, got, want)
		}
	}
	if !RunsGit("git commit -am fix", "commit") {
		t.Error("git commit should match commit")
	}
}

func setup(t *testing.T) (ws string, opts Options) {
	t.Helper()
	w := testkit.NewWorld(t)
	w.MakePersonal(t)
	w.Home.Write(map[string]string{
		"factories/oss-work.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, copilot-local, cursor-local]\ntargets: [github.com/acme/app]\nroutines: [review-before-push]\n",
	})
	ws = filepath.Join(t.TempDir(), "app")
	testkit.Git(t, "", "clone", "--quiet", "https://github.com/acme/app.git", ws)
	return ws, Options{
		Runtime: "claude-code-local", Factory: "oss-work", Routine: "review-before-push",
		Fingerprint: "abc123abc123", Event: "git.pre_push", HomeDir: w.Home.Dir,
		MarkerDir: t.TempDir(), Getenv: func(string) string { return "" },
	}
}

func run(t *testing.T, o Options, stdin string) map[string]any {
	t.Helper()
	var out strings.Builder
	o.Stdin, o.Stdout = strings.NewReader(stdin), &out
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out.String()), &v); err != nil {
		t.Fatalf("hook printed something that isn't JSON: %s", out.String())
	}
	return v
}

func TestPrePushHoldsBackOnce(t *testing.T) {
	ws, o := setup(t)
	in := `{"session_id": "s1", "cwd": "` + ws + `", "tool_name": "Bash", "tool_input": {"command": "git push origin main"}}`
	first := run(t, o, in)
	hso, _ := first["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" || !strings.Contains(hso["permissionDecisionReason"].(string), "routine `review-before-push`") {
		t.Fatalf("the first push should be held back with instructions, got %v", first)
	}
	if second := run(t, o, in); second != nil {
		t.Errorf("after the routine ran, the push should go through, got %v", second)
	}
	// Other commands pass right through.
	if out := run(t, o, `{"session_id": "s1", "cwd": "`+ws+`", "tool_input": {"command": "ls"}}`); out != nil {
		t.Errorf("ls shouldn't be held back: %v", out)
	}
}

func TestCursorFormats(t *testing.T) {
	ws, o := setup(t)
	o.Runtime = "cursor-local"
	out := run(t, o, `{"conversation_id": "c1", "workspace_roots": ["`+ws+`"], "command": "git push"}`)
	if out["permission"] != "deny" || !strings.Contains(out["agent_message"].(string), "run_begin") {
		t.Errorf("Cursor should get permission/agent_message, got %v", out)
	}
	o.Event = "session.stop"
	if out := run(t, o, `{"conversation_id": "c1", "workspace_roots": ["`+ws+`"], "status": "completed", "loop_count": 0}`); out["followup_message"] == nil {
		t.Errorf("Cursor stop should send a follow-up, got %v", out)
	}
	if out := run(t, o, `{"conversation_id": "c1", "workspace_roots": ["`+ws+`"], "status": "completed", "loop_count": 1}`); out != nil {
		t.Errorf("only follow up once, got %v", out)
	}
}

func TestCopilotFormats(t *testing.T) {
	ws, o := setup(t)
	o.Runtime = "copilot-local"
	// Copilot CLI sends the shell tool's arguments as a JSON-encoded string.
	in := `{"sessionId": "s1", "timestamp": 1704614400000, "cwd": "` + ws + `", "toolName": "bash", "toolArgs": "{\"command\":\"git push origin main\"}"}`
	out := run(t, o, in)
	if out["permissionDecision"] != "deny" || !strings.Contains(out["permissionDecisionReason"].(string), "run_begin") {
		t.Fatalf("Copilot should get permissionDecision/permissionDecisionReason, got %v", out)
	}
	if again := run(t, o, in); again != nil {
		t.Errorf("after the routine ran, the push should go through, got %v", again)
	}
	if out := run(t, o, `{"sessionId": "s1", "cwd": "`+ws+`", "toolName": "bash", "toolArgs": "{\"command\":\"ls\"}"}`); out != nil {
		t.Errorf("ls shouldn't be held back: %v", out)
	}
	// VS Code runs ~/.copilot/hooks too, with its own payload, but it has
	// no Fabrica MCP server.
	if out := run(t, o, `{"session_id": "v1", "hook_event_name": "PreToolUse", "cwd": "`+ws+`", "tool_name": "runInTerminal", "tool_input": {"command": "git push"}}`); out != nil {
		t.Errorf("Copilot hooks must stay quiet inside VS Code, got %v", out)
	}
	o.Event = "session.start"
	if out := run(t, o, `{"sessionId": "s1", "cwd": "`+ws+`", "source": "startup"}`); !strings.Contains(fmt.Sprint(out["additionalContext"]), "run_begin") {
		t.Errorf("sessionStart should add context, got %v", out)
	}
	o.Event = "session.stop"
	if out := run(t, o, `{"sessionId": "s1", "cwd": "`+ws+`", "stopReason": "end_turn"}`); out["decision"] != "block" {
		t.Errorf("agentStop should block once, got %v", out)
	}
	if out := run(t, o, `{"sessionId": "s1", "cwd": "`+ws+`", "stop_hook_active": true}`); out != nil {
		t.Errorf("agentStop must not loop, got %v", out)
	}
}

func TestClaudeEvents(t *testing.T) {
	ws, o := setup(t)
	o.Event = "session.stop"
	if out := run(t, o, `{"cwd": "`+ws+`"}`); out["decision"] != "block" {
		t.Errorf("Stop should block once, got %v", out)
	}
	if out := run(t, o, `{"cwd": "`+ws+`", "stop_hook_active": true}`); out != nil {
		t.Errorf("Stop must not loop, got %v", out)
	}
	o.Event = "session.start"
	out := run(t, o, `{"cwd": "`+ws+`"}`)
	if hso, _ := out["hookSpecificOutput"].(map[string]any); hso["hookEventName"] != "SessionStart" || hso["additionalContext"] == nil {
		t.Errorf("SessionStart should add context, got %v", out)
	}
}

// Payloads of SessionStart events, as the tools send them. CWD stands for
// the workspace.
const (
	claudeStart  = `{"session_id": "8c1f5e0a-52d4-4a39-9c0b-7a1d3e6f2b84", "transcript_path": "/home/me/.claude/projects/-home-me-app/8c1f5e0a-52d4-4a39-9c0b-7a1d3e6f2b84.jsonl", "cwd": "CWD", "hook_event_name": "SessionStart", "source": "startup", "model": "claude-sonnet-5"}`
	codexStart   = `{"session_id": "0199c4a2-77b1-7c3e-9a55-3d2f1e8b6a40", "transcript_path": "/home/me/.codex/sessions/2026/10/17/rollout-2026-10-17T09-41-02-0199c4a2-77b1-7c3e-9a55-3d2f1e8b6a40.jsonl", "cwd": "CWD", "hook_event_name": "SessionStart", "model": "gpt-5.5", "source": "startup"}`
	copilotStart = `{"sessionId": "3f2a91c4-d0b7-4e68-a1c5-9b7e24d8f630", "timestamp": 1704614400000, "cwd": "CWD", "source": "new", "initialPrompt": "Fix the flaky checkout test"}`
)

// setupSession is setup for the session pointer hook: a factory that runs
// no routines on the given runtimes, which is all that hook needs.
func setupSession(t *testing.T, runtimes string) (ws string, opts Options) {
	t.Helper()
	ws, o := setup(t)
	factory := "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [" + runtimes + "]\ntargets: [github.com/acme/app]\n"
	if err := os.WriteFile(filepath.Join(o.HomeDir, "factories", "oss-work.yaml"), []byte(factory), 0o644); err != nil {
		t.Fatal(err)
	}
	o.Session, o.Factory, o.Routine, o.Fingerprint, o.Event = true, "", "", "", ""
	return ws, o
}

func TestSessionPointer(t *testing.T) {
	ws, o := setupSession(t, "claude-code-local, codex-cli, copilot-local")
	want := func(pointer string) string {
		return `Fabrica: when you call run_begin, pass session: "` + pointer + `".`
	}
	// Claude Code and Codex CLI: the transcript's path, in hookSpecificOutput.
	for _, c := range []struct{ runtime, payload, pointer string }{
		{"claude-code-local", claudeStart, "/home/me/.claude/projects/-home-me-app/8c1f5e0a-52d4-4a39-9c0b-7a1d3e6f2b84.jsonl"},
		{"codex-cli", codexStart, "/home/me/.codex/sessions/2026/10/17/rollout-2026-10-17T09-41-02-0199c4a2-77b1-7c3e-9a55-3d2f1e8b6a40.jsonl"},
	} {
		o.Runtime = c.runtime
		out := run(t, o, strings.ReplaceAll(c.payload, "CWD", ws))
		hso, _ := out["hookSpecificOutput"].(map[string]any)
		if hso["hookEventName"] != "SessionStart" || hso["additionalContext"] != want(c.pointer) {
			t.Errorf("%s: want the transcript path as context, got %v", c.runtime, out)
		}
	}
	// Copilot CLI sends no transcript, so the pointer is its session ID,
	// in a flat additionalContext.
	o.Runtime = "copilot-local"
	out := run(t, o, strings.ReplaceAll(copilotStart, "CWD", ws))
	if len(out) != 1 || out["additionalContext"] != want("copilot-local:3f2a91c4-d0b7-4e68-a1c5-9b7e24d8f630") {
		t.Errorf("copilot-local: want its session ID as context, got %v", out)
	}
	// Without a transcript path, Claude Code and Codex CLI point at their
	// session ID; with neither, there's nothing to say.
	o.Runtime = "codex-cli"
	out = run(t, o, `{"session_id": "s1", "transcript_path": null, "cwd": "`+ws+`"}`)
	if hso, _ := out["hookSpecificOutput"].(map[string]any); hso["additionalContext"] != want("codex-cli:s1") {
		t.Errorf("codex-cli: want its session ID as context, got %v", out)
	}
	if out := run(t, o, `{"cwd": "`+ws+`"}`); out != nil {
		t.Errorf("with no session to point at the hook should stay quiet, got %v", out)
	}
	// The same guards as the routine hooks.
	o.Runtime = "copilot-local"
	if out := run(t, o, `{"session_id": "v1", "cwd": "`+ws+`", "hook_event_name": "SessionStart"}`); out != nil {
		t.Errorf("Copilot hooks must stay quiet inside VS Code, got %v", out)
	}
	o.Runtime = "claude-code-local"
	cursor := o
	cursor.Getenv = func(k string) string {
		if k == "CURSOR_VERSION" {
			return "2.0"
		}
		return ""
	}
	if out := run(t, cursor, strings.ReplaceAll(claudeStart, "CWD", ws)); out != nil {
		t.Errorf("Claude Code hooks must stay quiet inside Cursor, got %v", out)
	}
}

func TestSessionPointerQuietWhenItDoesNotApply(t *testing.T) {
	ws, o := setupSession(t, "claude-code-local")
	in := strings.ReplaceAll(claudeStart, "CWD", ws)
	if out := run(t, o, in); out == nil {
		t.Fatal("the hook should say something in a repo with a matching factory")
	}
	if out := run(t, o, strings.ReplaceAll(claudeStart, "CWD", t.TempDir())); out != nil {
		t.Errorf("outside a git repo the hook must stay quiet, got %v", out)
	}
	other := o
	other.Runtime = "codex-cli"
	if out := run(t, other, strings.ReplaceAll(codexStart, "CWD", ws)); out != nil {
		t.Errorf("no factory uses codex-cli, so the hook must stay quiet, got %v", out)
	}
	api := filepath.Join(t.TempDir(), "api")
	testkit.Git(t, "", "clone", "--quiet", "https://github.com/acme/api.git", api)
	if out := run(t, o, strings.ReplaceAll(claudeStart, "CWD", api)); out != nil {
		t.Errorf("no factory covers acme/api, so the hook must stay quiet, got %v", out)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("# App\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(t, o, in); out != nil {
		t.Errorf("without the Fabrica line the hook must stay quiet, got %v", out)
	}
}

func TestQuietWhenItDoesNotApply(t *testing.T) {
	ws, o := setup(t)
	in := `{"cwd": "` + ws + `", "tool_input": {"command": "git push"}}`
	other := o
	other.Factory = "something-else"
	if out := run(t, other, in); out != nil {
		t.Errorf("a hook for another factory must stay quiet, got %v", out)
	}
	inCursor := o
	inCursor.Getenv = func(k string) string {
		if k == "CURSOR_VERSION" {
			return "2.0"
		}
		return ""
	}
	if out := run(t, inCursor, in); out != nil {
		t.Errorf("Claude Code hooks must stay quiet inside Cursor, got %v", out)
	}
	if out := run(t, o, `{"cwd": "`+t.TempDir()+`", "tool_input": {"command": "git push"}}`); out != nil {
		t.Errorf("outside a git repo the hook must stay quiet, got %v", out)
	}
	narrowed := "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [github.com/acme/app]\nroutines: [{ routine: review-before-push, runtimes: [cursor-local] }]\n"
	if err := os.WriteFile(filepath.Join(o.HomeDir, "factories", "oss-work.yaml"), []byte(narrowed), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(t, o, in); out != nil {
		t.Errorf("a routine narrowed to other runtimes must stay quiet, got %v", out)
	}
}
