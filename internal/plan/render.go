package plan

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// Render writes the plan for people to read.
func (p *Plan) Render(w io.Writer) {
	for _, n := range p.Notes {
		fmt.Fprintf(w, "· %s\n", n)
	}
	if home := p.Diags.Sorted(); len(home) > 0 {
		fmt.Fprintf(w, "Home repo %s\n", p.Home.Dir)
		renderDiags(w, "  ", home)
		fmt.Fprintln(w)
	}
	for _, d := range p.Deployments {
		d.Render(w)
		fmt.Fprintln(w)
	}
	if p.Local != nil {
		p.Local.Render(w)
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, p.Summary())
}

// Summary is the last line of the plan.
func (p *Plan) Summary() string {
	changes, errors, pending := 0, len(p.Diags.Only(diag.Error)), 0
	for _, d := range p.Deployments {
		changes += len(d.Changes)
		errors += len(d.Diags.Only(diag.Error))
		pending += len(d.Pending)
	}
	if p.Local != nil {
		errors += len(p.Local.Diags.Only(diag.Error))
		for _, rt := range p.Local.Runtimes {
			changes += len(rt.Changes)
		}
	}
	var parts []string
	parts = append(parts, plural(changes, "change"))
	if pending > 0 {
		parts = append(parts, plural(pending, "pending handoff"))
	}
	parts = append(parts, plural(errors, "error"))
	if p.Destroy {
		return p.destroySummary(parts, pending)
	}
	switch p.ExitCode() {
	case 0:
		if pending > 0 {
			return fmt.Sprintf("Nothing for apply to do, but %s %s waiting for `fabrica confirm`.", plural(pending, "handoff"), map[bool]string{true: "is", false: "are"}[pending == 1])
		}
		return "Everything is in sync (" + strings.Join(parts, ", ") + ")."
	case 2:
		return "Plan: " + strings.Join(parts, ", ") + ". Run `fabrica apply` to make these changes."
	}
	return "Plan: " + strings.Join(parts, ", ") + ". Fix the errors before applying; apply skips any deployment that has them."
}

func (p *Plan) destroySummary(parts []string, pending int) string {
	switch p.ExitCode() {
	case 0:
		if pending > 0 {
			return fmt.Sprintf("Nothing to destroy, but %s %s waiting for `fabrica confirm`.", plural(pending, "handoff"), map[bool]string{true: "is", false: "are"}[pending == 1])
		}
		return "Nothing to destroy (" + strings.Join(parts, ", ") + ")."
	case 2:
		return "Destroy plan: " + strings.Join(parts, ", ") + "."
	}
	return "Destroy plan: " + strings.Join(parts, ", ") + ". Fix the errors first; destroy skips any deployment that has them."
}

// Render writes one deployment's plan.
func (d *Deployment) Render(w io.Writer) {
	kind := "cloud"
	switch {
	case d.Spec == nil:
		kind = "deleted, cleaning up"
	case d.Local():
		kind = "local"
	case d.Destroy:
		kind = "cloud, destroying"
	}
	fmt.Fprintf(w, "Deployment %s (%s)\n", d.Name, kind)
	if d.Spec != nil && d.Spec.Endpoint() != "" {
		fmt.Fprintf(w, "  endpoint %s\n", d.Spec.Endpoint())
	}
	for _, n := range d.Notes {
		fmt.Fprintf(w, "  · %s\n", n)
	}
	for _, rt := range d.Runtimes {
		fmt.Fprintf(w, "  %s: %s\n", rt.Name, describeRuntime(rt))
		for _, name := range sortedEvalNames(rt.Evaluations) {
			fmt.Fprintf(w, "    routine %s: %s\n", name, describeEvaluation(rt.Evaluations[name]))
		}
	}
	if len(d.Changes) > 0 {
		fmt.Fprintln(w, "  Changes:")
		for _, c := range d.Changes {
			fmt.Fprintf(w, "    %s\n", c.Describe())
		}
	}
	if len(d.Pending) > 0 {
		fmt.Fprintln(w, "  Waiting for `fabrica confirm`:")
		for _, p := range d.Pending {
			fmt.Fprintf(w, "    … %s: %s %s @ %s on %s, issued %s\n", p.ID, p.Action, p.Routine, p.Repo, p.Runtime, p.IssuedAt.Format("2006-01-02"))
		}
	}
	if d.State != nil {
		var unverifiable []string
		for _, r := range d.State.Resources {
			if r.Unverifiable() {
				unverifiable = append(unverifiable, fmt.Sprintf("%s @ %s on %s", r.Routine, r.Repo, r.Runtime))
			}
		}
		if len(unverifiable) > 0 {
			fmt.Fprintln(w, "  Unverifiable (confirmed without a remote ID, so no drift detection and no automatic deletion):")
			for _, u := range unverifiable {
				fmt.Fprintf(w, "    ? %s\n", u)
			}
		}
	}
	renderDiags(w, "  ", d.Diags.Sorted())
	if len(d.Changes) == 0 && len(d.Pending) == 0 && !d.Diags.HasErrors() && !d.Local() {
		if d.Destroy {
			fmt.Fprintln(w, "  Nothing to destroy.")
		} else {
			fmt.Fprintln(w, "  In sync.")
		}
	}
}

// Describe is a one-line summary of a change.
func (c *Change) Describe() string {
	sym := map[Action]string{Create: "+", Update: "~", Delete: "-", Adopt: "="}[c.Action]
	line := fmt.Sprintf("%s %-6s %s @ %s on %s (%s)", sym, c.Action, c.Routine, c.Repo, c.Key.Runtime, c.Mode)
	if len(c.Reasons) > 0 {
		line += ": " + strings.Join(c.Reasons, "; ")
	}
	if c.Supersedes != "" {
		line += fmt.Sprintf(" (replaces pending handoff %s)", c.Supersedes)
	}
	return line
}

// Render writes the local part of the plan.
func (l *LocalPlan) Render(w io.Writer) {
	fmt.Fprintln(w, "Local tools on this machine")
	for _, rt := range l.Runtimes {
		var files []string
		for _, f := range rt.Tool.Files() {
			files = append(files, Tildify(f))
		}
		fmt.Fprintf(w, "  %s (%s)\n", rt.Name, strings.Join(files, ", "))
		if len(rt.Changes) == 0 {
			fmt.Fprintln(w, "    In sync.")
		}
		for _, c := range rt.Changes {
			fmt.Fprintf(w, "    %s\n", c)
		}
	}
	renderDiags(w, "  ", l.Diags.Sorted())
}

// Tildify shortens a path inside your home folder to ~/…, which is easier
// to read.
func Tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rel
	}
	return p
}

func describeRuntime(rt *Runtime) string {
	m := rt.Provider.Matrix
	parts := []string{string(rt.Mode) + " mode"}
	if !rt.Provider.Builtin {
		parts = append(parts, "folder provider")
	}
	if m.SkillTransport == provider.Text {
		parts = append(parts, "text-only skills")
	} else {
		parts = append(parts, "skills unpacked on disk")
	}
	if m.Scope == provider.Cloud {
		parts = append(parts, "token in the "+m.TokenPlacement())
	}
	return strings.Join(parts, ", ")
}

func describeEvaluation(ev *stub.Evaluation) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("trigger %s [%s]", ev.Trigger.Name, ev.Trigger.Support))
	if len(ev.Connectors) > 0 {
		parts = append(parts, "connectors "+describeUses(ev.Connectors))
	}
	if len(ev.SideEffects) > 0 {
		parts = append(parts, "side effects "+describeUses(ev.SideEffects))
	}
	if ev.Memory.Support != "" {
		parts = append(parts, fmt.Sprintf("memory [%s]", ev.Memory.Support))
	}
	parts = append(parts, "stub "+ev.Shape.Fingerprint())
	return strings.Join(parts, " · ")
}

func describeUses(uses []stub.Use) string {
	var out []string
	for _, u := range uses {
		s := fmt.Sprintf("%s [%s]", u.Name, u.Support)
		if u.Dropped {
			s = fmt.Sprintf("%s [optional, not available here]", u.Name)
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func renderDiags(w io.Writer, indent string, diags diag.List) {
	for _, d := range diags {
		if d.Severity == diag.Note {
			continue
		}
		for i, line := range strings.Split(d.String(), "\n") {
			if i > 0 {
				line = "  " + strings.TrimLeft(line, " ")
			}
			fmt.Fprintf(w, "%s%s\n", indent, line)
		}
	}
}

func sortedEvalNames(m map[string]*stub.Evaluation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// JSON is a plain-data version of the plan, for `fabrica plan --json`.
type JSON struct {
	ExitCode    int              `json:"exit_code"`
	Destroy     bool             `json:"destroy,omitempty"`
	Diags       diag.List        `json:"diagnostics,omitempty"`
	Deployments []DeploymentJSON `json:"deployments"`
	Local       []LocalJSON      `json:"local,omitempty"`
}

// DeploymentJSON is one deployment in JSON output.
type DeploymentJSON struct {
	Name          string       `json:"name"`
	Scope         string       `json:"scope"`
	FactoryCommit string       `json:"factory_commit,omitempty"`
	Follows       bool         `json:"follows"`
	Changes       []ChangeJSON `json:"changes"`
	Pending       []string     `json:"pending,omitempty"`
	Diags         diag.List    `json:"diagnostics,omitempty"`
	Notes         []string     `json:"notes,omitempty"`
}

// ChangeJSON is one change in JSON output.
type ChangeJSON struct {
	Action      Action   `json:"action"`
	Runtime     string   `json:"runtime"`
	Identity    string   `json:"identity"`
	Routine     string   `json:"routine"`
	Repo        string   `json:"repo"`
	Mode        string   `json:"mode"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Reasons     []string `json:"reasons,omitempty"`
}

// LocalJSON is one local tool in JSON output.
type LocalJSON struct {
	Runtime string   `json:"runtime"`
	Changes []string `json:"changes"`
}

// ToJSON converts the plan to plain data.
func (p *Plan) ToJSON() JSON {
	out := JSON{ExitCode: p.ExitCode(), Destroy: p.Destroy, Diags: p.Diags}
	for _, d := range p.Deployments {
		dj := DeploymentJSON{Name: d.Name, Scope: "cloud", Diags: d.Diags, Notes: d.Notes, Changes: []ChangeJSON{}}
		if d.Local() {
			dj.Scope = "local"
		}
		if d.Resolved.Commit != "" {
			dj.FactoryCommit, dj.Follows = d.Resolved.Commit, d.Resolved.Kind == gitx.Branch
		}
		for _, c := range d.Changes {
			cj := ChangeJSON{Action: c.Action, Runtime: c.Key.Runtime, Identity: c.Key.ID, Routine: c.Routine, Repo: c.Repo, Mode: string(c.Mode), Reasons: c.Reasons}
			if c.Desired != nil {
				cj.Fingerprint = c.Desired.Fingerprint
			}
			dj.Changes = append(dj.Changes, cj)
		}
		for _, pend := range d.Pending {
			dj.Pending = append(dj.Pending, pend.ID)
		}
		out.Deployments = append(out.Deployments, dj)
	}
	if p.Local != nil {
		for _, rt := range p.Local.Runtimes {
			out.Local = append(out.Local, LocalJSON{Runtime: rt.Name, Changes: rt.Changes})
		}
		out.Diags = append(out.Diags, p.Local.Diags...)
	}
	return out
}
