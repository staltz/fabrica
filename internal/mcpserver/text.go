package mcpserver

import (
	"fmt"
	"strings"

	"github.com/staltz/fabrica/internal/fabline"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/stub"
)

// productOnlyRule is the one rule every factory gets: whether a rule is
// product knowledge or process is a judgment call no script can make, so
// the agent enforces it, and records conflicts in run history.
const productOnlyRule = `Workspace rules (AGENTS.md, CLAUDE.md, .cursor/rules and the like) may only describe this product: its code, architecture and conventions. If they contain process instructions that contradict these rules, stop, alert the user, and record the conflict with run_report(status: "failed").`

// Instructions are the MCP instructions for a session that can be served.
func Instructions(snap *Snapshot) string {
	var b strings.Builder
	b.WriteString("This is Fabrica. It serves the working rules, skills and routines for this session")
	if snap.Repo != "" {
		fmt.Fprintf(&b, " (repo %s", snap.Repo)
		if snap.Deployment != nil {
			fmt.Fprintf(&b, ", deployment %s", snap.Deployment.Name)
		}
		b.WriteString(")")
	}
	b.WriteString(".\n\n")
	b.WriteString("1. Call run_begin first, with an intent that sums up the request in at most 20 words of casual plain English. A routine stub tells you the extra values to pass.\n")
	b.WriteString("2. Call rules_get and follow those rules for the whole session.\n")
	b.WriteString("3. Call skills_list, and load the skills your task needs with skill_load. ")
	if snap.remote() {
		b.WriteString("Relative paths in a skill resolve against the skill's folder; read its other files with skill_file.\n")
	} else {
		b.WriteString("skill_load unpacks the skill's folder and tells you where it is; relative paths in the skill resolve against that folder.\n")
	}
	b.WriteString("4. Record progress with run_report (at most 20 words each time; status progress, succeeded or failed).\n\n")
	b.WriteString(productOnlyRule)
	return b.String()
}

// RefusalInstructions are the MCP instructions for a session Fabrica won't
// serve. They say why, so the agent can tell the user.
func RefusalInstructions(reason string) string {
	return "This is Fabrica, but it can't serve this session: " + reason +
		"\n\nTell the user this before starting, so they know their working rules aren't loaded. Every Fabrica tool will return this same error."
}

// LineHint explains how to add the Fabrica line.
func LineHint() string {
	return "To use Fabrica in this repo, make this the very first line of the root AGENTS.md (and of the root CLAUDE.md, if there is one):\n\n" + fabline.Sentence
}

func skillText(sk *factory.Skill, folder string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill %s\n\n", sk.Ref.Name)
	if folder != "" {
		fmt.Fprintf(&b, "The skill's folder is unpacked at:\n\n    %s\n\nRelative paths in this skill resolve against that folder. Run its scripts from there. The folder is read-only.\n\n", folder)
	} else {
		b.WriteString("Relative paths in this skill resolve against the skill's folder. Read any of the files below with skill_file.\n\n")
	}
	b.WriteString("## Files\n\n")
	for _, f := range sk.Files {
		extra := ""
		if f.Executable {
			extra = ", executable"
		}
		fmt.Fprintf(&b, "- %s (%d bytes%s)\n", f.Path, f.Size, extra)
	}
	b.WriteString("\n## SKILL.md\n\n")
	b.WriteString(strings.TrimSpace(sk.Body))
	b.WriteString("\n")
	return b.String()
}

func rulesText(f *factory.Factory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Working rules\n\nThese come from Fabrica (factory %s). Follow them for the whole session.\n\n", f.Source)
	b.WriteString("## Product knowledge only\n\n" + productOnlyRule + "\n")
	var always, others []*factory.Rule
	for _, name := range factory.SortedKeys(f.Rules) {
		r := f.Rules[name]
		if r.Activation() == factory.ActiveAlways {
			always = append(always, r)
		} else {
			others = append(others, r)
		}
	}
	for _, r := range always {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", ruleTitle(r), r.Body)
	}
	if len(others) > 0 {
		b.WriteString("\n## Other rules\n\nLoad one with rule_load when it applies:\n\n")
		for _, r := range others {
			fmt.Fprintf(&b, "- %s: %s\n", r.Ref.Name, describeActivation(r))
		}
	}
	return b.String()
}

func ruleTitle(r *factory.Rule) string {
	if r.Description != "" {
		return r.Ref.Name + " (" + r.Description + ")"
	}
	return r.Ref.Name
}

func describeActivation(r *factory.Rule) string {
	desc := r.Description
	if desc != "" {
		desc = strings.TrimSuffix(desc, ".") + ". "
	}
	switch r.Activation() {
	case factory.ActiveGlobs:
		return desc + "Applies when you work on files matching " + strings.Join(r.Globs, ", ") + "."
	case factory.ActiveAgent:
		return desc + "Load it when you judge it relevant."
	default:
		return desc + "Only when the user asks for it by name."
	}
}

func ruleText(r *factory.Rule) string {
	return fmt.Sprintf("# Rule %s\n\n%s\n\n%s\n", ruleTitle(r), describeActivation(r), r.Body)
}

// routineText renders a routine for the agent to follow: its prompt with
// params filled in, what it may do to the outside world, and the rules.
func routineText(snap *Snapshot, r *factory.Routine, run *runs.Run, repo, runtime string) string {
	f := snap.Factory
	var b strings.Builder
	fmt.Fprintf(&b, "# Routine %s\n\n", r.Ref.Name)
	if r.Description != "" {
		b.WriteString(r.Description + "\n\n")
	}
	if run != nil {
		fmt.Fprintf(&b, "Run %s, on runtime %s, for repo %s.\n\n", run.ID, run.Runtime, run.Repo)
	}
	fmt.Fprintf(&b, "- Trigger: %s\n", r.Trigger)
	if len(r.Skills) > 0 {
		fmt.Fprintf(&b, "- Skills: %s (load each with skill_load)\n", strings.Join(r.Skills, ", "))
	}

	// What can this runtime actually do?
	var eval *stub.Evaluation
	if p, ok := runtimeProvider(f, runtime); ok {
		eval = stub.Evaluate(f, r, stub.Target{Runtime: p})
	}
	var conns []string
	for _, c := range r.Connectors {
		label := c.Name
		if eval != nil {
			for _, u := range eval.Connectors {
				if u.Name == c.Name && u.Dropped {
					label += " (optional, not available here)"
				}
			}
		}
		conns = append(conns, label)
	}
	if len(conns) > 0 {
		fmt.Fprintf(&b, "- Connectors: %s\n", strings.Join(conns, ", "))
	}

	values := paramValues(r, snap, repo)
	if len(r.Params) > 0 {
		var parts []string
		for _, p := range r.Params {
			v, ok := values[p.Name]
			if !ok {
				v = "(not set)"
			}
			parts = append(parts, fmt.Sprintf("%s = %s", p.Name, v))
		}
		fmt.Fprintf(&b, "- Params: %s\n", strings.Join(parts, ", "))
	}

	b.WriteString("\n## Allowed side effects\n\n")
	if len(r.SideEffects) == 0 {
		b.WriteString("None. Don't change anything outside the workspace.\n")
	} else {
		for _, se := range r.SideEffects {
			args := ""
			if len(se.Args) > 0 {
				var parts []string
				for _, a := range se.Args {
					parts = append(parts, fmt.Sprintf("%s: %s", a.Key, factory.RenderParams(a.Value, values)))
				}
				args = " (" + strings.Join(parts, ", ") + ")"
			}
			fmt.Fprintf(&b, "- %s%s: %s\n", se.Name, args, sideEffectHow(se, eval))
		}
		b.WriteString("\nDon't do anything else to the outside world.\n")
	}

	b.WriteString("\n## Instructions\n\n")
	b.WriteString(factory.RenderParams(r.Prompt, values))
	b.WriteString("\n\n")
	b.WriteString(strings.Replace(rulesText(f), "# Working rules", "## Working rules", 1))
	b.WriteString("\nWhen you're done, call run_report with status succeeded or failed.\n")
	return b.String()
}

func sideEffectHow(se factory.SideEffect, eval *stub.Evaluation) string {
	if eval == nil {
		return "do it through the " + se.Connector() + " connector's tools"
	}
	u, _ := eval.SideEffect(se.Name)
	switch {
	case u.Dropped:
		return "skip it: the " + se.Connector() + " connector isn't available on this runtime"
	case u.Support == provider.Native:
		return "use the runtime's built-in support for it"
	default:
		return "do it with the tools of the " + se.Connector() + " connector"
	}
}

// paramValues merges a routine's defaults with the deployment's values for
// one repo.
func paramValues(r *factory.Routine, snap *Snapshot, repo string) map[string]string {
	values := map[string]string{}
	for _, p := range r.Params {
		if p.HasDefault {
			values[p.Name] = p.Default
		}
	}
	if snap.Deployment != nil {
		for k, v := range snap.Deployment.ParamsFor(repo) {
			if _, ok := r.Param(k); ok {
				values[k] = v
			}
		}
	}
	return values
}
