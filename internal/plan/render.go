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
	"github.com/staltz/fabrica/internal/style"
)

// Render writes the plan for people to read: what apply would change, and
// what stops it. How each factory is set up is for RenderShow.
func (p *Plan) Render(w io.Writer) {
	st := style.For(w)
	for _, n := range p.Notes {
		fmt.Fprintf(w, "%s %s\n", st.Dim("·"), n)
	}
	p.renderHomeDiags(w, st)
	for _, d := range p.Factories {
		d.Render(w)
		fmt.Fprintln(w)
	}
	if p.Local != nil {
		p.Local.Render(w)
		fmt.Fprintln(w)
	}
	line := p.Summary()
	switch p.ExitCode() {
	case 0:
		line = st.Green(line)
	case 2:
		line = st.Yellow(line)
	default:
		line = st.Red(line)
	}
	fmt.Fprintln(w, line)
}

// RenderShow writes how each factory is set up: its blueprint, endpoint
// and tag, and how each runtime handles each routine. It's `fabrica show`.
func (p *Plan) RenderShow(w io.Writer) {
	st := style.For(w)
	p.renderHomeDiags(w, st)
	for i, d := range p.Factories {
		if i > 0 {
			fmt.Fprintln(w)
		}
		d.RenderShow(w)
	}
	if len(p.Factories) == 0 {
		fmt.Fprintln(w, "No factories.")
	}
}

func (p *Plan) renderHomeDiags(w io.Writer, st style.Style) {
	if home := p.Diags.Sorted(); len(home) > 0 {
		fmt.Fprintf(w, "%s %s\n", st.Bold("Home repo"), p.Home.Dir)
		renderDiags(w, st, "  ", home)
		fmt.Fprintln(w)
	}
}

// Summary is the last line of the plan.
func (p *Plan) Summary() string {
	changes, errors, pending := 0, len(p.Diags.Only(diag.Error)), 0
	for _, d := range p.Factories {
		changes += len(d.Changes)
		errors += len(d.Diags.Only(diag.Error))
		pending += len(d.Pending)
		if d.Init.Start() {
			changes++
		}
		if d.Init != nil && d.Init.Waiting != nil {
			pending++
		}
	}
	if p.Local != nil {
		errors += len(p.Local.Diags.Only(diag.Error))
		for _, rt := range p.Local.Runtimes {
			changes += len(rt.Changes)
		}
		if hb := p.Local.Heartbeat; hb != nil {
			changes += len(hb.Changes)
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
	return "Plan: " + strings.Join(parts, ", ") + ". Fix the errors before applying; apply skips any factory that has them."
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
	return "Destroy plan: " + strings.Join(parts, ", ") + ". Fix the errors first; destroy skips any factory that has them."
}

// header writes the factory's first line, like "Factory acme (cloud)".
func (d *Factory) header(w io.Writer, st style.Style) {
	kind := "cloud"
	switch {
	case d.Spec == nil:
		kind = "deleted, cleaning up"
	case d.Local():
		kind = "local"
	case d.Destroy:
		kind = "cloud, destroying"
	}
	fmt.Fprintf(w, "%s %s\n", st.Bold("Factory "+d.Name), st.Dim("("+kind+")"))
}

// Render writes one factory's plan.
func (d *Factory) Render(w io.Writer) {
	st := style.For(w)
	d.header(w, st)
	for _, n := range d.Notes {
		fmt.Fprintf(w, "  %s %s\n", st.Dim("·"), n)
	}
	if d.HasWork() {
		fmt.Fprintln(w, "  Changes:")
		for _, c := range d.Changes {
			fmt.Fprintf(w, "    %s\n", c.styled(st))
		}
		if d.Init.Start() {
			fmt.Fprintf(w, "    %s\n", st.Mark(d.Init.Describe()))
		}
	}
	if len(d.Pending) > 0 || d.Init != nil && d.Init.Waiting != nil {
		fmt.Fprintln(w, "  Waiting for `fabrica confirm`:")
		for _, p := range d.Pending {
			what := stubName(p.Resource, p.Routine) + " @ " + p.Repo
			if p.Agent != "" {
				what = "agent " + p.Agent
			}
			fmt.Fprintf(w, "    %s %s: %s %s on %s, issued %s\n", st.Cyan("…"), p.ID, p.Action, what, p.Runtime, p.IssuedAt.Format("2006-01-02"))
		}
		if d.Init != nil && d.Init.Waiting != nil {
			in := d.Init.Waiting
			fmt.Fprintf(w, "    %s %s: start init %s @ %s on %s, issued %s\n", st.Cyan("…"), in.ID, in.Routine, in.Repo, in.Runtime, in.At.Format("2006-01-02"))
		}
	}
	if d.State != nil {
		var unverifiable []string
		for _, r := range d.State.Resources {
			switch {
			case !r.Unverifiable():
			case r.Agent != "":
				unverifiable = append(unverifiable, fmt.Sprintf("agent %s on %s", r.Agent, r.Runtime))
			default:
				unverifiable = append(unverifiable, fmt.Sprintf("%s @ %s on %s", stubName(r.ID, r.Routine), r.Repo, r.Runtime))
			}
		}
		if len(unverifiable) > 0 {
			fmt.Fprintln(w, "  Unverifiable (confirmed without a remote ID, so no drift detection and no automatic deletion):")
			for _, u := range unverifiable {
				fmt.Fprintf(w, "    %s %s\n", st.Yellow("?"), u)
			}
		}
	}
	renderDiags(w, st, "  ", d.Diags.Sorted())
	switch {
	case d.HasWork() || len(d.Pending) > 0 || d.Init != nil || d.Diags.HasErrors():
	case d.Local():
		// Its changes are under the local tools, at the end.
		fmt.Fprintln(w, "  "+st.Dim("Set up through the local tools on this machine."))
	case d.Destroy:
		fmt.Fprintln(w, "  "+st.Green("Nothing to destroy."))
	default:
		fmt.Fprintln(w, "  "+st.Green("In sync."))
	}
}

// RenderShow writes how one factory is set up.
func (d *Factory) RenderShow(w io.Writer) {
	st := style.For(w)
	d.header(w, st)
	for _, n := range d.Details {
		fmt.Fprintf(w, "  %s %s\n", st.Dim("·"), n)
	}
	if d.Spec != nil && d.Spec.Endpoint() != "" {
		fmt.Fprintf(w, "  %s %s\n", st.Dim("endpoint"), d.Spec.Endpoint())
		fmt.Fprintf(w, "  %s %s %s\n", st.Dim("tag"), d.Spec.Tag(), st.Dim("(runtimes name its routines like "+stub.Identity(d.Spec.Tag(), "<routine>", "<repo>")+")"))
	}
	for _, rt := range d.Runtimes {
		fmt.Fprintf(w, "  %s %s\n", st.Bold(rt.Name+":"), describeRuntime(rt))
		for _, name := range sortedEvalNames(rt.Evaluations) {
			fmt.Fprintf(w, "    routine %s %s\n", st.Bold(name+":"), describeEvaluation(st, rt.Evaluations[name]))
		}
		for _, a := range rt.Agents {
			fmt.Fprintf(w, "    agent %s %s\n", st.Bold(a.Agent.Ref.Name+":"), describeAgent(st, a))
		}
	}
	d.Init.render(w, st)
	renderDiags(w, st, "  ", d.Diags.Sorted())
}

// render writes how the init would start: each runtime it would like, and
// what that runtime can do.
func (i *Init) render(w io.Writer, st style.Style) {
	if i == nil || len(i.Candidates) == 0 {
		return
	}
	fmt.Fprintf(w, "  init routine %s (once, on the first of its runtimes Fabrica can start a session on):\n", st.Bold(i.Routine.Ref.Name))
	for _, c := range i.Candidates {
		mark := "  "
		if c == i.Chosen {
			mark = st.Cyan("→") + " "
		}
		switch c.Mode {
		case provider.API:
			fmt.Fprintf(w, "    %s%s: Fabrica starts it\n", mark, c.Runtime)
		case provider.Handoff:
			fmt.Fprintf(w, "    %s%s: a person starts it, from a handoff (Fabrica can't by itself: %s)\n", mark, c.Runtime, c.Why)
		default:
			fmt.Fprintf(w, "    %s%s: %s (%s)\n", mark, c.Runtime, st.Red("can't start it"), c.Why)
		}
	}
}

// Describe is a one-line summary of a change.
func (c *Change) Describe() string { return c.styled(style.Style{}) }

func (c *Change) styled(st style.Style) string {
	sym := map[Action]string{Create: "+", Update: "~", Delete: "-", Adopt: "="}[c.Action]
	line := st.Mark(fmt.Sprintf("%s %-6s %s @ %s on %s", sym, c.Action, stubName(c.Key.ID, c.Routine), c.Repo, c.Key.Runtime))
	if c.Agent != "" {
		line = st.Mark(fmt.Sprintf("%s %-6s agent %s on %s", sym, c.Action, c.Agent, c.Key.Runtime))
	}
	line += " " + st.Dim("("+string(c.Mode)+")")
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
	st := style.For(w)
	fmt.Fprintln(w, st.Bold("Local tools on this machine"))
	for _, rt := range l.Runtimes {
		var files []string
		for _, f := range rt.Tool.Files() {
			files = append(files, Tildify(f))
		}
		fmt.Fprintf(w, "  %s %s\n", st.Bold(rt.Name), st.Dim("("+strings.Join(files, ", ")+")"))
		if len(rt.Changes) == 0 {
			fmt.Fprintln(w, "    "+st.Green("In sync."))
		}
		for _, c := range rt.Changes {
			fmt.Fprintf(w, "    %s\n", st.Mark(c))
		}
	}
	if hb := l.Heartbeat; hb != nil {
		var files []string
		for _, f := range hb.Scheduler.Files() {
			files = append(files, Tildify(f))
		}
		fmt.Fprintf(w, "  %s %s\n", st.Bold("heartbeat"), st.Dim("("+strings.Join(files, ", ")+")"))
		if len(hb.Changes) == 0 {
			fmt.Fprintln(w, "    "+st.Green("In sync."))
		}
		for _, c := range hb.Changes {
			fmt.Fprintf(w, "    %s\n", st.Mark(c))
		}
	}
	renderDiags(w, st, "  ", l.Diags.Sorted())
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

// describeAgent says how an agent is deployed.
func describeAgent(st style.Style, a *AgentDeploy) string {
	from := a.Shape.Git + " at " + gitx.Short(a.Agent.Source.Commit)
	if a.Shape.Path != "" {
		from += ", " + a.Shape.Path
	}
	return fmt.Sprintf("deployment %s, built from %s, watching %s", a.Slug, from, strings.Join(a.Shape.EventRepos, ", "))
}

func describeRuntime(rt *Runtime) string {
	m := rt.Provider.Matrix
	parts := []string{string(rt.Mode) + " mode"}
	if m.Agents {
		return strings.Join(append(parts, "deploys the blueprint's agents, one deployment each, watching the factory's repos"), ", ")
	}
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

func describeEvaluation(st style.Style, ev *stub.Evaluation) string {
	var parts []string
	word := "trigger"
	if len(ev.Triggers) > 1 {
		word = "triggers"
	}
	parts = append(parts, word+" "+describeUses(st, ev.Triggers))
	if len(ev.Connectors) > 0 {
		parts = append(parts, "connectors "+describeUses(st, ev.Connectors))
	}
	if len(ev.SideEffects) > 0 {
		parts = append(parts, "side effects "+describeUses(st, ev.SideEffects))
	}
	if ev.Memory.Support != "" {
		parts = append(parts, "memory "+support(st, ev.Memory.Support))
	}
	// Stubs that run in a session have none of these settings.
	for _, s := range ev.Stubs {
		if len(s.Shape.SideEffects) == 0 {
			continue
		}
		var settings []string
		for _, name := range sortedSettings(s.Shape.SideEffects) {
			settings = append(settings, name+" "+s.Shape.SideEffects[name])
		}
		parts = append(parts, "runtime's own "+strings.Join(settings, ", "))
		break
	}
	if ev.Model.Name != "" {
		parts = append(parts, "model "+ev.Model.String())
	}
	if len(ev.Stubs) == 1 {
		parts = append(parts, st.Dim("stub "+ev.Stubs[0].Shape.Fingerprint()))
	} else {
		var stubs []string
		for _, s := range ev.Stubs {
			stubs = append(stubs, s.Trigger+" "+s.Shape.Fingerprint())
		}
		parts = append(parts, st.Dim("a stub per trigger ("+strings.Join(stubs, ", ")+")"))
	}
	return strings.Join(parts, st.Dim(" · "))
}

// support writes how a runtime supports something, like "[native]":
// native green, emulated yellow, anything else red.
func support(st style.Style, s provider.Support) string {
	label := "[" + string(s) + "]"
	switch s {
	case provider.Native:
		return st.Green(label)
	case provider.Emulated:
		return st.Yellow(label)
	}
	return st.Red(label)
}

// stubName names a stub by its routine, plus its trigger when the routine
// has a stub per trigger, which only its identity says.
func stubName(id, routine string) string {
	_, _, trigger, _ := stub.SplitIdentity(id)
	return stub.StubName(routine, trigger)
}

func describeUses(st style.Style, uses []stub.Use) string {
	var out []string
	for _, u := range uses {
		s := u.Name + " " + support(st, u.Support)
		if u.Dropped {
			s = u.Name + " " + st.Dim("[optional, not available here]")
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func renderDiags(w io.Writer, st style.Style, indent string, diags diag.List) {
	for _, d := range diags {
		if d.Severity == diag.Note {
			continue
		}
		for i, line := range strings.Split(st.Diag(d), "\n") {
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
	ExitCode  int           `json:"exit_code"`
	Destroy   bool          `json:"destroy,omitempty"`
	Diags     diag.List     `json:"diagnostics,omitempty"`
	Factories []FactoryJSON `json:"factories"`
	Local     []LocalJSON   `json:"local,omitempty"`
	// Heartbeat is what apply would do to the heartbeat job, which starts
	// local routines with a pace trigger.
	Heartbeat []string `json:"heartbeat,omitempty"`
}

// FactoryJSON is one factory in JSON output.
type FactoryJSON struct {
	Name            string       `json:"name"`
	Scope           string       `json:"scope"`
	BlueprintCommit string       `json:"blueprint_commit,omitempty"`
	Follows         bool         `json:"follows"`
	Changes         []ChangeJSON `json:"changes"`
	Pending         []string     `json:"pending,omitempty"`
	Init            *InitJSON    `json:"init,omitempty"`
	Diags           diag.List    `json:"diagnostics,omitempty"`
	Notes           []string     `json:"notes,omitempty"`
	Details         []string     `json:"details,omitempty"`
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

// InitJSON is what apply would do about a factory's init, in JSON output.
type InitJSON struct {
	// ID is empty before the first attempt to start it.
	ID      string `json:"id,omitempty"`
	Routine string `json:"routine"`
	// Action is "start" when apply starts the session, and "waiting" while
	// its handoff waits for `fabrica confirm`.
	Action  string `json:"action"`
	Runtime string `json:"runtime,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// LocalJSON is one local tool in JSON output.
type LocalJSON struct {
	Runtime string   `json:"runtime"`
	Changes []string `json:"changes"`
}

// ToJSON converts the plan to plain data.
func (p *Plan) ToJSON() JSON {
	out := JSON{ExitCode: p.ExitCode(), Destroy: p.Destroy, Diags: p.Diags}
	for _, d := range p.Factories {
		dj := FactoryJSON{Name: d.Name, Scope: "cloud", Diags: d.Diags, Notes: d.Notes, Details: d.Details, Changes: []ChangeJSON{}}
		if d.Local() {
			dj.Scope = "local"
		}
		if d.Resolved.Commit != "" {
			dj.BlueprintCommit, dj.Follows = d.Resolved.Commit, d.Resolved.Kind == gitx.Branch
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
		switch in := d.Init; {
		case in.Start():
			dj.Init = &InitJSON{ID: in.ID, Routine: in.Routine.Ref.Name, Action: "start", Runtime: in.Chosen.Runtime, Repo: in.Repo, Mode: string(in.Chosen.Mode), Reason: in.Reason}
		case in != nil && in.Waiting != nil:
			st := in.Waiting
			dj.Init = &InitJSON{ID: st.ID, Routine: st.Routine, Action: "waiting", Runtime: st.Runtime, Repo: st.Repo, Mode: st.Mode}
			dj.Pending = append(dj.Pending, st.ID)
		}
		out.Factories = append(out.Factories, dj)
	}
	if p.Local != nil {
		for _, rt := range p.Local.Runtimes {
			out.Local = append(out.Local, LocalJSON{Runtime: rt.Name, Changes: rt.Changes})
		}
		if hb := p.Local.Heartbeat; hb != nil {
			out.Heartbeat = hb.Changes
		}
		out.Diags = append(out.Diags, p.Local.Diags...)
	}
	return out
}
