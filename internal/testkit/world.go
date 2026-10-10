package testkit

import (
	"strings"
	"testing"
)

// FabricaLine is the Fabrica line, repeated here so testkit doesn't depend
// on the packages it helps test.
const FabricaLine = "Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line."

// Secret is a server secret for tests.
var Secret = strings.Repeat("test-secret-", 4)

// PRReviewRoutine is the routine from the design doc.
const PRReviewRoutine = `---
description: Review every new pull request
trigger: { on: github.pull_request.opened }
skills: [pr-review]
connectors: [github, slack?]
side_effects:
  - github.pr_comment
  - slack.notify: { channel: "{{params.slack_channel}}" }
params:
  slack_channel: { required: false, default: "#eng" }
---
Review the PR using the ` + "`pr-review`" + ` skill. Post one comment with your findings, then tell {{params.slack_channel}}.
`

// World is a small but complete Fabrica setup: a blueprint, two product
// repos, a home repo and the toy runtime.
type World struct {
	Host      *Host
	Blueprint *Repo
	App       *Repo
	API       *Repo
	Home      *HomeRepo
	Toy       *ToyRuntime
}

// BlueprintFiles are the blueprint's starting files.
func BlueprintFiles() map[string]string {
	files := map[string]string{
		"fabrica.yaml":                    "description: Acme's way of building software\nexpects: [test?]\n",
		"skills/pr-review/SKILL.md":       "---\nname: pr-review\ndescription: How we review pull requests.\n---\nRead the diff. Check the tests. See `checklist.md`.\n",
		"skills/pr-review/checklist.md":   "- Tests?\n- Docs?\n",
		"skills/pdf/SKILL.md":             "---\nname: pdf\ndescription: Pull text out of PDFs.\n---\nRun `scripts/extract.sh`.\n",
		"skills/pdf/scripts/extract.sh!x": "#!/bin/sh\necho extracted\n",
		"routines/pr-review.md":           PRReviewRoutine,
		"routines/review-before-push.md":  "---\ntrigger: { on: git.pre_push }\nskills: [pr-review]\n---\nReview your own diff with the `pr-review` skill before pushing.\n",
		"rules/tests-first.mdc":           "---\ndescription: Test-first workflow\nalwaysApply: true\n---\nWrite a failing test before changing behavior.\n",
		"rules/typescript.mdc":            "---\ndescription: TypeScript style\nglobs: \"*.ts\"\n---\nUse strict mode.\n",
		"connectors/github.yaml":          "kind: native\n",
		"connectors/slack.yaml":           "kind: native\n",
	}
	for k, v := range ToyProviderFiles {
		files[k] = v
	}
	return files
}

// NewWorld builds the world. The home repo is a team home repo unless you
// call MakePersonal.
func NewWorld(t *testing.T) *World {
	t.Helper()
	host := NewHost(t)
	w := &World{Host: host}
	w.Blueprint = host.Repo("github.com/acme/blueprint")
	w.Blueprint.Write(BlueprintFiles()).Commit("first blueprint")
	w.Blueprint.Branch("stable")
	w.App = host.Repo("github.com/acme/app")
	w.App.Write(map[string]string{"AGENTS.md": FabricaLine + "\n\n# App\n\nThis app uses pnpm.\n"}).Commit("init")
	w.API = host.Repo("github.com/acme/api")
	w.API.Write(map[string]string{"AGENTS.md": FabricaLine + "\n\n# API\n"}).Commit("init")
	w.Home = host.Home("github.com/acme/home")
	w.Toy = NewToyRuntime(t)
	t.Setenv("FABRICA_HOME", t.TempDir()) // someone else's personal home
	return w
}

// MakePersonal makes the world's home repo your personal home repo.
func (w *World) MakePersonal(t *testing.T) {
	t.Setenv("FABRICA_HOME", w.Home.Dir)
}

// PRReviewFactory is the cloud factory from the design doc, on the
// claude-code-cloud and toy runtimes.
const PRReviewFactory = `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, toy]
routines: [pr-review]
targets:
  - github.com/acme/app
  - repo: github.com/acme/api
    params: { slack_channel: "#api" }
params:
  slack_channel: "#eng"
`

// UpdateBlueprint commits changes to the blueprint and moves the stable
// branch to the new commit, like merging to a followed branch.
func (w *World) UpdateBlueprint(files map[string]string) string {
	commit := w.Blueprint.Write(files).Commit("blueprint change")
	w.Blueprint.Branch("stable")
	return commit
}
