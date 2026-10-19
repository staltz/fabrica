package apply_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/plan"
)

func TestLocalFactoriesInstallIntoTools(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\n",
		"factories/oss-work.yaml":   "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [github.com/acme/app]\nroutines: [review-before-push]\n",
	})
	// Local factories are read from the working tree; no commit or remote
	// is needed.
	p := h.plan()
	if p.ExitCode() != 2 || p.Local == nil {
		t.Fatalf("want local changes:\n%s", render(p))
	}
	out := render(p)
	for _, want := range []string{"+ add MCP server fabrica", "+ add hook for routine oss-work/review-before-push on git.pre_push"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}

	claude, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".claude.json"))
	if !strings.Contains(string(claude), `"--runtime"`) || !strings.Contains(string(claude), h.w.Home.Dir) {
		t.Errorf("~/.claude.json should register fabrica mcp with the home repo:\n%s", claude)
	}
	cursorHooks, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".cursor", "hooks.json"))
	if !strings.Contains(string(cursorHooks), "beforeShellExecution") || !strings.Contains(string(cursorHooks), "--event git.pre_push") {
		t.Errorf("Cursor should get a beforeShellExecution hook:\n%s", cursorHooks)
	}
	if _, err := os.Stat(filepath.Join(h.env.UserHome, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("codex-cli isn't used by any factory, so nothing should be written for it (got %v)", err)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}

	// Removing every local factory removes Fabrica from the tools.
	os.RemoveAll(filepath.Join(h.w.Home.Dir, "factories"))
	h.apply()
	claude, _ = os.ReadFile(filepath.Join(h.env.UserHome, ".claude.json"))
	if strings.Contains(string(claude), "fabrica") {
		t.Errorf("fabrica should be gone from ~/.claude.json:\n%s", claude)
	}
}

func TestDestroyLocalFactories(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
		"factories/oss-work.yaml":   "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [github.com/acme/app]\nroutines: [review-before-push]\n",
	})
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	read := func(rel string) string {
		data, _ := os.ReadFile(filepath.Join(h.env.UserHome, rel))
		return string(data)
	}
	yes := func(*plan.Plan) error { return nil }

	// Destroying oss-work removes its hooks. Cursor has nothing else from
	// Fabrica, so it loses the MCP server too; Claude Code keeps it for
	// my-default.
	h.env.Only = []string{"oss-work"}
	p := h.destroyPlan()
	text := render(p)
	for _, want := range []string{"- remove hook for routine oss-work/review-before-push", "- remove MCP server fabrica", "delete factories/oss-work.yaml to stop that"} {
		if !strings.Contains(text, want) {
			t.Errorf("destroy plan is missing %q:\n%s", want, text)
		}
	}
	if res, err := h.destroy(yes); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy oss-work: %v\n%s", err, h.out.String())
	}
	if settings := read(".claude/settings.json"); strings.Contains(settings, "oss-work") {
		t.Errorf("Claude Code still has oss-work's hook:\n%s", settings)
	}
	if claude := read(".claude.json"); !strings.Contains(claude, `"fabrica"`) {
		t.Errorf("my-default still needs the fabrica MCP server in Claude Code:\n%s", claude)
	}
	if cursor := read(".cursor/mcp.json") + read(".cursor/hooks.json"); strings.Contains(cursor, "fabrica") {
		t.Errorf("nothing uses Cursor anymore, so Fabrica should be gone from it:\n%s", cursor)
	}

	// Destroying everything removes the rest, and leaves the factory
	// files alone.
	h.env.Only = nil
	if res, err := h.destroy(yes); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy: %v\n%s", err, h.out.String())
	}
	if claude := read(".claude.json"); strings.Contains(claude, "fabrica") {
		t.Errorf("fabrica should be gone from ~/.claude.json:\n%s", claude)
	}
	if p := h.destroyPlan(); p.ExitCode() != 0 || p.Local != nil {
		t.Errorf("there should be nothing left to destroy:\n%s", render(p))
	}
	if p := h.plan(); p.ExitCode() != 2 || !strings.Contains(render(p), "+ add hook for routine oss-work/review-before-push") {
		t.Errorf("apply should put it all back:\n%s", render(p))
	}
}

func TestSessionPointerHook(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local, codex-cli]\ntargets: [\"*\"]\n",
		"factories/oss-work.yaml":   "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, copilot-local]\ntargets: [github.com/acme/app]\n",
	})
	// Someone else's session-start hook is already there.
	own := filepath.Join(h.env.UserHome, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte(`{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "my-session-hook"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		data, _ := os.ReadFile(filepath.Join(h.env.UserHome, rel))
		return string(data)
	}
	sessionHooks := map[string]string{
		"claude-code-local": ".claude/settings.json",
		"codex-cli":         ".codex/hooks.json",
		"copilot-local":     ".copilot/hooks/fabrica.json",
	}
	// count is how many session pointer hooks a runtime's hooks file has.
	count := func(rt string) int {
		return strings.Count(read(sessionHooks[rt]), "hook session --runtime "+rt+"\"")
	}

	// Plan shows one session hook for each runtime that has a session-start
	// event, though two factories use Claude Code. Cursor has none.
	out := render(h.plan())
	if n := strings.Count(out, "+ add session pointer hook on session.start"); n != 3 {
		t.Errorf("want a session hook for Claude Code, Codex CLI and Copilot CLI, got %d:\n%s", n, out)
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	for rt := range sessionHooks {
		if n := count(rt); n != 1 {
			t.Errorf("%s should have one session hook, has %d:\n%s", rt, n, read(sessionHooks[rt]))
		}
	}
	if strings.Contains(read(".cursor/hooks.json"), "session") || !strings.Contains(read(".cursor/mcp.json"), `"fabrica"`) {
		t.Errorf("Cursor has no session-start event, so it gets the MCP server and no session hook:\n%s\n%s", read(".cursor/hooks.json"), read(".cursor/mcp.json"))
	}
	if !strings.Contains(read(".claude/settings.json"), "my-session-hook") {
		t.Errorf("apply lost someone else's hook:\n%s", read(".claude/settings.json"))
	}
	if !strings.Contains(h.out.String(), "Codex skips new or changed hooks until you trust them") {
		t.Errorf("the session hook is new in Codex, so apply should say to trust it:\n%s", h.out.String())
	}

	// Applying again changes nothing.
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}
	before := userHomeFiles(t, h.env.UserHome)
	if res := h.apply(); res.ExitCode() != 0 || strings.Contains(h.out.String(), "Local ") {
		t.Fatalf("a second apply should do nothing:\n%s", h.out.String())
	}
	if after := userHomeFiles(t, h.env.UserHome); !slices.Equal(before, after) {
		t.Errorf("a second apply changed files:\n%v\n%v", before, after)
	}

	// A runtime no factory uses anymore loses its session hook.
	h.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\n",
	})
	if out := render(h.plan()); strings.Count(out, "- remove session pointer hook") != 1 {
		t.Errorf("plan should remove Codex CLI's session hook:\n%s", out)
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	if n := count("codex-cli"); n != 0 || count("claude-code-local") != 1 || count("copilot-local") != 1 {
		t.Errorf("only Codex CLI's session hook should be gone:\n%s", read(".codex/hooks.json"))
	}

	// So does destroying the last factory that uses it, while the hook of a
	// runtime that others still use stays.
	yes := func(*plan.Plan) error { return nil }
	h.env.Only = []string{"oss-work"}
	if out := render(h.destroyPlan()); strings.Count(out, "- remove session pointer hook") != 1 {
		t.Errorf("destroying oss-work should remove only Copilot CLI's session hook:\n%s", out)
	}
	if res, err := h.destroy(yes); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy oss-work: %v\n%s", err, h.out.String())
	}
	if count("copilot-local") != 0 || count("claude-code-local") != 1 {
		t.Errorf("Claude Code is still used by my-default, so it keeps its session hook:\n%s", read(".claude/settings.json"))
	}
	h.env.Only = nil
	if res, err := h.destroy(yes); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy: %v\n%s", err, h.out.String())
	}
	if settings := read(".claude/settings.json"); strings.Contains(settings, "fabrica") || !strings.Contains(settings, "my-session-hook") {
		t.Errorf("destroy should remove Fabrica's hooks and leave the rest:\n%s", settings)
	}
}

// userHomeFiles lists every file under dir with its content and
// modification time, to see whether anything changed.
func userHomeFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %s %s", p, info.ModTime(), data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLocalRoutineWithUnsupportedTrigger(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateBlueprint(map[string]string{"routines/hello.md": "---\ntrigger: { on: session.start }\n---\nSay hi.\n"})
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\nroutines: [hello]\n",
	})
	p := h.plan()
	d := p.Factory("mine")
	if !d.Diags.HasCode("capability.trigger") || !strings.Contains(render(p), "cursor-local doesn't support trigger session.start") {
		t.Fatalf("Cursor has no session.start hook, so this should be an error:\n%s", render(p))
	}
}

func TestLocalFactoryInTeamHomeIsAnError(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	}).CommitPush("oops")
	p := h.plan()
	if p.ExitCode() != 1 || !strings.Contains(render(p), "personal home repo") {
		t.Fatalf("want an error:\n%s", render(p))
	}
}

func TestLocalScheduleRoutineBecomesClaudeScheduledTask(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateBlueprint(map[string]string{"routines/update-dep.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\n---\nUpdate a dependency.\n"})
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\nroutines: [update-dep]\n",
	})
	p := h.plan()
	if p.ExitCode() != 2 || !strings.Contains(render(p), "+ add scheduled task fabrica-update-dep-") {
		t.Fatalf("want a scheduled task:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "in a desktop session on your checkout of github.com/acme/app") {
		t.Errorf("apply should say what to set up in the app:\n%s", h.out.String())
	}
	tasks, _ := filepath.Glob(filepath.Join(h.env.UserHome, ".claude", "scheduled-tasks", "fabrica-update-dep-*", "SKILL.md"))
	if len(tasks) != 1 {
		t.Fatalf("want one SKILL.md, got %v", tasks)
	}
	data, _ := os.ReadFile(tasks[0])
	if !strings.Contains(string(data), "Call `run_begin` with routine `update-dep`, runtime `claude-code-local`, repo `github.com/acme/app`") {
		t.Errorf("the task's prompt should be the local stub:\n%s", data)
	}
	settings, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".claude", "settings.json"))
	if strings.Contains(string(settings), "update-dep") {
		t.Errorf("a schedule routine shouldn't get a hook:\n%s", settings)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}

	// Dropping the routine removes the task, and apply says to delete it in
	// the app too.
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\n",
	})
	h.out.Reset()
	h.apply()
	if _, err := os.Stat(tasks[0]); !os.IsNotExist(err) {
		t.Errorf("the task's file should be gone (%v)", err)
	}
	if !strings.Contains(h.out.String(), "from the app's list of routines") {
		t.Errorf("apply should say to delete the task in the app:\n%s", h.out.String())
	}
}

func TestLocalScheduleRoutineNeedsSpecificTargets(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateBlueprint(map[string]string{"routines/update-dep.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\n---\nUpdate a dependency.\n"})
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\nroutines: [update-dep]\n",
	})
	p := h.plan()
	if !p.Factory("mine").Diags.HasCode("factory.schedule-wildcard") {
		t.Fatalf("want a wildcard error:\n%s", render(p))
	}
}

func TestLocalRoutineWithSeveralTriggers(t *testing.T) {
	// Locally, a routine gets a hook for each event and a scheduled task
	// for its schedule.
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateBlueprint(map[string]string{"routines/tidy.md": "---\ntriggers:\n  - { on: session.start }\n  - { on: session.stop }\n  - { on: schedule, cron: \"0 9 * * 1\" }\n---\nTidy up.\n"})
	h.w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\nroutines: [tidy]\n",
	})
	out := render(h.plan())
	for _, want := range []string{"+ add hook for routine mine/tidy on session.start", "+ add hook for routine mine/tidy on session.stop", "+ add scheduled task fabrica-tidy-"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	settings, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".claude", "settings.json"))
	for _, want := range []string{"SessionStart", "Stop", "--event session.start", "--event session.stop"} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("settings.json is missing %q:\n%s", want, settings)
		}
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}

	// Dropping one trigger removes just its hook.
	h.w.UpdateBlueprint(map[string]string{"routines/tidy.md": "---\ntriggers:\n  - { on: session.stop }\n  - { on: schedule, cron: \"0 9 * * 1\" }\n---\nTidy up.\n"})
	out = render(h.plan())
	if !strings.Contains(out, "- remove hook for routine mine/tidy on session.start") || strings.Contains(out, "hook for routine mine/tidy on session.stop") || strings.Contains(out, "scheduled task") {
		t.Errorf("only the session.start hook should go:\n%s", out)
	}
}
