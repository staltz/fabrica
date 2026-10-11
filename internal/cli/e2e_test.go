package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/mcpserver"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// TestMain lets the test binary stand in for the fabrica binary (and for
// the toy provider), so end-to-end tests can run real subprocesses without
// building anything first.
func TestMain(m *testing.M) {
	testkit.MaybeRunToyProvider()
	if os.Getenv("FABRICA_TEST_AS_CLI") == "1" {
		os.Exit(Main(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	// Every command creates your personal home repo if it's missing, so
	// tests that don't choose one get a throwaway one, never ~/.fabrica.
	dir, err := os.MkdirTemp("", "fabrica-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("FABRICA_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fab runs the CLI in-process and returns its output and exit code.
func fab(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	if dir != "" {
		t.Chdir(dir)
	}
	var out bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(""), &out, &out)
	return out.String(), code
}

func mustFab(t *testing.T, wantCode int, dir string, args ...string) string {
	t.Helper()
	out, code := fab(t, dir, args...)
	if code != wantCode {
		t.Fatalf("fabrica %s: exit %d, want %d\n%s", strings.Join(args, " "), code, wantCode, out)
	}
	return out
}

func setupCLI(t *testing.T) *testkit.World {
	t.Helper()
	w := testkit.NewWorld(t)
	t.Setenv("FABRICA_CACHE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CI", "")
	t.Setenv(token.EnvVar, testkit.Secret)
	// Start with the personal home repo there already, as it is after your
	// first command, so its creation doesn't show up in every output.
	if _, _, err := home.EnsurePersonal(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCheckCommands(t *testing.T) {
	w := setupCLI(t)

	out := mustFab(t, 0, w.Blueprint.Dir, "check")
	if !strings.Contains(out, "2 skills, 2 routines, 2 rules, 2 connectors, 1 provider") || !strings.Contains(out, "No problems found") {
		t.Errorf("blueprint check output:\n%s", out)
	}

	// A typo is an error, with the file and the fix.
	w.Blueprint.Write(map[string]string{"rules/broken.mdc": "---\nalwaysapply: true\n---\nHi\n"})
	out = mustFab(t, 1, w.Blueprint.Dir, "check")
	if !strings.Contains(out, "rules/broken.mdc") || !strings.Contains(out, `unknown key "alwaysapply" (rules only have description, globs and alwaysApply)`) {
		t.Errorf("want the typo reported:\n%s", out)
	}

	// A product repo checked against a blueprint.
	out = mustFab(t, 0, w.App.Dir, "check", "--blueprint", "github.com/acme/blueprint@stable")
	if !strings.Contains(out, "github.com/acme/app") || !strings.Contains(out, "doesn't provide optional skill \"test\"") {
		t.Errorf("product check output:\n%s", out)
	}
	w.API.Write(map[string]string{"AGENTS.md": "# API\n"})
	out = mustFab(t, 1, w.API.Dir, "check", "--blueprint", w.Blueprint.Dir)
	if !strings.Contains(out, "doesn't start with the Fabrica line") || !strings.Contains(out, testkit.FabricaLine) {
		t.Errorf("want a missing-line error that shows the line:\n%s", out)
	}
}

func TestLockAndUpgradeCommands(t *testing.T) {
	w := setupCLI(t)
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, map[string]string{
		"fabrica.yaml": "imports:\n  acme: { git: github.com/acme/blueprint, ref: main, only: [skills/pr-review, rules/tests-first] }\n",
	})
	out := mustFab(t, 1, dir, "check")
	if !strings.Contains(out, "run `fabrica lock`") {
		t.Errorf("want a hint to lock:\n%s", out)
	}
	out = mustFab(t, 0, dir, "lock")
	if !strings.Contains(out, "acme (main): new import") || !strings.Contains(out, "Wrote fabrica.lock") {
		t.Errorf("lock output:\n%s", out)
	}
	mustFab(t, 0, dir, "check")
	if out := mustFab(t, 0, dir, "lock"); !strings.Contains(out, "up to date") {
		t.Errorf("a second lock should be a no-op:\n%s", out)
	}

	w.UpdateBlueprint(map[string]string{"skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: Better reviews.\n---\nNew.\n"})
	out = mustFab(t, 1, dir, "upgrade") // no terminal and no --yes: refuses
	if !strings.Contains(out, "~ skills/pr-review") || !strings.Contains(out, "run again with --yes") {
		t.Errorf("upgrade without --yes:\n%s", out)
	}
	if out := mustFab(t, 0, dir, "upgrade", "--yes"); !strings.Contains(out, "Wrote fabrica.lock") {
		t.Errorf("upgrade --yes:\n%s", out)
	}
	if out := mustFab(t, 0, dir, "upgrade"); !strings.Contains(out, "already at the newest commit") {
		t.Errorf("nothing left to upgrade:\n%s", out)
	}
}

// TestSuccessTest is the v0.1 success test from the design: one person
// defines a PR-review routine once and has it running on both Claude and
// Cursor against one repo. Skills and rules are served live over MCP,
// every run is recorded in run history, and the only change to the
// product repo is the Fabrica line at the top of its AGENTS.md.
func TestSuccessTest(t *testing.T) {
	w := setupCLI(t)
	w.Home.Write(map[string]string{
		"factories/pr-review.yaml": `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines: [pr-review]
targets: [github.com/acme/app]
params: { slack_channel: "#eng" }
`,
	}).CommitPush("deploy pr-review")

	home := w.Home.Dir
	mustFab(t, 0, home, "check")
	out := mustFab(t, 2, home, "plan")
	if !strings.Contains(out, "+ create pr-review @ github.com/acme/app on claude-code-cloud (handoff)") || !strings.Contains(out, "on cursor-cloud (handoff)") {
		t.Fatalf("plan:\n%s", out)
	}

	handoffs := t.TempDir()
	out = mustFab(t, 0, home, "apply", "--handoff-dir", handoffs)
	files, _ := filepath.Glob(filepath.Join(handoffs, "pr-review", "*.md"))
	if len(files) != 2 {
		t.Fatalf("want 2 handoffs, got %v\n%s", files, out)
	}

	// The remote server, serving the home repo.
	dbPath := filepath.Join(t.TempDir(), "runs.db")
	db, err := runs.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache := gitx.NewCache(t.TempDir())
	srv, err := mcpserver.NewRemote(mcpserver.RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: cache, Loader: blueprint.NewLoader(cache), Runs: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	urlRe := regexp.MustCompile("URL `(https://fabrica\\.acme\\.dev[^`]*)`")
	headerRe := regexp.MustCompile("`Authorization: (Bearer [^`]+)`")
	stubRe := regexp.MustCompile("routine `([^`]+)`, runtime `([^`]+)`, repo `([^`]+)` and stub `([^`]+)`")
	confirmRe := regexp.MustCompile("fabrica confirm (h_[0-9a-f]+)")
	for i, file := range files {
		text, _ := os.ReadFile(file)
		// Follow the handoff: set up the routine with this URL, header and
		// prompt (here: connect an agent the way the runtime would).
		endpoint := strings.Replace(urlRe.FindStringSubmatch(string(text))[1], "https://fabrica.acme.dev", ts.URL, 1)
		m := stubRe.FindStringSubmatch(string(text))
		if m == nil {
			t.Fatalf("no stub prompt in handoff:\n%s", text)
		}
		transport := &mcp.StreamableClientTransport{Endpoint: endpoint}
		if h := headerRe.FindStringSubmatch(string(text)); h != nil {
			transport.HTTPClient = httpClientWithAuth(h[1])
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: m[2] + "-agent", Version: "1"}, nil).Connect(context.Background(), transport, nil)
		if err != nil {
			t.Fatalf("%s: %v", m[2], err)
		}
		call := func(name string, args map[string]any) string {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil || res.IsError {
				t.Fatalf("%s %s: %v %+v", m[2], name, err, res)
			}
			return res.Content[0].(*mcp.TextContent).Text
		}
		var begun struct {
			RunID string `json:"run_id"`
		}
		json.Unmarshal([]byte(call("run_begin", map[string]any{"intent": "review the new pull request", "routine": m[1], "runtime": m[2], "repo": m[3], "stub": m[4]})), &begun)
		routine := call("routine_get", map[string]any{"name": "pr-review"})
		if !strings.Contains(routine, "Write a failing test before changing behavior.") {
			t.Errorf("rules should be served with the routine:\n%s", routine)
		}
		call("skill_load", map[string]any{"name": "pr-review"})
		call("run_report", map[string]any{"run_id": begun.RunID, "summary": "reviewed it and left one comment", "status": "succeeded", "links": []any{"https://github.com/acme/app/pull/1"}})
		cs.Close()

		// Tell Fabrica the handoff is done.
		mustFab(t, 0, home, "confirm", confirmRe.FindStringSubmatch(string(text))[1], "--remote-id", "remote-"+string(rune('a'+i)))
	}
	w.Home.Pull()
	if out := mustFab(t, 0, home, "plan"); !strings.Contains(out, "Everything is in sync") {
		t.Errorf("after confirming both handoffs, the plan should be clean:\n%s", out)
	}

	// Every run is in run history, with the skill it loaded.
	raw, _ := sql.Open("sqlite", dbPath)
	defer raw.Close()
	rows, err := raw.Query("SELECT runtime, last_status, skills_loaded FROM run_overview ORDER BY runtime")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var rt, status, skills string
		rows.Scan(&rt, &status, &skills)
		got = append(got, rt+":"+status+":"+skills)
	}
	if strings.Join(got, " ") != "claude-code-cloud:succeeded:pr-review cursor-cloud:succeeded:pr-review" {
		t.Errorf("run history = %v", got)
	}

	// And the product repo only has the Fabrica line.
	agents, _ := os.ReadFile(filepath.Join(w.App.Dir, "AGENTS.md"))
	if !strings.HasPrefix(string(agents), testkit.FabricaLine+"\n") {
		t.Errorf("AGENTS.md = %q", agents)
	}
	if status := testkit.Git(t, w.App.Dir, "status", "--porcelain"); status != "" {
		t.Errorf("Fabrica must never write into the product repo, but it has changes:\n%s", status)
	}
}

// httpClientWithAuth sends an Authorization header with every request,
// like a runtime configured with a header does.
func httpClientWithAuth(value string) *http.Client {
	return &http.Client{Transport: authTransport(value)}
}

type authTransport string

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", string(a))
	return http.DefaultTransport.RoundTrip(r)
}

func TestDestroyCommand(t *testing.T) {
	w := setupCLI(t)
	w.Home.Write(map[string]string{
		"factories/pr-review.yaml": `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud]
routines: [pr-review]
targets: [github.com/acme/app]
`,
	}).CommitPush("deploy pr-review")
	home, handoffs := w.Home.Dir, t.TempDir()
	mustFab(t, 0, home, "apply", "--handoff-dir", handoffs)
	confirmRe := regexp.MustCompile("fabrica confirm (h_[0-9a-f]+)")
	files, _ := filepath.Glob(filepath.Join(handoffs, "pr-review", "*.md"))
	if len(files) != 1 {
		t.Fatalf("want 1 handoff, got %v", files)
	}
	text, _ := os.ReadFile(files[0])
	mustFab(t, 0, home, "confirm", confirmRe.FindStringSubmatch(string(text))[1], "--remote-id", "trig_1")
	w.Home.Pull()

	// Deleting needs no server secret.
	t.Setenv(token.EnvVar, "")
	out := mustFab(t, 2, home, "plan", "--destroy")
	if !strings.Contains(out, "- delete pr-review @ github.com/acme/app on claude-code-cloud (handoff): the factory is being destroyed") {
		t.Fatalf("plan --destroy:\n%s", out)
	}
	if out := mustFab(t, 0, home, "plan"); !strings.Contains(out, "In sync.") {
		t.Errorf("plan --destroy must not change anything:\n%s", out)
	}

	// Without a terminal to ask on, destroy wants --yes.
	out = mustFab(t, 1, home, "destroy", "--handoff-dir", handoffs)
	if !strings.Contains(out, "Destroy plan: 1 change, 0 errors.") || !strings.Contains(out, "run again with --yes") {
		t.Fatalf("destroy without --yes:\n%s", out)
	}
	out = mustFab(t, 0, home, "destroy", "--yes", "--handoff-dir", handoffs)
	if !strings.Contains(out, "1 delete handoff to follow") || !strings.Contains(out, "released the lock") {
		t.Fatalf("destroy --yes:\n%s", out)
	}
	files, _ = filepath.Glob(filepath.Join(handoffs, "pr-review", "*.md"))
	var deleteHandoff string
	for _, f := range files {
		if data, _ := os.ReadFile(f); strings.HasPrefix(string(data), "# Delete Claude routine") {
			deleteHandoff = string(data)
		}
	}
	if !strings.Contains(deleteHandoff, "trig_1") {
		t.Fatalf("want a delete handoff naming the routine's ID, got %v:\n%s", files, deleteHandoff)
	}
	w.Home.Pull()
	if out := mustFab(t, 0, home, "destroy", "--yes"); !strings.Contains(out, "Nothing to destroy.") {
		t.Errorf("a second destroy has nothing to do:\n%s", out)
	}
	mustFab(t, 0, home, "confirm", confirmRe.FindStringSubmatch(deleteHandoff)[1])
	w.Home.Pull()
	if out := mustFab(t, 0, home, "plan", "--destroy"); !strings.Contains(out, "Nothing to destroy (0 changes, 0 errors).") {
		t.Errorf("after confirming the delete, there's nothing left:\n%s", out)
	}
}

// TestLocalMCPOverStdio runs `fabrica mcp` as a real subprocess, the way
// Claude Code would after `fabrica apply`.
func TestLocalMCPOverStdio(t *testing.T) {
	w := setupCLI(t)
	w.MakePersonal(t)
	w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	})
	userHome := os.Getenv("HOME")
	bin, _ := os.Executable()
	t.Setenv("FABRICA_BIN", bin)
	mustFab(t, 0, w.Home.Dir, "apply")

	// Read the MCP entry apply wrote, and launch exactly that.
	data, err := os.ReadFile(filepath.Join(userHome, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	json.Unmarshal(data, &cfg)
	entry := cfg.MCPServers["fabrica"]
	if entry.Command != bin || strings.Join(entry.Args, " ") != "mcp --runtime claude-code-local --home "+w.Home.Dir {
		t.Fatalf("unexpected MCP entry: %+v", entry)
	}

	ws := filepath.Join(t.TempDir(), "app")
	testkit.Git(t, "", "clone", "--quiet", "https://github.com/acme/app.git", ws)
	cmd := exec.Command(entry.Command, entry.Args...)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "FABRICA_TEST_AS_CLI=1", "FABRICA_TOY_PROVIDER=")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if instr := cs.InitializeResult().Instructions; !strings.Contains(instr, "repo github.com/acme/app, factory my-default") {
		t.Errorf("instructions = %s", instr)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "run_begin", Arguments: map[string]any{"intent": "fix the login bug"}})
	if err != nil || res.IsError {
		t.Fatalf("run_begin: %v %+v", err, res)
	}
	cs.Close()

	// The run landed in the personal home's runs.db.
	db, err := runs.Open(filepath.Join(w.Home.Dir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out)
	if r, err := db.Get(context.Background(), out.RunID); err != nil || r.Repo != "github.com/acme/app" || r.ServedBy != runs.ServedLocal {
		t.Errorf("run = %+v, %v", r, err)
	}
}

func TestVersionAndUsageErrors(t *testing.T) {
	setupCLI(t)
	out := mustFab(t, 0, "", "version")
	if !regexp.MustCompile(`^fabrica \S+( \(.+\))?\n$`).MatchString(out) {
		t.Errorf("version = %q", out)
	}
	for _, flag := range []string{"-v", "--version"} {
		if got := mustFab(t, 0, "", flag); got != out {
			t.Errorf("fabrica %s = %q, want %q like `fabrica version`", flag, got, out)
		}
	}
	if out, code := fab(t, t.TempDir(), "lock"); code != 1 || !strings.Contains(out, "isn't a blueprint") {
		t.Errorf("lock outside a blueprint: %d %s", code, out)
	}
	if out, code := fab(t, "", "mcp"); code != 1 || !strings.Contains(out, "--runtime is required") {
		t.Errorf("mcp without --runtime: %d %s", code, out)
	}
}

// TestAnyCommandCreatesPersonalHome checks that whatever you run, even a
// command that fails, your personal home repo is there afterwards.
func TestAnyCommandCreatesPersonalHome(t *testing.T) {
	setupCLI(t)
	for _, args := range [][]string{{"version"}, {"-v"}, {"--help"}, {"mcp"}} {
		dir := filepath.Join(t.TempDir(), "fabrica-home")
		t.Setenv("FABRICA_HOME", dir)
		out, _ := fab(t, "", args...)
		if !strings.HasPrefix(out, "Created your personal home repo at "+dir+".\n") {
			t.Errorf("fabrica %s should say it created the home repo:\n%s", strings.Join(args, " "), out)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Errorf("fabrica %s didn't create the home repo: %v", strings.Join(args, " "), err)
		}
		if out, _ := fab(t, "", args...); strings.Contains(out, "Created") {
			t.Errorf("fabrica %s should create the home repo only once:\n%s", strings.Join(args, " "), out)
		}
	}

	// When it can't, the command still runs.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FABRICA_HOME", filepath.Join(file, "home"))
	out := mustFab(t, 0, "", "version")
	if !strings.Contains(out, "fabrica: couldn't create your personal home repo at "+filepath.Join(file, "home")) || !strings.Contains(out, "fabrica dev") {
		t.Errorf("want a warning, then the version:\n%s", out)
	}
}
