package home

import (
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/testkit"
)

func openHome(t *testing.T, personal bool, files map[string]string) *Home {
	t.Helper()
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, files)
	if personal {
		t.Setenv("FABRICA_HOME", dir)
	} else {
		t.Setenv("FABRICA_HOME", t.TempDir())
	}
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

const cloudDeployment = `factory: { git: github.com/acme/factory, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude, cursor]
routines: [pr-review]
repos:
  - github.com/acme/app
  - repo: git@github.com:acme/api.git
    params: { slack_channel: "#api" }
params:
  slack_channel: "#eng"
`

func TestCloudDeployment(t *testing.T) {
	h := openHome(t, false, map[string]string{"deployments/pr-review.yaml": cloudDeployment})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	d := h.Deployments["pr-review"]
	if d.Scope != provider.Cloud || d.Endpoint() != "https://fabrica.acme.dev/pr-review" {
		t.Errorf("deployment = %+v", d)
	}
	if got := strings.Join(d.SpecificRepos(), ","); got != "github.com/acme/app,github.com/acme/api" {
		t.Errorf("repos = %s (git remotes should be normalized)", got)
	}
	if d.ParamsFor("github.com/acme/api")["slack_channel"] != "#api" || d.ParamsFor("github.com/acme/app")["slack_channel"] != "#eng" {
		t.Error("per-repo params should override the deployment's")
	}
}

func TestDeploymentErrors(t *testing.T) {
	cases := map[string]string{
		"mixed runtimes":     "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude, claude-code]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"no server":          "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"cloud wildcard":     "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude]\nroutines: [r]\nrepos: [\"*\"]\n",
		"http server":        "factory: { git: g.com/a/f, ref: main }\nserver: http://x.dev\nruntimes: [claude]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"no routines":        "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude]\nrepos: [github.com/a/b]\n",
		"wildcard not alone": "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code]\nrepos: [\"*\", github.com/a/b]\n",
		"unknown key":        "factory: { git: g.com/a/f, ref: main }\nruntime: [claude-code]\nrepos: [\"*\"]\n",
		"bad repo":           "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code]\nrepos: [app]\n",
		"no ref":             "factory: { git: g.com/a/f }\nruntimes: [claude-code]\nrepos: [\"*\"]\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			h := openHome(t, true, map[string]string{"deployments/x.yaml": yaml})
			if !h.Diags.HasCode("deployment.invalid") {
				t.Errorf("want a deployment.invalid error, got:\n%s", h.Diags)
			}
		})
	}
}

func TestLocalDeploymentsOnlyInPersonalHome(t *testing.T) {
	local := "factory: { git: github.com/me/factory, ref: main }\nruntimes: [claude-code, cursor-ide]\nrepos: [\"*\"]\n"
	if h := openHome(t, true, map[string]string{"deployments/my-default.yaml": local}); h.Diags.HasErrors() {
		t.Errorf("a local deployment is fine in the personal home:\n%s", h.Diags)
	}
	h := openHome(t, false, map[string]string{"deployments/my-default.yaml": local})
	if !h.Diags.HasErrors() || !strings.Contains(h.Diags.String(), "personal home repo") {
		t.Errorf("a local deployment in a team home repo should be an error:\n%s", h.Diags)
	}
}

func TestMatchLocal(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"deployments/my-default.yaml": "factory: { git: github.com/me/factory, ref: main }\nruntimes: [claude-code, cursor-ide]\nrepos: [\"*\"]\n",
		"deployments/oss-work.yaml":   "factory: { git: github.com/me/oss-factory, ref: main }\nruntimes: [claude-code]\nrepos: [github.com/oss/project]\nroutines: [review-before-push]\n",
	})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	if d, _ := h.MatchLocal("github.com/oss/project", "claude-code"); d == nil || d.Name != "oss-work" {
		t.Errorf("a specific repo should beat the wildcard, got %v", d)
	}
	if d, _ := h.MatchLocal("github.com/oss/project", "cursor-ide"); d == nil || d.Name != "my-default" {
		t.Errorf("oss-work doesn't run on cursor-ide, so the wildcard should match, got %v", d)
	}
	if d, reason := h.MatchLocal("github.com/x/y", "codex-cli"); d != nil || !strings.Contains(reason, "no local deployment") {
		t.Errorf("nothing covers codex-cli, got %v %q", d, reason)
	}
}

func TestLocalOverlapIsAnError(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"deployments/a.yaml": "factory: { git: github.com/me/a, ref: main }\nruntimes: [claude-code]\nrepos: [\"*\"]\n",
		"deployments/b.yaml": "factory: { git: github.com/me/b, ref: main }\nruntimes: [claude-code]\nrepos: [\"*\"]\n",
		"deployments/c.yaml": "factory: { git: github.com/me/c, ref: main }\nruntimes: [claude-code]\nrepos: [github.com/x/y]\n",
		"deployments/d.yaml": "factory: { git: github.com/me/d, ref: main }\nruntimes: [claude-code]\nrepos: [github.com/x/y]\n",
	})
	if !h.Diags.HasCode("home.wildcard-overlap") || !h.Diags.HasCode("home.local-overlap") {
		t.Errorf("want both overlap errors, got:\n%s", h.Diags)
	}
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code"); d != nil || !strings.Contains(reason, "exactly one factory") {
		t.Errorf("an ambiguous match must not pick one, got %v %q", d, reason)
	}
}

func TestBrokenFileBlocksLocalMatching(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"deployments/a.yaml": "factory: { git: github.com/me/a, ref: main }\nruntimes: [claude-code]\nrepos: [\"*\"]\n",
		"deployments/b.yaml": "this is: [not valid\n",
	})
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code"); d != nil || !strings.Contains(reason, "deployments/b.yaml") {
		t.Errorf("a broken deployment file might cover the repo, so matching must refuse, got %v %q", d, reason)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		Lock: &Lock{Holder: "ci github.com/acme/home/actions/runs/1", Since: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		Resources: []Resource{{
			ID: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Runtime: "claude", Routine: "pr-review",
			Repo: "github.com/acme/app", RemoteID: "trig_1", Mode: "handoff", FactoryCommit: strings.Repeat("a", 40),
			Shape: Shape{Trigger: "github.pull_request.opened", Connectors: []string{"github"}, Fingerprint: "7f3a9c7f3a9c"},
		}},
	}
	if err := WriteState(dir, "pr-review", s); err != nil {
		t.Fatal(err)
	}
	back, err := ReadState(dir, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	if back.Lock.Holder != s.Lock.Holder || len(back.Resources) != 1 || back.Resources[0].Shape.Fingerprint != "7f3a9c7f3a9c" {
		t.Errorf("round trip = %+v", back)
	}
	if empty, err := ReadState(dir, "missing"); err != nil || !empty.Empty() {
		t.Errorf("a missing state file should read as empty, got %+v, %v", empty, err)
	}
}
