package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
)

// These tests are about the local MCP server: matching a personal-home
// factory to a workspace, serving a session, and recording that session's
// run. Shared MCP client helpers live in mcpserver_test.go.

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
	runID := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "add a dark mode toggle to settings"}))
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
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": ""}, "is empty")

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
	c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review my diff before pushing", "routine": "review-before-push", "stub": fp})
	routine := c.ok("routine_get", map[string]any{"name": "review-before-push"})
	if !strings.Contains(routine, "Review your own diff") || !strings.Contains(routine, "## Working rules") {
		t.Errorf("routine_get = %s", routine)
	}
	// The wrong stub is refused, and then routine_get won't serve it either.
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review again", "routine": "review-before-push", "stub": "000000000000"}, "this stub is outdated")
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
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}, "recorded as failed run")
}

func TestLocalRefusesSkillClashes(t *testing.T) {
	lw := newLocalWorld(t)
	testkit.WriteFiles(t, lw.ws, map[string]string{".claude/skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: x\n---\n"})
	c, _ := lw.connect(t)
	c.ok("rules_get", nil) // rules still load
	c.fails("skill_load", map[string]any{"name": "pdf"}, "same names as blueprint skills (pr-review)")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}, "same names as blueprint skills")
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
	c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "look around"})
}

// TestLocalRefusesAnotherRepo: an agent that works in another repo than the
// workspace Fabrica resolved gets none of the factory's blueprint.
func TestLocalRefusesAnotherRepo(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/api", "intent": "fix a bug"}, "you said repo github.com/acme/api, but this workspace is github.com/acme/app")
	// For the rest of the session, nothing from the blueprint is served,
	// not even after a run_begin with the workspace's repo.
	c.fails("rules_get", nil, "don't use any from this server")
	c.fails("skills_list", nil, "don't use any from this server")
	c.fails("skill_load", map[string]any{"name": "pr-review"}, "don't use any from this server")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}, "don't use any from this server")

	// A repo that isn't a repo name is refused too, but only that call.
	other, _ := lw.connect(t)
	other.fails("run_begin", map[string]any{"repo": "not a repo", "intent": "fix a bug"}, "repo:")
	other.ok("run_begin", map[string]any{"repo": "https://github.com/acme/app.git", "intent": "fix a bug"})
	other.ok("rules_get", nil)
}

func TestLocalStubNoLongerDeployed(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	// The routine is removed from the factory after the hook was
	// installed.
	lw.w.Home.Write(map[string]string{"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n"})
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review", "routine": "review-before-push", "stub": fp}, "no longer deployed: routine review-before-push isn't in factory my-default")
}

func TestLocalStubNoLongerRunsOnRuntime(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	// The routine is narrowed to another runtime after the hook was
	// installed.
	lw.w.Home.Write(map[string]string{"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\nroutines: [{ routine: review-before-push, runtimes: [cursor-local] }]\n"})
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review", "routine": "review-before-push", "stub": fp}, "no longer deployed: routine review-before-push doesn't run on runtime claude-code-local in factory my-default")
}

func TestLocalMemoryOnlyForRoutinesThatAskForIt(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "review-before-push")
	runID := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review my diff", "routine": "review-before-push", "stub": fp}))
	if r := c.ok("routine_get", map[string]any{"name": "review-before-push"}); strings.Contains(r, "## Memory") {
		t.Errorf("a routine without memory: true gets no memory section here:\n%s", r)
	}
	c.fails("memory_save", map[string]any{"run_id": runID, "content": "x"}, "doesn't use memory")
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

	runID := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "add a dark mode toggle"}))
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
	routineRun := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review my diff", "routine": "review-before-push", "stub": localStub(t, lw.opts, "review-before-push")}))
	c.ok("routine_get", map[string]any{"name": "review-before-push"})
	if got := loadsOf(t, lw.dbPath, routineRun); got != "routine:review-before-push" {
		t.Errorf("loads = %s", got)
	}
	// A refused routine isn't served, so it isn't loaded.
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review again", "routine": "review-before-push", "stub": "000000000000"}, "this stub is outdated")
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
	text := c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug", "session": "  /home/me/.claude/projects/app/3f2a.jsonl \n"})
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil || len(out) != 2 || out["run_id"] == nil || out["next"] == nil {
		t.Errorf("run_begin returns run_id and next, and nothing else: %s", text)
	}
	if run, _ := lw.db.Get(ctx, c.runID(text)); run.Session != "/home/me/.claude/projects/app/3f2a.jsonl" {
		t.Errorf("session = %q", run.Session)
	}
	// It's optional.
	if run, _ := lw.db.Get(ctx, c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix another bug"}))); run.Session != "" {
		t.Errorf("session = %q", run.Session)
	}

	// A bad one is an error, and the run isn't recorded, so the agent tries again.
	before := countRows(t, lw.dbPath, "runs")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug", "session": "line one\nline two"}, "one line")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug", "session": "tab\there"}, "control characters")
	c.fails("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug", "session": strings.Repeat("x", runs.MaxSessionBytes+1)}, "limit is 512")
	if after := countRows(t, lw.dbPath, "runs"); after != before {
		t.Errorf("runs went from %s to %s; a bad session shouldn't record a run", before, after)
	}

	// A refused run keeps its pointer, so a retro can find its transcript.
	refused := c.callText("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "review again", "routine": "review-before-push", "stub": "000000000000", "session": "copilot-local:3f2a"})
	id := regexp.MustCompile(`failed run (run_\w+);`).FindStringSubmatch(refused)
	if id == nil {
		t.Fatalf("no failed run in %s", refused)
	}
	if run, _ := lw.db.Get(ctx, id[1]); run.Session != "copilot-local:3f2a" {
		t.Errorf("a refused run's session = %q", run.Session)
	}
}

func TestRunReportFriction(t *testing.T) {
	lw := newLocalWorld(t)
	c, _ := lw.connect(t)
	ctx := context.Background()
	runID := c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "fix a bug"}))

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
