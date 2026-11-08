package home

import (
	"strings"
	"testing"
)

func TestFactoryWithAgents(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		all        bool
		agents     string
	}{
		{"agents only, all of them", "runtimes: [cursor-agents]\n", true, ""},
		{"named agents", "runtimes: [cursor-agents]\nagents: [pr-reviewer, triage]\n", false, "pr-reviewer,triage"},
		{"wildcard", "runtimes: [cursor-agents]\nagents: [\"*\"]\n", true, ""},
		{"with routines", "runtimes: [cursor-cloud, cursor-agents]\nroutines: [pr-review]\nagents: [triage]\n", false, "triage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := "blueprint: { git: github.com/acme/blueprint, ref: main }\nserver: https://fabrica.acme.dev\ntargets: [github.com/acme/app]\n" + tc.yaml
			h := openHome(t, false, map[string]string{"factories/x.yaml": yaml})
			if h.Diags.HasErrors() {
				t.Fatal(h.Diags)
			}
			d := h.Factories["x"]
			if d.AllAgents != tc.all || strings.Join(d.Agents, ",") != tc.agents {
				t.Errorf("all = %v, agents = %v", d.AllAgents, d.Agents)
			}
			if d.Runs("pr-review", "cursor-agents") {
				t.Error("no routine runs on cursor-agents")
			}
			if !d.HasAgent("triage") {
				t.Error("the factory should deploy triage")
			}
		})
	}
	// Without an agents runtime, there are no agents.
	h := openHome(t, false, map[string]string{"factories/x.yaml": cloudFactory})
	if d := h.Factories["x"]; d.AllAgents || d.HasAgent("triage") {
		t.Error("a factory without cursor-agents deploys no agents")
	}
}

func TestFactoryAgentErrors(t *testing.T) {
	base := "blueprint: { git: github.com/acme/blueprint, ref: main }\nserver: https://fabrica.acme.dev\ntargets: [github.com/acme/app]\n"
	for name, tc := range map[string]struct{ yaml, contains string }{
		"agents without the runtime": {base + "runtimes: [cursor-cloud]\nroutines: [r]\nagents: [x]\n", "no runtime that deploys agents"},
		"empty agents":               {base + "runtimes: [cursor-agents]\nagents: []\n", "is empty"},
		"bad agent name":             {base + "runtimes: [cursor-agents]\nagents: [Bad]\n", "isn't valid"},
		"agent twice":                {base + "runtimes: [cursor-agents]\nagents: [x, x]\n", "twice"},
		"routines on agents only":    {base + "runtimes: [cursor-agents]\nroutines: [r]\n", "only deploy agents"},
		"routine pinned to agents":   {base + "runtimes: [cursor-cloud, cursor-agents]\nroutines: [{ routine: r, runtimes: [cursor-agents] }]\n", "runs no routines"},
		"local agents":               {"blueprint: { git: g.com/a/f, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\nagents: [x]\n", "only for cloud factories"},
	} {
		t.Run(name, func(t *testing.T) {
			h := openHome(t, true, map[string]string{"factories/x.yaml": tc.yaml})
			if !h.Diags.HasCode("factory.invalid") || !strings.Contains(h.Diags.String(), tc.contains) {
				t.Errorf("want a factory.invalid error containing %q, got:\n%s", tc.contains, h.Diags)
			}
		})
	}
}
