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
