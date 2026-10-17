package runview

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/staltz/fabrica/internal/runs"
)

// MinRunPrefix is the fewest characters of a run ID the table shows.
const MinRunPrefix = 12

// Room the table keeps for the INTENT column, the one that gets cut first
// (in the order of "comfortable", then "at least"), and the least the other
// text columns are cut down to.
const (
	comfortableIntent = 12
	minIntent         = 8
	minOther          = 6
)

// column is one column of the table.
type column struct {
	head  string
	cells []string
	width int
	// fixed columns are never cut. The others (the text columns) are cut
	// when the table is too wide.
	fixed bool
}

// List writes runs as a table that fits in width columns, and a line under
// it. Long text is cut: first the intents, then the factory, routine and
// repo names. more says there are more runs than these. List is for a
// result that isn't empty.
//
// The factory column is left out when every run is from one factory, since
// there's no room to spare in 80 columns, and the line under the table
// names it.
func (v View) List(w io.Writer, list []runs.Summary, more bool, width int) {
	prefixes := UniquePrefixes(idsOf(list), MinRunPrefix)
	var started, outcome, factory, routine, repo, intent, run []string
	factories := map[string]bool{}
	for i, s := range list {
		factories[clean(s.Factory)] = true
		started = append(started, v.short(s.StartedAt))
		outcome = append(outcome, clean(s.Outcome))
		factory = append(factory, clean(s.Factory))
		routine = append(routine, orDash(clean(s.Routine)))
		repo = append(repo, shortRepo(clean(s.Repo)))
		intent = append(intent, clean(s.Intent))
		run = append(run, prefixes[i])
	}
	cols := []*column{{head: "STARTED", cells: started, fixed: true}, {head: "OUTCOME", cells: outcome, fixed: true}}
	if len(factories) > 1 {
		cols = append(cols, &column{head: "FACTORY", cells: factory})
	}
	cols = append(cols, &column{head: "ROUTINE", cells: routine}, &column{head: "REPO", cells: repo}, &column{head: "INTENT", cells: intent},
		&column{head: "RUN", cells: run, fixed: true})
	gap := strings.Repeat(" ", fit(cols, width))
	line := func(cell func(c *column) string) {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = pad(cut(cell(c), c.width), c.width)
		}
		fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, gap), " "))
	}
	line(func(c *column) string { return c.head })
	for i := range list {
		line(func(c *column) string { return c.cells[i] })
	}

	from := ""
	if len(factories) == 1 && list[0].Factory != "" {
		from = " from factory " + clean(list[0].Factory)
	}
	fmt.Fprintf(w, "\n%s%s. Show one with: fabrica runs show <run>\n", plural(len(list), "run"), from)
	if more {
		fmt.Fprintln(w, "There are more: raise --limit, or narrow things down with --since and filters.")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func idsOf(list []runs.Summary) []string {
	ids := make([]string, len(list))
	for i, s := range list {
		ids[i] = s.RunID
	}
	return ids
}

// fit sets the width of every column so that a row is at most width
// characters. The intent gives way first. When there's little room for it,
// the gaps between columns shrink from two spaces to one, and only when
// that isn't enough do the other text columns lose characters too, the
// widest first. It returns the gap between columns.
func fit(cols []*column, width int) (gap int) {
	for _, c := range cols {
		c.width = utf8.RuneCountInString(c.head)
		for _, cell := range c.cells {
			c.width = max(c.width, utf8.RuneCountInString(cell))
		}
	}
	intent := slices.IndexFunc(cols, func(c *column) bool { return c.head == "INTENT" })
	room := func() int {
		used := gap * (len(cols) - 1)
		for i, c := range cols {
			if i != intent {
				used += c.width
			}
		}
		return width - used
	}
	if gap = 2; room() < comfortableIntent {
		gap = 1
	}
	for room() < minIntent {
		widest := -1
		for i, c := range cols {
			if !c.fixed && i != intent && c.width > minOther && (widest < 0 || c.width > cols[widest].width) {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		cols[widest].width--
	}
	cols[intent].width = min(cols[intent].width, max(room(), 4))
	return gap
}

// UniquePrefixes gives each ID the shortest prefix that tells it apart from
// the other IDs, but at least least characters long.
func UniquePrefixes(ids []string, least int) []string {
	sorted := slices.Clone(ids)
	sort.Strings(sorted)
	common := func(a, b string) int {
		n := 0
		for n < len(a) && n < len(b) && a[n] == b[n] {
			n++
		}
		return n
	}
	need := map[string]int{}
	for i, id := range sorted {
		n := 0
		if i > 0 {
			n = max(n, common(id, sorted[i-1]))
		}
		if i+1 < len(sorted) {
			n = max(n, common(id, sorted[i+1]))
		}
		need[id] = n + 1
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[:min(len(id), max(least, need[id]))]
	}
	return out
}

// Empty says that no run matched.
func (v View) Empty(w io.Writer, since time.Time) {
	if since.IsZero() {
		fmt.Fprintln(w, "No runs found.")
	} else {
		fmt.Fprintf(w, "No runs found since %s.\n", v.short(since))
	}
	fmt.Fprintln(w, "Try a wider window, like `fabrica runs --since 30d`, or loosen the filters.")
}

// Candidates lists runs a prefix of an ID could mean, one to a line and in
// full, so any of them can be copied. more says there are others.
func (v View) Candidates(list []runs.Summary, more bool, width int) string {
	var b strings.Builder
	for _, s := range list {
		head := fmt.Sprintf("  %s  %s  ", s.RunID, v.short(s.StartedAt))
		fmt.Fprintf(&b, "%s%s\n", head, cut(clean(s.Intent), max(width-utf8.RuneCountInString(head), minIntent)))
	}
	if more {
		b.WriteString("  …and more\n")
	}
	return b.String()
}
