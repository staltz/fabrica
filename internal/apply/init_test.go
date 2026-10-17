package apply_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// onboardRoutine is an init routine that would like the given runtimes.
func onboardRoutine(runtimes string) string {
	return "---\ndescription: Get a new factory's repos ready\ntrigger: { on: init }\nruntimes: " + runtimes +
		"\nskills: [pr-review]\nconnectors: [github]\n---\nCheck that every repo is ready for this blueprint, and open an issue listing what's missing.\n"
}

func TestInitStartsOnceWhenTheFactoryIsFirstApplied(t *testing.T) {
	h := newHarness(t)
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[toy]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")

	p := h.plan()
	in := p.Factory("pr-review").Init
	if !in.Start() || in.Chosen.Runtime != "toy" || in.Chosen.Mode != provider.API || in.Repo != "github.com/acme/app" {
		t.Fatalf("the plan should start the init on toy, by itself:\n%s", render(p))
	}
	text := render(p)
	for _, want := range []string{"init routine onboard", "→ toy: Fabrica starts it", "! init   onboard @ github.com/acme/app on toy (api; Fabrica starts it): the factory is being applied for the first time", "5 changes"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan output is missing %q:\n%s", want, text)
		}
	}
	if js := p.ToJSON(); js.Factories[0].Init == nil || js.Factories[0].Init.Action != "start" {
		t.Errorf("the JSON plan should say the init starts: %+v", js.Factories[0].Init)
	}

	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "✓ init onboard @ github.com/acme/app on toy (api) (started: https://toy.example/sessions/") {
		t.Errorf("apply should say it started the init:\n%s", h.out.String())
	}
	sessions := h.w.Toy.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("toy runtime has %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Identity != stub.InitIdentity(stub.Tag("https://fabrica.acme.dev", "pr-review"), "onboard") || s.Repo != "github.com/acme/app" || strings.Join(s.Repos, " ") != "github.com/acme/app github.com/acme/api" {
		t.Errorf("the session is about the wrong thing: %+v", s)
	}
	if s.FabricaURL != "https://fabrica.acme.dev/pr-review" || s.Token != token.For(testkit.Secret, "pr-review") {
		t.Errorf("the session has the wrong connection to Fabrica: %+v", s)
	}
	for _, want := range []string{"init session of Fabrica factory `pr-review`", "Call `run_begin` with routine `onboard`, runtime `toy`, repo `github.com/acme/app` and stub `", "`github.com/acme/api`"} {
		if !strings.Contains(s.Prompt, want) {
			t.Errorf("the session's prompt is missing %q: %s", want, s.Prompt)
		}
	}
	st := h.state().Init
	if st == nil || st.Status != home.InitStarted || st.Runtime != "toy" || st.Session != s.SessionID || st.ID != s.ID || st.BlueprintCommit == "" {
		t.Fatalf("state should record the started init: %+v", st)
	}

	// It doesn't run again while the factory exists.
	p = h.plan()
	if p.ExitCode() != 0 || p.Factory("pr-review").Init != nil {
		t.Fatalf("the init already ran, so there's nothing to do:\n%s", render(p))
	}
	if !strings.Contains(render(p), "init onboard started on toy on ") {
		t.Errorf("the plan should say when the init ran:\n%s", render(p))
	}

	// Destroying the factory ends its life, and the apply that recreates
	// it runs the init again: a new session, with a new ID.
	if !strings.Contains(render(h.destroyPlan()), "the factory's init routine, onboard, runs again when the next apply recreates the factory") {
		t.Errorf("the destroy plan should say the init runs again:\n%s", render(h.destroyPlan()))
	}
	if _, err := h.destroy(func(*plan.Plan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after := h.state()
	if after.Init != nil || after.DestroyedAt == nil {
		t.Fatalf("destroy should reset the init and say when it destroyed the factory: init %+v, destroyed_at %v", after.Init, after.DestroyedAt)
	}
	p = h.plan()
	if in := p.Factory("pr-review").Init; !in.Start() || in.Reason != "the factory is being recreated after `fabrica destroy`" {
		t.Fatalf("the apply after a destroy should run the init again:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	sessions = h.w.Toy.Sessions()
	if len(sessions) != 2 || sessions[0].ID == sessions[1].ID {
		t.Fatalf("recreating the factory should start a second session, with its own ID: %+v", sessions)
	}
	after = h.state()
	if after.Init == nil || after.Init.Status != home.InitStarted || after.Init.ID == st.ID || after.DestroyedAt != nil {
		t.Errorf("state should record the new init, and forget the destroy: init %+v, destroyed_at %v", after.Init, after.DestroyedAt)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("once recreated, the factory is in sync:\n%s", render(p))
	}
}

func TestInitStartsOnTheFirstAvailableRuntime(t *testing.T) {
	t.Setenv(provider.CursorKeyEnv, "")
	t.Setenv(provider.CursorTokenEnv, "")
	t.Setenv(provider.ClaudeBinEnv, "fabrica-test-no-such-claude")
	h := newHarness(t)

	// Cursor can't start it by itself (no key), Claude Code can't run it at
	// all (it has no GitHub integration, and the connector has no
	// fallback), and Claude routines have no API for it, so toy wins,
	// though it comes last.
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[cursor-cloud, claude-code-local, claude-code-cloud, toy]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")
	p := h.plan()
	in := p.Factory("pr-review").Init
	if !in.Start() || in.Chosen.Runtime != "toy" {
		t.Fatalf("toy should win:\n%s", render(p))
	}
	text := render(p)
	for _, want := range []string{
		"cursor-cloud: a person starts it, from a handoff (Fabrica can't by itself: CURSOR_API_KEY isn't set)",
		"claude-code-local: can't start it (it can't run the routine: runtime claude-code-local can't provide connector github)",
		"claude-code-cloud: a person starts it, from a handoff (Fabrica can't by itself: it has no API for starting sessions)",
		"→ toy: Fabrica starts it",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan output is missing %q:\n%s", want, text)
		}
	}

	// Without toy, nobody can start it by itself, so the first runtime a
	// person can start it on gets a handoff.
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[claude-code-local, cursor-cloud, claude-code-cloud]")})
	p = h.plan()
	if in := p.Factory("pr-review").Init; !in.Start() || in.Chosen.Runtime != "cursor-cloud" || in.Chosen.Mode != provider.Handoff {
		t.Fatalf("cursor-cloud should get a handoff:\n%s", render(p))
	}
	res := h.apply()
	if res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	st := h.state().Init
	if st == nil || st.Status != home.InitWaiting || st.Mode != "handoff" {
		t.Fatalf("the init should wait for its handoff: %+v", st)
	}
	var handoff string
	for _, f := range res.Handoffs {
		if strings.HasSuffix(f, st.ID+".md") {
			handoff = f
		}
	}
	data, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("no handoff for the init in %v: %v", res.Handoffs, err)
	}
	for _, want := range []string{"# Start the init session of factory `pr-review` as a Cursor cloud agent", "**fabrica-pr-review**", "header `Authorization: Bearer " + token.For(testkit.Secret, "pr-review"), "`github.com/acme/api`", "fabrica confirm " + st.ID + " --remote-id"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the handoff is missing %q:\n%s", want, data)
		}
	}

	// While it waits, plans list it, and no apply starts it again.
	p = h.plan()
	if p.ExitCode() != 0 || p.Factory("pr-review").Init.Start() {
		t.Fatalf("the init waits for confirm:\n%s", render(p))
	}
	if !strings.Contains(render(p), "… "+st.ID+": start init onboard @ github.com/acme/app on cursor-cloud") || !strings.Contains(render(p), "3 handoffs are waiting") {
		t.Errorf("the plan should list the waiting init:\n%s", render(p))
	}

	c, err := apply.Confirm(context.Background(), h.w.Home.Dir, st.ID, "https://cursor.com/agents/bc-123", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.Init == nil || c.Init.Status != home.InitStarted {
		t.Fatalf("confirm should start the init: %+v", c)
	}
	if st := h.state().Init; st.Status != home.InitStarted || st.URL != "https://cursor.com/agents/bc-123" {
		t.Errorf("state should record the confirmed init: %+v", st)
	}
	if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, st.ID, "", false, time.Now()); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Errorf("confirming twice should fail, got %v", err)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Errorf("after confirm, everything is in sync:\n%s", render(p))
	}
}

func TestInitTriesAgainAfterAFailure(t *testing.T) {
	h := newHarness(t)
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[toy]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")

	t.Setenv(testkit.ToyLaunchFailEnv, "the toy runtime is down")
	if res := h.apply(); res.ExitCode() != 1 {
		t.Fatalf("apply should fail when the init can't start:\n%s", h.out.String())
	}
	st := h.state()
	if st.Init == nil || st.Init.Status != home.InitFailed || !strings.Contains(st.Init.Error, "the toy runtime is down") {
		t.Fatalf("state should record the failed init: %+v", st.Init)
	}
	if len(st.Resources) != 2 {
		t.Errorf("the stubs should be in all the same: %d resources", len(st.Resources))
	}
	id := st.Init.ID

	os.Unsetenv(testkit.ToyLaunchFailEnv)
	p := h.plan()
	in := p.Factory("pr-review").Init
	if !in.Start() || in.ID != id || !strings.Contains(in.Reason, "the last attempt failed: ") {
		t.Fatalf("the next plan should try again with the same ID:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}
	if sessions := h.w.Toy.Sessions(); len(sessions) != 1 || sessions[0].ID != id {
		t.Errorf("the retry should start one session, with the first try's ID: %+v", sessions)
	}
	if st := h.state().Init; st.Status != home.InitStarted || st.Error != "" {
		t.Errorf("state should record the started init: %+v", st)
	}
}

func TestInitOnlyRunsForNewFactories(t *testing.T) {
	h := newHarness(t)
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply failed:\n%s", h.out.String())
	}

	// The blueprint gets an init after the factory was first applied.
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[toy]")})
	p := h.plan()
	if p.ExitCode() != 0 || p.Factory("pr-review").Init != nil {
		t.Fatalf("an existing factory doesn't run a new init:\n%s", render(p))
	}
	if !strings.Contains(render(p), "init routine onboard doesn't run: it only runs when apply creates the factory, and this one was created before its blueprint had it") {
		t.Errorf("the plan should say why the init doesn't run:\n%s", render(p))
	}

	// Recreating it after a destroy runs it, like for any new factory.
	if _, err := h.destroy(func(*plan.Plan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if in := h.plan().Factory("pr-review").Init; !in.Start() {
		t.Fatalf("the apply that recreates the factory should run the init:\n%s", render(h.plan()))
	}
}

func TestInitCanBeTurnedOff(t *testing.T) {
	h := newHarness(t)
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[toy]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory + "init: false\n"}).CommitPush("add pr-review")
	p := h.plan()
	if p.Factory("pr-review").Init != nil || !strings.Contains(render(p), "init routine onboard never runs here: the factory says init: false") {
		t.Fatalf("init: false turns the init off:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 || len(h.w.Toy.Sessions()) != 0 {
		t.Fatalf("apply shouldn't start the init:\n%s", h.out.String())
	}
}

func TestInitProblems(t *testing.T) {
	h := newHarness(t)

	// Nowhere to start it.
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[copilot-cloud]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": testkit.PRReviewFactory}).CommitPush("add pr-review")
	p := h.plan()
	if !p.Factory("pr-review").Diags.HasCode("init.no-runtime") {
		t.Errorf("an init with nowhere to run is an error:\n%s", render(p))
	}
	if !strings.Contains(render(p), "Fabrica can't start a session on runtime copilot-cloud") {
		t.Errorf("the plan should say why copilot-cloud can't start it:\n%s", render(p))
	}

	// Listed under routines:.
	h.w.UpdateBlueprint(map[string]string{"routines/onboard.md": onboardRoutine("[toy]")})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": strings.Replace(testkit.PRReviewFactory, "routines: [pr-review]", "routines: [pr-review, onboard]", 1)}).CommitPush("list onboard")
	p = h.plan()
	if !p.Factory("pr-review").Diags.HasCode("factory.init-routine") {
		t.Errorf("listing the init routine under routines: is an error:\n%s", render(p))
	}
}
