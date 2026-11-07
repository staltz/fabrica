package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func newTestAgentSDK(t *testing.T) (*agentSDKAPI, *testkit.FakeAgentSDK) {
	t.Helper()
	sdk := testkit.NewFakeAgentSDK(t)
	api, err := newAgentSDKAPI()
	if err != nil {
		t.Fatal(err)
	}
	return api.(*agentSDKAPI), sdk
}

func TestAgentSDKNeedsKeyAndCLI(t *testing.T) {
	testkit.NewFakeAgentSDK(t)
	p, _ := Builtin("cursor-agents")
	if mode, err := p.Mode(); err != nil || mode != API {
		t.Fatalf("with the key and the CLI: %s, %v", mode, err)
	}
	t.Setenv(AgentSDKBinEnv, filepath.Join(t.TempDir(), "missing"))
	if mode, _ := p.Mode(); mode != Handoff || !strings.Contains(p.APIUnavailable().Error(), "doesn't exist") {
		t.Errorf("without the CLI: %s, %v", mode, p.APIUnavailable())
	}
	t.Setenv(AgentSDKBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	if why := p.APIUnavailable(); why == nil || !strings.Contains(why.Error(), "npm install --global @grok/agent-sdk") {
		t.Errorf("without agent-sdk on PATH: %v", why)
	}
	testkit.NewFakeAgentSDK(t)
	t.Setenv(CursorKeyEnv, "")
	if mode, _ := p.Mode(); mode != Handoff {
		t.Errorf("without the key: %s", mode)
	}
	// The service-account key works too.
	t.Setenv(CursorServiceAccountKeyEnv, "key_service")
	if mode, _ := p.Mode(); mode != API {
		t.Errorf("with the service-account key: %s", mode)
	}
}

func TestAgentSDKListsItsOwnDeployments(t *testing.T) {
	api, sdk := newTestAgentSDK(t)
	t.Setenv(testkit.FakeAgentSDKPageEnv, "2") // so listing takes several pages
	for _, slug := range []string{"k7q2_pr-reviewer", "k7q2_triage", "zzzz_other", "weather-agent", "k7q2_a_b"} {
		sdk.Put(testkit.FakeDeployment{Slug: slug})
	}
	got, err := api.List(context.Background(), "x", "k7q2#")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.Identity+"="+r.RemoteID)
	}
	if strings.Join(ids, ",") != "k7q2#agents/pr-reviewer=k7q2_pr-reviewer,k7q2#agents/triage=k7q2_triage" {
		t.Errorf("listed %v", ids)
	}
	// A prefix from before tags never had agents.
	if got, err := api.List(context.Background(), "x", "fabrica.acme.dev/x#"); err != nil || len(got) != 0 {
		t.Errorf("legacy prefix listed %v, %v", got, err)
	}
}

func TestAgentSDKDeleteAndErrors(t *testing.T) {
	api, sdk := newTestAgentSDK(t)
	sdk.Put(testkit.FakeDeployment{Slug: "k7q2_x"})
	ctx := context.Background()
	if err := api.Delete(ctx, "f", "k7q2_x"); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(ctx, "f", "k7q2_x"); err != nil {
		t.Errorf("deleting what's already gone is fine, got %v", err)
	}
	err := api.Update(ctx, "k7q2_y", Resource{Identity: "k7q2#agents/y", Agent: &ResourceAgent{Slug: "k7q2_y", EventRepos: []string{"acme/app"}}})
	if err == nil || !strings.Contains(err.Error(), "agent-sdk deployment update failed: There's no deployment k7q2_y.") {
		t.Errorf("update of a missing deployment: %v", err)
	}
	if err := api.Update(ctx, "other", Resource{Identity: "k7q2#agents/y", Agent: &ResourceAgent{Slug: "k7q2_y"}}); err == nil {
		t.Error("an update under another slug should fail")
	}
	if _, err := api.Create(ctx, Resource{Identity: "k7q2#pr-review@github.com/acme/app"}); err == nil {
		t.Error("creating a routine's stub should fail")
	}
}

func TestAgentSDKInstallsFromTheWorkspaceLockfile(t *testing.T) {
	api, sdk := newTestAgentSDK(t)
	root := t.TempDir()
	files := testkit.AgentProject("agents/bot", "bot")
	delete(files, "agents/bot/package-lock.json")
	files["package-lock.json"] = "{}"
	files["agents/bot/node_modules/stale.js"] = "x" // not copied
	testkit.WriteFiles(t, root, files)
	t.Setenv("LINEAR_TOKEN", "lin")
	res := Resource{Identity: "k7q2#agents/bot", Agent: &ResourceAgent{
		Name: "bot", Slug: "k7q2_bot", Repo: "https://github.com/acme/mono", Ref: "abc123", Path: "agents/bot",
		EventRepos: []string{"acme/app"}, Redeploy: true, Dir: filepath.Join(root, "agents/bot"), InstallDir: root,
	}}
	id, err := api.Create(context.Background(), res)
	if err != nil || id != "k7q2_bot" {
		t.Fatalf("create = %q, %v", id, err)
	}
	d := sdk.Deployments()[0]
	if d.Repo != "https://github.com/acme/mono" || d.Ref != "abc123" || d.Path != "agents/bot" || d.Secrets["LINEAR_TOKEN"] != "lin" {
		t.Errorf("deployment = %+v", d)
	}
	report, ok := api.AgentReport("k7q2#agents/bot")
	if !ok || strings.Join(report.Secrets, ",") != "LINEAR_TOKEN" || len(report.UnsetSecrets) != 0 || report.AliasToken != "alias_k7q2_bot" {
		t.Errorf("report = %+v", report)
	}
	// The scratch copy is gone, and the original was left alone.
	if _, err := os.Stat(filepath.Join(root, "node_modules", ".fake-installed")); err == nil {
		t.Error("the install should happen in a scratch copy")
	}
}

func TestInstallCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := installCommand(dir); err == nil {
		t.Error("no lockfile should be an error")
	}
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644)
	t.Setenv("PATH", t.TempDir())
	if _, err := installCommand(dir); err == nil || !strings.Contains(err.Error(), "pnpm isn't installed") {
		t.Errorf("pnpm without pnpm: %v", err)
	}
}
