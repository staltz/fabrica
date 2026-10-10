package localtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleConfig(runtime string) Config {
	hooks := []Hook{
		{Binary: "/opt/fab rica/bin/fabrica", Runtime: runtime, Event: GitPrePush, Deployment: "oss-work", Routine: "review-before-push", Fingerprint: "7f3a9c7f3a9c"},
		{Binary: "/opt/fab rica/bin/fabrica", Runtime: runtime, Event: SessionStop, Deployment: "oss-work", Routine: "tidy", Fingerprint: "abcdefabcdef"},
	}
	if runtime == "claude-code-local" || runtime == "codex-cli" {
		hooks = append(hooks, Hook{Binary: "/usr/bin/fabrica", Runtime: runtime, Event: SessionStart, Deployment: "d", Routine: "hello", Fingerprint: "111111111111"})
	}
	var tasks []Task
	if runtime == "claude-code-local" {
		tasks = []Task{
			{Deployment: "oss-work", Routine: "update-dep", Repo: "github.com/acme/app", Cron: "0 9 * * 1", Fingerprint: "222222222222", Prompt: "Run `update-dep`: it's Monday.\n\nSecond paragraph."},
			{Deployment: "oss-work", Routine: "update-dep", Repo: "github.com/acme/lib", Cron: "0 9 * * 1", Fingerprint: "222222222222", Prompt: "Run it on lib."},
		}
	}
	return Config{
		Tasks: tasks,
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
	// The user's own scheduled tasks, including one whose name happens to
	// start with fabrica- but that Fabrica didn't write.
	".claude/scheduled-tasks/morning/SKILL.md":       "---\nname: morning\ndescription: my morning brief\n---\n\nBrief me.\n",
	".claude/scheduled-tasks/fabrica-notes/SKILL.md": "---\nname: fabrica-notes\ndescription: notes about Fabrica\n---\n\nSummarize.\n",
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
				if info, err := os.Stat(f); err == nil && !info.IsDir() && info.Mode().Perm() != 0o600 {
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
			if got, _ := tool.Read(); len(got.MCP) != 0 || len(got.Hooks) != 0 || len(got.Tasks) != 0 {
				t.Errorf("after removing, still found %+v", got)
			}
			for name, content := range userFiles {
				if strings.HasPrefix(name, ".claude/scheduled-tasks/") && rt == "claude-code-local" {
					if data, _ := os.ReadFile(filepath.Join(home, name)); string(data) != content {
						t.Errorf("%s changed:\n%s", name, data)
					}
				}
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
		filepath.WalkDir(f, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				data, _ := os.ReadFile(p)
				info, _ := d.Info()
				out[p] = string(data) + info.ModTime().String()
			}
			return nil
		})
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
	tool, _ := For("cursor-local", t.TempDir())
	err := tool.Write(Config{Hooks: []Hook{{Binary: "fabrica", Event: SessionStart, Deployment: "d", Routine: "r", Fingerprint: "f"}}})
	if err == nil {
		t.Error("Cursor has no session.start hook, so writing one should fail")
	}
}

func TestHookCommandRoundTrip(t *testing.T) {
	h := Hook{Binary: "/Users/me/My Tools/fabrica", Runtime: "cursor-local", Event: GitPrePush, Deployment: "d", Routine: "r", Fingerprint: "f"}
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

func TestClaudeTaskShape(t *testing.T) {
	home := t.TempDir()
	tool, _ := For("claude-code-local", home)
	task := Task{Deployment: "dogfood", Routine: "update-dep", Repo: "github.com/staltz/fabrica", Cron: "0 9 * * 1", Fingerprint: "abc123abc123", Prompt: "Do it."}
	if err := tool.Write(Config{Tasks: []Task{task}}); err != nil {
		t.Fatal(err)
	}
	id := TaskID(task)
	if !strings.HasPrefix(id, "fabrica-update-dep-") {
		t.Errorf("task ID %q should start with the routine's name", id)
	}
	file := filepath.Join(home, ".claude", "scheduled-tasks", id, "SKILL.md")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "---\nname: \""+id+"\"\ndescription: \"Fabrica routine `update-dep`") || !strings.HasSuffix(string(data), "---\n\nDo it.\n") {
		t.Errorf("unexpected SKILL.md:\n%s", data)
	}
	if _, err := os.Stat(file + ".fabrica.bak"); !os.IsNotExist(err) {
		t.Error("there should be no backup inside the task's folder")
	}

	// If the app rewrites the frontmatter without quotes, Fabrica still
	// recognizes its task.
	rewritten := "---\nname: " + id + "\ndescription: " + taskDescription(task) + "\n---\n\nDo it.\n"
	os.WriteFile(file, []byte(rewritten), 0o644)
	got, err := tool.Read()
	if err != nil || len(got.Tasks) != 1 || got.Tasks[0] != task {
		t.Errorf("read back %+v (%v), want %+v", got.Tasks, err, task)
	}
}

func TestLocalCron(t *testing.T) {
	helsinki := time.FixedZone("EEST", 3*3600)
	kolkata := time.FixedZone("IST", 5*3600+1800)
	newYork := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		cron string
		loc  *time.Location
		want string // "" means it can't be converted
	}{
		{"0 9 * * 1", helsinki, "0 12 * * 1"},
		{"0 22 * * 1-5", helsinki, "0 1 * * 2,3,4,5,6"},
		{"0 22 * * 5", helsinki, "0 1 * * 6"},
		{"0 2 * * 1", newYork, "0 22 * * 0"},
		{"30 9 * * *", kolkata, "0 15 * * *"},
		{"0,30 9 * * *", kolkata, ""}, // 14:30 and 15:00 aren't a minute × hour grid
		{"15 * * * *", kolkata, "45 * * * *"},
		{"15 * * * 1", helsinki, ""},
		{"0 1,23 * * 1", helsinki, ""}, // lands on two different local days
		{"0 1,23 * * *", helsinki, "0 2,4 * * *"},
		{"0 9 1 * *", helsinki, ""},
		{"*/15 9 * * *", helsinki, ""},
		{"0 9 * * 0,7", helsinki, "0 12 * * 0"},
		{"0 9 * * 1", time.UTC, "0 9 * * 1"},
	}
	for _, c := range cases {
		got, ok := LocalCron(c.cron, now.In(c.loc))
		if (c.want == "") == ok || got != c.want {
			t.Errorf("LocalCron(%q, %s) = %q, %v; want %q", c.cron, c.loc, got, ok, c.want)
		}
	}
}

func TestTaskSetup(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.FixedZone("EEST", 3*3600))
	a := Task{Deployment: "d", Routine: "a", Repo: "github.com/x/y", Cron: "0 9 * * 1", Fingerprint: "1", Prompt: "A"}
	b := Task{Deployment: "d", Routine: "b", Repo: "github.com/x/y", Cron: "0 9 * * 1", Fingerprint: "1", Prompt: "B"}
	c := Task{Deployment: "d", Routine: "c", Repo: "github.com/x/y", Cron: "0 9 * * 1", Fingerprint: "1", Prompt: "C"}
	b2, c2 := b, c
	b2.Prompt = "B, reworded"
	c2.Cron, c2.Fingerprint = "0 10 * * 1", "2"
	got := strings.Join(TaskSetup([]Task{b, c, a}, []Task{b2, c2}, now), "\n")
	for _, want := range []string{
		"Change the schedule of " + TaskID(c) + " to cron `0 13 * * 1` (`0 10 * * 1` in UTC, at today's offset of UTC+03:00).",
		"Delete " + TaskID(a) + " from the app's list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("setup is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, TaskID(b)) {
		t.Errorf("a prompt change needs nothing in the app:\n%s", got)
	}
	got = strings.Join(TaskSetup(nil, []Task{a}, now), "\n")
	wantPrompt := "send Claude this prompt:\n-----BEGIN PROMPT-----\nCreate a scheduled task from ~/.claude/scheduled-tasks/" + TaskID(a) + "/SKILL.md."
	if !strings.Contains(got, "Register "+TaskID(a)+": in a desktop session on your checkout of github.com/x/y") || !strings.Contains(got, wantPrompt) ||
		!strings.Contains(got, "on cron `0 12 * * 1` (`0 9 * * 1` in UTC, at today's offset of UTC+03:00).\n-----END PROMPT-----") {
		t.Errorf("unexpected setup for a new task:\n%s", got)
	}
}
