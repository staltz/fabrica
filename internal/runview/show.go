package runview

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/runs"
)

// doc is one run laid out for people, before it's text or Markdown. Both
// `fabrica runs show` and the bundle's runs/<run_id>.md come from it, so
// they always say the same thing.
type doc struct {
	title  string
	fields []field
	rows   []row
	hint   hint
}

type field struct{ label, value string }

// row is one line of the timeline.
type row struct {
	// day is set on the first row of a new day, which gets a line of its
	// own.
	day string
	at  string // the time of day, like 09:41:05
	// label is a checkpoint's status, "friction" or "fabrica". Loads have
	// none.
	label string
	text  string
	links []string
}

// hint is how to read the instructions a run was served.
type hint struct {
	// commands are git commands that print the files, one per file.
	commands []string
	// subfolder says the blueprint may live in a subfolder of its repo,
	// because this machine doesn't know the factory.
	subfolder bool
}

// Show returns one run as text for a terminal.
func (v View) Show(d *runs.Detail) string {
	doc := v.build(d, false)
	var b strings.Builder
	fmt.Fprintln(&b, doc.title)
	for _, f := range doc.fields {
		fmt.Fprintf(&b, "  %-11s%s\n", f.label, f.value)
	}
	b.WriteString("\n")
	if len(doc.rows) == 0 {
		b.WriteString("No loads or checkpoints were recorded.\n")
	}
	for _, r := range doc.rows {
		if r.day != "" {
			fmt.Fprintln(&b, r.day)
		}
		line := r.at + "  "
		if r.label != "" {
			line += fmt.Sprintf("%-9s  ", r.label)
		}
		fmt.Fprintln(&b, line+r.text)
		for _, link := range r.links {
			fmt.Fprintf(&b, "%s  → %s\n", strings.Repeat(" ", len(r.at)), link)
		}
	}
	if len(doc.hint.commands) > 0 {
		b.WriteString("\nSee the instructions this run was served:\n")
		for _, c := range doc.hint.commands {
			fmt.Fprintf(&b, "  %s\n", c)
		}
		if doc.hint.subfolder {
			b.WriteString("  (If the blueprint lives in a subfolder of its repo, put that folder in front of the path.)\n")
		}
	}
	return b.String()
}

// Markdown returns one run as a Markdown file, for the retro bundle.
func (v View) Markdown(d *runs.Detail) string {
	doc := v.build(d, true)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", doc.title)
	for _, f := range doc.fields {
		fmt.Fprintf(&b, "- **%s:** %s\n", f.label, f.value)
	}
	fmt.Fprintf(&b, "\nTimes are local to the machine that made this bundle (%s when it was made).\n\n## Timeline\n\n", v.zone())
	if len(doc.rows) == 0 {
		b.WriteString("No loads or checkpoints were recorded.\n")
	}
	for _, r := range doc.rows {
		if r.day != "" {
			fmt.Fprintf(&b, "- **%s**\n", r.day)
		}
		line := "- " + r.at + " "
		if r.label != "" {
			line += "**" + r.label + "**: "
		}
		fmt.Fprintln(&b, line+r.text)
		for _, link := range r.links {
			fmt.Fprintf(&b, "  - %s\n", link)
		}
	}
	if len(doc.hint.commands) > 0 {
		b.WriteString("\n## Instructions this run was served\n\n```sh\n")
		for _, c := range doc.hint.commands {
			fmt.Fprintln(&b, c)
		}
		b.WriteString("```\n")
		if doc.hint.subfolder {
			b.WriteString("\nIf the blueprint lives in a subfolder of its repo, put that folder in front of the path.\n")
		}
	}
	return b.String()
}

// build lays a run out. Dates are written for Markdown files when markdown
// is true.
func (v View) build(d *runs.Detail, markdown bool) doc {
	out := doc{title: fmt.Sprintf("Run %s: %s", clean(d.RunID), clean(d.Outcome))}

	factory := orElse(clean(d.Factory), "no factory")
	switch d.ServedBy {
	case runs.ServedLocal:
		factory += " (served locally)"
	case runs.ServedRemote:
		factory += " (served remotely)"
	}
	if d.BlueprintSHA != "" {
		factory += ", blueprint " + gitx.Short(clean(d.BlueprintSHA))
	}
	routine := "none (interactive)"
	if d.Routine != "" {
		routine = clean(d.Routine)
	}
	if d.Runtime != "" {
		routine += " on " + clean(d.Runtime)
	}
	if d.Repo != "" {
		routine += ", repo " + clean(d.Repo)
	}
	out.fields = []field{{"Intent", orElse(clean(d.Intent), "—")}, {"Factory", factory}, {"Routine", routine}}
	if d.Session != "" {
		out.fields = append(out.fields, field{"Session", clean(d.Session)})
	}
	out.fields = append(out.fields, field{"Started", v.started(d, markdown)})

	out.rows = v.rows(d, markdown)
	out.hint = v.hint(d)
	return out
}

// started is the Started line: when the run began, and when it last
// reported.
func (v View) started(d *runs.Detail, markdown bool) string {
	s := v.when(d.StartedAt, true, markdown)
	if d.CheckpointCount == 0 {
		return s + ", never reported"
	}
	last := v.when(d.LastAt, true, markdown)
	if v.day(d.LastAt, markdown) == v.day(d.StartedAt, markdown) {
		last = d.LastAt.In(v.loc()).Format("15:04:05")
	}
	s += ", last heard from " + last
	if later := d.LastAt.Sub(d.StartedAt); later >= time.Second {
		s += " (" + elapsed(later) + " later)"
	}
	return s
}

// rows lays out the timeline: consecutive loads of the same kind become one
// row.
func (v View) rows(d *runs.Detail, markdown bool) []row {
	var rows []row
	today := v.day(d.StartedAt, markdown)
	add := func(r row, at time.Time) {
		r.at = at.In(v.loc()).Format("15:04:05")
		if day := v.day(at, markdown); day != today {
			r.day, today = day, day
		}
		rows = append(rows, r)
	}
	tl := d.Timeline
	for i := 0; i < len(tl); {
		e := tl[i]
		switch e.Type {
		case runs.EventLoad:
			var names []string
			for ; i < len(tl) && tl[i].Type == runs.EventLoad && tl[i].Kind == e.Kind; i++ {
				names = append(names, clean(tl[i].Name))
			}
			add(row{text: loaded(clean(e.Kind), names)}, e.At)
			continue
		case runs.EventCheckpoint:
			r := row{label: clean(e.Status), text: clean(e.Summary)}
			if e.Author == runs.ByFabrica {
				// Fabrica's own checkpoints, like refusals, stand out from
				// the agent's.
				r.label, r.text = "fabrica", clean(e.Status)+": "+clean(e.Summary)
			}
			for _, link := range e.Links {
				r.links = append(r.links, clean(link))
			}
			add(r, e.At)
		case runs.EventFriction:
			add(row{label: "friction", text: clean(e.Kind) + ": " + clean(e.Note)}, e.At)
		}
		i++
	}
	return rows
}

// loaded says what a run loaded: "loaded rules a, b". A name that was
// loaded more than once says how often.
func loaded(kind string, names []string) string {
	var distinct []string
	count := map[string]int{}
	for _, n := range names {
		if count[n]++; count[n] == 1 {
			distinct = append(distinct, n)
		}
	}
	for i, n := range distinct {
		if count[n] > 1 {
			distinct[i] = fmt.Sprintf("%s (×%d)", n, count[n])
		}
	}
	word := kind
	if len(distinct) > 1 {
		word += "s"
	}
	return "loaded " + word + " " + strings.Join(distinct, ", ")
}

// hint works out the git commands that print the instructions a run was
// served: the routine's file, or for an interactive run the skills and
// rules it loaded. Blueprints keep routines in routines/<name>.md, skills in
// skills/<name>/SKILL.md and rules in rules/<name>.mdc, inside the folder
// the factory's blueprint path: names, if it has one.
func (v View) hint(d *runs.Detail) hint {
	if d.BlueprintSHA == "" {
		return hint{}
	}
	var files []string
	if d.Routine != "" {
		files = append(files, "routines/"+d.Routine+".md")
	} else {
		for _, e := range d.Timeline {
			var file string
			switch {
			case e.Type == runs.EventLoad && e.Kind == runs.LoadSkill:
				file = "skills/" + e.Name + "/SKILL.md"
			case e.Type == runs.EventLoad && e.Kind == runs.LoadRule:
				file = "rules/" + e.Name + ".mdc"
			}
			if file != "" && !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
	}
	if len(files) == 0 {
		return hint{}
	}
	prefix, known := v.blueprintPath(d.Factory)
	s := hint{subfolder: !known}
	for _, f := range files {
		s.commands = append(s.commands, gitShow(d.BlueprintSHA, path.Join(prefix, f)))
	}
	return s
}

func (v View) blueprintPath(factory string) (string, bool) {
	if v.BlueprintPath == nil {
		return "", false
	}
	return v.BlueprintPath(factory)
}

// gitShow is the command that prints a file of the blueprint as it was at a
// commit.
func gitShow(sha, file string) string {
	return fmt.Sprintf("git -C <your blueprint clone> show %s:%s", gitx.Short(clean(sha)), clean(file))
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
