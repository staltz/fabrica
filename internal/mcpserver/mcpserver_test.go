package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	return stub.Evaluate(f, f.Routines[routine], stub.Target{Runtime: p}).Shape.Fingerprint()
}

func TestLocalSession(t *testing.T) {
	lw := newLocalWorld(t)
	c, instructions := lw.connect(t)
	if !strings.Contains(instructions, "Call run_begin first") || !strings.Contains(instructions, "github.com/acme/app") {
		t.Errorf("instructions = %s", instructions)
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
	loader *blueprint.Loader
	cache  *gitx.Cache
}

func newRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := testkit.NewWorld(t)
	w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")
	cache := gitx.NewCache(t.TempDir())
	db, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
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
	return &remoteWorld{w: w, srv: srv, ts: ts, db: db, loader: loader, cache: cache}
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
	return stub.Evaluate(f, f.Routines["pr-review"], stub.Target{Runtime: p, Endpoint: "https://fabrica.acme.dev/pr-review"}).Shape.Fingerprint()
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
	for _, want := range []string{"for repo github.com/acme/api", "slack_channel = #api", "slack.notify (channel: #api): do it with the tools of the slack connector", "then tell #api"} {
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
