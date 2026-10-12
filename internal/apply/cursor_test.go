package apply_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// cursorFactory is the design doc's factory, deployed to Cursor
// automations only.
var cursorFactory = strings.Replace(testkit.PRReviewFactory, "runtimes: [claude-code-cloud, toy]", "runtimes: [cursor-cloud]", 1)

func TestCursorCloudThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	cursor := testkit.NewFakeCursor(t)
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": cursorFactory}).CommitPush("add pr-review on Cursor")

	// 1. With a Cursor API key, cursor-cloud is an api runtime.
	p := h.plan()
	text := render(p)
	for _, want := range []string{"cursor-cloud: api mode", "+ create pr-review @ github.com/acme/app on cursor-cloud (api)"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan output is missing %q:\n%s", want, text)
		}
	}

	// 2. Apply creates the automations through the API: no handoffs.
	res := h.apply()
	if res.ExitCode() != 0 || len(res.Handoffs) != 0 {
		t.Fatalf("apply: exit %d, handoffs %v\n%s", res.ExitCode(), res.Handoffs, h.out.String())
	}
	autos := cursor.Automations()
	if len(autos) != 2 || autos[0].Name != "fabrica.acme.dev/pr-review#pr-review@github.com/acme/api" {
		t.Fatalf("Cursor has %+v", autos)
	}
	wf := toJSON(autos[0].Workflow)
	for _, want := range []string{
		`"pullRequest":{"prAction":"GIT_PULL_REQUEST_ACTION_OPENED","repos":["acme/api"]}`,
		`{"mcp":{"server":{"name":"fabrica-pr-review"}}},{"prComment":{}},{"slack":{"generalized":true}}`,
		`"memoryEnabled":false`,
		"runtime `cursor-cloud`, repo `github.com/acme/api`",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("automation workflow is missing %s:\n%s", want, wf)
		}
	}

	// Cursor's API can't set up the MCP servers an automation names, so
	// apply says what to set up, in a private file, like a handoff.
	if len(res.Setups) != 1 {
		t.Fatalf("setups = %v", res.Setups)
	}
	setup, _ := os.ReadFile(res.Setups[0])
	if !strings.Contains(string(setup), "**fabrica-pr-review**: HTTP, URL `https://fabrica.acme.dev/pr-review`, header `Authorization: Bearer "+token.For(testkit.Secret, "pr-review")+"`") {
		t.Errorf("setup doesn't describe the Fabrica server:\n%s", setup)
	}
	if info, _ := os.Stat(res.Setups[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("the setup file holds a token and must be private, got %v", info.Mode().Perm())
	}

	s := h.state()
	if s.Lock != nil || len(s.Resources) != 2 || s.Resources[0].RemoteID == "" || s.Resources[0].Mode != "api" {
		t.Fatalf("state after apply = %+v", s)
	}

	// Cursor can't list automations, so each one went into state as soon
	// as it was made, while the lock was held: a crash can't orphan it.
	log := testkit.Git(t, h.w.Home.Dir, "log", "--format=%h %s")
	if strings.Count(log, "(created pr-review @ ") != 2 || !strings.Contains(log, "fabrica: apply pr-review (2 changes)") {
		t.Errorf("home repo log should have a checkpoint per create, then the apply:\n%s", log)
	}
	first := lastLine(testkit.Git(t, h.w.Home.Dir, "log", "--format=%h", "--grep=(created pr-review @ "))
	checkpoint := testkit.Git(t, h.w.Home.Dir, "show", first+":state/pr-review.yaml")
	if !strings.Contains(checkpoint, "holder: test-holder") || strings.Count(checkpoint, "remote_id: auto_") != 1 {
		t.Errorf("the first checkpoint should record one automation, under the lock:\n%s", checkpoint)
	}

	// 3. Planning again reads each automation back by its ID: in sync.
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("second plan: exit %d\n%s", p.ExitCode(), render(p))
	}

	// 4. Someone renames one automation and deletes the other in Cursor.
	cursor.Rename(autos[0].ID, "my review bot")
	cursor.Remove(autos[1].ID)
	p = h.plan()
	text = render(p)
	var actions []string
	for _, c := range p.Factory("pr-review").Changes {
		actions = append(actions, string(c.Action)+" "+c.Repo)
	}
	if strings.Join(actions, ", ") != "update github.com/acme/api, create github.com/acme/app" ||
		!strings.Contains(text, `is named "my review bot" in the runtime; apply names it back`) ||
		!strings.Contains(text, "in state but gone from the runtime") {
		t.Fatalf("plan after hand edits: %v\n%s", actions, text)
	}
	h.apply()
	autos = cursor.Automations()
	if len(autos) != 2 || autos[0].Name != "fabrica.acme.dev/pr-review#pr-review@github.com/acme/api" {
		t.Fatalf("Cursor after the repair has %+v", autos)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan after the repair: exit %d\n%s", p.ExitCode(), render(p))
	}

	// 5. A routine change updates the automations in place.
	h.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "opened", "synchronize", 1)})
	h.apply()
	for _, a := range cursor.Automations() {
		if !strings.Contains(toJSON(a.Workflow), "GIT_PULL_REQUEST_ACTION_PUSHED") {
			t.Errorf("automation %s wasn't updated: %s", a.ID, toJSON(a.Workflow))
		}
	}

	// 6. Without the key, Fabrica can't reach Cursor, so it trusts state
	// and would use handoffs, and says why.
	t.Setenv("CURSOR_API_KEY", "")
	text = render(h.plan())
	if !strings.Contains(text, "cursor-cloud uses handoff mode because CURSOR_API_KEY isn't set") || !strings.Contains(text, "In sync.") {
		t.Errorf("plan without a key:\n%s", text)
	}
	t.Setenv("CURSOR_API_KEY", testkit.FakeCursorKey)

	// 7. Destroy deletes the automations through the API.
	if res, err := h.destroy(nil); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy: %v\n%s", err, h.out.String())
	}
	if autos := cursor.Automations(); len(autos) != 0 {
		t.Errorf("Cursor still has %+v", autos)
	}
	if s := h.state(); len(s.Resources) != 0 {
		t.Errorf("state after destroy = %+v", s)
	}
}

func TestCursorFailedCreateKeepsTheRest(t *testing.T) {
	h := newHarness(t)
	cursor := testkit.NewFakeCursor(t)
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": cursorFactory}).CommitPush("add pr-review on Cursor")

	cursor.FailNext("CreateAutomation", "resource_exhausted")
	res := h.apply()
	if res.ExitCode() != 1 || !strings.Contains(h.out.String(), "Cursor's CreateAutomation failed: resource_exhausted") {
		t.Fatalf("apply should report the failed create:\n%s", h.out.String())
	}
	if s := h.state(); s.Lock != nil || len(s.Resources) != 1 {
		t.Fatalf("state should keep the automation that was made, and no lock: %+v", s)
	}
	// What to set up by hand is written even when a call fails, since a
	// missing MCP server could be why it failed.
	if len(res.Setups) != 1 {
		t.Errorf("setups = %v, want the Cursor one", res.Setups)
	}
	p := h.plan()
	if changes := p.Factory("pr-review").Changes; len(changes) != 1 || changes[0].Action != plan.Create {
		t.Fatalf("the next plan should only create the missing one:\n%s", render(p))
	}
	h.apply()
	if got := len(cursor.Automations()); got != 2 {
		t.Errorf("Cursor has %d automations, want 2", got)
	}
}

func TestCursorHandoffsCarryOverToTheAPI(t *testing.T) {
	h := newHarness(t)
	cursor := testkit.NewFakeCursor(t)
	t.Setenv("CURSOR_API_KEY", "")
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": cursorFactory}).CommitPush("add pr-review on Cursor")

	// 1. Without a key, Cursor gets handoffs, which name the MCP server
	// the way api mode will.
	res := h.apply()
	if len(res.Handoffs) != 2 {
		t.Fatalf("handoffs = %v\n%s", res.Handoffs, h.out.String())
	}
	handoff, _ := os.ReadFile(res.Handoffs[0])
	if !strings.Contains(string(handoff), "**fabrica-pr-review** (custom MCP server, HTTP)") {
		t.Errorf("the handoff should name the MCP server fabrica-pr-review:\n%s", handoff)
	}

	// Someone follows both: they confirm the first with the automation's
	// ID, and the second without one.
	s := h.state()
	first := s.Pending[0]
	id := cursor.Put(first.Resource, "… and stub `"+first.Shape.Fingerprint+"`, and …")
	if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, first.ID, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, s.Pending[1].ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// 2. With the key, Fabrica finds the first by its ID: in sync.
	t.Setenv("CURSOR_API_KEY", testkit.FakeCursorKey)
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan with the key: exit %d\n%s", p.ExitCode(), render(p))
	}

	// 3. A trigger change updates the first through the API. The second
	// has no ID, so a person has to update it: that's a handoff.
	h.w.UpdateBlueprint(map[string]string{"routines/pr-review.md": strings.Replace(testkit.PRReviewRoutine, "opened", "synchronize", 1)})
	res = h.apply()
	if res.ExitCode() != 0 || len(res.Handoffs) != 1 {
		t.Fatalf("apply: exit %d, handoffs %v\n%s", res.ExitCode(), res.Handoffs, h.out.String())
	}
	if handoff, _ := os.ReadFile(res.Handoffs[0]); !strings.Contains(string(handoff), "# Update Cursor automation for `pr-review` on `github.com/acme/app`") {
		t.Errorf("want an update handoff for the unverifiable one:\n%s", handoff)
	}
	if wf := toJSON(cursor.Automations()[0].Workflow); !strings.Contains(wf, "GIT_PULL_REQUEST_ACTION_PUSHED") {
		t.Errorf("the first automation wasn't updated through the API: %s", wf)
	}

	// 4. The second is confirmed with a mistyped ID. Fabrica can't find it,
	// so it makes a new one, and warns that the old one may still run.
	s = h.state()
	if _, err := apply.Confirm(context.Background(), h.w.Home.Dir, s.Pending[0].ID, "auto_404", time.Now()); err != nil {
		t.Fatal(err)
	}
	p := h.plan()
	if changes := p.Factory("pr-review").Changes; len(changes) != 1 || changes[0].Action != plan.Create ||
		!strings.Contains(render(p), "it was set up by hand, so if auto_404 isn't its real ID, it's still there") {
		t.Errorf("plan after a mistyped ID:\n%s", render(p))
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
