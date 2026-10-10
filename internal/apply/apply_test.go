package apply_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

func TestMain(m *testing.M) {
	testkit.MaybeRunToyProvider()
	os.Exit(m.Run())
}

type harness struct {
	t      *testing.T
	w      *testkit.World
	env    *plan.Env
	out    strings.Builder
	handof string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	w := testkit.NewWorld(t)
	cache := gitx.NewCache(t.TempDir())
	h := &harness{t: t, w: w, handof: t.TempDir(), env: &plan.Env{
		Loader: factory.NewLoader(cache), Git: cache, Secret: testkit.Secret,
		Binary: "/usr/local/bin/fabrica", UserHome: t.TempDir(), Refresh: true,
		Now: func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) },
	}}
	return h
}

func (h *harness) plan() *plan.Plan {
	h.t.Helper()
	hm, err := home.Open(h.w.Home.Dir)
	if err != nil {
		h.t.Fatal(err)
	}
	return plan.Build(context.Background(), h.env, hm)
}

func (h *harness) apply() *apply.Result {
	h.t.Helper()
	h.out.Reset()
	res, err := apply.Run(context.Background(), apply.Options{Plan: h.env, Out: &h.out, HandoffDir: h.handof, Holder: "test-holder"}, h.w.Home.Dir)
	if err != nil {
		h.t.Fatalf("apply: %v\n%s", err, h.out.String())
	}
	return res
}

func (h *harness) state() *home.State {
	h.t.Helper()
	h.w.Home.Pull()
	s, err := home.ReadState(h.w.Home.Dir, "pr-review")
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func render(p *plan.Plan) string {
	var b strings.Builder
	p.Render(&b)
	return b.String()
}

func TestFullCloudLifecycle(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add pr-review")

	// 1. The first plan wants 4 stubs: 1 routine × 2 repos × 2 runtimes.
	p := h.plan()
	if p.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", p.ExitCode(), render(p))
	}
	if got := len(p.Deployment("pr-review").Changes); got != 4 {
		t.Fatalf("changes = %d, want 4\n%s", got, render(p))
	}
	text := render(p)
	for _, want := range []string{"follows the branch", "toy: api mode", "claude-code-cloud: handoff mode", "token in the url", "slack [native]"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan output is missing %q:\n%s", want, text)
		}
	}

	// 2. Apply: the toy runtime gets its stubs through the API, Claude gets
	// handoff instructions.
	res := h.apply()
	if res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	toy := h.w.Toy.Resources()
	if len(toy) != 2 {
		t.Fatalf("toy runtime has %d resources, want 2", len(toy))
	}
	wantToken := token.For(testkit.Secret, "pr-review")
	if toy[0].Token != wantToken || toy[0].FabricaURL != "https://fabrica.acme.dev/pr-review" {
		t.Errorf("toy stub has the wrong connection: %+v", toy[0])
	}
	if !strings.Contains(toy[0].Prompt, "Call `run_begin` with routine `pr-review`, runtime `toy`, repo `github.com/acme/api`") {
		t.Errorf("toy stub prompt = %q", toy[0].Prompt)
	}
	if len(res.Handoffs) != 2 {
		t.Fatalf("handoffs = %v, want 2", res.Handoffs)
	}
	handoff, _ := os.ReadFile(res.Handoffs[0])
	if !strings.Contains(string(handoff), "/t/"+wantToken) || !strings.Contains(string(handoff), "fabrica confirm h_") {
		t.Errorf("claude-code-cloud handoff should put the token in the URL and end with confirm:\n%s", handoff)
	}
	if info, _ := os.Stat(res.Handoffs[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("handoff files hold a token and must be private, got %v", info.Mode().Perm())
	}

	s := h.state()
	if s.Lock != nil || len(s.Resources) != 2 || len(s.Pending) != 2 {
		t.Fatalf("state after apply = %+v", s)
	}
	log := testkit.Git(t, h.w.Home.Dir, "log", "--format=%s")
	if !strings.Contains(log, "fabrica: lock pr-review for apply") || !strings.Contains(log, "fabrica: apply pr-review (4 changes)") {
		t.Errorf("home repo log should show the lock and the apply commits:\n%s", log)
	}

	// 3. Planning again: the toy stubs are in sync, the handoffs pending.
	// Pending handoffs aren't apply's work, so the plan is clean.
	p = h.plan()
	if p.ExitCode() != 0 || len(p.Deployment("pr-review").Pending) != 2 {
		t.Fatalf("second plan: exit %d\n%s", p.ExitCode(), render(p))
	}
	// Applying again issues nothing new.
	if res := h.apply(); len(res.Handoffs) != 0 || !strings.Contains(h.out.String(), "already in sync") {
		t.Errorf("a second apply should do nothing:\n%s", h.out.String())
	}

	// 4. Confirm one handoff with a remote ID, one without.
	confirmed, err := apply.Confirm(context.Background(), h.w.Home.Dir, s.Pending[0].ID, "trig_123", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Unverifiable {
		t.Error("confirming with a remote ID shouldn't be unverifiable")
	}
	confirmed, err = apply.Confirm(context.Background(), h.w.Home.Dir, s.Pending[1].ID, "", time.Now())
	if err != nil || !confirmed.Unverifiable {
		t.Fatalf("Confirm without ID = %+v, %v", confirmed, err)
	}
	s = h.state()
	if len(s.Resources) != 4 || len(s.Pending) != 0 {
		t.Fatalf("state after confirms = %+v", s)
	}
	if out := render(h.plan()); !strings.Contains(out, "Unverifiable") {
		t.Errorf("plan should list the unverifiable resource:\n%s", out)
	}

	// 5. The factory changes the routine's trigger and merges to stable:
	// every stub is now outdated, which is an update, not an error.
	h.w.UpdateFactory(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "opened", "synchronize", 1)})
	p = h.plan()
	changes := p.Deployment("pr-review").Changes
	if p.ExitCode() != 2 || len(changes) != 4 || changes[0].Action != plan.Update {
		t.Fatalf("after a trigger change: exit %d\n%s", p.ExitCode(), render(p))
	}
	if !strings.Contains(render(p), "the trigger changed from github.pull_request.opened to github.pull_request.synchronize") {
		t.Errorf("plan should explain the update:\n%s", render(p))
	}
	h.apply()
	for _, r := range h.w.Toy.Resources() {
		if r.Trigger != "github.pull_request.synchronize" {
			t.Errorf("toy stub wasn't updated: %+v", r)
		}
	}

	// 6. Removing a repo from the deployment deletes its stubs.
	h.w.Home.Pull()
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": strings.Replace(testkit.PRReviewDeployment, "  - repo: github.com/acme/api\n    params: { slack_channel: \"#api\" }\n", "", 1)}).CommitPush("drop api")
	p = h.plan()
	var deletes []string
	for _, c := range p.Deployment("pr-review").Changes {
		if c.Action == plan.Delete {
			deletes = append(deletes, c.Key.Runtime+" "+c.Repo+" "+strings.Join(c.Reasons, ""))
		}
	}
	if len(deletes) != 2 || !strings.Contains(deletes[0], "repo github.com/acme/api was removed") {
		t.Fatalf("deletes = %v\n%s", deletes, render(p))
	}
	h.apply()
	if got := h.w.Toy.Resources(); len(got) != 1 || !strings.Contains(got[0].Identity, "github.com/acme/app") {
		t.Errorf("toy runtime after delete = %+v", got)
	}
}

func TestProductRepoProblemsBlockTheDeployment(t *testing.T) {
	h := newHarness(t)
	h.w.API.Write(map[string]string{
		"AGENTS.md":                         "# API\n",
		".claude/skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: Our own.\n---\n",
	}).Commit("break it")
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")

	p := h.plan()
	d := p.Deployment("pr-review")
	if p.ExitCode() != 1 || !d.Diags.HasCode("product.fabrica-line") || !d.Diags.HasCode("product.skill-clash") {
		t.Fatalf("want product errors:\n%s", render(p))
	}
	res := h.apply()
	if res.ExitCode() != 1 || len(h.w.Toy.Resources()) != 0 {
		t.Errorf("apply should skip the whole deployment:\n%s", h.out.String())
	}
}

func TestUnsupportedTriggerIsAnError(t *testing.T) {
	h := newHarness(t)
	h.w.UpdateFactory(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "github.pull_request.opened", "github.issues.opened", 1)})
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")
	p := h.plan()
	if !p.Deployment("pr-review").Diags.HasCode("capability.trigger") {
		t.Fatalf("want a capability.trigger error:\n%s", render(p))
	}
}

func TestLockIsRespectedAndCanBeRemoved(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")

	// Someone else's apply took the lock and crashed.
	other := h.w.Home.Clone(filepath.Join(t.TempDir(), "ci"))
	repo, err := apply.OpenRepo(context.Background(), other.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.TakeLock(context.Background(), "pr-review", "ci run 42", time.Now()); err != nil {
		t.Fatal(err)
	}

	// A second lock attempt loses.
	err = repo.TakeLock(context.Background(), "pr-review", "me", time.Now())
	var locked *apply.ErrLocked
	if !errors.As(err, &locked) || locked.Lock.Holder != "ci run 42" {
		t.Fatalf("want ErrLocked naming the holder, got %v", err)
	}

	h.w.Home.Pull()
	p := h.plan()
	if !p.Deployment("pr-review").Diags.HasCode("deployment.locked") {
		t.Fatalf("plan should report the lock:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 1 || len(h.w.Toy.Resources()) != 0 {
		t.Errorf("apply must not touch a locked deployment:\n%s", h.out.String())
	}

	removed, err := apply.Unlock(context.Background(), h.w.Home.Dir, "pr-review")
	if err != nil || removed == nil || removed.Holder != "ci run 42" {
		t.Fatalf("Unlock = %+v, %v", removed, err)
	}
	if res := h.apply(); res.ExitCode() != 0 || len(h.w.Toy.Resources()) != 2 {
		t.Errorf("after unlocking, apply should work:\n%s", h.out.String())
	}
}

func TestRefreshAdoptsAndRefusesDuplicates(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")
	id := "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app"

	// A crashed apply created this but never recorded it.
	h.w.Toy.Put(testkit.ToyResource{RemoteID: "orphan", Identity: id})
	p := h.plan()
	var actions []string
	for _, c := range p.Deployment("pr-review").Changes {
		if c.Key.Runtime == "toy" && c.Key.ID == id {
			actions = append(actions, string(c.Action))
		}
	}
	if strings.Join(actions, ",") != "adopt,update" {
		t.Fatalf("want the orphan adopted and updated, got %v\n%s", actions, render(p))
	}
	h.apply()
	if got := h.w.Toy.Resources(); len(got) != 2 {
		t.Errorf("adopting must not create a duplicate, got %+v", got)
	}

	// Someone copies a resource by hand: now two share one identity.
	h.w.Toy.Put(testkit.ToyResource{RemoteID: "copy", Identity: id})
	p = h.plan()
	if !p.Deployment("pr-review").Diags.HasCode("refresh.duplicate") || !strings.Contains(render(p), "orphan, copy") && !strings.Contains(render(p), "copy, orphan") {
		t.Fatalf("want a duplicate error naming both:\n%s", render(p))
	}
}

func TestRotatedSecretUpdatesStubs(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")
	h.apply()
	h.env.Secret = strings.Repeat("rotated-secret-", 3)
	p := h.plan()
	if !strings.Contains(render(p), "the deployment's token changed") {
		t.Fatalf("plan should notice the rotated secret:\n%s", render(p))
	}
	h.apply()
	for _, r := range h.w.Toy.Resources() {
		if r.Token != token.For(h.env.Secret, "pr-review") {
			t.Errorf("toy stub still has the old token: %+v", r)
		}
	}
}

func TestDeletedDeploymentIsCleanedUp(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")
	h.apply()
	h.w.Home.Pull()
	testkit.Git(t, h.w.Home.Dir, "rm", "--quiet", "deployments/pr-review.yaml")
	h.w.Home.CommitPush("delete deployment")

	p := h.plan()
	d := p.Deployment("pr-review")
	if d == nil || d.Spec != nil || len(d.Changes) != 4 {
		t.Fatalf("want 4 deletes for the deleted deployment:\n%s", render(p))
	}
	h.apply()
	if got := h.w.Toy.Resources(); len(got) != 0 {
		t.Errorf("toy resources left behind: %+v", got)
	}
	// The claude-code-cloud stubs need delete handoffs; once confirmed, the state
	// file goes away entirely.
	s := h.state()
	if len(s.Pending) != 2 {
		t.Fatalf("want 2 delete handoffs pending, got %+v", s)
	}
	for _, pend := range s.Pending {
		if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, pend.ID, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h.w.Home.Pull()
	if _, err := os.Stat(filepath.Join(h.w.Home.Dir, "state", "pr-review.yaml")); !os.IsNotExist(err) {
		t.Errorf("the state file of a fully cleaned-up deployment should be removed, got %v", err)
	}
}

func TestStateFromV01MovesToRenamedRuntime(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment}).CommitPush("add")
	h.apply()
	for _, pend := range h.state().Pending {
		if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, pend.ID, "trig_"+pend.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Make the state look like Fabrica v0.1 wrote it, when
	// claude-code-cloud was called claude.
	h.w.Home.Pull()
	data, _ := os.ReadFile(filepath.Join(h.w.Home.Dir, "state", "pr-review.yaml"))
	h.w.Home.Write(map[string]string{"state/pr-review.yaml": strings.ReplaceAll(string(data), "runtime: claude-code-cloud", "runtime: claude")}).CommitPush("v0.1 state")

	// The stubs on claude.ai still name runtime claude in their prompts.
	p := h.plan()
	if d := p.Deployment("pr-review"); d.Diags.HasErrors() || len(d.Changes) != 2 || !strings.Contains(render(p), "its prompt still names runtime claude, which is now called claude-code-cloud") {
		t.Fatalf("want 2 updates on claude-code-cloud:\n%s", render(p))
	}

	// Once the deployment is gone, they can still be cleaned up.
	testkit.Git(t, h.w.Home.Dir, "rm", "--quiet", "deployments/pr-review.yaml")
	h.w.Home.CommitPush("delete deployment")
	p = h.plan()
	if d := p.Deployment("pr-review"); d.Diags.HasErrors() || len(d.Changes) != 4 {
		t.Fatalf("want 4 deletes:\n%s", render(p))
	}
}

func TestApplyRefusesUnpushedHomeChanges(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"deployments/pr-review.yaml": testkit.PRReviewDeployment})
	_, err := apply.Run(context.Background(), apply.Options{Plan: h.env, Out: &h.out, HandoffDir: h.handof}, h.w.Home.Dir)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("want an uncommitted-changes error, got %v", err)
	}
}

func TestLocalDeploymentsInstallIntoTools(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.Home.Write(map[string]string{
		"deployments/my-default.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local, cursor-local]\nrepos: [\"*\"]\n",
		"deployments/oss-work.yaml":   "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local, cursor-local]\nrepos: [github.com/acme/app]\nroutines: [review-before-push]\n",
	})
	// Local deployments are read from the working tree; no commit or remote
	// is needed.
	p := h.plan()
	if p.ExitCode() != 2 || p.Local == nil {
		t.Fatalf("want local changes:\n%s", render(p))
	}
	out := render(p)
	for _, want := range []string{"+ add MCP server fabrica", "+ add hook for routine oss-work/review-before-push on git.pre_push"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}

	claude, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".claude.json"))
	if !strings.Contains(string(claude), `"--runtime"`) || !strings.Contains(string(claude), h.w.Home.Dir) {
		t.Errorf("~/.claude.json should register fabrica mcp with the home repo:\n%s", claude)
	}
	cursorHooks, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".cursor", "hooks.json"))
	if !strings.Contains(string(cursorHooks), "beforeShellExecution") || !strings.Contains(string(cursorHooks), "--event git.pre_push") {
		t.Errorf("Cursor should get a beforeShellExecution hook:\n%s", cursorHooks)
	}
	if _, err := os.Stat(filepath.Join(h.env.UserHome, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("codex-cli isn't used by any deployment, so nothing should be written for it (got %v)", err)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}

	// Removing every local deployment removes Fabrica from the tools.
	os.RemoveAll(filepath.Join(h.w.Home.Dir, "deployments"))
	h.apply()
	claude, _ = os.ReadFile(filepath.Join(h.env.UserHome, ".claude.json"))
	if strings.Contains(string(claude), "fabrica") {
		t.Errorf("fabrica should be gone from ~/.claude.json:\n%s", claude)
	}
}

func TestLocalRoutineWithUnsupportedTrigger(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateFactory(map[string]string{"routines/hello.md": "---\ntrigger: { on: session.start }\n---\nSay hi.\n"})
	h.w.Home.Write(map[string]string{
		"deployments/mine.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local, cursor-local]\nrepos: [\"*\"]\nroutines: [hello]\n",
	})
	p := h.plan()
	d := p.Deployment("mine")
	if !d.Diags.HasCode("capability.trigger") || !strings.Contains(render(p), "cursor-local doesn't support trigger session.start") {
		t.Fatalf("Cursor has no session.start hook, so this should be an error:\n%s", render(p))
	}
}

func TestLocalDeploymentInTeamHomeIsAnError(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{
		"deployments/mine.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\n",
	}).CommitPush("oops")
	p := h.plan()
	if p.ExitCode() != 1 || !strings.Contains(render(p), "personal home repo") {
		t.Fatalf("want an error:\n%s", render(p))
	}
}

func TestLocalScheduleRoutineBecomesClaudeScheduledTask(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateFactory(map[string]string{"routines/update-dep.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\n---\nUpdate a dependency.\n"})
	h.w.Home.Write(map[string]string{
		"deployments/mine.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local]\nrepos: [github.com/acme/app]\nroutines: [update-dep]\n",
	})
	p := h.plan()
	if p.ExitCode() != 2 || !strings.Contains(render(p), "+ add scheduled task fabrica-update-dep-") {
		t.Fatalf("want a scheduled task:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "in a desktop session on your checkout of github.com/acme/app") {
		t.Errorf("apply should say what to set up in the app:\n%s", h.out.String())
	}
	tasks, _ := filepath.Glob(filepath.Join(h.env.UserHome, ".claude", "scheduled-tasks", "fabrica-update-dep-*", "SKILL.md"))
	if len(tasks) != 1 {
		t.Fatalf("want one SKILL.md, got %v", tasks)
	}
	data, _ := os.ReadFile(tasks[0])
	if !strings.Contains(string(data), "Call `run_begin` with routine `update-dep`, runtime `claude-code-local`, repo `github.com/acme/app`") {
		t.Errorf("the task's prompt should be the local stub:\n%s", data)
	}
	settings, _ := os.ReadFile(filepath.Join(h.env.UserHome, ".claude", "settings.json"))
	if strings.Contains(string(settings), "update-dep") {
		t.Errorf("a schedule routine shouldn't get a hook:\n%s", settings)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after applying, local tools should be in sync:\n%s", render(p))
	}

	// Dropping the routine removes the task, and apply says to delete it in
	// the app too.
	h.w.Home.Write(map[string]string{
		"deployments/mine.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local]\nrepos: [github.com/acme/app]\n",
	})
	h.out.Reset()
	h.apply()
	if _, err := os.Stat(tasks[0]); !os.IsNotExist(err) {
		t.Errorf("the task's file should be gone (%v)", err)
	}
	if !strings.Contains(h.out.String(), "from the app's list of routines") {
		t.Errorf("apply should say to delete the task in the app:\n%s", h.out.String())
	}
}

func TestLocalScheduleRoutineNeedsSpecificRepos(t *testing.T) {
	h := newHarness(t)
	h.w.MakePersonal(t)
	h.w.UpdateFactory(map[string]string{"routines/update-dep.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\n---\nUpdate a dependency.\n"})
	h.w.Home.Write(map[string]string{
		"deployments/mine.yaml": "factory: { git: github.com/acme/factory, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\nroutines: [update-dep]\n",
	})
	p := h.plan()
	if !p.Deployment("mine").Diags.HasCode("deployment.schedule-wildcard") {
		t.Fatalf("want a wildcard error:\n%s", render(p))
	}
}
