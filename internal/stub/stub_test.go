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
	id := stub.Identity("https://fabrica.acme.dev/pr-review/", "pr-review", "github.com/acme/app")
	if id != "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app" {
		t.Errorf("Identity = %q", id)
	}
	p := stub.Prompt(id, "pr-review", "claude-code-cloud", "github.com/acme/app", "7f3a9c")
	for _, want := range []string{"`run_begin`", "routine `pr-review`", "runtime `claude-code-cloud`", "repo `github.com/acme/app`", "stub `7f3a9c`", "`routine_get`", id} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
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
