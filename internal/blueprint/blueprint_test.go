package blueprint_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/testkit"
)

// skill returns a minimal valid SKILL.md for a skill called name.
func skill(name, body string) string {
	return "---\nname: " + name + "\ndescription: The " + name + " skill.\n---\n" + body + "\n"
}

const prReviewRoutine = `---
trigger: { on: github.pull_request.opened }
skills: [pr-review]
connectors: [github, slack?]
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false }
---
Review the PR using the ` + "`pr-review`" + ` skill.
`

func newLoader(t *testing.T) *blueprint.Loader {
	t.Helper()
	return blueprint.NewLoader(gitx.NewCache(t.TempDir()))
}

func writeBlueprint(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, files)
	return dir
}

func mustLoad(t *testing.T, l *blueprint.Loader, dir string) *blueprint.Blueprint {
	t.Helper()
	f, diags := l.LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags.Only(diag.Error))
	}
	return f
}

func expectError(t *testing.T, diags diag.List, code, contains string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == diag.Error && d.Code == code && strings.Contains(d.String(), contains) {
			return
		}
	}
	t.Fatalf("want error %s containing %q, got:\n%s", code, contains, diags)
}

func TestLoadLocalBlueprint(t *testing.T) {
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml":                    "description: Acme's way\nexpects: [test, deploy?]\n",
		"skills/pr-review/SKILL.md":       skill("pr-review", "Look at the diff."),
		"skills/pdf/SKILL.md":             skill("pdf", "Run scripts/extract.py."),
		"skills/pdf/scripts/extract.py!x": "print('hi')\n",
		"routines/pr-review.md":           prReviewRoutine,
		"rules/tests-first.mdc":           "---\ndescription: Test-first workflow\nglobs:\nalwaysApply: true\n---\nWrite a failing test first.\n",
		"rules/typescript.mdc":            "---\ndescription:\nglobs: *.ts, *.tsx\nalwaysApply: false\n---\nUse strict mode.\n",
		"rules/by-name.mdc":               "Only when asked.\n",
		"connectors/github.yaml":          "kind: native\n",
		"connectors/slack.yaml":           "kind: mcp\nurl: https://mcp.slack.example/mcp\nauth: bearer\nsecret: SLACK_TOKEN\n",
	})
	f := mustLoad(t, newLoader(t), dir)

	if got := f.SkillNames(); strings.Join(got, ",") != "pdf,pr-review" {
		t.Errorf("skills = %v", got)
	}
	if f.Skills["pr-review"].TextOnly != true {
		t.Error("pr-review should be text-only")
	}
	if pdf := f.Skills["pdf"]; pdf.TextOnly || !strings.Contains(pdf.NotTextOnlyReason, "executable") {
		t.Errorf("pdf should not be text-only because of its script, got %v %q", pdf.TextOnly, pdf.NotTextOnlyReason)
	}

	r := f.Routines["pr-review"]
	if len(r.Triggers) != 1 || r.Triggers[0].On != "github.pull_request.opened" || len(r.SideEffects) != 2 || r.SideEffects[1].Args[0].Value != "{{params.slack_channel}}" {
		t.Errorf("routine parsed wrong: %+v", r)
	}
	if !r.Connectors[1].Optional || r.Connectors[1].Name != "slack" {
		t.Errorf("slack? should be optional: %+v", r.Connectors)
	}

	if a := f.Rules["tests-first"].Activation(); a != blueprint.ActiveAlways {
		t.Errorf("tests-first activation = %s", a)
	}
	if ts := f.Rules["typescript"]; ts.Activation() != blueprint.ActiveGlobs || strings.Join(ts.Globs, "|") != "*.ts|*.tsx" {
		t.Errorf("typescript rule = %+v (Cursor-style globs should parse even though they aren't valid YAML)", ts)
	}
	if a := f.Rules["by-name"].Activation(); a != blueprint.ActiveManual {
		t.Errorf("by-name activation = %s", a)
	}
	if s := f.Connectors["slack"].Server; s.Transport != "http" || s.Auth != "bearer" || s.Secret != "SLACK_TOKEN" {
		t.Errorf("slack connector = %+v", s)
	}
	if !f.Expects("deploy") || f.Expects("nope") {
		t.Error("Expects lookup is wrong")
	}
}

func TestStrictErrors(t *testing.T) {
	cases := []struct {
		name, code, contains string
		files                map[string]string
	}{
		{"unknown key in routine", "routine.invalid", `unknown key "trigers"`, map[string]string{
			"routines/x.md": "---\ntrigers: { on: schedule }\n---\nHi\n",
		}},
		{"missing skill", "routine.missing-skill", "skills/nope", map[string]string{
			"routines/x.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\nskills: [nope]\n---\nHi\n",
		}},
		{"side effect without connector", "routine.invalid", "isn't in this routine's connectors", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nside_effects: [github.pr_comment]\n---\nHi\n",
		}},
		{"undeclared param", "routine.invalid", "{{params.who}}", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\n---\nHi {{params.who}}\n",
		}},
		{"unknown effort", "routine.invalid", `effort "hihg" isn't one Fabrica knows`, map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: claude-opus-5-5, effort: hihg }]\n---\nHi\n",
		}},
		{"unknown key in a model", "routine.invalid", `unknown key "efort"`, map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: claude-opus-5-5, efort: high }]\n---\nHi\n",
		}},
		{"model after default", "routine.invalid", "put default last", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [default, gpt-5.5]\n---\nHi\n",
		}},
		{"model listed twice", "routine.invalid", "lists gpt-5.5 (high) twice", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: gpt-5.5, effort: high }, { model: gpt-5.5, effort: high }]\n---\nHi\n",
		}},
		{"default with an effort", "routine.invalid", "default can't have an effort", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: default, effort: high }]\n---\nHi\n",
		}},
		{"fast default", "routine.invalid", "default can't be fast", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: default, fast: true }]\n---\nHi\n",
		}},
		{"fast that isn't true or false", "routine.invalid", `fast is "yes"; use true or false`, map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [{ model: grok-4.7, fast: yes }]\n---\nHi\n",
		}},
		{"fast and not fast is the same entry twice", "routine.invalid", "lists grok-4.7 twice", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\nmodels: [grok-4.7, { model: grok-4.7, fast: false }]\n---\nHi\n",
		}},
		{"schedule without cron", "routine.invalid", "cron", map[string]string{
			"routines/x.md": "---\ntrigger: { on: schedule }\n---\nHi\n",
		}},
		{"trigger and triggers", "routine.invalid", "both trigger: and triggers:", map[string]string{
			"routines/x.md": "---\ntrigger: { on: github.push }\ntriggers: [{ on: github.push }]\n---\nHi\n",
		}},
		{"no triggers", "routine.invalid", "triggers: is empty", map[string]string{
			"routines/x.md": "---\ntriggers: []\n---\nHi\n",
		}},
		{"the same event twice", "routine.invalid", "lists schedule twice", map[string]string{
			"routines/x.md": "---\ntriggers: [{ on: schedule, cron: \"0 9 * * 1\" }, { on: schedule, cron: \"0 9 * * 5\" }]\n---\nHi\n",
		}},
		{"init with other triggers", "routine.invalid", "init can't share a routine", map[string]string{
			"routines/x.md": "---\ntriggers: [{ on: init }, { on: github.push }]\n---\nHi\n",
		}},
		{"a schedule without cron among several", "routine.invalid", "cron", map[string]string{
			"routines/x.md": "---\ntriggers: [{ on: github.push }, { on: schedule }]\n---\nHi\n",
		}},
		{"skill name mismatch", "skill.invalid", "needs them to match", map[string]string{
			"skills/a/SKILL.md": skill("b", ""),
		}},
		{"rule with .md extension", "rule.extension", "rename it", map[string]string{
			"rules/x.md": "Hi\n",
		}},
		{"rule with unknown key", "rule.invalid", `unknown key`, map[string]string{
			"rules/x.mdc": "---\nalwaysapply: true\n---\nHi\n",
		}},
		{"bearer without secret", "connector.invalid", "secret", map[string]string{
			"connectors/x.yaml": "kind: mcp\nurl: https://x.example\nauth: bearer\n",
		}},
		{"plain http url", "connector.invalid", "https", map[string]string{
			"connectors/x.yaml": "kind: mcp\nurl: http://x.example\n",
		}},
		{"reserved connector name", "connector.invalid", "taken", map[string]string{
			"connectors/fabrica.yaml": "kind: native\n",
		}},
		{"lock missing", "lock.missing", "fabrica lock", map[string]string{
			"fabrica.yaml": "imports:\n  base: { git: github.com/acme/base, ref: v1 }\n",
		}},
		{"only and exclude", "manifest.invalid", "not both", map[string]string{
			"fabrica.yaml": "imports:\n  base: { git: github.com/acme/base, ref: v1, only: [skills/a], exclude: [skills/b] }\n",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"fabrica.yaml": ""}
			for k, v := range c.files {
				files[k] = v
			}
			_, diags := newLoader(t).LoadDir(context.Background(), writeBlueprint(t, files), blueprint.LoadOptions{})
			expectError(t, diags, c.code, c.contains)
		})
	}
}

func TestEvalsFolderWarns(t *testing.T) {
	dir := writeBlueprint(t, map[string]string{"fabrica.yaml": "", "evals/README.md": "later"})
	_, diags := newLoader(t).LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	if !diags.HasCode("blueprint.evals") || diags.HasErrors() {
		t.Errorf("want only an evals warning, got:\n%s", diags)
	}
}

// importWorld sets up a fake host with a base blueprint and a plain skills
// repo, the way the design's fabrica.yaml example does.
type importWorld struct {
	host      *testkit.Host
	base      *testkit.Repo
	community *testkit.Repo
}

func newImportWorld(t *testing.T) *importWorld {
	host := testkit.NewHost(t)
	base := host.Repo("github.com/acme/base-blueprint")
	base.Write(map[string]string{
		"fabrica.yaml":              "expects: [test]\n",
		"skills/pr-review/SKILL.md": skill("pr-review", "Upstream review."),
		"skills/test/SKILL.md":      skill("test", "Default test skill."),
		"routines/pr-review.md":     prReviewRoutine,
		"connectors/github.yaml":    "kind: native\n",
		"connectors/slack.yaml":     "kind: native\n",
		"rules/tests-first.mdc":     "---\nalwaysApply: true\n---\nTests first.\n",
	}).Commit("v1")
	base.Tag("v1.2")

	community := host.Repo("github.com/someone/skills")
	community.Write(map[string]string{
		"skills/pdf/SKILL.md":       skill("pdf", "PDF things."),
		"skills/changelog/SKILL.md": skill("changelog", "Changelogs. Use the `pdf` skill for PDFs."),
		"skills/other/SKILL.md":     skill("other", "Other."),
		".cursor/rules/style.mdc":   "---\ndescription: Style\n---\nBe nice.\n",
		"AGENTS.md":                 "Always be kind.\n",
	}).Commit("init")
	return &importWorld{host: host, base: base, community: community}
}

func lockAndLoad(t *testing.T, l *blueprint.Loader, dir string) (*blueprint.Blueprint, diag.List) {
	t.Helper()
	lock, _, _, diags := l.Lock(context.Background(), dir)
	if lock != nil && !diags.HasErrors() {
		if err := blueprint.WriteLock(dir, lock); err != nil {
			t.Fatal(err)
		}
		return l.LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	}
	return nil, diags
}

func TestImportsWithFiltersAndPlainRepos(t *testing.T) {
	newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": `imports:
  base:
    git: github.com/acme/base-blueprint
    ref: v1.2
    exclude: [skills/pr-review]
  community:
    git: github.com/someone/skills
    ref: main
    path: skills
    only: [skills/pdf, skills/changelog, rules/agents-md]
expects: [test]
`,
		"skills/pr-review/SKILL.md": skill("pr-review", "Our own review."),
	})
	l := newLoader(t)
	f, diags := lockAndLoad(t, l, dir)
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags)
	}
	if got := strings.Join(f.SkillNames(), ","); got != "changelog,pdf,pr-review,test" {
		t.Errorf("skills = %s", got)
	}
	if f.Skills["pr-review"].Origin.Local() != true || !strings.Contains(f.Skills["pr-review"].Body, "Our own") {
		t.Error("our pr-review should override the imported one")
	}
	if len(f.Overrides) != 1 || f.Overrides[0].Ref.String() != "skills/pr-review" || f.Overrides[0].Alias != "base" {
		t.Errorf("overrides = %+v", f.Overrides)
	}
	// The imported routine now uses our pr-review skill, by name.
	if r := f.Routines["pr-review"]; r == nil || r.Origin.String() != "import base" {
		t.Errorf("routine pr-review should come from base, got %+v", r)
	}
	if rule := f.Rules["agents-md"]; rule == nil || !rule.AlwaysApply || !strings.Contains(rule.Body, "kind") {
		t.Errorf("AGENTS.md should come in as always-on rules/agents-md, got %+v", rule)
	}
	if _, ok := f.Rules["style"]; ok {
		t.Error("rules/style wasn't in only: and shouldn't be imported")
	}

	lockData, _ := os.ReadFile(filepath.Join(dir, "fabrica.lock"))
	if !strings.Contains(string(lockData), "commit: ") || !strings.Contains(string(lockData), "Don't edit by hand") {
		t.Errorf("lock file looks wrong:\n%s", lockData)
	}
}

func TestImportCollisionsAreErrors(t *testing.T) {
	w := newImportWorld(t)
	other := w.host.Repo("github.com/acme/other")
	other.Write(map[string]string{"skills/pdf/SKILL.md": skill("pdf", "Another pdf.")}).Commit("init")

	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": `imports:
  community: { git: github.com/someone/skills, ref: main }
  other: { git: github.com/acme/other, ref: main }
`,
	})
	_, diags := lockAndLoad(t, newLoader(t), dir)
	expectError(t, diags, "import.collision", `skills/pdf comes from more than one import: "community" and "other"`)

	// Defining an imported item yourself without excluding it is an error
	// too, with a hint on how to override properly.
	dir2 := writeBlueprint(t, map[string]string{
		"fabrica.yaml":        "imports:\n  community: { git: github.com/someone/skills, ref: main }\n",
		"skills/pdf/SKILL.md": skill("pdf", "Mine."),
	})
	_, diags = lockAndLoad(t, newLoader(t), dir2)
	expectError(t, diags, "import.collision", "add skills/pdf to the exclude: list of import community")
}

func TestOnlyDoesNotPullInDependencies(t *testing.T) {
	newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  base: { git: github.com/acme/base-blueprint, ref: v1.2, only: [routines/pr-review, connectors/github, connectors/slack] }\nexpects: [test]\n",
	})
	_, diags := lockAndLoad(t, newLoader(t), dir)
	expectError(t, diags, "routine.missing-skill", `add skills/pr-review to its only: list`)
}

func TestFilterTyposAreErrors(t *testing.T) {
	newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  community: { git: github.com/someone/skills, ref: main, exclude: [skills/pfd] }\n",
	})
	_, diags := lockAndLoad(t, newLoader(t), dir)
	expectError(t, diags, "import.unknown-item", "skills/pfd")
}

func TestStaleLockIsAnError(t *testing.T) {
	w := newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  community: { git: github.com/someone/skills, ref: main }\n",
	})
	l := newLoader(t)
	if _, diags := lockAndLoad(t, l, dir); diags.HasErrors() {
		t.Fatal(diags)
	}
	w.community.Branch("next")
	testkit.WriteFiles(t, dir, map[string]string{
		"fabrica.yaml": "imports:\n  community: { git: github.com/someone/skills, ref: next }\n",
	})
	_, diags := l.LoadDir(context.Background(), dir, blueprint.LoadOptions{})
	expectError(t, diags, "lock.stale", `import "community" changed`)
}

func TestImportExpectsWarning(t *testing.T) {
	newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  base: { git: github.com/acme/base-blueprint, ref: v1.2 }\n",
	})
	f, diags := lockAndLoad(t, newLoader(t), dir)
	if diags.HasErrors() || f == nil {
		t.Fatal(diags)
	}
	if !diags.HasCode("expects.uncovered") {
		t.Errorf("want an expects.uncovered warning, got:\n%s", diags)
	}
}

func TestBrokenItemOnlyMattersIfUsed(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/mixed")
	repo.Write(map[string]string{
		"fabrica.yaml":         "",
		"skills/good/SKILL.md": skill("good", "Fine."),
		"skills/bad/SKILL.md":  "no frontmatter at all\n",
	}).Commit("init")

	use := writeBlueprint(t, map[string]string{"fabrica.yaml": "imports:\n  mixed: { git: github.com/acme/mixed, ref: main }\n"})
	_, diags := lockAndLoad(t, newLoader(t), use)
	expectError(t, diags, "skill.invalid", "from import mixed")

	skip := writeBlueprint(t, map[string]string{"fabrica.yaml": "imports:\n  mixed: { git: github.com/acme/mixed, ref: main, exclude: [skills/bad] }\n"})
	if _, diags := lockAndLoad(t, newLoader(t), skip); diags.HasErrors() {
		t.Errorf("excluding the broken skill should make it go away, got:\n%s", diags)
	}
}

func TestNestedBlueprintUsesItsOwnLock(t *testing.T) {
	w := newImportWorld(t)
	middle := w.host.Repo("github.com/acme/middle")
	middle.Write(map[string]string{
		"fabrica.yaml":     "imports:\n  community: { git: github.com/someone/skills, ref: main, only: [skills/pdf] }\n",
		"rules/middle.mdc": "---\nalwaysApply: true\n---\nMiddle rule.\n",
	})
	// Lock the middle blueprint in its own repo, the way its author would.
	l := newLoader(t)
	lock, _, _, diags := l.Lock(context.Background(), middle.Dir)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if err := blueprint.WriteLock(middle.Dir, lock); err != nil {
		t.Fatal(err)
	}
	middle.Commit("init")

	top := writeBlueprint(t, map[string]string{"fabrica.yaml": "imports:\n  middle: { git: github.com/acme/middle, ref: main }\n"})
	f, diags := lockAndLoad(t, newLoader(t), top)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	pdf := f.Skills["pdf"]
	if pdf == nil || pdf.Origin.String() != "import middle › community" {
		t.Errorf("pdf should come through middle › community, got %+v", pdf)
	}
	if _, ok := f.Skills["changelog"]; ok {
		t.Error("middle's only: should keep changelog out")
	}
}

func TestUpgradeShowsDiffIncludingOverrides(t *testing.T) {
	w := newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml":              "imports:\n  base: { git: github.com/acme/base-blueprint, ref: main, exclude: [skills/pr-review] }\nexpects: [test]\n",
		"skills/pr-review/SKILL.md": skill("pr-review", "Ours."),
	})
	l := newLoader(t)
	if _, diags := lockAndLoad(t, l, dir); diags.HasErrors() {
		t.Fatal(diags)
	}

	// Upstream changes the skill we override, adds a skill and drops a rule.
	w.base.Write(map[string]string{
		"skills/pr-review/SKILL.md": skill("pr-review", "Upstream review, improved."),
		"skills/release/SKILL.md":   skill("release", "Ship it."),
		"rules/tests-first.mdc!rm":  "",
	}).Commit("v2")

	ctx := context.Background()
	lock, changes, _, diags := l.Upgrade(ctx, dir, nil)
	if diags.HasErrors() || lock == nil {
		t.Fatal(diags)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	d := changes[0].Diff
	if !refsEqual(d.Added, "skills/release") || !refsEqual(d.Removed, "rules/tests-first") ||
		!refsEqual(d.Changed, "skills/pr-review") || !refsEqual(d.Overridden, "skills/pr-review") {
		t.Errorf("diff = %+v", d)
	}
	out := blueprint.FormatChanges(changes)
	if !strings.Contains(out, "~ skills/pr-review   (you override this") {
		t.Errorf("formatted diff doesn't call out the override:\n%s", out)
	}

	// Plan-time drift: the lock still points at v1, upstream moved on.
	f := mustLoad(t, l, dir)
	drift, err := l.OverrideDrift(ctx, f)
	if err != nil || len(drift) != 1 || drift[0].Ref.String() != "skills/pr-review" {
		t.Errorf("OverrideDrift = %+v, %v", drift, err)
	}
}

// newRefactorRepo makes a blueprint with two routines whose params an
// importer might want to tune.
func newRefactorRepo(host *testkit.Host) *testkit.Repo {
	repo := host.Repo("github.com/acme/refactor")
	repo.Write(map[string]string{
		"fabrica.yaml":      "",
		"routines/split.md": "---\ntrigger: { on: github.push }\nparams:\n  split_proceed_threshold: { default: 11 }\n  owner: { required: true }\n---\nSplit files over {{params.split_proceed_threshold}} and tell {{params.owner}}.\n",
		"routines/cruft.md": "---\ntrigger: { on: github.push }\nparams:\n  cruft_proceed_threshold: { default: 5 }\n---\nClean up over {{params.cruft_proceed_threshold}}.\n",
	}).Commit("v1")
	return repo
}

func paramOf(t *testing.T, f *blueprint.Blueprint, routine, name string) blueprint.Param {
	t.Helper()
	r := f.Routines[routine]
	if r == nil {
		t.Fatalf("there's no routines/%s", routine)
	}
	p, ok := r.Param(name)
	if !ok {
		t.Fatalf("routines/%s has no param %s", routine, name)
	}
	return p
}

func TestImportParamsBecomeDefaults(t *testing.T) {
	newRefactorRepo(testkit.NewHost(t))
	l := newLoader(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  refactor:\n    git: github.com/acme/refactor\n    ref: main\n    params:\n      split_proceed_threshold: 20\n      owner: \"@platform\"\n",
	})
	f, diags := lockAndLoad(t, l, dir)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if p := paramOf(t, f, "split", "split_proceed_threshold"); p.Default != "20" || !p.HasDefault {
		t.Errorf("the import's value should be the new default, got %+v", p)
	}
	// A required param the import sets has a default now, so factories
	// don't have to set it.
	if p := paramOf(t, f, "split", "owner"); p.Required || p.Default != "@platform" {
		t.Errorf("owner should be optional with the import's default, got %+v", p)
	}
	if p := paramOf(t, f, "cruft", "cruft_proceed_threshold"); p.Default != "5" {
		t.Errorf("a param the import doesn't set keeps upstream's default, got %+v", p)
	}
	if len(f.Overrides) != 0 {
		t.Errorf("setting a param isn't an override, got %+v", f.Overrides)
	}

	// The loader shares the imported blueprint with everyone who imports it,
	// so another importer still sees upstream's defaults.
	other := writeBlueprint(t, map[string]string{"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main }\n"})
	g, diags := lockAndLoad(t, l, other)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if p := paramOf(t, g, "split", "split_proceed_threshold"); p.Default != "11" {
		t.Errorf("another importer should see upstream's default, got %+v", p)
	}
	if p := paramOf(t, g, "split", "owner"); !p.Required || p.HasDefault {
		t.Errorf("another importer should see owner as required, got %+v", p)
	}
}

func TestImportParamErrors(t *testing.T) {
	newRefactorRepo(testkit.NewHost(t))
	cases := []struct {
		name, imp, code, contains string
	}{
		{"typo", "params: { split_threshold: 20 }", "import.unknown-param",
			`import "refactor" sets param "split_threshold", but none of the routines it brings declares it
  hint: check the spelling; the routines this import brings declare cruft_proceed_threshold, owner, split_proceed_threshold`},
		{"excluded routine", "exclude: [routines/split], params: { split_proceed_threshold: 20 }", "import.unknown-param",
			"routines/split declares it, but exclude: leaves it out; if you replaced it with your own, set the default in your routines/split.md"},
		{"routine outside only", "only: [routines/cruft], params: { split_proceed_threshold: 20 }", "import.unknown-param",
			"routines/split declares it, but it isn't in the import's only: list"},
		{"a list", "params: { split_proceed_threshold: [20, 30] }", "manifest.invalid",
			"imports.refactor.params.split_proceed_threshold must be a single value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeBlueprint(t, map[string]string{
				"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main, " + c.imp + " }\n",
			})
			_, diags := lockAndLoad(t, newLoader(t), dir)
			expectError(t, diags, c.code, c.contains)
		})
	}
}

func TestNestedImportParamsStack(t *testing.T) {
	host := testkit.NewHost(t)
	newRefactorRepo(host)
	middle := host.Repo("github.com/acme/middle")
	writeMiddle := func(params string) {
		middle.Write(map[string]string{
			"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main, params: " + params + " }\n",
		})
		lock, _, _, diags := newLoader(t).Lock(context.Background(), middle.Dir)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		if err := blueprint.WriteLock(middle.Dir, lock); err != nil {
			t.Fatal(err)
		}
		middle.Commit("set params")
	}
	writeMiddle("{ split_proceed_threshold: 15, cruft_proceed_threshold: 4 }")

	top := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  middle: { git: github.com/acme/middle, ref: main, params: { split_proceed_threshold: 20 } }\n",
	})
	l := newLoader(t)
	f, diags := lockAndLoad(t, l, top)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if p := paramOf(t, f, "split", "split_proceed_threshold"); p.Default != "20" {
		t.Errorf("the outermost import should win, got %+v", p)
	}
	if p := paramOf(t, f, "cruft", "cruft_proceed_threshold"); p.Default != "4" {
		t.Errorf("middle's value should come through, got %+v", p)
	}
	if o := f.Routines["split"].Origin.String(); o != "import middle › refactor" {
		t.Errorf("split's origin = %s", o)
	}

	// When middle changes a default, the routine it offers changed, so
	// upgrading shows it; the routine whose params middle left alone didn't.
	writeMiddle("{ split_proceed_threshold: 16, cruft_proceed_threshold: 4 }")
	_, changes, _, diags := l.Upgrade(context.Background(), top, nil)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if len(changes) != 1 || !refsEqual(changes[0].Diff.Changed, "routines/split") {
		t.Errorf("changes = %+v", changes)
	}
}

func TestImportParamsDontNeedANewLock(t *testing.T) {
	newRefactorRepo(testkit.NewHost(t))
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main, params: { split_proceed_threshold: 20 } }\n",
	})
	l := newLoader(t)
	if _, diags := lockAndLoad(t, l, dir); diags.HasErrors() {
		t.Fatal(diags)
	}
	testkit.WriteFiles(t, dir, map[string]string{
		"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main, params: { split_proceed_threshold: 30, cruft_proceed_threshold: 2 } }\n",
	})
	f := mustLoad(t, l, dir)
	if p := paramOf(t, f, "split", "split_proceed_threshold"); p.Default != "30" {
		t.Errorf("the new value should apply without a new lock, got %+v", p)
	}
}

func TestUpgradeIgnoresImportParams(t *testing.T) {
	repo := newRefactorRepo(testkit.NewHost(t))
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml": "imports:\n  refactor: { git: github.com/acme/refactor, ref: main, params: { split_proceed_threshold: 20 } }\n",
	})
	l := newLoader(t)
	if _, diags := lockAndLoad(t, l, dir); diags.HasErrors() {
		t.Fatal(diags)
	}

	// Upstream changes something else; routines/split is as it was.
	repo.Write(map[string]string{"routines/cruft.md": "---\ntrigger: { on: github.push }\n---\nClean up.\n"}).Commit("v2")
	ctx := context.Background()
	_, changes, _, diags := l.Upgrade(ctx, dir, nil)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if d := changes[0].Diff; !refsEqual(d.Changed, "routines/cruft") || len(d.Overridden) != 0 {
		t.Errorf("only routines/cruft changed upstream, got %+v", d)
	}
	f := mustLoad(t, l, dir)
	if drift, err := l.OverrideDrift(ctx, f); err != nil || len(drift) != 0 {
		t.Errorf("a routine with a param set isn't an override, got %+v, %v", drift, err)
	}
}

func TestLintMentions(t *testing.T) {
	newImportWorld(t)
	dir := writeBlueprint(t, map[string]string{
		"fabrica.yaml":           "imports:\n  community: { git: github.com/someone/skills, ref: main, only: [skills/changelog] }\n",
		"skills/review/SKILL.md": skill("review", "Then run the `changelog` skill."),
		"routines/r.md":          "---\ntrigger: { on: github.push }\n---\nUse /review and then the changelog skill.\n",
	})
	l := newLoader(t)
	_, _, f, diags := l.Lock(context.Background(), dir)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	_ = f
	// changelog mentions `pdf`, which community has but we left out.
	if !diags.HasCode("skill.mentions-missing") {
		t.Errorf("want a skill.mentions-missing warning, got:\n%s", diags)
	}
	if !diags.HasCode("routine.unlisted-skill") {
		t.Errorf("want a routine.unlisted-skill warning, got:\n%s", diags)
	}
}

func TestMentions(t *testing.T) {
	for text, want := range map[string]bool{
		"run the `test` skill":      true,
		"Use /changelog now":        true,
		"use the changelog skill":   true,
		"$changelog":                true,
		"we test things":            false,
		"/changelogs are different": false,
	} {
		name := "test"
		if strings.Contains(text, "changelog") {
			name = "changelog"
		}
		if got := blueprint.Mentions(text, name); got != want {
			t.Errorf("Mentions(%q, %q) = %v, want %v", text, name, got, want)
		}
	}
}

func TestParamRefs(t *testing.T) {
	refs, err := blueprint.ParamRefs("a {{params.x}} b {{ params.y }} {{params.x}}")
	if err != nil || strings.Join(refs, ",") != "x,y" {
		t.Errorf("ParamRefs = %v, %v", refs, err)
	}
	if _, err := blueprint.ParamRefs("{{params.x}"); err == nil {
		t.Error("malformed placeholder should fail")
	}
	if got := blueprint.RenderParams("#{{params.c}}!", map[string]string{"c": "eng"}); got != "#eng!" {
		t.Errorf("RenderParams = %q", got)
	}
}

func refsEqual(refs []names.Ref, want ...string) bool {
	if len(refs) != len(want) {
		return false
	}
	for i := range refs {
		if refs[i].String() != want[i] {
			return false
		}
	}
	return true
}

func TestRoutineWithSeveralTriggers(t *testing.T) {
	f := mustLoad(t, newLoader(t), writeBlueprint(t, map[string]string{
		"fabrica.yaml":       "",
		"routines/review.md": "---\ntriggers:\n  - { on: github.pull_request.opened }\n  - { on: schedule, cron: \" 0  9 * * 1\" }\n---\nReview.\n",
	}))
	r := f.Routines["review"]
	want := []blueprint.Trigger{{On: "github.pull_request.opened"}, {On: "schedule", Cron: "0 9 * * 1"}}
	if !reflect.DeepEqual(r.Triggers, want) {
		t.Errorf("Triggers = %+v, want %+v", r.Triggers, want)
	}
	if !r.HasTrigger("schedule") || r.HasTrigger("github.push") || r.IsInit() {
		t.Errorf("HasTrigger or IsInit is wrong for %+v", r.Triggers)
	}
	if got := blueprint.DescribeTriggers(r.Triggers); got != "github.pull_request.opened, schedule (0 9 * * 1)" {
		t.Errorf("DescribeTriggers = %q", got)
	}
}

func TestInitRoutines(t *testing.T) {
	const init = "---\ntrigger: { on: init }\nruntimes: [cursor-cloud, claude-code-local]\n---\nGet the repos ready.\n"
	f := mustLoad(t, newLoader(t), writeBlueprint(t, map[string]string{
		"fabrica.yaml":     "",
		"routines/init.md": init,
	}))
	r := f.Init()
	if r == nil || !r.IsInit() || strings.Join(r.Runtimes, ",") != "cursor-cloud,claude-code-local" {
		t.Fatalf("Init = %+v", r)
	}
	if got := r.Triggers[0].String(); got != "init (once, when the factory is created)" {
		t.Errorf("Trigger = %q", got)
	}

	for name, c := range map[string]struct {
		files       map[string]string
		code, about string
	}{
		"runtimes on a triggered routine": {map[string]string{"routines/r.md": "---\ntrigger: { on: schedule, cron: \"0 9 * * 1\" }\nruntimes: [toy]\n---\nGo.\n"}, "routine.invalid", "runtimes: is only for init routines"},
		"memory on an init":               {map[string]string{"routines/r.md": "---\ntrigger: { on: init }\nmemory: true\n---\nGo.\n"}, "routine.invalid", "nothing to remember"},
		"a renamed runtime":               {map[string]string{"routines/r.md": "---\ntrigger: { on: init }\nruntimes: [cursor]\n---\nGo.\n"}, "routine.invalid", "cursor was renamed to cursor-cloud"},
		"a runtime twice":                 {map[string]string{"routines/r.md": "---\ntrigger: { on: init }\nruntimes: [codex, codex]\n---\nGo.\n"}, "routine.invalid", "twice"},
		"an unknown runtime":              {map[string]string{"routines/r.md": "---\ntrigger: { on: init }\nruntimes: [acme-runner]\n---\nGo.\n"}, "init.unknown-runtime", "no providers/acme-runner"},
		"two inits":                       {map[string]string{"routines/a.md": init, "routines/b.md": init}, "init.ambiguous", "2 init routines (routines/a.md, routines/b.md)"},
	} {
		files := map[string]string{"fabrica.yaml": ""}
		for k, v := range c.files {
			files[k] = v
		}
		f, diags := newLoader(t).LoadDir(context.Background(), writeBlueprint(t, files), blueprint.LoadOptions{})
		t.Run(name, func(t *testing.T) { expectError(t, diags, c.code, c.about) })
		if f != nil && name == "two inits" && f.Init() != nil {
			t.Errorf("with two inits, Init should be nil")
		}
	}
}
