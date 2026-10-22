package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// client wraps an MCP client session with test helpers.
type client struct {
	t  *testing.T
	cs *mcp.ClientSession
}

func (c *client) call(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// ok calls a tool and fails the test if it returns an error.
func (c *client) ok(name string, args map[string]any) string {
	c.t.Helper()
	text, isErr := c.call(name, args)
	if isErr {
		c.t.Fatalf("%s failed: %s", name, text)
	}
	return text
}

// fails calls a tool and fails the test unless it returns an error
// containing want.
func (c *client) fails(name string, args map[string]any, want string) {
	c.t.Helper()
	text, isErr := c.call(name, args)
	if !isErr || !strings.Contains(text, want) {
		c.t.Fatalf("%s should fail with %q, got (error=%v): %s", name, want, isErr, text)
	}
}

func (c *client) runID(text string) string {
	c.t.Helper()
	var out struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.RunID == "" {
		c.t.Fatalf("no run_id in %s", text)
	}
	return out.RunID
}

func connect(t *testing.T, srv *mcp.Server, roots ...string) *client {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	for _, r := range roots {
		//lint:ignore SA1019 the local server still reads roots during their deprecation window
		cl.AddRoots(&mcp.Root{URI: "file://" + r})
	}
	cs, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &client{t: t, cs: cs}
}

type localWorld struct {
	w      *testkit.World
	db     *runs.DB
	opts   LocalOptions
	ws     string
	dbPath string
}

func newLocalWorld(t *testing.T) *localWorld {
	t.Helper()
	w := testkit.NewWorld(t)
	w.MakePersonal(t)
	w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\nroutines: [review-before-push]\n",
	})
	ws := filepath.Join(t.TempDir(), "app")
	testkit.Git(t, "", "clone", "--quiet", "https://github.com/acme/app.git", ws)
	cache := gitx.NewCache(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "runs.db")
	db, err := runs.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &localWorld{w: w, db: db, ws: ws, dbPath: dbPath, opts: LocalOptions{
		Runtime: "claude-code-local", HomeDir: w.Home.Dir, Workspace: ws,
		Git: cache, Loader: blueprint.NewLoader(cache), Runs: db, SkillCache: t.TempDir(),
	}}
}

func (lw *localWorld) connect(t *testing.T) (*client, string) {
	core, instructions := NewLocal(context.Background(), lw.opts)
	return connect(t, core.NewServer(instructions)), instructions
}

// localStub computes the fingerprint a local hook for the routine carries.
func localStub(t *testing.T, opts LocalOptions, routine string) string {
	t.Helper()
	remote := opts.Git.Remote("https://github.com/acme/blueprint.git", gitx.Full)
	resolved, err := remote.Resolve(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	f, diags := opts.Loader.LoadGit(context.Background(), "github.com/acme/blueprint", resolved.Commit, "")
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	p, _ := provider.Builtin(opts.Runtime)
	return stub.Evaluate(f, f.Routines[routine], stub.Target{Runtime: p}).Stubs[0].Shape.Fingerprint()
}

func TestLocalSession(t *testing.T) {
	lw := newLocalWorld(t)
	c, instructions := lw.connect(t)
	if !strings.Contains(instructions, "Call run_begin first") || !strings.Contains(instructions, "github.com/acme/app") {
		t.Errorf("instructions = %s", instructions)
	}
	if !strings.Contains(instructions, "say so in run_report's friction") {
		t.Errorf("instructions should ask for friction notes: %s", instructions)
	}
	tools, err := c.cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 9 {
		t.Fatalf("want the 9 Fabrica tools, got %d (%v)", len(tools.Tools), err)
	}

	// An interactive session.
	runID := c.runID(c.ok("run_begin", map[string]any{"intent": "add a dark mode toggle to settings"}))
	rules := c.ok("rules_get", nil)
	for _, want := range []string{"Product knowledge only", "tests-first (Test-first workflow)", "Write a failing test", "typescript: TypeScript style. Applies when you work on files matching *.ts."} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules_get is missing %q:\n%s", want, rules)
		}
	}
	if r := c.ok("rule_load", map[string]any{"name": "typescript"}); !strings.Contains(r, "Use strict mode.") {
		t.Errorf("rule_load = %s", r)
	}
	list := c.ok("skills_list", nil)
	if !strings.Contains(list, `"name":"pdf"`) || strings.Contains(list, `"available":false`) {
		t.Errorf("locally every skill is available: %s", list)
	}

	// skill_load unpacks the whole folder, keeping executable bits, outside
	// the workspace.
	loaded := c.ok("skill_load", map[string]any{"name": "pdf"})
	folder := ""
	for _, line := range strings.Split(loaded, "\n") {
		if strings.HasPrefix(line, "    /") {
			folder = strings.TrimSpace(line)
		}
	}
	if !strings.HasPrefix(folder, lw.opts.SkillCache) || filepath.Base(folder) != "pdf" {
		t.Fatalf("skill folder = %q\n%s", folder, loaded)
	}
	info, err := os.Stat(filepath.Join(folder, "scripts", "extract.sh"))
	if err != nil || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o222 != 0 {
		t.Errorf("unpacked script should be executable and read-only: %v %v", info.Mode(), err)
	}
	if f := c.ok("skill_file", map[string]any{"name": "pr-review", "path": "checklist.md"}); !strings.Contains(f, "Tests?") {
		t.Errorf("skill_file = %s", f)
	}
	c.fails("skill_file", map[string]any{"name": "pr-review", "path": "../../etc/passwd"}, "has no file")

	c.ok("run_report", map[string]any{"run_id": runID, "summary": "added the toggle and its test", "status": "succeeded", "links": []any{"https://github.com/acme/app/pull/7"}})
	c.fails("run_report", map[string]any{"run_id": runID, "summary": strings.Repeat("too many words ", 8)}, "limit is 20")
	c.fails("run_begin", map[string]any{"intent": ""}, "is empty")

	// Run history knows what happened.
	run, err := lw.db.Get(context.Background(), runID)
	if err != nil || run.Repo != "github.com/acme/app" || run.Runtime != "claude-code-local" || run.Factory != "my-default" || len(run.BlueprintSHA) != 40 {
		t.Errorf("run = %+v, %v", run, err)
	}
	skills, _ := lw.db.SkillsLoaded(context.Background(), runID)
	if strings.Join(skills, ",") != "pdf" {
		t.Errorf("skills loaded = %v", skills)
	}

	// A local routine run, from a hook, with the right stub.
	fp := localStub(t, lw.opts, "review-before-push")
	c.ok("run_begin", map[string]any{"intent": "review my diff before pushing", "routine": "review-before-push", "stub": fp})
	routine := c.ok("routine_get", map[string]any{"name": "review-before-push"})
	if !strings.Contains(routine, "Review your own diff") || !strings.Contains(routine, "## Working rules") {
		t.Errorf("routine_get = %s", routine)
	}
	// The wrong stub is refused, and then routine_get won't serve it either.
	c.fails("run_begin", map[string]any{"intent": "review again", "routine": "review-before-push", "stub": "000000000000"}, "this stub is outdated")
	c.fails("routine_get", map[string]any{"name": "review-before-push"}, "refused")
}

func TestLocalRefusesWithoutTheFabricaLine(t *testing.T) {
	lw := newLocalWorld(t)
	os.WriteFile(filepath.Join(lw.ws, "AGENTS.md"), []byte("# App\n"), 0o644)
	c, instructions := lw.connect(t)
	if !strings.Contains(instructions, "can't serve this session") || !strings.Contains(instructions, "Before starting, load") {
		t.Errorf("instructions should explain the refusal and show the line:\n%s", instructions)
	}
	c.fails("rules_get", nil, "AGENTS.md doesn't start with the Fabrica line")
	c.fails("run_begin", map[string]any{"intent": "fix a bug"}, "recorded as failed run")
}

func TestLocalRefusesSkillClashes(t *testing.T) {
	lw := newLocalWorld(t)
	testkit.WriteFiles(t, lw.ws, map[string]string{".claude/skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: x\n---\n"})
	c, _ := lw.connect(t)
	c.ok("rules_get", nil) // rules still load
	c.fails("skill_load", map[string]any{"name": "pdf"}, "same names as blueprint skills (pr-review)")
	c.fails("run_begin", map[string]any{"intent": "fix a bug"}, "same names as blueprint skills")
}

func TestLocalUsesRootsFromTheClient(t *testing.T) {
	lw := newLocalWorld(t)
	lw.opts.Workspace = ""
	t.Chdir(t.TempDir()) // not a repo: only the roots say where the workspace is
	core, instructions := NewLocal(context.Background(), lw.opts)
	if strings.Contains(instructions, "can't serve this session") {
		t.Errorf("without a workspace yet, the instructions shouldn't refuse:\n%s", instructions)
	}
	// Roots are deprecated from protocol 2026-07-28 on, so speak the
	// version today's editors use.
	core.ProtocolVersions = []string{"2025-11-25"}
	c := connect(t, core.NewServer(instructions), lw.ws)
	c.ok("run_begin", map[string]any{"intent": "look around"})
}

func TestLocalStubNoLongerDeployed(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	// The routine is removed from the factory after the hook was
	// installed.
	lw.w.Home.Write(map[string]string{"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n"})
	c.fails("run_begin", map[string]any{"intent": "review", "routine": "review-before-push", "stub": fp}, "no longer deployed: routine review-before-push isn't in factory my-default")
}

func TestLocalStubNoLongerRunsOnRuntime(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	// The routine is narrowed to another runtime after the hook was
	// installed.
	lw.w.Home.Write(map[string]string{"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\nroutines: [{ routine: review-before-push, runtimes: [cursor-local] }]\n"})
	c.fails("run_begin", map[string]any{"intent": "review", "routine": "review-before-push", "stub": fp}, "no longer deployed: routine review-before-push doesn't run on runtime claude-code-local in factory my-default")
}

type remoteWorld struct {
	w      *testkit.World
	srv    *Remote
	ts     *httptest.Server
	db     *runs.DB
	dbPath string
	loader *blueprint.Loader
	cache  *gitx.Cache
}

func newRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := testkit.NewWorld(t)
	w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")
	cache := gitx.NewCache(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "runs.db")
	db, err := runs.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loader := blueprint.NewLoader(cache)
	srv, err := NewRemote(RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: cache, Loader: loader, Runs: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &remoteWorld{w: w, srv: srv, ts: ts, db: db, dbPath: dbPath, loader: loader, cache: cache}
}

func (rw *remoteWorld) connect(t *testing.T, path string) *client {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "cloud-agent", Version: "1"}, nil)
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: rw.ts.URL + path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &client{t: t, cs: cs}
}

// remoteStub computes the fingerprint of the claude-code-cloud stub, the way apply does.
func (rw *remoteWorld) remoteStub(t *testing.T) string {
	t.Helper()
	resolved, _ := rw.cache.Remote("https://github.com/acme/blueprint.git", gitx.Full).Resolve(context.Background(), "stable")
	f, _ := rw.loader.LoadGit(context.Background(), "github.com/acme/blueprint", resolved.Commit, "")
	p, _ := provider.Builtin("claude-code-cloud")
	return stub.Evaluate(f, f.Routines["pr-review"], stub.Target{Runtime: p, Endpoint: "https://fabrica.acme.dev/pr-review"}).Stubs[0].Shape.Fingerprint()
}

func TestRemoteAuth(t *testing.T) {
	rw := newRemoteWorld(t)
	tok := token.For(testkit.Secret, "pr-review")
	for _, c := range []struct {
		path, header string
		want         int
	}{
		{"/healthz", "", 200},
		{"/nope", "", 404},
		{"/pr-review", "", 401},
		{"/pr-review", "Bearer " + token.For("x-wrong-secret-x-wrong-secret-x", "pr-review"), 401},
		{"/pr-review/t/" + token.For(testkit.Secret, "other"), "", 401},
	} {
		req, _ := http.NewRequest("POST", rw.ts.URL+c.path, strings.NewReader("{}"))
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s with %q: status %d, want %d", c.path, c.header, res.StatusCode, c.want)
		}
	}
	// Both token placements work.
	rw.connect(t, "/pr-review/t/"+tok).ok("rules_get", nil)
	cl := mcp.NewClient(&mcp.Implementation{Name: "x", Version: "1"}, nil)
	httpClient := &http.Client{Transport: headerTransport{"Bearer " + tok}}
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: rw.ts.URL + "/pr-review", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs.Close()
}

// TestRemoteOffersToolsAsJSON is the cloud-client handshake: initialize with
// Accept: */* (what Claude's broker sends), then tools/list on the session.
// A text/event-stream initialize is how those clients end up with Fabrica's
// instructions and no tools.
func TestRemoteOffersToolsAsJSON(t *testing.T) {
	rw := newRemoteWorld(t)
	endpoint := rw.ts.URL + "/pr-review/t/" + token.For(testkit.Secret, "pr-review")

	initReq, err := http.NewRequest("POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Anthropic","version":"1.0.0"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "*/*")
	initRes, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatal(err)
	}
	defer initRes.Body.Close()
	if initRes.StatusCode != 200 {
		t.Fatalf("initialize status %d", initRes.StatusCode)
	}
	if ct := initRes.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("initialize Content-Type = %q, want application/json", ct)
	}
	sid := initRes.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize should set Mcp-Session-Id")
	}
	var init struct {
		Result struct {
			Instructions string `json:"instructions"`
			Capabilities struct {
				Tools *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.NewDecoder(initRes.Body).Decode(&init); err != nil {
		t.Fatalf("initialize body: %v", err)
	}
	if !strings.Contains(init.Result.Instructions, "Call run_begin first") {
		t.Errorf("instructions = %s", init.Result.Instructions)
	}
	if init.Result.Capabilities.Tools == nil {
		t.Fatal("initialize should advertise tools")
	}
	if init.Result.Capabilities.Tools.ListChanged {
		t.Error("tools.listChanged should be off: cloud clients wait for a GET SSE stream when it's on")
	}

	notify, err := http.NewRequest("POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	notify.Header.Set("Content-Type", "application/json")
	notify.Header.Set("Accept", "application/json, text/event-stream")
	notify.Header.Set("Mcp-Session-Id", sid)
	notifyRes, err := http.DefaultClient.Do(notify)
	if err != nil {
		t.Fatal(err)
	}
	notifyRes.Body.Close()
	if notifyRes.StatusCode != http.StatusAccepted {
		t.Fatalf("initialized status %d, want 202", notifyRes.StatusCode)
	}

	listReq, err := http.NewRequest("POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	listReq.Header.Set("Content-Type", "application/json")
	listReq.Header.Set("Accept", "application/json, text/event-stream")
	listReq.Header.Set("Mcp-Session-Id", sid)
	listRes, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listRes.Body.Close()
	if listRes.StatusCode != 200 {
		t.Fatalf("tools/list status %d", listRes.StatusCode)
	}
	if ct := listRes.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("tools/list Content-Type = %q, want application/json", ct)
	}
	var listed struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&listed); err != nil {
		t.Fatalf("tools/list body: %v", err)
	}
	if listed.Error != nil {
		t.Fatalf("tools/list error: %s", listed.Error.Message)
	}
	var names []string
	for _, tool := range listed.Result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"memory_save", "routine_get", "rule_load", "rules_get", "run_begin", "run_report", "skill_file", "skill_load", "skills_list"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

type headerTransport struct{ auth string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", h.auth)
	return http.DefaultTransport.RoundTrip(r)
}

func TestRemoteRoutineRun(t *testing.T) {
	rw := newRemoteWorld(t)
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	fp := rw.remoteStub(t)

	// Routines only run from stubs, with every value filled in.
	c.fails("routine_get", map[string]any{"name": "pr-review"}, "only run from stubs")
	c.fails("run_begin", map[string]any{"intent": "review PR 7", "routine": "pr-review"}, "needs routine, runtime, repo and stub")

	args := map[string]any{"intent": "review PR 7 on the app", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/api", "stub": fp}
	runID := c.runID(c.ok("run_begin", args))
	routine := c.ok("routine_get", map[string]any{"name": "pr-review"})
	for _, want := range []string{"for repo github.com/acme/api", "slack_channel = #api", "slack.notify (channel: #api): do it with the tools of the slack connector", "then tell #api", "Auto-fix is off for this routine's pull requests"} {
		if !strings.Contains(routine, want) {
			t.Errorf("routine_get is missing %q:\n%s", want, routine)
		}
	}

	// Remote runs can only use text-only skills.
	var list skillsListOut
	if err := json.Unmarshal([]byte(c.ok("skills_list", nil)), &list); err != nil {
		t.Fatal(err)
	}
	for _, sk := range list.Skills {
		if want := sk.Name != "pdf"; sk.Available != want {
			t.Errorf("remotely, skill %s should have available=%v: %+v", sk.Name, want, sk)
		}
	}
	c.fails("skill_load", map[string]any{"name": "pdf"}, "cloud runtimes can't have that")
	if s := c.ok("skill_load", map[string]any{"name": "pr-review"}); !strings.Contains(s, "read any of the files below with skill_file") && !strings.Contains(s, "Read any of the files below with skill_file") {
		t.Errorf("remote skill_load = %s", s)
	}
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "left a review comment", "status": "succeeded"})
	if skills, _ := rw.db.SkillsLoaded(context.Background(), runID); strings.Join(skills, ",") != "pr-review" {
		t.Errorf("skills loaded = %v", skills)
	}
	if got := loadsOf(t, rw.dbPath, runID); got != "routine:pr-review,skill:pr-review" {
		t.Errorf("loads = %s", got)
	}

	// The blueprint changes the routine's trigger. Once the server has
	// fetched it, the old stub is refused, even in this old session.
	rw.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "opened", "synchronize", 1)})
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	fresh.fails("run_begin", args, "this stub is outdated")
	if r, _ := rw.db.Get(context.Background(), runID); r == nil {
		t.Error("the earlier run should still be in history")
	}

	// A repo removed from the factory stops its stub too.
	rw.w.Home.Pull()
	rw.w.Home.Write(map[string]string{"factories/pr-review.yaml": strings.Replace(testkit.PRReviewFactory, "  - repo: github.com/acme/api\n    params: { slack_channel: \"#api\" }\n", "", 1)}).CommitPush("drop api")
	rw.srv.Refresh(context.Background())
	c.fails("run_begin", args, "repo github.com/acme/api isn't in factory pr-review anymore")
}

func TestRemoteDeletedFactoryStopsServing(t *testing.T) {
	rw := newRemoteWorld(t)
	testkit.Git(t, rw.w.Home.Dir, "rm", "--quiet", "factories/pr-review.yaml")
	rw.w.Home.CommitPush("delete")
	rw.srv.Refresh(context.Background())
	res, err := http.Post(rw.ts.URL+"/pr-review/t/"+token.For(testkit.Secret, "pr-review"), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 once the factory is deleted", res.StatusCode)
	}
}

func TestRemoteRoutineMemory(t *testing.T) {
	rw := newRemoteWorld(t)
	rw.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "skills: [pr-review]\n", "skills: [pr-review]\nmemory: true\n", 1)})
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := "/pr-review/t/" + token.For(testkit.Secret, "pr-review")
	args := map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/app", "stub": rw.remoteStub(t)}

	// Claude routines have no memory of their own, so Fabrica keeps it.
	c := rw.connect(t, path)
	c.fails("memory_save", map[string]any{"run_id": "run_nope", "content": "x"}, "isn't a routine run begun in this session")
	runID := c.runID(c.ok("run_begin", args))
	if r := c.ok("routine_get", map[string]any{"name": "pr-review"}); !strings.Contains(r, "## Memory") || !strings.Contains(r, "Nothing is saved yet") || !strings.Contains(r, "call memory_save") {
		t.Errorf("routine_get should explain the routine's memory:\n%s", r)
	}
	c.fails("memory_save", map[string]any{"run_id": runID, "content": strings.Repeat("x", runs.MaxMemoryBytes+1)}, "the limit is")
	c.ok("memory_save", map[string]any{"run_id": runID, "content": "- The app's tests live in `spec/`."})

	// The next run on the same repo sees it; another repo doesn't.
	next := rw.connect(t, path)
	next.fails("memory_save", map[string]any{"run_id": runID, "content": "overwritten"}, "isn't a routine run begun in this session")
	next.ok("run_begin", args)
	if r := next.ok("routine_get", map[string]any{"name": "pr-review"}); !strings.Contains(r, "-----BEGIN MEMORY-----\n- The app's tests live in `spec/`.\n-----END MEMORY-----") || !strings.Contains(r, "saved on") {
		t.Errorf("the next run should get the memory:\n%s", r)
	}
	other := rw.connect(t, path)
	apiArgs := map[string]any{}
	for k, v := range args {
		apiArgs[k] = v
	}
	apiArgs["repo"] = "github.com/acme/api"
	other.ok("run_begin", apiArgs)
	if r := other.ok("routine_get", map[string]any{"name": "pr-review"}); strings.Contains(r, "spec/") {
		t.Errorf("memories are per repo:\n%s", r)
	}
}

func TestRemoteRoutineAutofix(t *testing.T) {
	rw := newRemoteWorld(t)
	rw.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "  - github.pr_comment\n", "  - github.pr_comment\n  - github.pr_autofix\n", 1)})
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	c.ok("run_begin", map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/app", "stub": rw.remoteStub(t)})
	if r := c.ok("routine_get", map[string]any{"name": "pr-review"}); !strings.Contains(r, "- github.pr_autofix: the runtime does it: once you've opened a pull request") || !strings.Contains(r, "finish the run without waiting for CI") || strings.Contains(r, "Auto-fix is off") {
		t.Errorf("routine_get should say the runtime auto-fixes the routine's pull requests:\n%s", r)
	}
}

func TestLocalMemoryOnlyForRoutinesThatAskForIt(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	runID := c.runID(c.ok("run_begin", map[string]any{"intent": "review my diff", "routine": "review-before-push", "stub": fp}))
	if r := c.ok("routine_get", map[string]any{"name": "review-before-push"}); strings.Contains(r, "## Memory") {
		t.Errorf("a routine without memory: true gets no memory section here:\n%s", r)
	}
	c.fails("memory_save", map[string]any{"run_id": runID, "content": "x"}, "doesn't use memory")
}

func TestRemoteInitRun(t *testing.T) {
	rw := newRemoteWorld(t)
	rw.w.UpdateBlueprint(map[string]string{"routines/onboard.md": "---\ntrigger: { on: init }\nruntimes: [cursor-cloud, claude-code-local]\nskills: [pr-review]\n---\nCheck the repos are ready for this blueprint.\n"})
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolved, _ := rw.cache.Remote("https://github.com/acme/blueprint.git", gitx.Full).Resolve(context.Background(), "stable")
	f, _ := rw.loader.LoadGit(context.Background(), "github.com/acme/blueprint", resolved.Commit, "")
	p, _ := provider.Builtin("claude-code-local")
	fp := stub.Evaluate(f, f.Routines["onboard"], stub.Target{Runtime: p, Endpoint: "https://fabrica.acme.dev/pr-review"}).Stubs[0].Shape.Fingerprint()

	// A local runtime runs the init session of a cloud factory, from the
	// factory's own server, like apply starts it.
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	args := map[string]any{"intent": "get the factory's repos ready", "routine": "onboard", "runtime": "claude-code-local", "repo": "github.com/acme/app", "stub": fp}
	runID := c.runID(c.ok("run_begin", args))
	routine := c.ok("routine_get", map[string]any{"name": "onboard"})
	for _, want := range []string{"# Routine onboard", "for repo github.com/acme/app", "Trigger: init (once, when the factory is created)", "Check the repos are ready"} {
		if !strings.Contains(routine, want) {
			t.Errorf("routine_get is missing %q:\n%s", want, routine)
		}
	}
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "opened an issue listing what's missing", "status": "succeeded"})

	// Only on the init's runtimes, with the right shape.
	other := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	other.fails("run_begin", map[string]any{"intent": "get the repos ready", "routine": "onboard", "runtime": "codex", "repo": "github.com/acme/app", "stub": fp}, "doesn't run on runtime codex anymore")
	other.fails("run_begin", map[string]any{"intent": "get the repos ready", "routine": "onboard", "runtime": "claude-code-local", "repo": "github.com/acme/app", "stub": "000000000000"}, "this stub is outdated")

	// A factory that turns its init off refuses it.
	rw.w.Home.Pull()
	rw.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory + "init: false\n"}).CommitPush("no init")
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review")).fails("run_begin", args, "factory pr-review says init: false")
}

// rawQuery runs a query on the run-history file, as any SQLite tool would,
// and returns each row with its columns joined by colons.
func rawQuery(t *testing.T, dbPath, query string, args ...any) []string {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = v.String
		}
		out = append(out, strings.Join(parts, ":"))
	}
	return out
}

// loadsOf lists a run's loads in order, like kind:name,kind:name.
func loadsOf(t *testing.T, dbPath, runID string) string {
	t.Helper()
	return strings.Join(rawQuery(t, dbPath, `SELECT kind, name FROM loads WHERE run_id = ? ORDER BY id`, runID), ",")
}

func countRows(t *testing.T, dbPath, table string) string {
	t.Helper()
	return rawQuery(t, dbPath, "SELECT COUNT(*) FROM "+table)[0]
}

func toolNames(t *testing.T, c *client) []string {
	t.Helper()
	tools, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// TestOnlyTheseTools is a guard for the rule that agents write run history
// and only people read it (manual/run-history.md). None of these tools
// returns run history. Adding a tool means changing this list, and saying in
// review why the new tool can't leak runs to agents.
func TestOnlyTheseTools(t *testing.T) {
	want := []string{"memory_save", "routine_get", "rule_load", "rules_get", "run_begin", "run_report", "skill_file", "skill_load", "skills_list"}
	lw := newLocalWorld(t)
	local, _ := lw.connect(t)
	if got := toolNames(t, local); !slices.Equal(got, want) {
		t.Errorf("the local server has tools %v, want exactly %v", got, want)
	}
	rw := newRemoteWorld(t)
	remote := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	if got := toolNames(t, remote); !slices.Equal(got, want) {
		t.Errorf("the remote server has tools %v, want exactly %v", got, want)
	}
}

func TestLoadsAreRecorded(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	// Before run_begin there's no run to link to.
	c.ok("rules_get", nil)
	c.ok("skill_load", map[string]any{"name": "pdf"})
	if n := countRows(t, lw.dbPath, "loads"); n != "0" {
		t.Fatalf("loads before run_begin = %s", n)
	}

	runID := c.runID(c.ok("run_begin", map[string]any{"intent": "add a dark mode toggle"}))
	c.ok("rules_get", nil)
	c.ok("rule_load", map[string]any{"name": "typescript"})
	c.ok("skill_load", map[string]any{"name": "pdf"})
	// Loads that fail aren't recorded.
	c.fails("rule_load", map[string]any{"name": "nope"}, "no rule called")
	c.fails("skill_load", map[string]any{"name": "nope"}, "no skill called")
	c.fails("routine_get", map[string]any{"name": "nope"}, "no routine called")
	// rules_get counts every always-on rule it hands over, and not the others.
	if got, want := loadsOf(t, lw.dbPath, runID), "rule:product-knowledge-only,rule:tests-first,rule:typescript,skill:pdf"; got != want {
		t.Errorf("loads = %s, want %s", got, want)
	}

	// A routine run: routine_get is a routine load.
	routineRun := c.runID(c.ok("run_begin", map[string]any{"intent": "review my diff", "routine": "review-before-push", "stub": localStub(t, lw.opts, "review-before-push")}))
	c.ok("routine_get", map[string]any{"name": "review-before-push"})
	if got := loadsOf(t, lw.dbPath, routineRun); got != "routine:review-before-push" {
		t.Errorf("loads = %s", got)
	}
	// A refused routine isn't served, so it isn't loaded.
	c.fails("run_begin", map[string]any{"intent": "review again", "routine": "review-before-push", "stub": "000000000000"}, "this stub is outdated")
	c.fails("routine_get", map[string]any{"name": "review-before-push"}, "refused")
	if got := countRows(t, lw.dbPath, "loads"); got != "5" {
		t.Errorf("%s loads in all, want 5", got)
	}
}

func TestRunBeginSession(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	ctx := context.Background()

	// The pointer is stored, and the return value is the same as ever.
	text := c.ok("run_begin", map[string]any{"intent": "fix a bug", "session": "  /home/me/.claude/projects/app/3f2a.jsonl \n"})
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil || len(out) != 2 || out["run_id"] == nil || out["next"] == nil {
		t.Errorf("run_begin returns run_id and next, and nothing else: %s", text)
	}
	if run, _ := lw.db.Get(ctx, c.runID(text)); run.Session != "/home/me/.claude/projects/app/3f2a.jsonl" {
		t.Errorf("session = %q", run.Session)
	}
	// It's optional.
	if run, _ := lw.db.Get(ctx, c.runID(c.ok("run_begin", map[string]any{"intent": "fix another bug"}))); run.Session != "" {
		t.Errorf("session = %q", run.Session)
	}

	// A bad one is an error, and the run isn't recorded, so the agent tries again.
	before := countRows(t, lw.dbPath, "runs")
	c.fails("run_begin", map[string]any{"intent": "fix a bug", "session": "line one\nline two"}, "one line")
	c.fails("run_begin", map[string]any{"intent": "fix a bug", "session": "tab\there"}, "control characters")
	c.fails("run_begin", map[string]any{"intent": "fix a bug", "session": strings.Repeat("x", runs.MaxSessionBytes+1)}, "limit is 512")
	if after := countRows(t, lw.dbPath, "runs"); after != before {
		t.Errorf("runs went from %s to %s; a bad session shouldn't record a run", before, after)
	}

	// A refused run keeps its pointer, so a retro can find its transcript.
	refused := c.callText("run_begin", map[string]any{"intent": "review again", "routine": "review-before-push", "stub": "000000000000", "session": "copilot-local:3f2a"})
	id := regexp.MustCompile(`failed run (run_\w+);`).FindStringSubmatch(refused)
	if id == nil {
		t.Fatalf("no failed run in %s", refused)
	}
	if run, _ := lw.db.Get(ctx, id[1]); run.Session != "copilot-local:3f2a" {
		t.Errorf("a refused run's session = %q", run.Session)
	}
}

// callText is the text of a tool's answer, error or not.
func (c *client) callText(name string, args map[string]any) string {
	c.t.Helper()
	text, _ := c.call(name, args)
	return text
}

func TestRunReportFriction(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	ctx := context.Background()
	runID := c.runID(c.ok("run_begin", map[string]any{"intent": "fix a bug"}))

	report := func(friction any) map[string]any {
		return map[string]any{"run_id": runID, "summary": "kept going", "friction": friction}
	}
	note := func(kind, text string) map[string]any { return map[string]any{"kind": kind, "note": text} }

	text := c.ok("run_report", report([]any{note("navigation", "couldn't find   the retry config"), note("checks", "no lint for imports")}))
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil || len(out) != 3 || out["recorded"] != true || out["run_id"] != runID || out["status"] != "progress" {
		t.Errorf("run_report returns recorded, run_id and status, and nothing else: %s", text)
	}
	if got := strings.Join(rawQuery(t, lw.dbPath, `SELECT kind, note FROM friction WHERE run_id = ? ORDER BY id`, runID), "|"); got != "navigation:couldn't find the retry config|checks:no lint for imports" {
		t.Errorf("friction = %s", got)
	}
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "no friction this time"})

	// Anything wrong with the friction is an error, and nothing is recorded.
	six := []any{}
	for range 6 {
		six = append(six, note("other", "slow"))
	}
	for _, bad := range []struct {
		friction any
		want     string
	}{
		{[]any{note("speed", "it was slow")}, `"speed" isn't one of navigation, checks, standards, tools, info or other`},
		{[]any{note("tools", "")}, "is empty"},
		{[]any{note("tools", strings.Repeat("slow ", 21))}, "has 21 words"},
		{six, "at most 5"},
	} {
		c.fails("run_report", report(bad.friction), bad.want)
	}
	cps, _ := lw.db.Checkpoints(ctx, runID)
	if len(cps) != 2 {
		t.Errorf("%d checkpoints, want the 2 good ones", len(cps))
	}
	if n := countRows(t, lw.dbPath, "friction"); n != "2" {
		t.Errorf("%s friction notes, want 2", n)
	}
}

func TestRunReportOnlyForRunsOfThisFactoryAndRepo(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	seed := func(db *runs.DB, factory, repo string) string {
		id := runs.NewID(now)
		if err := db.Begin(ctx, runs.Run{ID: id, StartedAt: now, Intent: "someone else's run", Factory: factory, Repo: repo, ServedBy: runs.ServedLocal}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	report := func(id string) map[string]any {
		return map[string]any{"run_id": id, "summary": "sneaking a checkpoint in", "status": "succeeded"}
	}
	// Whatever is wrong, the agent hears the same thing, so it can't tell a run
	// that doesn't exist from one in another factory or repo.
	refused := func(id string) string {
		return "run " + id + " wasn't begun for this factory and repo; use the run_id run_begin gave you"
	}

	t.Run("local", func(t *testing.T) {
		lw := newLocalWorld(t)
		c, _ := lw.connect(t)
		ids := []string{
			seed(lw.db, "other-factory", "github.com/acme/app"),
			seed(lw.db, "my-default", "github.com/acme/api"),
			seed(lw.db, "", ""),
			"run_missing",
		}
		for _, id := range ids {
			if got := c.callText("run_report", report(id)); got != refused(id) {
				t.Errorf("run_report(%s) = %q, want %q", id, got, refused(id))
			}
			if cps, _ := lw.db.Checkpoints(ctx, id); len(cps) != 0 {
				t.Errorf("run %s got a checkpoint", id)
			}
		}

		// Connections drop and come back: another session of the same
		// factory and repo can keep reporting on the run.
		runID := c.runID(c.ok("run_begin", map[string]any{"intent": "fix a bug"}))
		again, _ := lw.connect(t)
		again.ok("run_report", report(runID))
		if cps, _ := lw.db.Checkpoints(ctx, runID); len(cps) != 1 {
			t.Errorf("%d checkpoints, want 1", len(cps))
		}
		// ...but it can't take over the run's memory.
		again.fails("memory_save", map[string]any{"run_id": runID, "content": "x"}, "isn't a routine run begun in this session")
	})

	t.Run("remote", func(t *testing.T) {
		rw := newRemoteWorld(t)
		c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
		for _, id := range []string{seed(rw.db, "other-factory", "github.com/acme/app"), seed(rw.db, "", ""), "run_missing"} {
			if got := c.callText("run_report", report(id)); got != refused(id) {
				t.Errorf("run_report(%s) = %q, want %q", id, got, refused(id))
			}
			if cps, _ := rw.db.Checkpoints(ctx, id); len(cps) != 0 {
				t.Errorf("run %s got a checkpoint", id)
			}
		}
		// Remotely the repo comes from each run's stub, so it's the factory
		// that counts.
		sameFactory := seed(rw.db, "pr-review", "github.com/acme/api")
		c.ok("run_report", report(sameFactory))
		other := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
		other.ok("run_report", report(c.runID(c.ok("run_begin", map[string]any{"intent": "look around"}))))
	})
}

func TestRemoteRoutineWithAStubPerTrigger(t *testing.T) {
	rw := newRemoteWorld(t)
	rw.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine,
		"trigger: { on: github.pull_request.opened }", "triggers:\n  - { on: github.pull_request.opened }\n  - { on: github.pull_request.synchronize }", 1)})
	if err := rw.srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	resolved, _ := rw.cache.Remote("https://github.com/acme/blueprint.git", gitx.Full).Resolve(context.Background(), "stable")
	f, _ := rw.loader.LoadGit(context.Background(), "github.com/acme/blueprint", resolved.Commit, "")
	p, _ := provider.Builtin("claude-code-cloud")
	ev := stub.Evaluate(f, f.Routines["pr-review"], stub.Target{Runtime: p, Endpoint: "https://fabrica.acme.dev/pr-review"})
	if len(ev.Stubs) != 2 {
		t.Fatalf("claude-code-cloud should get a stub per trigger, got %d", len(ev.Stubs))
	}

	// Each of the routine's stubs runs it.
	for _, st := range ev.Stubs {
		c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
		c.ok("run_begin", map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/api", "stub": st.Shape.Fingerprint()})
		if routine := c.ok("routine_get", map[string]any{"name": "pr-review"}); !strings.Contains(routine, "- Triggers: github.pull_request.opened, github.pull_request.synchronize (any of them)") {
			t.Errorf("routine_get should list the triggers:\n%s", routine)
		}
	}
	// A stub with both triggers isn't one of them.
	both := stub.Evaluate(f, f.Routines["pr-review"], stub.Target{Runtime: mustBuiltin(t, "cursor-cloud"), Endpoint: "https://fabrica.acme.dev/pr-review"})
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	c.fails("run_begin", map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/api", "stub": both.Stubs[0].Shape.Fingerprint()}, "this stub is outdated")
}

func mustBuiltin(t *testing.T, name string) *provider.Provider {
	t.Helper()
	p, ok := provider.Builtin(name)
	if !ok {
		t.Fatalf("no built-in %s", name)
	}
	return p
}
