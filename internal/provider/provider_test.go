package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsLoad(t *testing.T) {
	t.Setenv(CursorKeyEnv, "") // without its key, cursor-cloud uses handoffs too
	t.Setenv(CursorTokenEnv, "")
	want := []string{"claude-code-cloud", "claude-code-local", "codex", "codex-cli", "copilot-cloud", "copilot-local", "cursor-cloud", "cursor-local"}
	got := BuiltinNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("BuiltinNames = %v, want %v", got, want)
	}
	for _, name := range got {
		p, _ := Builtin(name)
		mode, err := p.Mode()
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if p.IsLocal() != (mode == API) {
			t.Errorf("%s: local=%v but mode=%s; built-in cloud providers use handoff without their API's credentials, local ones api", name, p.IsLocal(), mode)
		}
	}
}

func TestParseMatrixRejectsMistakes(t *testing.T) {
	cases := map[string]string{
		"emulated trigger":    "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\ntriggers: {schedule: emulated}\n",
		"api without find":    "kind: cloud\napply_modes: [api]\nskill_transport: text\nmcp_auth: {url: native}\n",
		"cloud directory":     "kind: cloud\napply_modes: [handoff]\nskill_transport: directory\nmcp_auth: {url: native}\n",
		"no token path":       "kind: cloud\napply_modes: [handoff]\nskill_transport: text\n",
		"unknown key":         "kind: cloud\napply_mode: [handoff]\n",
		"bad support value":   "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nconnectors: {github: yes}\n",
		"bad memory value":    "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmemory: sometimes\n",
		"model named default": "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmodels: {default: []}\n",
		"bad model name":      "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmodels: {Opus 5: []}\n",
		"unknown effort":      "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmodels: {gpt-5.5: [extra-high]}\n",
		"effort twice":        "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmodels: {gpt-5.5: [high, high]}\n",
	}
	for name, yaml := range cases {
		if _, err := ParseMatrix("x", []byte(yaml)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	m, err := ParseMatrix("x", []byte("kind: cloud\napply_modes: [api]\nfind_by_remote_id: true\nskill_transport: text\nmcp_auth: {url: native}\n"))
	if err != nil || !m.FindByRemoteID {
		t.Errorf("api mode with find_by_remote_id should be fine, got %+v, %v", m, err)
	}
}

func TestMemorySupport(t *testing.T) {
	want := map[string]Support{
		"claude-code-cloud": Emulated, "cursor-cloud": Native, "codex": Emulated,
		"claude-code-local": Emulated, "cursor-local": Emulated, "codex-cli": Emulated,
	}
	for name, w := range want {
		p, _ := Builtin(name)
		if p.Matrix.Memory != w {
			t.Errorf("%s: memory = %s, want %s", name, p.Matrix.Memory, w)
		}
	}
	m, err := ParseMatrix("x", []byte("kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\n"))
	if err != nil || m.Memory != Unsupported {
		t.Errorf("a matrix without memory: should mean unsupported, got %q, %v", m.Memory, err)
	}
}

func TestModels(t *testing.T) {
	m, err := ParseMatrix("x", []byte("kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmodels: {gpt-5.5: [low, high], composer-2.5: []}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		model, effort string
		want          bool
	}{
		{"gpt-5.5", "", true}, {"gpt-5.5", "high", true}, {"gpt-5.5", "max", false},
		{"composer-2.5", "", true}, {"composer-2.5", "low", false}, {"claude-opus-5-5", "", false},
	} {
		if got := m.HasModel(c.model, c.effort); got != c.want {
			t.Errorf("HasModel(%q, %q) = %v, want %v", c.model, c.effort, got, c.want)
		}
	}
	if got := strings.Join(m.ModelNames(), ","); got != "composer-2.5,gpt-5.5" {
		t.Errorf("ModelNames = %s", got)
	}
	// Local routines run in the session that's open, on its model.
	for _, name := range BuiltinNames() {
		p, _ := Builtin(name)
		if p.IsLocal() && len(p.Matrix.Models) > 0 {
			t.Errorf("%s lists models, but a local runtime can't pick one for a routine", name)
		}
	}
}

func TestSideEffectLookupOrder(t *testing.T) {
	m, err := ParseMatrix("x", []byte(`kind: cloud
apply_modes: [handoff]
skill_transport: text
mcp_auth: {header: native}
side_effects:
  github.pr_comment: native
  "github.*": emulated
  "*": unsupported
`))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]Support{
		"github.pr_comment": Native,
		"github.merge":      Emulated,
		"slack.notify":      Unsupported,
	} {
		if got := m.SideEffect(name); got != want {
			t.Errorf("SideEffect(%q) = %s, want %s", name, got, want)
		}
	}
	if m.TokenPlacement() != TokenInHeader {
		t.Errorf("TokenPlacement = %s, want header", m.TokenPlacement())
	}
	if m.Trigger("schedule") != Unsupported {
		t.Error("unlisted triggers should be unsupported")
	}
}

func writeFolderProvider(t *testing.T, script string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "acme-runner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `kind: cloud
description: Acme's runner
apply_modes: [api, handoff]
find_by_identity: true
skill_transport: text
mcp_auth: {header: native}
triggers: {schedule: native}
connectors: {mcp.http: native}
`
	files := map[string]string{
		"provider.yaml": yaml,
		"handoff.md":    "Do {{.Action}} for {{.Identity}}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "api"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadFolderNeedsExecutableAPI(t *testing.T) {
	dir := writeFolderProvider(t, "")
	if _, err := LoadFolder("acme-runner", dir); err == nil || !strings.Contains(err.Error(), "no api program") {
		t.Fatalf("want a missing-api error, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFolder("acme-runner", dir); err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("want a not-executable error, got %v", err)
	}
	if _, err := LoadFolder("claude-code-cloud", dir); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("want a can't-replace-built-in error, got %v", err)
	}
	for _, name := range []string{"claude", "cursor-ide"} {
		if _, err := LoadFolder(name, dir); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("%s: want a reserved-name error, got %v", name, err)
		}
	}
}

func TestFolderProviderAPINeedsListing(t *testing.T) {
	dir := writeFolderProvider(t, "#!/bin/sh\n")
	yaml := "kind: cloud\napply_modes: [api]\nfind_by_remote_id: true\nskill_transport: text\nmcp_auth: {header: native}\n"
	if err := os.WriteFile(filepath.Join(dir, "provider.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFolder("acme-runner", dir)
	if err == nil || !strings.Contains(err.Error(), "needs find_by_identity: true") {
		t.Errorf("the api protocol has no get operation, so find_by_remote_id alone isn't enough; got %v", err)
	}
}

func TestAPIClientProtocol(t *testing.T) {
	// A tiny provider written in shell. It echoes back canned responses
	// and records the request it got, so we can check what Fabrica sent.
	script := `#!/bin/sh
cat > "$(dirname "$0")/last-request.json"
case "$1" in
  list)   echo '{"resources":[{"identity":"x#r@github.com/a/b","remote_id":"r1"}]}' ;;
  create) echo '{"remote_id":"r2"}' ;;
  update) echo '{}' ;;
  delete) echo 'nope' >&2; exit 3 ;;
esac
`
	dir := writeFolderProvider(t, script)
	p, err := LoadFolder("acme-runner", dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.Mode(); mode != API {
		t.Fatalf("mode = %s, want api", mode)
	}
	client, _ := p.API()
	api, ok := client.(*APIClient)
	if !ok {
		t.Fatalf("a folder provider's API should be its api program, got %T", client)
	}
	ctx := context.Background()

	list, err := api.List(ctx, "pr-review", "https://f.example/pr-review")
	if err != nil || len(list) != 1 || list[0].RemoteID != "r1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	id, err := api.Create(ctx, Resource{Identity: "x#r@github.com/a/b", Factory: "pr-review", Fabrica: ResourceFabrica{Token: "secret"}})
	if err != nil || id != "r2" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	sent, _ := os.ReadFile(filepath.Join(dir, "last-request.json"))
	if !strings.Contains(string(sent), `"version":1`) || !strings.Contains(string(sent), `"op":"create"`) {
		t.Errorf("unexpected request: %s", sent)
	}
	if err := api.Update(ctx, "r2", Resource{Factory: "pr-review"}); err != nil {
		t.Fatal(err)
	}
	err = api.Delete(ctx, "pr-review", "r2")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Delete should fail with the program's stderr, got %v", err)
	}
}

func TestRenderBuiltinHandoffs(t *testing.T) {
	for _, name := range []string{"claude-code-cloud", "cursor-cloud", "codex", "copilot-cloud"} {
		p, _ := Builtin(name)
		for _, action := range []string{"create", "update", "delete"} {
			out, err := p.RenderHandoff(HandoffData{
				ID: "h_abc", Action: action, Runtime: name, Factory: "pr-review",
				Identity: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app",
				Routine:  "pr-review", Repo: "github.com/acme/app", RemoteID: "trig_1",
				Trigger:        HandoffTrigger{On: "github.pull_request.opened", Description: "when a pull request is opened"},
				Connectors:     []HandoffConnector{{Name: "github", Setup: "the built-in GitHub integration"}},
				Fabrica:        HandoffFabrica{URL: "https://fabrica.acme.dev/pr-review/t/tok"},
				Model:          HandoffModel{Name: "m", Effort: "high", Setup: "pick model `m`, with effort `high`"},
				Prompt:         "line one\nline two",
				ConfirmCommand: "fabrica confirm h_abc --remote-id <id>",
			})
			if err != nil {
				t.Fatalf("%s %s: %v", name, action, err)
			}
			if !strings.Contains(out, "fabrica confirm h_abc") {
				t.Errorf("%s %s: handoff doesn't end with the confirm command:\n%s", name, action, out)
			}
			if action != "delete" && !strings.Contains(out, "   line two") {
				t.Errorf("%s %s: prompt isn't included and indented:\n%s", name, action, out)
			}
			if action != "delete" && !strings.Contains(out, "6. Memory: ") {
				t.Errorf("%s %s: handoff doesn't say what to do with the runtime's memory:\n%s", name, action, out)
			}
			if action != "delete" && len(p.Matrix.Models) > 0 && !strings.Contains(out, "7. Model: pick model `m`, with effort `high`.") {
				t.Errorf("%s %s: handoff doesn't say which model to pick:\n%s", name, action, out)
			}
		}
	}
}

func TestCopilotHandoffKeepsTheTokenInASecret(t *testing.T) {
	p, _ := Builtin("copilot-cloud")
	if p.Matrix.TokenPlacement() != TokenInHeader {
		t.Fatalf("copilot-cloud should send the token in a header, not %s", p.Matrix.TokenPlacement())
	}
	out, err := p.RenderHandoff(HandoffData{
		ID: "h_abc", Action: "create", Runtime: "copilot-cloud", Factory: "nightly",
		Identity: "fabrica.acme.dev/nightly#nightly@github.com/acme/app", Routine: "nightly", Repo: "github.com/acme/app",
		Trigger: HandoffTrigger{On: "schedule", Cron: "0 3 * * *", Description: "on a schedule, cron `0 3 * * *` (UTC)"},
		Fabrica: HandoffFabrica{URL: "https://fabrica.acme.dev/nightly", Header: "Bearer tok"},
		Prompt:  "x", ConfirmCommand: "fabrica confirm h_abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"named `COPILOT_MCP_FABRICA_AUTHORIZATION`, with the value `Bearer tok`",
		`"headers": {"Authorization": "$COPILOT_MCP_FABRICA_AUTHORIZATION"}`,
		"hourly, daily or weekly",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

func TestCursorHandoffMirrorsTheRoutinesMemory(t *testing.T) {
	p, _ := Builtin("cursor-cloud")
	for needed, want := range map[bool]string{true: "memory tool **on**", false: "memory tool **off** (it's on by default)"} {
		out, err := p.RenderHandoff(HandoffData{
			ID: "h_abc", Action: "create", Runtime: "cursor-cloud", Factory: "pr-review",
			Identity: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Routine: "pr-review", Repo: "github.com/acme/app",
			Memory: HandoffMemory{Needed: needed, Support: string(Native)}, Prompt: "x", ConfirmCommand: "fabrica confirm h_abc",
		})
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("memory needed=%v: want %q in:\n%s (%v)", needed, want, out, err)
		}
	}
}
