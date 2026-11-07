package blueprint_test

import (
	"context"
	"maps"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/testkit"
)

func withFiles(base map[string]string, more map[string]string) map[string]string {
	out := maps.Clone(base)
	maps.Copy(out, more)
	return out
}

func TestAgentsInABlueprint(t *testing.T) {
	files := withFiles(testkit.AgentProject("agents/pr-reviewer", "@acme/reviewer"), map[string]string{
		"fabrica.yaml": "",
		// npm install puts node_modules next to the code, but git doesn't
		// have it, so a working folder can have one.
		"agents/pr-reviewer/node_modules/zod/index.js": "x",
	})
	dir := writeBlueprint(t, files)
	f := mustLoad(t, newLoader(t), dir)
	a := f.Agents["pr-reviewer"]
	if a == nil {
		t.Fatalf("agents = %v", blueprint.SortedKeys(f.Agents))
	}
	if a.File != "agents/pr-reviewer" || a.Hash == "" || a.Source.Git != "" || a.Source.Path != "agents/pr-reviewer" {
		t.Errorf("agent = %+v", a)
	}
	if _, ok := f.Item(names.Ref{Kind: names.Agents, Name: "pr-reviewer"}); !ok {
		t.Error("Item should find agents/pr-reviewer")
	}

	// node_modules isn't part of the agent's content.
	files["agents/pr-reviewer/node_modules/zod/index.js"] = "y"
	if b := mustLoad(t, newLoader(t), writeBlueprint(t, files)).Agents["pr-reviewer"]; b.Hash != a.Hash {
		t.Error("node_modules changed the agent's hash")
	}
	files["agents/pr-reviewer/bot/instructions.md"] = "Something else.\n"
	if b := mustLoad(t, newLoader(t), writeBlueprint(t, files)).Agents["pr-reviewer"]; b.Hash == a.Hash {
		t.Error("changing instructions should change the agent's hash")
	}
}

func TestBrokenAgents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    map[string]string
		contains string
	}{
		{"no package.json", map[string]string{"package.json!rm": ""}, "no package.json"},
		{"not the SDK", map[string]string{"package.json": `{"name": "x", "dependencies": {"zod": "1"}}`}, "doesn't depend on @grok/agent-sdk"},
		{"former name", map[string]string{"package.json": `{"name": "x", "dependencies": {"@cursor/bdk": "1"}}`}, "former name"},
		{"bad json", map[string]string{"package.json": `{`}, "isn't valid JSON"},
		{"no bot", map[string]string{"bot!rm": ""}, "no bot/ folder"},
		{"no instructions", map[string]string{"bot/instructions.md!rm": ""}, "has no instructions"},
		{"no lockfile", map[string]string{"package-lock.json!rm": ""}, "no lockfile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBlueprint(t, map[string]string{"fabrica.yaml": ""})
			// A .git stops the lockfile search at the blueprint's folder.
			testkit.WriteFiles(t, dir, map[string]string{".git/HEAD": "ref: refs/heads/main\n"})
			testkit.WriteFiles(t, dir+"/agents/x", testkit.AgentProject("", "x"))
			testkit.WriteFiles(t, dir+"/agents/x", tc.files)
			_, diags := newLoader(t).LoadDir(context.Background(), dir, blueprint.LoadOptions{})
			expectError(t, diags, "agent.invalid", tc.contains)
		})
	}

	t.Run("bad name", func(t *testing.T) {
		dir := writeBlueprint(t, testkit.AgentProject("agents/Bad_Name", "x"))
		testkit.WriteFiles(t, dir, map[string]string{"fabrica.yaml": ""})
		_, diags := newLoader(t).LoadDir(context.Background(), dir, blueprint.LoadOptions{})
		expectError(t, diags, "agent.invalid", "isn't a valid agent name")
	})

	t.Run("lockfile at the workspace root", func(t *testing.T) {
		dir := writeBlueprint(t, withFiles(testkit.AgentProject("agents/x", "x"), map[string]string{
			"fabrica.yaml":                  "",
			"agents/x/package-lock.json!rm": "",
			"package-lock.json":             "{}",
		}))
		mustLoad(t, newLoader(t), dir)
	})
}

func TestImportAgents(t *testing.T) {
	host := testkit.NewHost(t)
	// A blueprint with an agent of its own.
	bp := host.Repo("github.com/acme/base-blueprint")
	bp.Write(withFiles(testkit.AgentProject("agents/triage", "triage"), map[string]string{"fabrica.yaml": ""})).Commit("init")
	// A plain repo that is one Agent SDK project, named after its package.
	single := host.Repo("github.com/acme/pr-reviewer")
	singleCommit := single.Write(testkit.AgentProject("", "@acme/pr-reviewer")).Commit("init")
	// A plain repo with a folder of several projects.
	multi := host.Repo("github.com/acme/agents")
	multiCommit := multi.Write(withFiles(testkit.AgentProject("agents/weather", "weather-bot"),
		withFiles(testkit.AgentProject("agents/oncall", "oncall"), map[string]string{"agents/README.md": "Agents.\n"}))).Commit("init")

	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": `imports:
  base: { git: github.com/acme/base-blueprint, ref: main }
  reviewer: { git: github.com/acme/pr-reviewer, ref: main }
  fleet: { git: github.com/acme/agents, ref: main, path: agents, only: [agents/weather] }
`,
	})
	f, diags := lockAndLoad(t, newLoader(t), dir)
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags)
	}
	got := blueprint.SortedKeys(f.Agents)
	if len(got) != 3 || got[0] != "pr-reviewer" || got[1] != "triage" || got[2] != "weather" {
		t.Fatalf("agents = %v", got)
	}
	if s := f.Agents["pr-reviewer"].Source; s.Git != "github.com/acme/pr-reviewer" || s.Commit != singleCommit || s.Path != "" {
		t.Errorf("pr-reviewer source = %+v", s)
	}
	if s := f.Agents["weather"].Source; s.Git != "github.com/acme/agents" || s.Commit != multiCommit || s.Path != "agents/weather" {
		t.Errorf("weather source = %+v", s)
	}
	if s := f.Agents["triage"].Source; s.Git != "github.com/acme/base-blueprint" || s.Path != "agents/triage" || s.Commit == "" {
		t.Errorf("triage source = %+v", s)
	}
	if o := f.Agents["weather"].Origin.String(); o != "import fleet" {
		t.Errorf("weather origin = %s", o)
	}

	// Defining an agent an import brings too is a collision.
	testkit.WriteFiles(t, dir, testkit.AgentProject("agents/triage", "triage"))
	_, diags = newLoader(t).LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	expectError(t, diags, "import.collision", "agents/triage")
}

func TestAgentsFromGit(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/monorepo")
	commit := repo.Write(withFiles(testkit.AgentProject("factory/agents/pr-reviewer", "pr-reviewer"), map[string]string{
		"factory/fabrica.yaml": "",
	})).Commit("init")

	f, diags := newLoader(t).LoadGit(context.Background(), "github.com/acme/monorepo", commit, "factory")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags)
	}
	s := f.Agents["pr-reviewer"].Source
	if s.Git != "github.com/acme/monorepo" || s.Commit != commit || s.Path != "factory/agents/pr-reviewer" {
		t.Errorf("source = %+v", s)
	}

	// A committed node_modules is an error.
	commit = repo.Write(map[string]string{"factory/agents/pr-reviewer/node_modules/x.js": "x"}).Commit("oops")
	_, diags = newLoader(t).LoadGit(context.Background(), "github.com/acme/monorepo", commit, "factory")
	expectError(t, diags, "agent.invalid", "node_modules/ is committed")
}
