package runview

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/runs"
)

// Bundle is what `fabrica runs export` writes: runs in full, and where they
// came from. See manual/run-history.md for the layout of the folder it
// makes, which is a public interface.
type Bundle struct {
	// Server is the address of the server the runs were read from. Empty
	// means they came from this machine's run history, the file at
	// LocalPath.
	Server    string
	LocalPath string
	Filter    runs.Filter
	// More says more runs matched than the bundle holds.
	More bool
	// Runs are the runs in full, newest first.
	Runs []*runs.Detail
}

// Filters is the "filters" of runs.json: what the bundle was asked for.
type Filters struct {
	Factory  string    `json:"factory,omitempty"`
	Routine  string    `json:"routine,omitempty"`
	Repo     string    `json:"repo,omitempty"`
	Runtime  string    `json:"runtime,omitempty"`
	Outcomes []string  `json:"outcomes,omitempty"`
	Since    time.Time `json:"since"`
	Limit    int       `json:"limit"`
}

// bundleJSON is runs.json.
type bundleJSON struct {
	SchemaVersion int            `json:"schema_version"`
	Filters       Filters        `json:"filters"`
	Runs          []*runs.Detail `json:"runs"`
}

// CheckDir checks that a bundle can be written to dir: it isn't there yet,
// or it's an empty folder. A bundle never lands among other files.
func CheckDir(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("can't export to %s: %w", dir, err)
	case len(entries) > 0:
		return fmt.Errorf("%s already has files in it; export to a new or empty folder, or pick one with --out", dir)
	}
	return nil
}

// WriteBundle writes the bundle to dir: README.md, runs.json and a file for
// every run in runs/.
func (v View) WriteBundle(dir string, b Bundle) error {
	if err := CheckDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		return err
	}
	f := b.Filter
	data, err := json.MarshalIndent(bundleJSON{
		SchemaVersion: runs.SchemaVersion,
		Filters: Filters{Factory: f.Factory, Routine: f.Routine, Repo: f.Repo, Runtime: f.Runtime, Outcomes: f.Outcomes,
			Since: f.Since.UTC(), Limit: f.Limit},
		Runs: append([]*runs.Detail{}, b.Runs...),
	}, "", "  ")
	if err != nil {
		return err
	}
	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644)
	}
	if err := write("README.md", v.readme(b)); err != nil {
		return err
	}
	if err := write("runs.json", string(data)+"\n"); err != nil {
		return err
	}
	for _, d := range b.Runs {
		if err := write("runs/"+d.RunID+".md", v.Markdown(d)); err != nil {
			return err
		}
	}
	return nil
}

// byOutcome groups runs by outcome, in the order the README lists them.
func byOutcome(list []*runs.Detail) [][]*runs.Detail {
	groups := make([][]*runs.Detail, len(outcomeOrder))
	for _, d := range list {
		i := slices.Index(outcomeOrder, d.Outcome)
		if i < 0 {
			i = len(outcomeOrder) - 1
		}
		groups[i] = append(groups[i], d)
	}
	return groups
}

// readme is the bundle's README.md.
func (v View) readme(b Bundle) string {
	var w strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&w, format+"\n", args...) }
	t := totalsOf(b.Runs)
	groups := byOutcome(b.Runs)

	line("# Fabrica run history")
	line("")
	line("%s", v.intro(b))
	line("")
	line("Read this file first. `runs/<run_id>.md` has one run in full, the same as `fabrica runs show`, and `runs.json` has all of them as JSON, for programs. Transcripts aren't copied into the bundle: it only points at them.")
	line("")

	line("## Totals")
	line("")
	line("%s in this bundle.", plural(len(b.Runs), "run"))
	line("")
	line("### Runs by outcome")
	line("")
	for _, o := range outcomeOrder {
		if t.outcomes[o] > 0 {
			line("- %s: %d", o, t.outcomes[o])
		}
	}
	line("")
	v.routinesTable(&w, t)
	line("### Friction notes")
	line("")
	if len(t.friction) == 0 {
		line("None. No agent said anything slowed it down.")
	}
	for _, c := range t.friction {
		line("- %s: %d", c.name, c.n)
	}
	line("")
	line("### Most-loaded skills and rules")
	line("")
	if len(t.loads) == 0 {
		line("None. No run loaded a skill or a rule.")
	} else {
		line("| Kind | Name | Runs that loaded it |")
		line("|---|---|---|")
		for _, l := range t.loads {
			line("| %s | %s | %d |", l.kind, cell(l.name), l.runs)
		}
	}
	line("")
	line("### Refusals")
	line("")
	if len(t.refusals) == 0 {
		line("None. Fabrica refused no run.")
	} else {
		line("Fabrica refused these runs itself (a failed checkpoint it wrote, not the agent), for these reasons:")
		line("")
		for _, c := range t.refusals {
			line("- %s: %d", c.name, c.n)
		}
	}
	line("")

	line("## Runs")
	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		line("")
		line("### %s (%d)", strings.ReplaceAll(outcomeOrder[i], "_", " "), len(group))
		line("")
		for _, d := range group {
			line("%s", v.runLine(d, outcomeOrder[i]))
		}
	}
	line("")

	line("## Where to look next")
	line("")
	v.whereToLook(&w, b.Runs)

	line("## Fields you can't fully trust")
	line("")
	line("Agents write the intents, summaries, statuses, friction notes, links and session pointers, on a best-effort basis, so they can be missing, wrong or vague. A run that says `succeeded` may not have. Fabrica writes the times, the blueprint commits, the factory and the loads, so those are reliable, and so are its own refusals.")
	return w.String()
}

// intro is the README's first paragraph: what the bundle is and where it
// came from.
func (v View) intro(b Bundle) string {
	f := b.Filter
	var factories []string
	for _, d := range b.Runs {
		if d.Factory != "" && !slices.Contains(factories, d.Factory) {
			factories = append(factories, d.Factory)
		}
	}
	slices.Sort(factories)
	of := "of every factory (" + strings.Join(factories, ", ") + ")"
	switch {
	case f.Factory != "":
		of = "of factory " + f.Factory
	case len(factories) == 1:
		of = "of factory " + factories[0]
	}

	from := "this machine's run history (" + b.LocalPath + ")"
	if b.Server != "" {
		from = "the Fabrica server at " + b.Server
	}
	var matching []string
	for _, m := range []struct{ name, value string }{{"routine", f.Routine}, {"repo", f.Repo}, {"runtime", f.Runtime}} {
		if m.value == "" {
			continue
		}
		if m.name == "routine" && m.value == runs.InteractiveRoutine {
			matching = append(matching, "interactive runs only")
			continue
		}
		matching = append(matching, m.name+" "+m.value)
	}
	if len(f.Outcomes) > 0 {
		matching = append(matching, "outcome "+strings.Join(f.Outcomes, " or "))
	}
	s := fmt.Sprintf("This is a retro bundle, made by `fabrica runs export` on %s (%s). It holds %s %s, read from %s",
		v.when(v.now(), false, true), v.zone(), plural(len(b.Runs), "run"), of, from)
	if !f.Since.IsZero() {
		s += ", the ones that started since " + v.when(f.Since, false, true)
	}
	if len(matching) > 0 {
		s += " and match " + strings.Join(matching, ", ")
	}
	s += ", newest first."
	if b.More {
		s += fmt.Sprintf(" More runs match than that: this is the newest %d of them.", len(b.Runs))
	}
	return s
}

// routinesTable writes the runs of each routine, with its success rate.
func (v View) routinesTable(w *strings.Builder, t totals) {
	factories := map[string]bool{}
	for _, r := range t.routines {
		factories[r.factory] = true
	}
	w.WriteString("### Runs by routine\n\n| Routine | Runs | Succeeded | Failed | Stale | In progress | Success rate |\n|---|---|---|---|---|---|---|\n")
	for _, r := range t.routines {
		name := "(interactive)"
		if r.routine != "" {
			name = r.routine
		}
		if len(factories) > 1 && r.factory != "" {
			name = r.factory + "/" + name
		}
		rate := "—"
		if p, ok := r.successRate(); ok {
			rate = fmt.Sprintf("%d%%", p)
		}
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %s |\n", cell(name), r.runs, r.byOutcome[runs.OutcomeSucceeded], r.byOutcome[runs.OutcomeFailed],
			r.byOutcome[runs.OutcomeStale], r.byOutcome[runs.OutcomeInProgress], rate)
	}
	w.WriteString("\nThe success rate is the runs that succeeded out of those that succeeded or failed. Stale and in-progress runs aren't counted in it.\n\n")
}

// cell makes text safe for a cell of a Markdown table.
func cell(s string) string { return strings.ReplaceAll(clean(s), "|", `\|`) }

// runLine is one run in the README's list, linking to its file.
func (v View) runLine(d *runs.Detail, outcome string) string {
	what := "interactive"
	if d.Routine != "" {
		what = clean(d.Routine)
	}
	where := ""
	if d.Repo != "" {
		where = " on " + shortRepo(clean(d.Repo))
	}
	s := fmt.Sprintf("- [%s](runs/%s.md) %s, %s%s: %s", d.RunID, d.RunID, v.when(d.StartedAt, false, true), what, where, orElse(clean(d.Intent), "—"))
	if d.FrictionCount > 0 {
		s += " (" + plural(d.FrictionCount, "friction note") + ")"
	}
	if d.LastSummary != "" && (outcome == runs.OutcomeFailed || outcome == runs.OutcomeStale) {
		s += "\n  - Last report: " + clean(d.LastSummary)
	}
	return s
}

// whereToLook writes the README's pointers to the sources behind the runs:
// the instructions they were served, their PRs and their transcripts.
func (v View) whereToLook(w *strings.Builder, list []*runs.Detail) {
	ordered := slices.Concat(byOutcome(list)...)
	line := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	line("### Instructions the runs were served")
	line("")
	var shas []string
	for _, d := range ordered {
		if d.BlueprintSHA != "" && !slices.Contains(shas, d.BlueprintSHA) {
			shas = append(shas, d.BlueprintSHA)
		}
	}
	if len(shas) == 0 {
		line("None of the runs has a blueprint commit.")
	} else {
		line("Each run was served the blueprint as it was at one commit. In a clone of your blueprint repo, `git show <commit>:<path>` prints a file as that run saw it. Routines are in `routines/<name>.md`, skills in `skills/<name>/SKILL.md` and rules in `rules/<name>.mdc`; a run's own file lists the skills and rules it loaded.")
	}
	subfolder := false
	for _, sha := range shas {
		var factories, commands []string
		n := 0
		for _, d := range ordered {
			if d.BlueprintSHA != sha {
				continue
			}
			n++
			if d.Factory != "" && !slices.Contains(factories, clean(d.Factory)) {
				factories = append(factories, clean(d.Factory))
			}
			if d.Routine != "" {
				h := v.hint(d)
				subfolder = subfolder || h.subfolder
				for _, c := range h.commands {
					if !slices.Contains(commands, c) {
						commands = append(commands, c)
					}
				}
			}
		}
		line("")
		desc := plural(n, "run")
		if len(factories) > 0 {
			desc = "factory " + strings.Join(factories, ", ") + ", " + desc
		}
		line("- `%s` (%s)", gitx.Short(clean(sha)), desc)
		for _, c := range commands {
			line("  - `%s`", c)
		}
	}
	if subfolder {
		line("")
		line("If your blueprint lives in a subfolder of its repo, put that folder in front of the path.")
	}
	line("")

	line("### Pull requests")
	line("")
	var urls []string
	touched := map[string][]string{} // PR link → the runs that touched it
	for _, d := range ordered {
		for _, link := range d.PRLinks {
			link = clean(link)
			if _, ok := touched[link]; !ok {
				urls = append(urls, link)
			}
			touched[link] = append(touched[link], fmt.Sprintf("%s, %s", d.RunID, strings.ReplaceAll(d.Outcome, "_", " ")))
		}
	}
	if len(urls) == 0 {
		line("No run linked a pull request.")
	} else {
		line("The review comments on these are often the best sign of what went wrong.")
		line("")
		for _, u := range urls {
			line("- %s (%s)", u, strings.Join(touched[u], "; "))
		}
	}
	line("")

	line("### Transcripts")
	line("")
	var found []string
	for _, d := range ordered {
		if file, ok := localFile(d.Session); ok {
			found = append(found, fmt.Sprintf("- %s (%s, %s)", file, d.RunID, strings.ReplaceAll(d.Outcome, "_", " ")))
		}
	}
	if len(found) == 0 {
		line("No run points at a file on this machine. Some may point at a session URL or ID instead; that's the `session` of each run.")
	} else {
		line("These runs point at a transcript that is a file on this machine:")
		line("")
		for _, f := range found {
			line("%s", f)
		}
	}
	line("")
}

// localFile reports whether a session pointer is a file that exists on this
// machine, and if so, where. Only a path is a candidate: a session URL or
// ID isn't.
func localFile(session string) (string, bool) {
	session = clean(session)
	if rest, ok := strings.CutPrefix(session, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		session = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(session) {
		return "", false
	}
	info, err := os.Stat(session)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return session, true
}
