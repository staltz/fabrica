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
runtimes: [claude-code-cloud, cursor-cloud]
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
		"mixed runtimes":     "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, claude-code-local]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"no server":          "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-cloud]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"cloud wildcard":     "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud]\nroutines: [r]\nrepos: [\"*\"]\n",
		"http server":        "factory: { git: g.com/a/f, ref: main }\nserver: http://x.dev\nruntimes: [claude-code-cloud]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"no routines":        "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud]\nrepos: [github.com/a/b]\n",
		"wildcard not alone": "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\", github.com/a/b]\n",
		"unknown key":        "factory: { git: g.com/a/f, ref: main }\nruntime: [claude-code-local]\nrepos: [\"*\"]\n",
		"bad repo":           "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\nrepos: [app]\n",
		"no ref":             "factory: { git: g.com/a/f }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\n",
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

func TestRoutineOnSomeRuntimes(t *testing.T) {
	yaml := `factory: { git: github.com/acme/factory, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines:
  - pr-review
  - routine: release-notes
    runtimes: [claude-code-cloud]
repos: [github.com/acme/app]
`
	h := openHome(t, false, map[string]string{"deployments/x.yaml": yaml})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	d := h.Deployments["x"]
	if !d.Runs("pr-review", "cursor-cloud") || !d.Runs("release-notes", "claude-code-cloud") || d.Runs("release-notes", "cursor-cloud") {
		t.Errorf("routine runtimes = %v", d.RoutineRuntimes)
	}
	if got := strings.Join(d.RoutinesOn("cursor-cloud"), ","); got != "pr-review" {
		t.Errorf("routines on cursor-cloud = %s", got)
	}
	if got := strings.Join(d.Routines, ","); got != "pr-review,release-notes" {
		t.Errorf("routines = %s", got)
	}
}

func TestRoutineRuntimesErrors(t *testing.T) {
	head := "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, cursor-cloud]\nrepos: [github.com/a/b]\n"
	cases := map[string]string{
		"runtimes of r: codex isn't in the deployment's runtimes":     "routines: [{ routine: r, runtimes: [codex] }]\n",
		"runtimes of r: claude was renamed to claude-code-cloud":      "routines: [{ routine: r, runtimes: [claude] }]\n",
		"runtimes of r is empty":                                      "routines: [{ routine: r, runtimes: [] }]\n",
		`runtimes of r lists "cursor-cloud" twice`:                    "routines: [{ routine: r, runtimes: [cursor-cloud, cursor-cloud] }]\n",
		`unknown key "runtime"`:                                       "routines: [{ routine: r, runtime: [cursor-cloud] }]\n",
		"a routine entry needs a routine key":                         "routines: [{ runtimes: [cursor-cloud] }]\n",
		`routines: lists "r" twice`:                                   "routines: [r, { routine: r, runtimes: [cursor-cloud] }]\n",
		"runtimes: lists claude-code-cloud, but none of the routines": "routines: [{ routine: r, runtimes: [cursor-cloud] }]\n",
	}
	for want, routines := range cases {
		h := openHome(t, false, map[string]string{"deployments/x.yaml": head + routines})
		if got := h.Diags.String(); !h.Diags.HasCode("deployment.invalid") || !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
	}
}

func TestDeploymentWithOldRuntimeName(t *testing.T) {
	cases := map[string]string{
		"runtimes: claude-code was renamed to claude-code-local": "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code]\nrepos: [\"*\"]\n",
		"runtimes: claude was renamed to claude-code-cloud":      "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude, cursor-cloud]\nroutines: [r]\nrepos: [github.com/a/b]\n",
		"runtimes: cursor-ide was renamed to cursor-local":       "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local, cursor-ide]\nrepos: [\"*\"]\n",
		"runtimes: cursor was renamed to cursor-cloud":           "factory: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, cursor]\nroutines: [r]\nrepos: [github.com/a/b]\n",
	}
	for want, yaml := range cases {
		h := openHome(t, true, map[string]string{"deployments/x.yaml": yaml})
		if got := h.Diags.String(); len(h.Diags) != 1 || !strings.Contains(got, want) {
			t.Errorf("want only %q, got:\n%s", want, got)
		}
	}
}

func TestLocalDeploymentsOnlyInPersonalHome(t *testing.T) {
	local := "factory: { git: github.com/me/factory, ref: main }\nruntimes: [claude-code-local, cursor-local]\nrepos: [\"*\"]\n"
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
		"deployments/my-default.yaml": "factory: { git: github.com/me/factory, ref: main }\nruntimes: [claude-code-local, cursor-local]\nrepos: [\"*\"]\n",
		"deployments/oss-work.yaml":   "factory: { git: github.com/me/oss-factory, ref: main }\nruntimes: [claude-code-local]\nrepos: [github.com/oss/project]\nroutines: [review-before-push]\n",
	})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	if d, _ := h.MatchLocal("github.com/oss/project", "claude-code-local"); d == nil || d.Name != "oss-work" {
		t.Errorf("a specific repo should beat the wildcard, got %v", d)
	}
	if d, _ := h.MatchLocal("github.com/oss/project", "cursor-local"); d == nil || d.Name != "my-default" {
		t.Errorf("oss-work doesn't run on cursor-local, so the wildcard should match, got %v", d)
	}
	if d, reason := h.MatchLocal("github.com/x/y", "codex-cli"); d != nil || !strings.Contains(reason, "no local deployment") {
		t.Errorf("nothing covers codex-cli, got %v %q", d, reason)
	}
}

func TestLocalOverlapIsAnError(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"deployments/a.yaml": "factory: { git: github.com/me/a, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\n",
		"deployments/b.yaml": "factory: { git: github.com/me/b, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\n",
		"deployments/c.yaml": "factory: { git: github.com/me/c, ref: main }\nruntimes: [claude-code-local]\nrepos: [github.com/x/y]\n",
		"deployments/d.yaml": "factory: { git: github.com/me/d, ref: main }\nruntimes: [claude-code-local]\nrepos: [github.com/x/y]\n",
	})
	if !h.Diags.HasCode("home.wildcard-overlap") || !h.Diags.HasCode("home.local-overlap") {
		t.Errorf("want both overlap errors, got:\n%s", h.Diags)
	}
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code-local"); d != nil || !strings.Contains(reason, "exactly one factory") {
		t.Errorf("an ambiguous match must not pick one, got %v %q", d, reason)
	}
}

func TestBrokenFileBlocksLocalMatching(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"deployments/a.yaml": "factory: { git: github.com/me/a, ref: main }\nruntimes: [claude-code-local]\nrepos: [\"*\"]\n",
		"deployments/b.yaml": "this is: [not valid\n",
	})
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code-local"); d != nil || !strings.Contains(reason, "deployments/b.yaml") {
		t.Errorf("a broken deployment file might cover the repo, so matching must refuse, got %v %q", d, reason)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		Lock: &Lock{Holder: "ci github.com/acme/home/actions/runs/1", Since: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		Resources: []Resource{{
			ID: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Runtime: "claude-code-cloud", Routine: "pr-review",
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

func TestStateMovesRenamedRuntimes(t *testing.T) {
	dir := t.TempDir()
	// What Fabrica v0.1 wrote, when claude-code-cloud was called claude.
	shape := Shape{Trigger: "github.pull_request.opened", Connectors: []string{"github"}, Fingerprint: "7f3a9c7f3a9c"}
	pendingShape := shape
	s := &State{
		Resources: []Resource{{ID: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Runtime: "claude", Routine: "pr-review", Repo: "github.com/acme/app", Mode: "handoff", Shape: shape}},
		Pending:   []Pending{{ID: "h_0123456789", Action: "create", Resource: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/api", Runtime: "claude", Routine: "pr-review", Repo: "github.com/acme/api", Shape: &pendingShape}},
	}
	if err := WriteState(dir, "pr-review", s); err != nil {
		t.Fatal(err)
	}
	back, err := ReadState(dir, "pr-review")
	if err != nil {
		t.Fatal(err)
	}
	r, p := back.Resources[0], back.Pending[0]
	if r.Runtime != "claude-code-cloud" || r.RenamedFrom != "claude" || r.Shape.Fingerprint != "" || p.Runtime != "claude-code-cloud" || p.Shape.Fingerprint != "" {
		t.Errorf("v0.1 state should move to the new name and forget its stubs' fingerprints: %+v, %+v", r, p)
	}
}

func TestMissingServerSaysWhatToDo(t *testing.T) {
	yaml := "factory: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-cloud]\nroutines: [r]\nrepos: [github.com/a/b]\n"
	for _, personal := range []bool{false, true} {
		h := openHome(t, personal, map[string]string{"deployments/dogfood.yaml": yaml})
		got := h.Diags.String()
		for _, want := range []string{"claude-code-cloud", "deployments/dogfood.yaml", "server: https://", "fabrica mcp --http", "mcp-server.md#running-the-remote-server"} {
			if !strings.Contains(got, want) {
				t.Errorf("personal=%v: want %q in:\n%s", personal, want, got)
			}
		}
		if strings.Contains(got, "claude-code-local") != personal {
			t.Errorf("personal=%v: local runtimes should only be suggested in the personal home repo:\n%s", personal, got)
		}
	}
}
