package localtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sampleConfig(runtime string) Config {
	hooks := []Hook{
		{Binary: "/opt/fab rica/bin/fabrica", Runtime: runtime, Event: GitPrePush, Deployment: "oss-work", Routine: "review-before-push", Fingerprint: "7f3a9c7f3a9c"},
		{Binary: "/opt/fab rica/bin/fabrica", Runtime: runtime, Event: SessionStop, Deployment: "oss-work", Routine: "tidy", Fingerprint: "abcdefabcdef"},
	}
	if runtime == "claude-code-local" || runtime == "codex-cli" {
		hooks = append(hooks, Hook{Binary: "/usr/bin/fabrica", Runtime: runtime, Event: SessionStart, Deployment: "d", Routine: "hello", Fingerprint: "111111111111"})
	}
	return Config{
		MCP: []MCPEntry{
			{Name: "fabrica", Transport: "stdio", Command: "/opt/fab rica/bin/fabrica", Args: []string{"mcp", "--runtime", runtime, "--home", "/home/me/.fabrica"}},
			{Name: "fabrica-linear", Transport: "http", URL: "https://mcp.linear.app/mcp", BearerEnv: "LINEAR_API_KEY"},
			{Name: "fabrica-local", Transport: "stdio", Command: "my-mcp", Args: []string{"--x"}, EnvNames: []string{"TOKEN"}},
		},
		Hooks: hooks,
	}
}

// userFiles are config files with the user's own settings, which must
// survive Fabrica's edits untouched.
var userFiles = map[string]string{
	".claude.json":          `{"numStartups": 7, "mcpServers": {"github": {"type": "http", "url": "https://gh.example"}}, "zzz": true}`,
	".claude/settings.json": `{"model": "x", "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}]}}`,
	".cursor/mcp.json":      `{"mcpServers": {"mine": {"command": "mine"}}}`,
	".cursor/hooks.json":    `{"version": 1, "hooks": {"afterFileEdit": [{"command": "./format.sh"}]}}`,
	".codex/config.toml":    "# my codex config\nmodel = \"o5\"\n\n[mcp_servers.mine]\ncommand = \"mine\" # keep me\n",
	".codex/hooks.json":     `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify-me"}]}]}}`,
}

func TestToolsRoundTripAndPreserveUserSettings(t *testing.T) {
	for _, rt := range Runtimes() {
		t.Run(rt, func(t *testing.T) {
			home := t.TempDir()
			for name, content := range userFiles {
				p := filepath.Join(home, name)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte(content), 0o600)
			}
			tool, err := For(rt, home)
			if err != nil {
				t.Fatal(err)
			}
			want := sampleConfig(rt).Normalize()
			if err := tool.Write(want); err != nil {
				t.Fatal(err)
			}
			got, err := tool.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Normalize(), want) {
				t.Errorf("read back:\n%+v\nwant:\n%+v", got.Normalize(), want)
			}

			// The user's own settings are all still there.
			for _, f := range tool.Files() {
				data, _ := os.ReadFile(f)
				for _, keep := range []string{"numStartups", "github", "my-linter", "mine", "./format.sh", "# my codex config", "# keep me", "notify-me"} {
					rel, _ := filepath.Rel(home, f)
					if strings.Contains(userFiles[rel], keep) && !strings.Contains(string(data), keep) {
						t.Errorf("%s lost the user's %q:\n%s", rel, keep, data)
					}
				}
				if info, err := os.Stat(f); err == nil && info.Mode().Perm() != 0o600 {
					t.Errorf("%s permissions changed to %v", f, info.Mode().Perm())
				}
			}

			// Writing the same config again changes nothing on disk.
			before := snapshot(t, tool)
			if err := tool.Write(want); err != nil {
				t.Fatal(err)
			}
			if after := snapshot(t, tool); !reflect.DeepEqual(before, after) {
				t.Error("a second identical Write changed the files")
			}

			// Writing an empty config removes everything Fabrica added.
			if err := tool.Write(Config{}); err != nil {
				t.Fatal(err)
			}
			if got, _ := tool.Read(); len(got.MCP) != 0 || len(got.Hooks) != 0 {
				t.Errorf("after removing, still found %+v", got)
			}
			for _, f := range tool.Files() {
				data, _ := os.ReadFile(f)
				if strings.Contains(string(data), "fabrica") {
					t.Errorf("%s still mentions fabrica:\n%s", f, data)
				}
			}
		})
	}
}

func snapshot(t *testing.T, tool Tool) map[string]string {
	out := map[string]string{}
	for _, f := range tool.Files() {
		data, _ := os.ReadFile(f)
		out[f] = string(data)
	}
	return out
}

func TestClaudeHookShape(t *testing.T) {
	home := t.TempDir()
	tool, _ := For("claude-code-local", home)
	if err := tool.Write(Config{Hooks: []Hook{{Binary: "/usr/bin/fabrica", Runtime: "claude-code-local", Event: GitPrePush, Deployment: "d", Routine: "r", Fingerprint: "f"}}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	var v struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	g := v.Hooks["PreToolUse"]
	if len(g) != 1 || g[0].Matcher != "Bash" || g[0].Hooks[0].Type != "command" || g[0].Hooks[0].Timeout != 30 ||
		g[0].Hooks[0].Command != "/usr/bin/fabrica hook --runtime claude-code-local --deployment d --routine r --stub f --event git.pre_push" {
		t.Errorf("unexpected hooks:\n%s", data)
	}
}

func TestCursorRefusesUnsupportedTrigger(t *testing.T) {
	tool, _ := For("cursor-ide", t.TempDir())
	err := tool.Write(Config{Hooks: []Hook{{Binary: "fabrica", Event: SessionStart, Deployment: "d", Routine: "r", Fingerprint: "f"}}})
	if err == nil {
		t.Error("Cursor has no session.start hook, so writing one should fail")
	}
}

func TestHookCommandRoundTrip(t *testing.T) {
	h := Hook{Binary: "/Users/me/My Tools/fabrica", Runtime: "cursor-ide", Event: GitPrePush, Deployment: "d", Routine: "r", Fingerprint: "f"}
	got, ok := ParseHookCommand(HookCommand(h))
	if !ok || got != h {
		t.Errorf("round trip = %+v, %v", got, ok)
	}
	for _, notOurs := range []string{"my-linter", "fabrica mcp", "/bin/fabricate hook", "fabrica hook --deployment d"} {
		if _, ok := ParseHookCommand(notOurs); ok {
			t.Errorf("%q shouldn't be recognized as a Fabrica hook", notOurs)
		}
	}
}

func TestInvalidJSONIsReported(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{not json"), 0o644)
	tool, _ := For("claude-code-local", home)
	if err := tool.Write(Config{}); err == nil || !strings.Contains(err.Error(), "isn't valid JSON") {
		t.Errorf("want a clear error, got %v", err)
	}
}
