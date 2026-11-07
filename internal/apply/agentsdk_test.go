package apply_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// fakeAgentSDKScript stands in for the agent-sdk command: each deployment
// is a file in $AGENT_SDK_STATE, and each secret one next to it.
const fakeAgentSDKScript = `#!/bin/sh
state="$AGENT_SDK_STATE"
echo "$*" >> "$state/calls"
case "$1 $2" in
  "deploy --dir")
    slug=""
    while [ $# -gt 0 ]; do [ "$1" = "--slug" ] && slug="$2"; shift; done
    touch "$state/$slug"; echo '{"status":"running"}' ;;
  "secret set") cat > "$state/$3.$4" ; echo '{}' ;;
  "deployment delete")
    [ -f "$state/$3" ] || { echo '{"error":{"code":"not_found","message":"no such deployment"}}'; exit 4; }
    rm -f "$state/$3" "$state/$3".*; echo '{}' ;;
  "deployment list")
    printf '{"deployments":['; sep=""
    for f in "$state"/fab-*; do
      [ -f "$f" ] || continue
      case "$f" in *.*) continue ;; esac
      printf '%s{"slug":"%s"}' "$sep" "$(basename "$f")"; sep=","
    done
    printf '],"nextPageToken":null}\n' ;;
  *) echo '{"error":{"code":"usage","message":"unexpected command"}}'; exit 2 ;;
esac
`

func TestCursorAgentSDKThroughItsCommandLine(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-sdk")
	if err := os.WriteFile(bin, []byte(fakeAgentSDKScript), 0o755); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv(provider.AgentSDKBinEnv, bin)
	t.Setenv("AGENT_SDK_STATE", state)
	t.Setenv(provider.AgentSDKRepoEnv, "")
	t.Setenv(provider.CursorKeyEnv, "crsr_test")
	t.Setenv("FABRICA_CACHE", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "gh_secret")
	// Like a real home repo, its remote is on GitHub, and Fabrica deploys
	// from a branch of it.
	testkit.Git(t, h.w.Home.Dir, "remote", "set-url", "origin", "https://github.com/acme/home.git")

	// GitHub needs its fallback MCP server here; Slack is optional, and
	// without one the routine runs without it.
	h.w.UpdateBlueprint(map[string]string{
		"connectors/github.yaml": "kind: native\nfallback: { url: \"https://api.githubcopilot.com/mcp/\", transport: http, auth: bearer, secret: GITHUB_TOKEN }\n",
	})
	factory := strings.Replace(testkit.PRReviewFactory, "runtimes: [claude-code-cloud, toy]", "runtimes: [cursor-agent-sdk]", 1)
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": factory}).CommitPush("add pr-review on the Agent SDK")

	p := h.plan()
	text := render(p)
	if !strings.Contains(text, "+ create pr-review @ github.com/acme/app on cursor-agent-sdk (api)") {
		t.Fatalf("plan:\n%s", text)
	}

	res := h.apply()
	if res.ExitCode() != 0 || len(res.Handoffs) != 0 {
		t.Fatalf("apply: exit %d, handoffs %v\n%s", res.ExitCode(), res.Handoffs, h.out.String())
	}
	tag := stub.Tag("https://fabrica.acme.dev", "pr-review")
	app := provider.AgentSDKSlug(stub.Identity(tag, "pr-review", "github.com/acme/app"), "pr-review")
	api := provider.AgentSDKSlug(stub.Identity(tag, "pr-review", "github.com/acme/api"), "pr-review")
	for _, slug := range []string{app, api} {
		if _, err := os.Stat(filepath.Join(state, slug)); err != nil {
			t.Errorf("%s wasn't deployed", slug)
		}
		if tok, _ := os.ReadFile(filepath.Join(state, slug+".FABRICA_TOKEN")); string(tok) != token.For(testkit.Secret, "pr-review") {
			t.Errorf("%s's FABRICA_TOKEN = %q", slug, tok)
		}
		if gh, _ := os.ReadFile(filepath.Join(state, slug+".GITHUB_TOKEN")); string(gh) != "gh_secret" {
			t.Errorf("%s's GITHUB_TOKEN = %q", slug, gh)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(state, "calls"))
	if !strings.Contains(string(calls), "--repo https://github.com/acme/home --ref ") || !strings.Contains(string(calls), "--cursor-events-repo acme/app") {
		t.Errorf("agent-sdk calls:\n%s", calls)
	}

	// The projects are on their own branch of the home repo's remote; the
	// home repo's branch only got state.
	branch := "refs/heads/" + provider.AgentSDKBranch
	files := testkit.Git(t, h.w.Home.Bare, "ls-tree", "-r", "--name-only", branch)
	instructions := testkit.Git(t, h.w.Home.Bare, "show", branch+":"+app+"/bot/instructions.md")
	if !strings.Contains(files, "deployed/"+app+".json") || !strings.Contains(instructions, "runtime `cursor-agent-sdk`, repo `github.com/acme/app`") {
		t.Errorf("branch files:\n%s\ninstructions:\n%s", files, instructions)
	}
	if strings.Contains(testkit.Git(t, h.w.Home.Bare, "ls-tree", "-r", "--name-only", "main"), "bot/") {
		t.Error("the projects ended up on the home repo's own branch")
	}
	s := h.state()
	if len(s.Resources) != 2 || s.Resources[0].RemoteID != api || s.Resources[0].Mode != "api" {
		t.Fatalf("state = %+v", s.Resources)
	}

	// Planning again lists the deployments back: in sync.
	if p := h.plan(); p.ExitCode() != 0 || p.HasChanges() {
		t.Fatalf("second plan: exit %d\n%s", p.ExitCode(), render(p))
	}

	// A deployment deleted by hand is put back.
	os.Remove(filepath.Join(state, app))
	p = h.plan()
	if c := p.Factory("pr-review").Changes; len(c) != 1 || c[0].Repo != "github.com/acme/app" {
		t.Fatalf("plan after a hand delete:\n%s", render(p))
	}
	if res := h.apply(); res.ExitCode() != 0 {
		t.Fatalf("repair: %s", h.out.String())
	}

	// Destroy deletes them, and their projects.
	if res, err := h.destroy(nil); err != nil || res.ExitCode() != 0 {
		t.Fatalf("destroy: %v\n%s", err, h.out.String())
	}
	for _, slug := range []string{app, api} {
		if _, err := os.Stat(filepath.Join(state, slug)); err == nil {
			t.Errorf("%s is still deployed", slug)
		}
	}
	if files := testkit.Git(t, h.w.Home.Bare, "ls-tree", "-r", "--name-only", branch); strings.TrimSpace(files) != "README.md" {
		t.Errorf("branch after destroy:\n%s", files)
	}
}
