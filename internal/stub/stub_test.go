package stub_test

import (
	"context"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
)

func load(t *testing.T, files map[string]string) *blueprint.Blueprint {
	t.Helper()
	dir := t.TempDir()
	base := map[string]string{
		"fabrica.yaml":              "",
		"skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: Reviews.\n---\nReview.\n",
		"skills/pdf/SKILL.md":       "---\nname: pdf\ndescription: PDFs.\n---\nRun the script.\n",
		"skills/pdf/extract.py!x":   "print(1)\n",
		"connectors/github.yaml":    "kind: native\nfallback: { url: https://gh-mcp.example/mcp, auth: bearer, secret: GITHUB_TOKEN }\n",
		"connectors/slack.yaml":     "kind: native\n",
		"connectors/linear.yaml":    "kind: mcp\nurl: https://mcp.linear.app/mcp\nauth: oauth\n",
		"connectors/local.yaml":     "kind: mcp\ncommand: my-mcp\n",
	}
	for k, v := range files {
		base[k] = v
	}
	testkit.WriteFiles(t, dir, base)
	f, diags := blueprint.NewLoader(gitx.NewCache(t.TempDir())).LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return f
}

func target(t *testing.T, runtime string) stub.Target {
	p, ok := provider.Builtin(runtime)
	if !ok {
		t.Fatalf("no built-in %s", runtime)
	}
	tg := stub.Target{Factory: "pr-review", Runtime: p}
	if !p.IsLocal() {
		tg.Endpoint = "https://fabrica.acme.dev/pr-review"
	}
	return tg
}

const routine = "---\ntrigger: { on: github.pull_request.opened }\nskills: [pr-review]\nconnectors: [github, slack?, linear]\nside_effects:\n  - github.pr_comment\n  - slack.notify: { channel: \"#eng\" }\n---\nReview.\n"

func TestEvaluateOnClaude(t *testing.T) {
	f := load(t, map[string]string{"routines/pr-review.md": routine})
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "claude-code-cloud"))
	if !e.OK() {
		t.Fatalf("unexpected problems:\n%s", e.Diags)
	}
	if got := strings.Join(e.Shape.ConnectorNames(), ","); got != "github,slack,linear" {
		t.Errorf("connectors = %s", got)
	}
	if e.Shape.TokenPlacement != provider.TokenInURL || e.Shape.Endpoint == "" {
		t.Errorf("claude-code-cloud should get the token in the URL: %+v", e.Shape)
	}
	if fp := e.Shape.Fingerprint(); len(fp) != 12 || fp != e.Shape.Fingerprint() {
		t.Errorf("fingerprint %q isn't stable", fp)
	}
}

func TestFingerprintChangesWithShapeOnly(t *testing.T) {
	f1 := load(t, map[string]string{"routines/pr-review.md": routine})
	e1 := stub.Evaluate(f1, f1.Routines["pr-review"], target(t, "claude-code-cloud"))

	// A new prompt doesn't change the shape: prompts are served live.
	f2 := load(t, map[string]string{"routines/pr-review.md": strings.Replace(routine, "Review.", "Review carefully.", 1)})
	e2 := stub.Evaluate(f2, f2.Routines["pr-review"], target(t, "claude-code-cloud"))
	if e1.Shape.Fingerprint() != e2.Shape.Fingerprint() {
		t.Error("changing the prompt should not change the fingerprint")
	}

	// A new trigger does.
	f3 := load(t, map[string]string{"routines/pr-review.md": strings.Replace(routine, "opened", "synchronize", 1)})
	e3 := stub.Evaluate(f3, f3.Routines["pr-review"], target(t, "claude-code-cloud"))
	if e1.Shape.Fingerprint() == e3.Shape.Fingerprint() {
		t.Error("changing the trigger should change the fingerprint")
	}

	// So does a connector's definition.
	f4 := load(t, map[string]string{
		"routines/pr-review.md":  routine,
		"connectors/linear.yaml": "kind: mcp\nurl: https://mcp.linear.app/v2\nauth: oauth\n",
	})
	e4 := stub.Evaluate(f4, f4.Routines["pr-review"], target(t, "claude-code-cloud"))
	if e1.Shape.Fingerprint() == e4.Shape.Fingerprint() {
		t.Error("changing a connector's URL should change the fingerprint")
	}
}

func TestOptionalConnectorIsDroppedWithItsSideEffects(t *testing.T) {
	// Codex cloud has no Slack integration.
	f := load(t, map[string]string{"routines/pr-review.md": strings.Replace(routine, "linear]", "]", 1)})
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "codex"))
	if !e.OK() {
		t.Fatalf("an optional connector shouldn't cause errors:\n%s", e.Diags)
	}
	if got := strings.Join(e.Shape.ConnectorNames(), ","); got != "github" {
		t.Errorf("connectors = %s, want just github", got)
	}
	if se, _ := e.SideEffect("slack.notify"); !se.Dropped {
		t.Error("slack.notify should be dropped along with its optional connector")
	}
}

func TestUnsupportedThingsAreErrors(t *testing.T) {
	f := load(t, map[string]string{
		"routines/push.md": "---\ntrigger: { on: github.push }\nconnectors: [local]\n---\nHi\n",
		"routines/pdf.md":  "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\nskills: [pdf]\n---\nHi\n",
	})
	e := stub.Evaluate(f, f.Routines["push"], target(t, "claude-code-cloud"))
	if !e.Diags.HasCode("capability.trigger") || !e.Diags.HasCode("capability.connector") {
		t.Errorf("want trigger and connector errors, got:\n%s", e.Diags)
	}
	e = stub.Evaluate(f, f.Routines["pdf"], target(t, "claude-code-cloud"))
	if !e.Diags.HasCode("capability.skill") {
		t.Errorf("a skill with scripts should be refused on a cloud runtime, got:\n%s", e.Diags)
	}
	// Locally, the same skill is fine, because its folder is unpacked on disk.
	f2 := load(t, map[string]string{"routines/pdf.md": "---\ntrigger: { on: session.start }\nskills: [pdf]\n---\nHi\n"})
	if e := stub.Evaluate(f2, f2.Routines["pdf"], target(t, "claude-code-local")); !e.OK() {
		t.Errorf("locally the pdf skill should work:\n%s", e.Diags)
	}
}

func TestEmulatedConnectorUsesFallback(t *testing.T) {
	f := load(t, map[string]string{"routines/r.md": "---\ntrigger: { on: session.start }\nconnectors: [github]\n---\nHi\n"})
	e := stub.Evaluate(f, f.Routines["r"], target(t, "claude-code-local"))
	if !e.OK() {
		t.Fatal(e.Diags)
	}
	if c := e.Shape.Connectors[0]; c.Kind != "mcp" || c.Server.URL != "https://gh-mcp.example/mcp" {
		t.Errorf("github should be emulated through its fallback, got %+v", c)
	}
	// Slack has no fallback, so it can't be emulated.
	f2 := load(t, map[string]string{"routines/r.md": "---\ntrigger: { on: session.start }\nconnectors: [slack]\n---\nHi\n"})
	e2 := stub.Evaluate(f2, f2.Routines["r"], target(t, "claude-code-local"))
	if !e2.Diags.HasCode("capability.connector") || !strings.Contains(e2.Diags.String(), "fallback") {
		t.Errorf("want a hint about adding a fallback, got:\n%s", e2.Diags)
	}
}

func TestIdentityAndPrompt(t *testing.T) {
	id := stub.Identity("k7q2", "pr-review", "github.com/acme/app")
	if id != "k7q2#pr-review@github.com/acme/app" {
		t.Errorf("Identity = %q", id)
	}
	if prefix, routine, repo := stub.SplitIdentity(id); prefix != "k7q2#" || routine != "pr-review" || repo != "github.com/acme/app" {
		t.Errorf("SplitIdentity = %q, %q, %q", prefix, routine, repo)
	}
	p := stub.Prompt(id, "pr-review", "claude-code-cloud", "github.com/acme/app", "7f3a9c")
	for _, want := range []string{"`run_begin`", "routine `pr-review`", "runtime `claude-code-cloud`", "repo `github.com/acme/app`", "stub `7f3a9c`", "`routine_get`", id} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestTagHidesTheServerAndTellsFactoriesApart(t *testing.T) {
	tag := stub.Tag("https://mcp.acme.dev/fabrica", "exis")
	if len(tag) != stub.TagLen || strings.Trim(tag, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Errorf("Tag = %q, want %d lowercase base32 characters", tag, stub.TagLen)
	}
	if again := stub.Tag("http://mcp.acme.dev/fabrica/", "exis"); again != tag {
		t.Errorf("the scheme or a trailing slash changed the tag: %q vs %q", again, tag)
	}
	for _, other := range [][2]string{{"https://mcp.acme.dev/fabrica", "oss-work"}, {"https://mcp.other.dev/fabrica", "exis"}} {
		if got := stub.Tag(other[0], other[1]); got == tag {
			t.Errorf("Tag(%q, %q) = %q, the same as for exis on mcp.acme.dev", other[0], other[1], got)
		}
	}
}

func TestMemoryOnlyShapesRuntimesWithTheirOwn(t *testing.T) {
	plain := load(t, map[string]string{"routines/pr-review.md": routine})
	withMemory := load(t, map[string]string{"routines/pr-review.md": strings.Replace(routine, "skills: [pr-review]\n", "skills: [pr-review]\nmemory: true\n", 1)})

	// Cursor automations have their own memory, on by default, so the stub
	// turns it on or off to match the routine.
	off := stub.Evaluate(plain, plain.Routines["pr-review"], target(t, "cursor-cloud"))
	on := stub.Evaluate(withMemory, withMemory.Routines["pr-review"], target(t, "cursor-cloud"))
	if off.Shape.Memory != stub.MemoryOff || on.Shape.Memory != stub.MemoryOn || on.Memory.Support != provider.Native {
		t.Errorf("cursor-cloud memory: off=%q on=%q (%s)", off.Shape.Memory, on.Shape.Memory, on.Memory.Support)
	}
	if off.Shape.Fingerprint() == on.Shape.Fingerprint() {
		t.Error("turning memory on should change a cursor-cloud stub's fingerprint")
	}

	// Claude routines have none, so Fabrica keeps the memory and nothing
	// changes in the stub.
	c1 := stub.Evaluate(plain, plain.Routines["pr-review"], target(t, "claude-code-cloud"))
	c2 := stub.Evaluate(withMemory, withMemory.Routines["pr-review"], target(t, "claude-code-cloud"))
	if !c2.OK() || c2.Memory.Support != provider.Emulated || c2.Shape.Memory != "" || c1.Shape.Fingerprint() != c2.Shape.Fingerprint() {
		t.Errorf("claude-code-cloud memory should be emulated without touching the stub: %+v %+v", c2.Memory, c2.Shape)
	}
}

func TestMemoryOnRuntimeWithoutIt(t *testing.T) {
	f := load(t, map[string]string{"routines/pr-review.md": strings.Replace(routine, "skills: [pr-review]\n", "skills: [pr-review]\nmemory: true\n", 1)})
	m, err := provider.ParseMatrix("forgetful", []byte("kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\ntriggers: {github.pull_request.opened: native}\nconnectors: {github: native, slack: native, mcp.http: native}\nside_effects: {\"*\": emulated}\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := stub.Evaluate(f, f.Routines["pr-review"], stub.Target{Runtime: &provider.Provider{Matrix: m}, Endpoint: "https://fabrica.acme.dev/pr-review"})
	if e.OK() || !strings.Contains(e.Diags.String(), "can't give routine pr-review the memory it asks for") {
		t.Errorf("want a memory error, got:\n%s", e.Diags)
	}
}

func withAutofix(routine string) string {
	return strings.Replace(routine, "  - github.pr_comment\n", "  - github.pr_comment\n  - github.pr_autofix\n", 1)
}

func TestNativeSideEffectsMirrorTheRoutine(t *testing.T) {
	// On a runtime that auto-fixes pull requests itself, the stub sets it
	// either way, so the runtime's own default never decides.
	plain := load(t, map[string]string{"routines/pr-review.md": routine})
	fixing := load(t, map[string]string{"routines/pr-review.md": withAutofix(routine)})
	off := stub.Evaluate(plain, plain.Routines["pr-review"], target(t, "claude-code-cloud"))
	on := stub.Evaluate(fixing, fixing.Routines["pr-review"], target(t, "claude-code-cloud"))
	if !off.OK() || !on.OK() || off.Shape.SideEffects[provider.PRAutofix] != stub.SideEffectOff || on.Shape.SideEffects[provider.PRAutofix] != stub.SideEffectOn {
		t.Errorf("claude-code-cloud auto-fix: off=%v on=%v %s %s", off.Shape.SideEffects, on.Shape.SideEffects, off.Diags, on.Diags)
	}
	if u, _ := on.SideEffect(provider.PRAutofix); u.Support != provider.Native {
		t.Errorf("auto-fix should be native on claude-code-cloud, got %+v", u)
	}
	if off.Shape.Fingerprint() == on.Shape.Fingerprint() {
		t.Error("turning auto-fix on should change the stub's fingerprint")
	}
	if got := strings.Join(off.Shape.NativeSideEffectsOff(), ","); got != provider.PRAutofix {
		t.Errorf("NativeSideEffectsOff = %q", got)
	}

	// Runtimes without one bake nothing in, and their "*": emulated
	// doesn't match it: it can't be emulated.
	cursor := stub.Evaluate(plain, plain.Routines["pr-review"], target(t, "cursor-cloud"))
	if !cursor.OK() || cursor.Shape.SideEffects != nil {
		t.Errorf("cursor-cloud has no native side effects, so the shape shouldn't have any: %v %s", cursor.Shape.SideEffects, cursor.Diags)
	}
	cursor = stub.Evaluate(fixing, fixing.Routines["pr-review"], target(t, "cursor-cloud"))
	if cursor.OK() || !strings.Contains(cursor.Diags.String(), "doesn't support side effect github.pr_autofix") || !strings.Contains(cursor.Diags.String(), "can't be emulated") {
		t.Errorf("auto-fix on cursor-cloud should be an error: %s", cursor.Diags)
	}
}

func TestNativeSideEffectsLocally(t *testing.T) {
	// Claude Code's desktop scheduled tasks have an auto-fix setting.
	const scheduled = "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\nconnectors: [github]\nside_effects:\n  - github.pr_comment\n---\nUpdate.\n"
	f := load(t, map[string]string{"routines/pr-review.md": withAutofix(scheduled), "routines/plain.md": scheduled})
	if e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "claude-code-local")); !e.OK() || e.Shape.SideEffects[provider.PRAutofix] != stub.SideEffectOn {
		t.Errorf("a scheduled task should get auto-fix on: %v %s", e.Shape.SideEffects, e.Diags)
	}
	if e := stub.Evaluate(f, f.Routines["plain"], target(t, "claude-code-local")); !e.OK() || e.Shape.SideEffects[provider.PRAutofix] != stub.SideEffectOff {
		t.Errorf("a scheduled task should get auto-fix off: %v %s", e.Shape.SideEffects, e.Diags)
	}

	// Hooks run inside the session you have open, which has its own
	// settings.
	hook := strings.Replace(scheduled, `{ on: schedule, cron: "0 9 * * 1" }`, "{ on: session.stop }", 1)
	f = load(t, map[string]string{"routines/pr-review.md": withAutofix(hook), "routines/plain.md": hook})
	if e := stub.Evaluate(f, f.Routines["plain"], target(t, "claude-code-local")); !e.OK() || e.Shape.SideEffects != nil {
		t.Errorf("a hook has no settings of its own: %v %s", e.Shape.SideEffects, e.Diags)
	}
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "claude-code-local"))
	if e.OK() || !strings.Contains(e.Diags.String(), "runs as a hook inside the session you have open") {
		t.Errorf("a hook routine with github.pr_autofix should be an error: %s", e.Diags)
	}
}

func TestNativeSideEffectsInInitSessions(t *testing.T) {
	// An init routine runs in a one-off session, which has no stub to set
	// the runtime's own features.
	const initRoutine = "---\ntrigger: { on: init }\nconnectors: [github]\nside_effects:\n  - github.pr_comment\n---\nSet up.\n"
	f := load(t, map[string]string{"routines/plain.md": initRoutine})
	if e := stub.Evaluate(f, f.Routines["plain"], target(t, "claude-code-cloud")); !e.OK() || e.Shape.SideEffects != nil {
		t.Errorf("an init session has no settings of its own: %v %s", e.Shape.SideEffects, e.Diags)
	}
	f = load(t, map[string]string{"routines/fixing.md": withAutofix(initRoutine)})
	e := stub.Evaluate(f, f.Routines["fixing"], target(t, "claude-code-cloud"))
	if e.OK() || !strings.Contains(e.Diags.String(), "is an init routine, which runs in a one-off session Fabrica starts") {
		t.Errorf("an init routine with github.pr_autofix should be an error: %s", e.Diags)
	}
}

func TestDescribeSideEffect(t *testing.T) {
	for _, c := range []struct{ name, setting, want string }{
		{provider.PRAutofix, stub.SideEffectOn, "turn **Auto-fix pull requests** **on**"},
		{provider.PRAutofix, stub.SideEffectOff, "turn **Auto-fix pull requests** **off**"},
		{"github.issue_comment", stub.SideEffectOn, "turn the runtime's own `github.issue_comment` **on**"},
		{"github.issue_comment", stub.SideEffectOff, "turn the runtime's own `github.issue_comment` **off**"},
	} {
		if got := stub.DescribeSideEffect(c.name, c.setting); !strings.HasPrefix(got, c.want) {
			t.Errorf("DescribeSideEffect(%s, %s) = %q, want it to start with %q", c.name, c.setting, got, c.want)
		}
	}
}

func withModels(models string) string {
	return strings.Replace(routine, "skills: [pr-review]\n", "skills: [pr-review]\nmodels: "+models+"\n", 1)
}

func TestModelIsTheFirstTheRuntimeHas(t *testing.T) {
	f := load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: gpt-5.6-sol, effort: high }, { model: claude-opus-5-5, effort: max }, claude-sonnet-5-5, default]")})
	r := f.Routines["pr-review"]

	// Codex has the first one, and Claude routines only the second.
	codex := stub.Evaluate(f, r, target(t, "codex"))
	claude := stub.Evaluate(f, r, target(t, "claude-code-cloud"))
	if !codex.OK() || codex.Shape.Model != "gpt-5.6-sol" || codex.Shape.Effort != "high" {
		t.Errorf("codex: model %s (%s): %s", codex.Shape.Model, codex.Shape.Effort, codex.Diags)
	}
	if !claude.OK() || claude.Shape.Model != "claude-opus-5-5" || claude.Shape.Effort != "max" {
		t.Errorf("claude-code-cloud: model %s (%s): %s", claude.Shape.Model, claude.Shape.Effort, claude.Diags)
	}

	// Copilot automations and local tools pick their own, so they get
	// default, and nothing about the model goes into the stub.
	for _, rt := range []string{"copilot-cloud", "claude-code-local"} {
		plain := load(t, map[string]string{"routines/pr-review.md": routine})
		e := stub.Evaluate(f, r, target(t, rt))
		if e.Model.Name != "default" || e.Shape.Model != "" || e.Shape.Fingerprint() != stub.Evaluate(plain, plain.Routines["pr-review"], target(t, rt)).Shape.Fingerprint() {
			t.Errorf("%s: picked %s, shape %+v", rt, e.Model, e.Shape)
		}
	}

	// The model is part of the shape, so changing it changes the stub.
	other := load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: claude-opus-5-5, effort: high }]")})
	if stub.Evaluate(other, other.Routines["pr-review"], target(t, "claude-code-cloud")).Shape.Fingerprint() == claude.Shape.Fingerprint() {
		t.Error("changing the model's effort should change the stub's fingerprint")
	}
}

func TestNoModelTheRuntimeHasIsAnError(t *testing.T) {
	cases := map[string]string{
		// The runtime has the model, but not at that effort.
		"[{ model: claude-opus-5-5, effort: none }]": "has claude-opus-5-5, at effort low, medium, high, xhigh, max",
		"[gpt-5.5]":                          "add one it has (claude-fable-5-1, claude-haiku-5-5, claude-opus-5-5, claude-sonnet-5-5)",
		"[{ model: gpt-5.5, effort: high }]": "end models: with default",
	}
	for models, hint := range cases {
		f := load(t, map[string]string{"routines/pr-review.md": withModels(models)})
		e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "claude-code-cloud"))
		if e.OK() || !strings.Contains(e.Diags.String(), "has none of the models routine pr-review lists") || !strings.Contains(e.Diags.String(), hint) {
			t.Errorf("%s: want an error with %q, got:\n%s", models, hint, e.Diags)
		}
	}
	f := load(t, map[string]string{"routines/pr-review.md": withModels("[gpt-5.5]")})
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "cursor-local"))
	if e.OK() || !strings.Contains(e.Diags.String(), "picks its model itself") {
		t.Errorf("cursor-local: want an error saying it picks its own model, got:\n%s", e.Diags)
	}
}

func TestDescribeModel(t *testing.T) {
	for _, c := range []struct {
		shape stub.Shape
		want  string
	}{
		{stub.Shape{}, "leave it at the runtime's default"},
		{stub.Shape{Model: "gpt-5.5"}, "pick model `gpt-5.5`, and leave its effort at the default"},
		{stub.Shape{Model: "claude-opus-5-5", Effort: "max"}, "pick model `claude-opus-5-5`, with effort `max`"},
		{stub.Shape{Model: "grok-4.7", Effort: "high", Fast: "off"}, "pick model `grok-4.7`, with effort `high`, with fast mode **off**"},
		{stub.Shape{Model: "composer-2.5", Fast: "on"}, "pick model `composer-2.5`, and leave its effort at the default, with fast mode **on**"},
	} {
		if got := stub.DescribeModel(c.shape); got != c.want {
			t.Errorf("DescribeModel(%+v) = %q, want %q", c.shape, got, c.want)
		}
	}
}

func TestFastMode(t *testing.T) {
	// On a runtime with a fast mode for the model, the stub sets it either
	// way, so the runtime's own default (Grok runs fast on Cursor) never
	// decides.
	for models, want := range map[string]string{
		"[{ model: grok-4.7, effort: high }]":              "off",
		"[{ model: grok-4.7, effort: high, fast: false }]": "off",
		"[{ model: grok-4.7, effort: high, fast: true }]":  "on",
		"[{ model: claude-sonnet-5-5, effort: high }]":     "",
	} {
		f := load(t, map[string]string{"routines/pr-review.md": withModels(models)})
		e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "cursor-cloud"))
		if !e.OK() || e.Shape.Fast != want {
			t.Errorf("%s: fast %q, want %q: %s", models, e.Shape.Fast, want, e.Diags)
		}
	}
	// Fast mode is part of the shape, so turning it on changes the stub.
	slow := load(t, map[string]string{"routines/pr-review.md": withModels("[grok-4.7]")})
	fast := load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: grok-4.7, fast: true }]")})
	if stub.Evaluate(slow, slow.Routines["pr-review"], target(t, "cursor-cloud")).Shape.Fingerprint() == stub.Evaluate(fast, fast.Routines["pr-review"], target(t, "cursor-cloud")).Shape.Fingerprint() {
		t.Error("turning fast mode on should change the stub's fingerprint")
	}
	// An entry asking for fast mode only matches runtimes that have it for
	// the model; the next entry gets its turn elsewhere.
	f := load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: claude-opus-5-5, effort: high, fast: true }, { model: claude-opus-5-5, effort: high }]")})
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "claude-code-cloud"))
	if !e.OK() || e.Model.Fast || e.Shape.Fast != "" {
		t.Errorf("claude-code-cloud: picked %s, shape fast %q: %s", e.Model, e.Shape.Fast, e.Diags)
	}
	f = load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: claude-sonnet-5-5, fast: true }]")})
	e = stub.Evaluate(f, f.Routines["pr-review"], target(t, "cursor-cloud"))
	if e.OK() || !strings.Contains(e.Diags.String(), "no fast mode for it") {
		t.Errorf("want an error saying claude-sonnet-5-5 has no fast mode, got:\n%s", e.Diags)
	}
}

func TestModelIsCalledWhatTheRuntimeCallsIt(t *testing.T) {
	// Cursor calls GLM 5.3 glm-5p3, but blueprints use its maker's name.
	f := load(t, map[string]string{"routines/pr-review.md": withModels("[{ model: glm-5.3, effort: low }]")})
	e := stub.Evaluate(f, f.Routines["pr-review"], target(t, "cursor-cloud"))
	if !e.OK() || e.Model.Name != "glm-5.3" || e.Shape.Model != "glm-5p3" || e.Shape.Effort != "low" {
		t.Errorf("picked %s, shape model %s (%s): %s", e.Model, e.Shape.Model, e.Shape.Effort, e.Diags)
	}
	f = load(t, map[string]string{"routines/pr-review.md": withModels("[glm-5p3]")})
	e = stub.Evaluate(f, f.Routines["pr-review"], target(t, "cursor-cloud"))
	if e.OK() || !strings.Contains(e.Diags.String(), "glm-5p3 is what the runtime calls glm-5.3") {
		t.Errorf("want an error pointing at glm-5.3, got:\n%s", e.Diags)
	}
}
