package mcpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
// instructions and no tools. The listed tools must also be bare name /
// description / inputSchema: outputSchema and annotations make Claude Code
// drop the whole list.
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
			Tools []map[string]any `json:"tools"`
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
		name, _ := tool["name"].(string)
		names = append(names, name)
		// Claude Code drops every tool from a server whose listing includes
		// these 2025-11-25 fields (anthropics/claude-code#25081).
		for _, extra := range []string{"outputSchema", "annotations", "title", "execution"} {
			if _, ok := tool[extra]; ok {
				t.Errorf("tool %s has %s; Claude cloud will drop the whole list", name, extra)
			}
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if _, ok := schema["$schema"]; ok {
			t.Errorf("tool %s inputSchema has $schema; Claude cloud will drop the whole list", name)
		}
	}
	slices.Sort(names)
	want := []string{"memory_save", "routine_get", "rule_load", "rules_get", "run_begin", "run_report", "skill_file", "skill_load", "skills_list"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// TestRemoteSessionlessListsTools is Claude cloud since ~2.1.304: it
// negotiates MCP 2026-07-28, calls server/discover (instructions), then
// tools/list with Mcp-Protocol-Version: 2026-07-28. A stateful handler
// answers discover and rejects tools/list, which looks like "connected,
// sent instructions, offered no tools".
func TestRemoteSessionlessListsTools(t *testing.T) {
	rw := newRemoteWorld(t)
	endpoint := rw.ts.URL + "/pr-review/t/" + token.For(testkit.Secret, "pr-review")

	status, _, discovered := sessionlessRPC(t, endpoint, "server/discover", "", nil)
	if status != 200 {
		t.Fatalf("discover status %d, body %v", status, discovered)
	}
	result, _ := discovered["result"].(map[string]any)
	if result["resultType"] != "complete" {
		t.Errorf("discover resultType = %v, want complete", result["resultType"])
	}
	if _, ok := result["ttlMs"]; !ok {
		t.Error("discover missing ttlMs")
	}
	if _, ok := result["cacheScope"]; !ok {
		t.Error("discover missing cacheScope")
	}
	instr, _ := result["instructions"].(string)
	if !strings.Contains(instr, "Call run_begin first") {
		t.Errorf("discover instructions = %s", instr)
	}
	var versions []string
	for _, v := range result["supportedVersions"].([]any) {
		s, _ := v.(string)
		versions = append(versions, s)
	}
	if !slices.Contains(versions, sessionlessProtocol) {
		t.Errorf("supportedVersions = %v, want %s", versions, sessionlessProtocol)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Error("discover should advertise tools")
	}

	status, hdr, listed := sessionlessRPC(t, endpoint, "tools/list", "", nil)
	if status != 200 {
		t.Fatalf("tools/list status %d, body %v", status, listed)
	}
	if sid := hdr.Get("Mcp-Session-Id"); sid != "" {
		t.Errorf("sessionless tools/list set Mcp-Session-Id %s", sid)
	}
	listedResult, _ := listed["result"].(map[string]any)
	if errObj, _ := listed["error"].(map[string]any); errObj != nil {
		t.Fatalf("tools/list error: %v", errObj["message"])
	}
	if listedResult["resultType"] != "complete" {
		t.Errorf("tools/list resultType = %v, want complete", listedResult["resultType"])
	}
	var names []string
	tools, _ := listedResult["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		names = append(names, name)
		for _, extra := range []string{"outputSchema", "annotations", "title", "execution"} {
			if _, ok := tool[extra]; ok {
				t.Errorf("tool %s has %s; Claude cloud will drop the whole list", name, extra)
			}
		}
	}
	slices.Sort(names)
	want := []string{"memory_save", "routine_get", "rule_load", "rules_get", "run_begin", "run_report", "skill_file", "skill_load", "skills_list"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// TestRemoteSessionlessRoutineGet is a Claude grind: run_begin and
// routine_get arrive on separate POSTs with no Mcp-Session-Id.
func TestRemoteSessionlessRoutineGet(t *testing.T) {
	rw := newRemoteWorld(t)
	endpoint := rw.ts.URL + "/pr-review/t/" + token.For(testkit.Secret, "pr-review")
	fp := rw.remoteStub(t)

	status, _, body := sessionlessRPC(t, endpoint, "tools/call", "routine_get", map[string]any{
		"name":      "routine_get",
		"arguments": map[string]any{"name": "pr-review"},
	})
	text, isErr := sessionlessToolText(t, body)
	if status != 200 || !isErr || !strings.Contains(text, "only run from stubs") {
		t.Fatalf("routine_get before run_begin: status %d isErr %v %s", status, isErr, text)
	}

	status, _, body = sessionlessRPC(t, endpoint, "tools/call", "run_begin", map[string]any{"repo": "github.com/acme/app",
		"name": "run_begin",
		"arguments": map[string]any{
			"intent":  "review PR 7 on the app",
			"routine": "pr-review",
			"runtime": "claude-code-cloud",
			"repo":    "github.com/acme/api",
			"stub":    fp,
		},
	})
	text, isErr = sessionlessToolText(t, body)
	if status != 200 || isErr {
		t.Fatalf("run_begin: status %d %s", status, text)
	}
	var began runBeginOut
	if err := json.Unmarshal([]byte(text), &began); err != nil || began.RunID == "" {
		t.Fatalf("run_begin body: %s", text)
	}

	status, _, body = sessionlessRPC(t, endpoint, "tools/call", "routine_get", map[string]any{
		"name":      "routine_get",
		"arguments": map[string]any{"name": "pr-review"},
	})
	text, isErr = sessionlessToolText(t, body)
	if status != 200 || isErr {
		t.Fatalf("routine_get after run_begin: status %d %s", status, text)
	}
	if !strings.Contains(text, "for repo github.com/acme/api") {
		t.Errorf("routine_get is missing the stub's repo:\n%s", text)
	}

	status, _, body = sessionlessRPC(t, endpoint, "tools/call", "run_report", map[string]any{
		"name": "run_report",
		"arguments": map[string]any{
			"run_id":  began.RunID,
			"summary": "left a review comment",
			"status":  "succeeded",
		},
	})
	text, isErr = sessionlessToolText(t, body)
	if status != 200 || isErr {
		t.Fatalf("run_report: status %d %s", status, text)
	}
}

func sessionlessRPC(t *testing.T, endpoint, method, name string, params map[string]any) (int, http.Header, map[string]any) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["_meta"]; !ok {
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    sessionlessProtocol,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "Anthropic", "version": "1.0.0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Mcp-Protocol-Version", sessionlessProtocol)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body %s: %v", raw, err)
		}
	}
	return res.StatusCode, res.Header.Clone(), body
}

func sessionlessToolText(t *testing.T, body map[string]any) (string, bool) {
	t.Helper()
	if errObj, ok := body["error"].(map[string]any); ok {
		msg, _ := errObj["message"].(string)
		t.Fatalf("jsonrpc error: %s (%v)", msg, errObj)
	}
	result, _ := body["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	var b strings.Builder
	for _, item := range result["content"].([]any) {
		m, _ := item.(map[string]any)
		if m["type"] == "text" {
			s, _ := m["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String(), isErr
}

type headerTransport struct{ auth string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", h.auth)
	return http.DefaultTransport.RoundTrip(r)
}

// TestRemoteRefusesAnotherRepo: a cloud session whose MCP config points at
// a factory that isn't for its repo gets none of that factory's blueprint.
func TestRemoteRefusesAnotherRepo(t *testing.T) {
	rw := newRemoteWorld(t)
	path := "/pr-review/t/" + token.For(testkit.Secret, "pr-review")
	c := rw.connect(t, path)
	text := c.callText("run_begin", map[string]any{"repo": "github.com/acme/billing", "intent": "fix a bug"})
	if !strings.Contains(text, "factory pr-review isn't for repo github.com/acme/billing") || !strings.Contains(text, "recorded as failed run") {
		t.Fatalf("run_begin = %s", text)
	}

	// A client with MCP sessions (Cursor) gets nothing more from the
	// blueprint for the rest of the session. Sessionless HTTP has no
	// session to remember the refusal in, so there the refusal itself has
	// to stop the agent.
	stateful := connect(t, rw.srv.newSession("pr-review"))
	stateful.fails("run_begin", map[string]any{"repo": "github.com/acme/billing", "intent": "fix a bug"}, "factory pr-review isn't for repo github.com/acme/billing")
	stateful.fails("rules_get", nil, "factory pr-review isn't for repo github.com/acme/billing")
	stateful.fails("skills_list", nil, "factory pr-review isn't for repo github.com/acme/billing")
	stateful.fails("skill_file", map[string]any{"name": "pr-review", "path": "checklist.md"}, "factory pr-review isn't for repo github.com/acme/billing")
	stateful.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}, "factory pr-review isn't for repo github.com/acme/billing")

	// Both refusals for github.com/acme/billing are in run history, under the
	// repo the agent said.
	list, _, err := rw.db.List(context.Background(), runs.Filter{Repo: "github.com/acme/billing"})
	if err != nil || len(list) != 2 || list[0].Factory != "pr-review" {
		t.Fatalf("runs for github.com/acme/billing = %+v, %v", list, err)
	}
	if cps, _ := rw.db.Checkpoints(context.Background(), list[0].RunID); len(cps) != 1 || cps[0].Status != runs.Failed || !strings.Contains(cps[0].Summary, "repo this factory isn't for") {
		t.Errorf("checkpoints = %+v", cps)
	}

	// A session for one of the factory's repos is served as usual.
	ok := rw.connect(t, path)
	ok.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"})
	ok.ok("rules_get", nil)
}

func TestRemoteRoutineRun(t *testing.T) {
	rw := newRemoteWorld(t)
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	fp := rw.remoteStub(t)

	// Routines only run from stubs, with every value filled in.
	c.fails("routine_get", map[string]any{"name": "pr-review"}, "only run from stubs")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review PR 7", "routine": "pr-review"}, "needs routine, runtime, repo and stub")

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

	// Sessionless HTTP has no MCP session, so a later connection can still
	// save with the run_id, the way run_report already works.
	dropped := rw.connect(t, path)
	dropped.ok("memory_save", map[string]any{"run_id": runID, "content": "- The app's tests live in `spec/`."})

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

func TestRemoteRoutineParamsFromImports(t *testing.T) {
	rw := newRemoteWorld(t)
	ctx := context.Background()

	// The blueprint imports someone else's routine and sets two of its
	// params; the factory sets one of them again, and a target sets it once
	// more.
	rw.w.Host.Repo("github.com/acme/refactor").Write(map[string]string{
		"fabrica.yaml":      "",
		"routines/split.md": "---\ntrigger: { on: github.pull_request.opened }\nparams:\n  split_proceed_threshold: { default: 11 }\n  cruft_proceed_threshold: { default: 5 }\n---\nSplit files scoring over {{params.split_proceed_threshold}}, and clean up over {{params.cruft_proceed_threshold}}.\n",
	}).Commit("init")
	rw.w.Blueprint.Write(map[string]string{
		"fabrica.yaml": "expects: [test?]\nimports:\n  refactor:\n    git: github.com/acme/refactor\n    ref: main\n    params: { split_proceed_threshold: 20, cruft_proceed_threshold: 3 }\n",
	})
	lock, _, _, diags := rw.loader.Lock(ctx, rw.w.Blueprint.Dir)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if err := blueprint.WriteLock(rw.w.Blueprint.Dir, lock); err != nil {
		t.Fatal(err)
	}
	rw.w.UpdateBlueprint(nil)
	rw.w.Home.Write(map[string]string{"factories/refactor.yaml": `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud]
routines: [split]
targets:
  - github.com/acme/app
  - repo: github.com/acme/api
    params: { cruft_proceed_threshold: 1 }
params:
  cruft_proceed_threshold: 2
`}).CommitPush("add refactor")
	if err := rw.srv.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	resolved, _ := rw.cache.Remote("https://github.com/acme/blueprint.git", gitx.Full).Resolve(ctx, "stable")
	f, _ := rw.loader.LoadGit(ctx, "github.com/acme/blueprint", resolved.Commit, "")
	p, _ := provider.Builtin("claude-code-cloud")
	fp := stub.Evaluate(f, f.Routines["split"], stub.Target{Runtime: p, Endpoint: "https://fabrica.acme.dev/refactor"}).Stubs[0].Shape.Fingerprint()

	for repo, want := range map[string]string{
		"github.com/acme/app": "Split files scoring over 20, and clean up over 2.", // the import's value beats the routine's, the factory's beats the import's
		"github.com/acme/api": "Split files scoring over 20, and clean up over 1.", // the target's beats the factory's
	} {
		c := rw.connect(t, "/refactor/t/"+token.For(testkit.Secret, "refactor"))
		c.runID(c.ok("run_begin", map[string]any{"intent": "split big files", "routine": "split", "runtime": "claude-code-cloud", "repo": repo, "stub": fp}))
		if routine := c.ok("routine_get", map[string]any{"name": "split"}); !strings.Contains(routine, want) {
			t.Errorf("for %s, routine_get should say %q:\n%s", repo, want, routine)
		}
	}
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

// callText is the text of a tool's answer, error or not.
func (c *client) callText(name string, args map[string]any) string {
	c.t.Helper()
	text, _ := c.call(name, args)
	return text
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
		runID := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}))
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
		other.ok("run_report", report(c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "look around"}))))
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
