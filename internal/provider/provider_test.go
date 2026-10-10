package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinsLoad(t *testing.T) {
	want := []string{"claude-code-cloud", "claude-code-local", "codex", "codex-cli", "cursor-cloud", "cursor-local"}
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
			t.Errorf("%s: local=%v but mode=%s; built-in cloud providers use handoff, local ones api", name, p.IsLocal(), mode)
		}
	}
}

func TestParseMatrixRejectsMistakes(t *testing.T) {
	cases := map[string]string{
		"emulated trigger":  "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\ntriggers: {schedule: emulated}\n",
		"api without find":  "kind: cloud\napply_modes: [api]\nskill_transport: text\nmcp_auth: {url: native}\n",
		"cloud directory":   "kind: cloud\napply_modes: [handoff]\nskill_transport: directory\nmcp_auth: {url: native}\n",
		"no token path":     "kind: cloud\napply_modes: [handoff]\nskill_transport: text\n",
		"unknown key":       "kind: cloud\napply_mode: [handoff]\n",
		"bad support value": "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nconnectors: {github: yes}\n",
		"bad memory value":  "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {url: native}\nmemory: sometimes\n",
	}
	for name, yaml := range cases {
		if _, err := ParseMatrix("x", []byte(yaml)); err == nil {
			t.Errorf("%s: want an error", name)
		}
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
	api, _ := p.API()
	ctx := context.Background()

	list, err := api.List(ctx, "pr-review", "https://f.example/pr-review")
	if err != nil || len(list) != 1 || list[0].RemoteID != "r1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	id, err := api.Create(ctx, Resource{Identity: "x#r@github.com/a/b", Deployment: "pr-review", Fabrica: ResourceFabrica{Token: "secret"}})
	if err != nil || id != "r2" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	sent, _ := os.ReadFile(filepath.Join(dir, "last-request.json"))
	if !strings.Contains(string(sent), `"version":1`) || !strings.Contains(string(sent), `"op":"create"`) {
		t.Errorf("unexpected request: %s", sent)
	}
	if err := api.Update(ctx, "r2", Resource{Deployment: "pr-review"}); err != nil {
		t.Fatal(err)
	}
	err = api.Delete(ctx, "pr-review", "r2")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Delete should fail with the program's stderr, got %v", err)
	}
}

func TestRenderBuiltinHandoffs(t *testing.T) {
	for _, name := range []string{"claude-code-cloud", "cursor-cloud", "codex"} {
		p, _ := Builtin(name)
		for _, action := range []string{"create", "update", "delete"} {
			out, err := p.RenderHandoff(HandoffData{
				ID: "h_abc", Action: action, Runtime: name, Deployment: "pr-review",
				Identity: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app",
				Routine:  "pr-review", Repo: "github.com/acme/app", RemoteID: "trig_1",
				Trigger:        HandoffTrigger{On: "github.pull_request.opened", Description: "when a pull request is opened"},
				Connectors:     []HandoffConnector{{Name: "github", Setup: "the built-in GitHub integration"}},
				Fabrica:        HandoffFabrica{URL: "https://fabrica.acme.dev/pr-review/t/tok"},
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
		}
	}
}

func TestCursorHandoffMirrorsTheRoutinesMemory(t *testing.T) {
	p, _ := Builtin("cursor-cloud")
	for needed, want := range map[bool]string{true: "memory tool **on**", false: "memory tool **off** (it's on by default)"} {
		out, err := p.RenderHandoff(HandoffData{
			ID: "h_abc", Action: "create", Runtime: "cursor-cloud", Deployment: "pr-review",
			Identity: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Routine: "pr-review", Repo: "github.com/acme/app",
			Memory: HandoffMemory{Needed: needed, Support: string(Native)}, Prompt: "x", ConfirmCommand: "fabrica confirm h_abc",
		})
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("memory needed=%v: want %q in:\n%s (%v)", needed, want, out, err)
		}
	}
}
