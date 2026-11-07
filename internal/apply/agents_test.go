package apply_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
)

// agentsFactory deploys the blueprint's agents to Cursor, watching both
// product repos.
const agentsFactory = `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
alias: acme
runtimes: [cursor-agents]
targets: [github.com/acme/app, github.com/acme/api]
`

const reviewerSlug = "acme_pr-reviewer"

func agentHarness(t *testing.T) (*harness, *testkit.FakeAgentSDK, string) {
	t.Helper()
	h := newHarness(t)
	sdk := testkit.NewFakeAgentSDK(t)
	commit := h.w.UpdateBlueprint(testkit.AgentProject("agents/pr-reviewer", "@acme/pr-reviewer"))
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": agentsFactory}).CommitPush("deploy the agents")
	return h, sdk, commit
}

func changes(p *plan.Plan) string {
	var out []string
	for _, c := range p.Factory("pr-review").Changes {
		out = append(out, c.Describe())
	}
	return strings.Join(out, "\n")
}

func TestAgentLifecycle(t *testing.T) {
	h, sdk, commit := agentHarness(t)
	t.Setenv("LINEAR_TOKEN", "lin_secret")

	// 1. The plan deploys the agent, once, watching both repos.
	p := h.plan()
	if p.ExitCode() != 2 {
		t.Fatalf("exit %d\n%s", p.ExitCode(), render(p))
	}
	if got := changes(p); !strings.HasPrefix(got, "+ create agent pr-reviewer on cursor-agents (api)") || strings.Count(got, "\n") != 0 {
		t.Errorf("changes:\n%s", got)
	}
	if text := show(p); !strings.Contains(text, "agent pr-reviewer: deployment acme_pr-reviewer, built from https://github.com/acme/blueprint at "+commit[:7]+", agents/pr-reviewer, watching acme/api, acme/app") {
		t.Errorf("show:\n%s", text)
	}

	// 2. Apply installs the agent's dependencies, deploys it from git, and
	// sets the secret it declares from apply's environment.
	res := h.apply()
	if res.ExitCode() != 0 || len(res.Handoffs) != 0 {
		t.Fatalf("apply: exit %d, handoffs %v\n%s", res.ExitCode(), res.Handoffs, h.out.String())
	}
	deps := sdk.Deployments()
	if len(deps) != 1 {
		t.Fatalf("deployments = %+v", deps)
	}
	d := deps[0]
	if d.Slug != reviewerSlug || d.Repo != "https://github.com/acme/blueprint" || d.Ref != commit || d.Path != "agents/pr-reviewer" ||
		strings.Join(d.EventRepos, ",") != "acme/api,acme/app" || d.Secrets["LINEAR_TOKEN"] != "lin_secret" {
		t.Errorf("deployment = %+v", d)
	}
	calls := strings.Join(sdk.Calls(), "\n")
	if !strings.Contains(calls, "npm ci --ignore-scripts") {
		t.Errorf("apply should install dependencies without their scripts:\n%s", calls)
	}
	// Cursor shows the alias token once, so apply keeps it, privately.
	tokenFile := filepath.Join(h.handof, "pr-review", reviewerSlug+"-alias-token")
	if data, err := os.ReadFile(tokenFile); err != nil || strings.TrimSpace(string(data)) != "alias_"+reviewerSlug {
		t.Errorf("alias token file: %q, %v", data, err)
	}
	if info, _ := os.Stat(tokenFile); info == nil || info.Mode().Perm() != 0o600 {
		t.Error("the alias token file must be private")
	}
	s := h.state()
	if len(s.Resources) != 1 {
		t.Fatalf("state = %+v", s)
	}
	r := s.Resources[0]
	if r.ID != stub.AgentIdentity("acme", "pr-reviewer") || r.Agent != "pr-reviewer" || r.RemoteID != reviewerSlug || r.Shape.Agent == nil ||
		r.Shape.Agent.Commit != commit || strings.Join(r.Shape.Agent.Secrets, ",") != "LINEAR_TOKEN" || len(r.Shape.Agent.UnsetSecrets) != 0 {
		t.Errorf("resource = %+v, agent = %+v", r, r.Shape.Agent)
	}

	// 3. In sync, even after a blueprint change that doesn't touch the
	// agent: it keeps the commit it was built from.
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan after apply: exit %d\n%s", p.ExitCode(), render(p))
	}
	h.w.UpdateBlueprint(map[string]string{"rules/style.mdc": "---\nalwaysApply: true\n---\nBe brief.\n"})
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan after an unrelated merge: exit %d\n%s", p.ExitCode(), render(p))
	}

	// 4. Changing the agent's files redeploys it from the new commit.
	newCommit := h.w.UpdateBlueprint(map[string]string{"agents/pr-reviewer/bot/instructions.md": "# PR reviewer\n\nBe kind.\n"})
	p = h.plan()
	if got := changes(p); !strings.Contains(got, "~ update agent pr-reviewer on cursor-agents (api): its files changed in the blueprint (redeploys it from "+newCommit[:7]+")") {
		t.Errorf("changes:\n%s", got)
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply: %s", h.out.String())
	}
	if d := sdk.Deployments()[0]; d.Deploys != 2 || d.Ref != newCommit {
		t.Errorf("deployment after a change = %+v", d)
	}

	// 5. A new target only changes the repos it watches: no new build.
	other := h.w.Host.Repo("gitlab.com/acme/web")
	other.Write(map[string]string{"README.md": "Web.\n"}).Commit("init")
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": strings.Replace(agentsFactory, "github.com/acme/api]", "github.com/acme/api, gitlab.com/acme/web]", 1)}).CommitPush("watch web")
	p = h.plan()
	if got := changes(p); !strings.Contains(got, "the repos it watches changed (adds gitlab.com/acme/web)") {
		t.Errorf("changes:\n%s", got)
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply: %s", h.out.String())
	}
	if d := sdk.Deployments()[0]; d.Deploys != 2 || strings.Join(d.EventRepos, ",") != "acme/api,acme/app,gitlab.com/acme/web" {
		t.Errorf("deployment after a new target = %+v", d)
	}
	if calls := strings.Join(sdk.Calls(), "\n"); !strings.Contains(calls, "deployment update acme_pr-reviewer --event-repo acme/api --event-repo acme/app --event-repo gitlab.com/acme/web") {
		t.Errorf("calls:\n%s", calls)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan after the target change: exit %d\n%s", p.ExitCode(), render(p))
	}

	// 6. Destroying the factory deletes the deployment, and the plan says,
	// loudly, that its hosted data goes with it.
	dp := h.destroyPlan()
	if text := render(dp); !strings.Contains(text, "- delete agent pr-reviewer on cursor-agents") || !strings.Contains(text, "CAN'T BE UNDONE") || !strings.Contains(text, "KV store") {
		t.Errorf("destroy plan:\n%s", text)
	}
	if _, err := h.destroy(nil); err != nil {
		t.Fatal(err)
	}
	if deps := sdk.Deployments(); len(deps) != 0 {
		t.Errorf("deployments after destroy = %+v", deps)
	}
}

func TestAgentSecretsApplyDoesntHave(t *testing.T) {
	h, sdk, _ := agentHarness(t)
	t.Setenv("LINEAR_TOKEN", "")
	res := h.apply()
	if res.ExitCode() != 0 {
		t.Fatalf("apply: %s", h.out.String())
	}
	if d := sdk.Deployments()[0]; len(d.Secrets) != 0 {
		t.Errorf("secrets = %v", d.Secrets)
	}
	// What's left to set up is in a file, and the plan keeps saying so.
	if len(res.Setups) != 1 {
		t.Fatalf("setups = %v\n%s", res.Setups, h.out.String())
	}
	setup, _ := os.ReadFile(res.Setups[0])
	if !strings.Contains(string(setup), "agent-sdk secret set acme_pr-reviewer LINEAR_TOKEN") {
		t.Errorf("setup:\n%s", setup)
	}
	p := h.plan()
	if text := render(p); p.ExitCode() != 0 || !strings.Contains(text, "agent pr-reviewer needs secrets that weren't set when it was last deployed (LINEAR_TOKEN)") {
		t.Errorf("plan: exit %d\n%s", p.ExitCode(), text)
	}
}

func TestAgentDeployFails(t *testing.T) {
	h, sdk, _ := agentHarness(t)
	t.Setenv(testkit.FakeAgentSDKFailEnv, "the build failed")
	res := h.apply()
	if res.ExitCode() == 0 || !strings.Contains(h.out.String(), "the deployment of acme_pr-reviewer ended failed: the build failed") {
		t.Fatalf("apply should fail: exit %d\n%s", res.ExitCode(), h.out.String())
	}
	// The failed deployment exists in Cursor, so the next plan adopts it and
	// deploys it again, rather than creating a second one.
	if len(sdk.Deployments()) != 1 {
		t.Fatalf("deployments = %+v", sdk.Deployments())
	}
	if _, err := os.Stat(filepath.Join(h.handof, "pr-review", reviewerSlug+"-alias-token")); err != nil {
		t.Error("the alias token should be kept even when the deploy fails")
	}
	t.Setenv(testkit.FakeAgentSDKFailEnv, "")
	p := h.plan()
	if got := changes(p); !strings.Contains(got, "= adopt  agent pr-reviewer on cursor-agents") || !strings.Contains(got, "~ update agent pr-reviewer on cursor-agents") {
		t.Fatalf("changes:\n%s", got)
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("apply: %s", h.out.String())
	}
	if d := sdk.Deployments()[0]; d.Status != "running" || d.Deploys != 2 {
		t.Errorf("deployment = %+v", d)
	}
}

func TestAgentsThroughHandoffs(t *testing.T) {
	h, sdk, commit := agentHarness(t)
	t.Setenv("CURSOR_API_KEY", "") // no key: a person deploys it
	p := h.plan()
	if got := changes(p); !strings.Contains(got, "+ create agent pr-reviewer on cursor-agents (handoff)") {
		t.Fatalf("changes:\n%s\n%s", got, render(p))
	}
	res := h.apply()
	if res.ExitCode() != 0 || len(res.Handoffs) != 1 {
		t.Fatalf("apply: exit %d, handoffs %v\n%s", res.ExitCode(), res.Handoffs, h.out.String())
	}
	text, _ := os.ReadFile(res.Handoffs[0])
	for _, want := range []string{
		"# Deploy Cursor agent `pr-reviewer` (deployment `acme_pr-reviewer`)",
		"git checkout " + commit,
		"agent-sdk deploy --slug acme_pr-reviewer --repo https://github.com/acme/blueprint --ref " + commit + " --path agents/pr-reviewer --cursor-events-repo acme/api --cursor-events-repo acme/app",
		"--remote-id acme_pr-reviewer",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("handoff is missing %q:\n%s", want, text)
		}
	}
	if len(sdk.Deployments()) != 0 {
		t.Error("a handoff deploys nothing by itself")
	}

	// Confirming records it like any other resource.
	pend := h.state().Pending
	if len(pend) != 1 || pend[0].Agent != "pr-reviewer" {
		t.Fatalf("pending = %+v", pend)
	}
	if _, err := apply.Confirm(t.Context(), h.w.Home.Dir, pend[0].ID, reviewerSlug, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := h.state()
	if len(s.Resources) != 1 || s.Resources[0].Agent != "pr-reviewer" || s.Resources[0].RemoteID != reviewerSlug {
		t.Fatalf("state = %+v", s)
	}
	if p := h.plan(); p.ExitCode() != 0 {
		t.Fatalf("plan after confirm: exit %d\n%s", p.ExitCode(), render(p))
	}
}

func TestAgentPlanErrors(t *testing.T) {
	h, _, _ := agentHarness(t)
	for _, tc := range []struct {
		factory, code string
	}{
		{strings.Replace(agentsFactory, "targets:", "agents: [nope]\ntargets:", 1), "factory.unknown-agent"},
		{strings.Replace(agentsFactory, "github.com/acme/api]", "github.com/acme/api, example.org/acme/x]", 1), "agent.target"},
		{strings.Replace(agentsFactory, "alias: acme", "alias: "+strings.Repeat("a", 60), 1), "agent.slug"},
	} {
		h.w.Home.Write(map[string]string{"factories/pr-review.yaml": tc.factory}).CommitPush("change")
		if p := h.plan(); !p.Factory("pr-review").Diags.HasCode(tc.code) {
			t.Errorf("want %s, got:\n%s", tc.code, render(p))
		}
	}
	// A factory of only agents doesn't need its repos to have the
	// Fabrica line: agents don't read it.
	h.w.App.Write(map[string]string{"AGENTS.md": "# App\n"}).Commit("no line")
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": agentsFactory}).CommitPush("back")
	if p := h.plan(); p.HasErrors() {
		t.Errorf("unexpected errors:\n%s", render(p))
	}
}
