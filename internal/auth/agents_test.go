package auth

import (
	"context"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/testkit"
)

func TestAgentSecretsFromState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FABRICA_HOME", t.TempDir())
	testkit.WriteFiles(t, dir, map[string]string{
		"factories/bots.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nserver: https://fabrica.acme.dev\nruntimes: [cursor-agents]\ntargets: [github.com/acme/app]\n",
		"state/bots.yaml": `resources:
  - id: acme#agents/pr-reviewer
    runtime: cursor-agents
    agent: pr-reviewer
    remote_id: acme_pr-reviewer
    mode: api
    blueprint_commit: abc
    shape:
      connectors: []
      agent: { git: "https://github.com/acme/blueprint", commit: abc, hash: h, event_repos: [acme/app], secrets: [LINEAR_TOKEN, SLACK_TOKEN], unset_secrets: [SLACK_TOKEN] }
      fingerprint: f
    applied_at: 2026-10-10T12:00:00Z
pending: []
`,
	})
	h, err := home.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cache := gitx.NewCache(t.TempDir())
	r := Find(context.Background(), &Env{Git: cache, Loader: blueprint.NewLoader(cache)}, h)
	if len(r.AgentSecrets) != 2 {
		t.Fatalf("agent secrets = %+v", r.AgentSecrets)
	}
	linear, slack := r.AgentSecrets[0], r.AgentSecrets[1]
	if linear.Name != "LINEAR_TOKEN" || linear.Unset || linear.Slug != "acme_pr-reviewer" || linear.Factory != "bots" {
		t.Errorf("LINEAR_TOKEN = %+v", linear)
	}
	t.Setenv("SLACK_TOKEN", "xoxb")
	if slack.Name != "SLACK_TOKEN" || !slack.Unset || !slack.InEnv() {
		t.Errorf("SLACK_TOKEN = %+v", slack)
	}
	// The key cursor-agents' API needs is a runtime credential, like
	// cursor-cloud's.
	if needs := r.Lookup("cursor-agents"); len(needs) != 1 || needs[0].Name != "CURSOR_API_KEY" {
		t.Errorf("cursor-agents needs %+v", needs)
	}
}
