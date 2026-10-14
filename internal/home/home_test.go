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

const cloudFactory = `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines: [pr-review]
targets:
  - github.com/acme/app
  - repo: git@github.com:acme/api.git
    params: { slack_channel: "#api" }
params:
  slack_channel: "#eng"
`

func TestCloudFactory(t *testing.T) {
	h := openHome(t, false, map[string]string{"factories/pr-review.yaml": cloudFactory})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	d := h.Factories["pr-review"]
	if d.Scope != provider.Cloud || d.Endpoint() != "https://fabrica.acme.dev/pr-review" {
		t.Errorf("factory = %+v", d)
	}
	if got := strings.Join(d.SpecificTargets(), ","); got != "github.com/acme/app,github.com/acme/api" {
		t.Errorf("repos = %s (git remotes should be normalized)", got)
	}
	if d.ParamsFor("github.com/acme/api")["slack_channel"] != "#api" || d.ParamsFor("github.com/acme/app")["slack_channel"] != "#eng" {
		t.Error("per-repo params should override the factory's")
	}
}

func TestFactoryErrors(t *testing.T) {
	cases := map[string]string{
		"mixed runtimes":     "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, claude-code-local]\nroutines: [r]\ntargets: [github.com/a/b]\n",
		"no server":          "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-cloud]\nroutines: [r]\ntargets: [github.com/a/b]\n",
		"cloud wildcard":     "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud]\nroutines: [r]\ntargets: [\"*\"]\n",
		"http server":        "blueprint: { git: g.com/a/f, ref: main }\nserver: http://x.dev\nruntimes: [claude-code-cloud]\nroutines: [r]\ntargets: [github.com/a/b]\n",
		"no routines":        "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud]\ntargets: [github.com/a/b]\n",
		"wildcard not alone": "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\", github.com/a/b]\n",
		"unknown key":        "blueprint: { git: g.com/a/f, ref: main }\nruntime: [claude-code-local]\ntargets: [\"*\"]\n",
		"bad repo":           "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\ntargets: [app]\n",
		"no ref":             "blueprint: { git: g.com/a/f }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
		"local init":         "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\ninit: false\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			h := openHome(t, true, map[string]string{"factories/x.yaml": yaml})
			if !h.Diags.HasCode("factory.invalid") {
				t.Errorf("want a factory.invalid error, got:\n%s", h.Diags)
			}
		})
	}
}

func TestRoutineOnSomeRuntimes(t *testing.T) {
	yaml := `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines:
  - pr-review
  - routine: release-notes
    runtimes: [claude-code-cloud]
targets: [github.com/acme/app]
`
	h := openHome(t, false, map[string]string{"factories/x.yaml": yaml})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	d := h.Factories["x"]
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

func TestAllRoutinesWithExceptions(t *testing.T) {
	yaml := `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines:
  - { routine: "*", runtimes: [claude-code-cloud] }
  - { routine: release-notes, runtimes: [cursor-cloud] }
  - { routine: weekly-report, runtimes: [] }
targets: [github.com/acme/app]
`
	h := openHome(t, false, map[string]string{"factories/x.yaml": yaml})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	d := h.Factories["x"]
	if !d.AllRoutines || strings.Join(d.Routines, ",") != "release-notes,weekly-report" {
		t.Fatalf("all = %v, routines = %v", d.AllRoutines, d.Routines)
	}
	for _, c := range []struct {
		routine, runtime string
		want             bool
	}{
		{"pr-review", "claude-code-cloud", true}, // through the wildcard
		{"pr-review", "cursor-cloud", false},
		{"release-notes", "cursor-cloud", true}, // its own entry wins
		{"release-notes", "claude-code-cloud", false},
		{"weekly-report", "claude-code-cloud", false}, // left out
		{"weekly-report", "cursor-cloud", false},
	} {
		if got := d.Runs(c.routine, c.runtime); got != c.want {
			t.Errorf("Runs(%s, %s) = %v, want %v", c.routine, c.runtime, got, c.want)
		}
	}

	// A bare "*" runs everything everywhere.
	h = openHome(t, false, map[string]string{"factories/x.yaml": strings.Replace(yaml,
		"  - { routine: \"*\", runtimes: [claude-code-cloud] }\n  - { routine: release-notes, runtimes: [cursor-cloud] }\n  - { routine: weekly-report, runtimes: [] }\n",
		"  - \"*\"\n", 1)})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	if d := h.Factories["x"]; !d.Runs("anything", "cursor-cloud") || !d.Runs("anything", "claude-code-cloud") || len(d.Routines) != 0 {
		t.Errorf("routines: [\"*\"] should run every routine on every runtime: %+v", d)
	}
}

func TestRoutineRuntimesErrors(t *testing.T) {
	head := "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, cursor-cloud]\ntargets: [github.com/a/b]\n"
	cases := map[string]string{
		"runtimes of r: codex isn't in the factory's runtimes":        "routines: [{ routine: r, runtimes: [codex] }]\n",
		"runtimes of r: claude was renamed to claude-code-cloud":      "routines: [{ routine: r, runtimes: [claude] }]\n",
		"runtimes of r is empty":                                      "routines: [{ routine: r, runtimes: [] }]\n",
		`runtimes of r lists "cursor-cloud" twice`:                    "routines: [{ routine: r, runtimes: [cursor-cloud, cursor-cloud] }]\n",
		`unknown key "runtime"`:                                       "routines: [{ routine: r, runtime: [cursor-cloud] }]\n",
		"a routine entry needs a routine key":                         "routines: [{ runtimes: [cursor-cloud] }]\n",
		`routines: lists "r" twice`:                                   "routines: [r, { routine: r, runtimes: [cursor-cloud] }]\n",
		"runtimes: lists claude-code-cloud, but none of the routines": "routines: [{ routine: r, runtimes: [cursor-cloud] }]\n",
		`runtimes of "*" is empty`:                                    "routines: [{ routine: \"*\", runtimes: [] }]\n",
		`routines: lists "*" twice`:                                   "routines: [\"*\", { routine: \"*\", runtimes: [cursor-cloud] }]\n",
		"runtimes: lists cursor-cloud, but none of the routines":      "routines: [{ routine: \"*\", runtimes: [claude-code-cloud] }]\n",
	}
	for want, routines := range cases {
		h := openHome(t, false, map[string]string{"factories/x.yaml": head + routines})
		if got := h.Diags.String(); !h.Diags.HasCode("factory.invalid") || !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
	}
}

func TestFactoryWithOldRuntimeName(t *testing.T) {
	cases := map[string]string{
		"runtimes: claude-code was renamed to claude-code-local": "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code]\ntargets: [\"*\"]\n",
		"runtimes: claude was renamed to claude-code-cloud":      "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude, cursor-cloud]\nroutines: [r]\ntargets: [github.com/a/b]\n",
		"runtimes: cursor-ide was renamed to cursor-local":       "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local, cursor-ide]\ntargets: [\"*\"]\n",
		"runtimes: cursor was renamed to cursor-cloud":           "blueprint: { git: g.com/a/f, ref: main }\nserver: https://x.dev\nruntimes: [claude-code-cloud, cursor]\nroutines: [r]\ntargets: [github.com/a/b]\n",
	}
	for want, yaml := range cases {
		h := openHome(t, true, map[string]string{"factories/x.yaml": yaml})
		if got := h.Diags.String(); len(h.Diags) != 1 || !strings.Contains(got, want) {
			t.Errorf("want only %q, got:\n%s", want, got)
		}
	}
}

func TestLocalFactoriesOnlyInPersonalHome(t *testing.T) {
	local := "blueprint: { git: github.com/me/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\n"
	if h := openHome(t, true, map[string]string{"factories/my-default.yaml": local}); h.Diags.HasErrors() {
		t.Errorf("a local factory is fine in the personal home:\n%s", h.Diags)
	}
	h := openHome(t, false, map[string]string{"factories/my-default.yaml": local})
	if !h.Diags.HasErrors() || !strings.Contains(h.Diags.String(), "personal home repo") {
		t.Errorf("a local factory in a team home repo should be an error:\n%s", h.Diags)
	}
}

func TestMatchLocal(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/me/blueprint, ref: main }\nruntimes: [claude-code-local, cursor-local]\ntargets: [\"*\"]\n",
		"factories/oss-work.yaml":   "blueprint: { git: github.com/me/oss-blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/oss/project]\nroutines: [review-before-push]\n",
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
	if d, reason := h.MatchLocal("github.com/x/y", "codex-cli"); d != nil || !strings.Contains(reason, "no local factory") {
		t.Errorf("nothing covers codex-cli, got %v %q", d, reason)
	}
}

func TestLocalOverlapIsAnError(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"factories/a.yaml": "blueprint: { git: github.com/me/a, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
		"factories/b.yaml": "blueprint: { git: github.com/me/b, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
		"factories/c.yaml": "blueprint: { git: github.com/me/c, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/x/y]\n",
		"factories/d.yaml": "blueprint: { git: github.com/me/d, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/x/y]\n",
	})
	if !h.Diags.HasCode("home.wildcard-overlap") || !h.Diags.HasCode("home.local-overlap") {
		t.Errorf("want both overlap errors, got:\n%s", h.Diags)
	}
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code-local"); d != nil || !strings.Contains(reason, "exactly one blueprint") {
		t.Errorf("an ambiguous match must not pick one, got %v %q", d, reason)
	}
}

func TestCopilotCloudOverlapIsAnError(t *testing.T) {
	cloud := func(blueprint, runtimes, targets string) string {
		return "blueprint: { git: github.com/me/" + blueprint + ", ref: main }\nserver: https://x.dev\nruntimes: " + runtimes + "\nroutines: [r]\ntargets: " + targets + "\n"
	}
	h := openHome(t, false, map[string]string{
		"factories/a.yaml": cloud("a", "[copilot-cloud, claude-code-cloud]", "[github.com/x/y, github.com/x/z]"),
		"factories/b.yaml": cloud("b", "[copilot-cloud]", "[github.com/x/y]"),
		// Other cloud runtimes keep their MCP servers per routine, so
		// overlapping there is fine.
		"factories/c.yaml": cloud("c", "[claude-code-cloud]", "[github.com/x/z]"),
	})
	var overlaps []string
	for _, d := range h.Diags {
		if d.Code == "home.copilot-overlap" {
			overlaps = append(overlaps, d.Message)
		}
	}
	if len(overlaps) != 1 || !strings.Contains(overlaps[0], "factories a and b both deploy to github.com/x/y on copilot-cloud") {
		t.Errorf("want one copilot-cloud overlap error for github.com/x/y, got:\n%s", h.Diags)
	}
}

func TestBrokenFileBlocksLocalMatching(t *testing.T) {
	h := openHome(t, true, map[string]string{
		"factories/a.yaml": "blueprint: { git: github.com/me/a, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
		"factories/b.yaml": "this is: [not valid\n",
	})
	if d, reason := h.MatchLocal("github.com/x/y", "claude-code-local"); d != nil || !strings.Contains(reason, "factories/b.yaml") {
		t.Errorf("a broken factory file might cover the repo, so matching must refuse, got %v %q", d, reason)
	}
}

func TestFactoryInit(t *testing.T) {
	h := openHome(t, false, map[string]string{"factories/pr-review.yaml": cloudFactory, "factories/no-init.yaml": cloudFactory + "init: false\n", "factories/init.yaml": cloudFactory + "init: true\n"})
	if h.Diags.HasErrors() {
		t.Fatal(h.Diags)
	}
	if h.Factories["pr-review"].NoInit || h.Factories["init"].NoInit || !h.Factories["no-init"].NoInit {
		t.Errorf("only init: false turns the init off")
	}
}

func TestStateInit(t *testing.T) {
	dir := t.TempDir()
	if s, _ := ReadState(dir, "x"); s.Born() {
		t.Error("a factory with no state was never applied")
	}
	s := &State{Init: &Init{ID: "i_1", Routine: "onboard", Runtime: "toy", Repo: "github.com/acme/app", Mode: "api", Status: InitStarted, Session: "s1", At: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}}
	if !s.Born() || !s.Empty() {
		t.Errorf("an init record means the factory was applied, but it's nothing to clean up: born=%v empty=%v", s.Born(), s.Empty())
	}
	destroyed := &State{Blueprint: &StateBlueprint{Git: "github.com/acme/blueprint", Commit: strings.Repeat("a", 40)}, DestroyedAt: &s.Init.At}
	if destroyed.Born() {
		t.Error("a destroyed factory doesn't exist, whatever state still remembers about it")
	}
	if err := WriteState(dir, "x", s); err != nil {
		t.Fatal(err)
	}
	back, err := ReadState(dir, "x")
	if err != nil || back.Init == nil || *back.Init != *s.Init {
		t.Errorf("round trip = %+v, %v", back.Init, err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		Lock: &Lock{Holder: "ci github.com/acme/home/actions/runs/1", Since: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		Resources: []Resource{{
			ID: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app", Runtime: "claude-code-cloud", Routine: "pr-review",
			Repo: "github.com/acme/app", RemoteID: "trig_1", Mode: "handoff", BlueprintCommit: strings.Repeat("a", 40),
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
	yaml := "blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-cloud]\nroutines: [r]\ntargets: [github.com/a/b]\n"
	for _, personal := range []bool{false, true} {
		h := openHome(t, personal, map[string]string{"factories/dogfood.yaml": yaml})
		got := h.Diags.String()
		for _, want := range []string{"claude-code-cloud", "factories/dogfood.yaml", "server: https://", "fabrica mcp --http", "mcp-server.md#running-the-remote-server"} {
			if !strings.Contains(got, want) {
				t.Errorf("personal=%v: want %q in:\n%s", personal, want, got)
			}
		}
		if strings.Contains(got, "claude-code-local") != personal {
			t.Errorf("personal=%v: local runtimes should only be suggested in the personal home repo:\n%s", personal, got)
		}
	}
}
