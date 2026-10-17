package mcpserver

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	ftoken "github.com/staltz/fabrica/internal/token"
)

// These tests guard the rule that agents write run history and only people
// read it (see "The big rule" in specs/v2-run-history.md). The factory plane,
// which is everything in this package, must never hand run history to an
// agent: not another factory's, not another repo's, not even the agent's own.

// seedMark is in everything seeded, so a leak is easy to spot.
const seedMark = "zzseed"

// seedRun puts a run with recognizable text everywhere in run history, and
// returns its ID.
func seedRun(t *testing.T, db *runs.DB, factory, repo, n string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	run := runs.Run{
		ID: runs.NewID(now), StartedAt: now, Intent: seedMark + " intent " + n, Routine: seedMark + "-routine", Runtime: "claude-code-cloud",
		Repo: repo, Factory: factory, BlueprintSHA: seedMark + "sha" + n, Stub: seedMark + "stub" + n, ServedBy: runs.ServedRemote,
		Session: seedMark + "-session-" + n,
	}
	if err := db.Begin(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.Loaded(ctx, run.ID, runs.LoadSkill, seedMark+"-skill-"+n, now); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCheckpoint(ctx, run.ID, runs.Checkpoint{
		At: now, Author: runs.ByAgent, Status: runs.Failed, Summary: seedMark + " summary " + n,
		Links:    []string{"https://github.com/" + seedMark + "/repo/pull/" + n},
		Friction: []runs.Friction{{Kind: "navigation", Note: seedMark + " friction " + n}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMemory(ctx, factory, seedMark+"-routine", repo, seedMark+" memory "+n, run.ID, now); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// try calls a tool and returns everything the agent would see, whether the
// call worked, failed or was turned down before it reached the tool.
func (c *client) try(name string, args map[string]any) string {
	c.t.Helper()
	res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	// Typed tools send their result as text and as structured content, which
	// say the same thing; if they ever differ, the agent sees both.
	if res.StructuredContent != nil {
		if data, _ := json.Marshal(res.StructuredContent); string(data) != b.String() {
			b.WriteString("\n")
			b.Write(data)
		}
	}
	return b.String()
}

// leakProbe calls every tool with good and bad arguments, and fails if any
// answer holds something seeded. The runs the agent names itself may come
// back in the error, because an agent that sends a run ID knows it already.
type leakProbe struct {
	t       *testing.T
	c       *client
	needles []string
}

func (p *leakProbe) call(name string, args map[string]any) string {
	p.t.Helper()
	out := p.c.try(name, args)
	seen := out
	for _, v := range args {
		if s, ok := v.(string); ok && len(s) >= 8 {
			seen = strings.ReplaceAll(seen, s, "<argument>")
		}
	}
	for _, needle := range p.needles {
		if strings.Contains(seen, needle) {
			p.t.Errorf("%s(%v) leaked %q:\n%s", name, args, needle, out)
		}
	}
	return out
}

// everyTool calls each tool with good arguments and with bad ones, aiming
// run_report and memory_save at every run ID in ids.
func (p *leakProbe) everyTool(routine string, ids []string) {
	p.t.Helper()
	p.call("skills_list", nil)
	for _, name := range []string{"pr-review", "pdf", "nope", ""} {
		p.call("skill_load", map[string]any{"name": name})
	}
	p.call("skill_file", map[string]any{"name": "pr-review", "path": "checklist.md"})
	p.call("skill_file", map[string]any{"name": "pr-review", "path": "../../runs.db"})
	p.call("skill_file", map[string]any{"name": seedMark + "-skill-1", "path": "SKILL.md"})
	p.call("rules_get", nil)
	for _, name := range []string{"typescript", "nope", ""} {
		p.call("rule_load", map[string]any{"name": name})
	}
	for _, name := range []string{routine, "nope", ""} {
		p.call("routine_get", map[string]any{"name": name})
	}
	friction := func(kind string) []any { return []any{map[string]any{"kind": kind, "note": "a note"}} }
	for _, id := range ids {
		p.call("memory_save", map[string]any{"run_id": id, "content": "notes"})
		p.call("run_report", map[string]any{"run_id": id, "summary": "did a thing", "status": "progress"})
		p.call("run_report", map[string]any{"run_id": id, "summary": "did a thing", "status": "succeeded", "friction": friction("tools"), "links": []any{"https://github.com/acme/app/pull/1"}})
		p.call("run_report", map[string]any{"run_id": id, "summary": strings.Repeat("many ", 30)})
		p.call("run_report", map[string]any{"run_id": id, "summary": "did a thing", "status": "weird"})
		p.call("run_report", map[string]any{"run_id": id, "summary": "did a thing", "friction": friction("nonsense")})
		p.call("run_report", map[string]any{"run_id": id, "summary": "did a thing", "links": []any{"javascript:alert(1)"}})
	}
}

// probeSession runs the whole battery on one session: before it has a run,
// then with bad run_begin arguments, then with its own run.
func probeSession(t *testing.T, c *client, foreign []string, routine string, begin map[string]any) {
	t.Helper()
	needles := []string{seedMark}
	needles = append(needles, foreign...)
	p := &leakProbe{t: t, c: c, needles: needles}

	ids := append([]string{"", "run_missing"}, foreign...)
	p.everyTool(routine, ids)

	p.call("run_begin", map[string]any{"intent": ""})
	p.call("run_begin", map[string]any{"intent": "look around", "session": "two\nlines"})
	p.call("run_begin", map[string]any{"intent": "look around", "routine": "nope", "runtime": "claude-code-cloud", "repo": "github.com/acme/app", "stub": "000000000000"})
	p.call("run_begin", map[string]any{"intent": "look around", "repo": "not a repo"})
	out := p.call("run_begin", begin)
	var begun struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &begun); err != nil || begun.RunID == "" {
		t.Fatalf("run_begin failed: %s", out)
	}

	// With its own run, the agent can use every tool, and still sees none of
	// the history: not the seeded runs, and not its own checkpoints.
	p.call("run_report", map[string]any{"run_id": begun.RunID, "summary": "ownsummary alpha", "friction": []any{map[string]any{"kind": "info", "note": "ownfriction beta"}}})
	p.needles = append(p.needles, "ownsummary alpha", "ownfriction beta")
	p.everyTool(routine, append(ids, begun.RunID))
	p.needles = needles
	// What run_begin and run_report answer hasn't changed.
	if got := keysOf(t, out); got != "next,run_id" {
		t.Errorf("run_begin answers with %s", got)
	}
	ack := p.call("run_report", map[string]any{"run_id": begun.RunID, "summary": "all done", "status": "succeeded"})
	if got := keysOf(t, ack); got != "recorded,run_id,status" {
		t.Errorf("run_report answers with %s", got)
	}
}

func keysOf(t *testing.T, text string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("not a JSON object: %s", text)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

func TestNoToolLeaksRunHistory(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		lw := newLocalWorld(t)
		foreign := []string{
			seedRun(t, lw.db, seedMark+"-factory", seedMark+".dev/other/repo", "1"),
			seedRun(t, lw.db, "my-default", "github.com/"+seedMark+"/other-repo", "2"),
			seedRun(t, lw.db, "my-default", "github.com/acme/app", "3"), // same factory and repo, begun earlier
		}
		c, _ := lw.connect(t)
		begin := map[string]any{"intent": "review my diff", "routine": "review-before-push", "stub": localStub(t, lw.opts, "review-before-push")}
		probeSession(t, c, foreign, "review-before-push", begin)
	})

	t.Run("remote", func(t *testing.T) {
		rw := newRemoteWorld(t)
		foreign := []string{
			seedRun(t, rw.db, seedMark+"-factory", "github.com/acme/app", "1"),
			seedRun(t, rw.db, "pr-review", "github.com/"+seedMark+"/other-repo", "2"),
			seedRun(t, rw.db, "pr-review", "github.com/acme/app", "3"),
		}
		c := rw.connect(t, "/pr-review/t/"+ftoken.For(testkit.Secret, "pr-review"))
		begin := map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/app", "stub": rw.remoteStub(t)}
		probeSession(t, c, foreign, "pr-review", begin)
	})
}

// readFunctions are the read API of internal/runs. Only operator-plane code
// (the CLI, the admin API and the dashboard) may call them.
var readFunctions = []string{"List", "Detail", "Stats"}

// readCalls lists where a Go file calls (or takes) a read function of the
// run-history database. It looks at who the function is called on: a name
// that says it's the run-history database, like Runs, runs or db. That
// catches the obvious mistake without tripping on, say, ListRoots or
// diag.List.
func readCalls(fset *token.FileSet, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !isOneOf(sel.Sel.Name, readFunctions) {
			return true
		}
		var recv string
		switch x := sel.X.(type) {
		case *ast.Ident:
			recv = x.Name
		case *ast.SelectorExpr:
			recv = x.Sel.Name
		}
		if lower := strings.ToLower(recv); strings.Contains(lower, "runs") || lower == "db" {
			found = append(found, fset.Position(sel.Pos()).String()+": "+recv+"."+sel.Sel.Name)
		}
		return true
	})
	return found
}

func isOneOf(s string, list []string) bool {
	for _, item := range list {
		if s == item {
			return true
		}
	}
	return false
}

func TestMCPServerNeverCallsTheReadFunctions(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files: %v", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range readCalls(fset, file) {
			t.Errorf("%s: the MCP server must not read run history, because agents would be able to read it too (specs/v2-run-history.md)", call)
		}
	}
}

// TestReadCallsFindsTheObviousMistakes makes sure the check above isn't
// vacuous, now that the read functions don't exist yet.
func TestReadCallsFindsTheObviousMistakes(t *testing.T) {
	src := `package x

func bad(s *session, db *runs.DB, opts Options) {
	s.core.Runs.List(ctx, f)
	opts.Runs.Detail(ctx, id)
	db.Stats(ctx, f)
	runs.List(ctx, f)
	fn := s.core.Runs.Stats
	_ = fn
}

func fine(ss *mcp.ServerSession, diags diag.List) {
	ss.ListRoots(ctx, nil)
	_ = diag.List(nil)
	s.core.Runs.Get(ctx, id)
	s.core.Runs.Begin(ctx, run)
	tools.List(ctx)
	x.Detail()
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCalls(fset, file); len(got) != 5 {
		t.Errorf("found %d read calls, want the 5 bad ones: %v", len(got), got)
	}
}
