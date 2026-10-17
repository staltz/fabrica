package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/runs"
)

type tile struct {
	Label, Value, Note string
}

// runHref is the address of a run's page, with the view carried along so
// the way back keeps its filters.
func runHref(base, id string, v view) string {
	return base + "/runs/" + url.PathEscape(id) + v.query()
}

// routineHref is the address of a routine's page, or "" when there can't be
// one. Agents choose the routine a run records (a refused local run has no
// factory at all), and a page exists only for names that follow the naming
// rule, so anything else is text and not a link that ends in a 404.
func routineHref(base, factory, routine string, v view) string {
	if names.Validate(factory) != nil || names.Validate(routine) != nil {
		return ""
	}
	return routineAddress(base, factory, routine, v)
}

// routineAddress is where a routine's page is, whatever its names are. A
// routine's page links to itself with it.
func routineAddress(base, factory, routine string, v view) string {
	return base + "/routines/" + url.PathEscape(factory) + "/" + url.PathEscape(routine) + v.query()
}

// noteRow is a friction note with where it came from.
type noteRow struct {
	At               timeCell
	Kind, Note       string
	Factory, Routine string
	RunHref, RunID   string
}

func noteRows(base string, v view, notes []runs.FrictionNote) []noteRow {
	var out []noteRow
	for _, n := range notes {
		out = append(out, noteRow{At: timeOf(n.At), Kind: n.Kind, Note: n.Note, Factory: n.Factory, Routine: n.Routine,
			RunHref: runHref(base, n.RunID, v), RunID: shortRun(n.RunID)})
	}
	return out
}

type routineRow struct {
	Factory, Routine string
	Href             string // empty for interactive sessions
	Runs             int
	Success, Detail  string
	Failed, Stale    int
	Friction         string
	LastRun          timeCell
}

type refusalRow struct {
	Latest                            timeCell
	Factory, Routine, Runtime, Reason string
	Count                             int
	Apply                             bool // the fix is `fabrica apply`
	RunHref, RunID                    string
}

type prRow struct {
	URL, Label       string
	Link             bool
	Factory, Routine string
	Outcome          string
	RunHref, RunID   string
	At               timeCell
}

type overviewPage struct {
	pageBase
	Filters   []chipGroup
	Window    string
	Tiles     []tile
	NoRuns    bool
	EmptyRuns string
	Chart     barChart

	Routines      []routineRow
	EmptyRoutines string
	Refusals      []refusalRow
	EmptyRefusals string

	FrictionTotal int
	Friction      hbarChart
	Notes         []noteRow
	EmptyFriction string

	PRs      []prRow
	EmptyPRs string
}

func sum(m map[string]int) (n int) {
	for _, c := range m {
		n += c
	}
	return n
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request, base string) {
	v := parseView(r.URL.Query())
	now := h.now()
	stats, err := h.db.Stats(r.Context(), v.filter(now), runs.WithoutLoads())
	if err != nil {
		h.failed(w, base, err)
		return
	}
	window := v.window()
	total := sum(stats.Totals)
	rate, rateNote := successRate(stats.Totals[runs.OutcomeSucceeded], stats.Totals[runs.OutcomeFailed])
	refused := 0
	for _, g := range stats.Refusals {
		refused += g.Count
	}

	p := overviewPage{pageBase: h.base("Overview", base, true), Filters: v.filterGroups(func(w view) string { return base + w.query() }, stats.Factories), Window: window}
	p.Tiles = []tile{
		{"Runs", fmt.Sprint(total), "in the " + window},
		{"Success rate", rate, rateNote},
		{"Failed", fmt.Sprint(stats.Totals[runs.OutcomeFailed]), "last report was failed"},
		{"Stale", fmt.Sprint(stats.Totals[runs.OutcomeStale]), "quiet for 6 hours, never finished"},
		{"PRs touched", fmt.Sprint(stats.PRCount), "linked from a checkpoint"},
		{"Refusals", fmt.Sprint(refused), "runs Fabrica turned away"},
	}
	p.NoRuns = total == 0
	p.EmptyRuns = "No runs in the " + window + ". Runs show up here once agents call run_begin."
	if v.Factory != "" || v.Kind != kindBoth {
		p.EmptyRuns = "No " + v.kindWords() + " in the " + window + v.scope() + ". Try a longer window or other filters."
	}
	p.Chart = newBarChart("runs-per-day", daysBetween(v.since(now), now, stats.PerDay), window)

	for _, rs := range stats.Routines {
		row := routineRow{Factory: rs.Factory, Routine: rs.Routine, Runs: rs.Runs,
			Failed: rs.Outcomes[runs.OutcomeFailed], Stale: rs.Outcomes[runs.OutcomeStale], Friction: "–", LastRun: timeOf(rs.LastRun)}
		row.Success, row.Detail = successRate(rs.Outcomes[runs.OutcomeSucceeded], rs.Outcomes[runs.OutcomeFailed])
		row.Href = routineHref(base, rs.Factory, rs.Routine, v)
		if rs.TopFriction != "" {
			row.Friction = fmt.Sprintf("%s (%d)", rs.TopFriction, rs.TopFrictionCount)
		}
		p.Routines = append(p.Routines, row)
	}
	p.EmptyRoutines = "No routines ran in the " + window + v.scope() + ". They show up here when an agent calls run_begin with a routine."

	for _, g := range stats.Refusals {
		p.Refusals = append(p.Refusals, refusalRow{Latest: timeOf(g.Latest), Factory: g.Factory, Routine: g.Routine, Runtime: g.Runtime,
			Reason: g.Summary, Count: g.Count, Apply: staleStub(g.Summary), RunHref: runHref(base, g.LatestRunID, v), RunID: shortRun(g.LatestRunID)})
	}
	p.EmptyRefusals = "No refusals in the " + window + v.scope() + ". Fabrica turns a run away when, say, its stub is out of date."

	p.FrictionTotal = sum(stats.Friction)
	kinds := runs.FrictionKinds()
	slices.SortStableFunc(kinds, func(a, b string) int { return stats.Friction[b] - stats.Friction[a] })
	var rows []hbarRow
	var counts []int
	for _, k := range kinds {
		rows = append(rows, hbarRow{Label: k, Title: fmt.Sprintf("%s: %s", frictionGloss[k], plural(stats.Friction[k], "note", "notes"))})
		counts = append(counts, stats.Friction[k])
	}
	p.Friction = newHBarChart("friction-kinds", "Friction notes by kind", rows, counts)
	p.Notes = noteRows(base, v, stats.Recent)
	p.EmptyFriction = "No friction notes in the " + window + v.scope() + ". Agents leave one in run_report when something slows them down."

	for _, pr := range stats.PRs {
		p.PRs = append(p.PRs, prRow{URL: pr.URL, Label: prLabel(pr.URL), Link: webLink(pr.URL), Factory: pr.Factory, Routine: pr.Routine,
			Outcome: pr.Outcome, RunHref: runHref(base, pr.RunID, v), RunID: shortRun(pr.RunID), At: timeOf(pr.At)})
	}
	p.EmptyPRs = "No PRs in the " + window + v.scope() + ". They show up here when a run links one in run_report."

	h.render(w, r, http.StatusOK, "overview", p)
}

type bpRow struct {
	SHA     string
	At      timeCell
	Subject string
}

type runRow struct {
	Started               timeCell
	Outcome, Intent, Repo string
	Runtime               string
	Friction              int
	Href, ID              string
}

type frictionGroup struct {
	Kind, Gloss string
	Count       int
	Notes       []noteRow
}

type loadRow struct {
	Kind, Name string
	Runs       int
	Share      string
}

type routinePage struct {
	pageBase
	Filters          []chipGroup
	BackHref         string
	Factory, Routine string
	Window           string
	Tiles            []tile
	NoRuns           bool
	EmptyRuns        string
	Line             lineChart
	Blueprint        []bpRow
	Runs             []runRow
	RunsNote         string
	FrictionTotal    int
	FrictionGroups   []frictionGroup
	FrictionNote     string
	Loads            []loadRow
}

// How much of a routine's history its page lists.
const (
	routineRuns  = 50
	routineNotes = 100
)

func (h *handler) routine(w http.ResponseWriter, r *http.Request, base, factory, routine string) {
	ctx := r.Context()
	v := parseView(r.URL.Query())
	now := h.now()

	// A routine is known by its runs. The interactive "routine" isn't one.
	known := false
	if routine != runs.InteractiveRoutine {
		any, _, err := h.db.List(ctx, runs.Filter{Factory: factory, Routine: routine, Limit: 1})
		if err != nil {
			h.failed(w, base, err)
			return
		}
		known = len(any) > 0
	}
	if !known {
		h.notFound(w, base, fmt.Sprintf("There are no runs of routine %s in factory %s, so there's nothing to show. The routine's name or its factory may be misspelled.", routine, factory))
		return
	}

	f := runs.Filter{Factory: factory, Routine: routine, Since: v.since(now)}
	statsFilter := f
	statsFilter.Limit = routineNotes
	stats, err := h.db.Stats(ctx, statsFilter)
	if err != nil {
		h.failed(w, base, err)
		return
	}
	listFilter := f
	listFilter.Limit = routineRuns
	list, next, err := h.db.List(ctx, listFilter)
	if err != nil {
		h.failed(w, base, err)
		return
	}

	window := v.window()
	total := sum(stats.Totals)
	rate, rateNote := successRate(stats.Totals[runs.OutcomeSucceeded], stats.Totals[runs.OutcomeFailed])
	p := routinePage{
		pageBase: h.base(routine+" in "+factory, base, true), BackHref: base + v.query(),
		Factory: factory, Routine: routine, Window: window,
		// The window links keep the view's other filters, so the way back to
		// the overview still has them.
		Filters: v.filterGroups(func(w view) string { return routineAddress(base, factory, routine, w) }, nil, "factory", "kind"),
	}
	p.Tiles = []tile{
		{"Runs", fmt.Sprint(total), "in the " + window},
		{"Success rate", rate, rateNote},
		{"Failed", fmt.Sprint(stats.Totals[runs.OutcomeFailed]), "last report was failed"},
		{"Stale", fmt.Sprint(stats.Totals[runs.OutcomeStale]), "quiet for 6 hours, never finished"},
		{"Friction notes", fmt.Sprint(sum(stats.Friction)), "left by its agents"},
	}
	p.NoRuns = total == 0
	p.EmptyRuns = "No runs of this routine in the " + window + ". Try a longer window."

	// Blueprint commits that were first served within the window.
	days := daysBetween(v.since(now), now, stats.PerDay)
	var marks []blueprintMark
	for _, b := range stats.Blueprint {
		if b.At.Before(days[0].Day) {
			continue
		}
		m := blueprintMark{SHA: b.SHA, At: b.At}
		if h.opts.Subject != nil {
			sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			m.Subject = h.opts.Subject(sctx, factory, b.SHA)
			cancel()
		}
		marks = append(marks, m)
		p.Blueprint = append(p.Blueprint, bpRow{SHA: shortSHA(b.SHA), At: timeOf(b.At), Subject: m.Subject})
	}
	p.Line = newLineChart("failure-rate", days, marks, window)

	for _, s := range list {
		p.Runs = append(p.Runs, runRow{Started: timeOf(s.StartedAt), Outcome: s.Outcome, Intent: s.Intent, Repo: repoName(s.Repo), Runtime: s.Runtime,
			Friction: s.FrictionCount, Href: runHref(base, s.RunID, v), ID: shortRun(s.RunID)})
	}
	if next != "" {
		p.RunsNote = fmt.Sprintf("Showing the newest %d of %s.", len(list), plural(total, "run", "runs"))
	}

	p.FrictionTotal = sum(stats.Friction)
	notes := noteRows(base, v, stats.Recent)
	for _, k := range runs.FrictionKinds() {
		g := frictionGroup{Kind: k, Gloss: frictionGloss[k], Count: stats.Friction[k]}
		for _, n := range notes {
			if n.Kind == k {
				g.Notes = append(g.Notes, n)
			}
		}
		if g.Count > 0 {
			p.FrictionGroups = append(p.FrictionGroups, g)
		}
	}
	if p.FrictionTotal > len(notes) {
		p.FrictionNote = fmt.Sprintf("Showing the newest %d of %d notes.", len(notes), p.FrictionTotal)
	}

	for _, l := range stats.Loads {
		if l.Kind == runs.LoadRoutine {
			continue // every run loads its routine
		}
		p.Loads = append(p.Loads, loadRow{Kind: l.Kind, Name: l.Name, Runs: l.Runs, Share: fmt.Sprintf("%d of %s", l.Runs, plural(total, "run", "runs"))})
	}

	h.render(w, r, http.StatusOK, "routine", p)
}

type tlLink struct {
	URL  string
	Link bool
}

type tlRow struct {
	Clock, ISO string
	Class      string // load, progress, succeeded, failed or friction
	Label      string
	Who        string // fabrica, for Fabrica's own checkpoints
	Prefix     string // what kind of thing a load row loaded
	Items      []string
	Text       string
	Links      []tlLink
}

type runPage struct {
	pageBase
	BackHref      string
	ID            string
	Outcome       string
	Intent        string
	Factory       string
	Served        string
	SHA, FullSHA  string
	Routine       string
	RoutineHref   string
	Runtime, Repo string
	Session       string
	SessionLink   bool
	Stub          string
	Started       timeCell
	LastHeard     string
	Timeline      []tlRow
	ServedHint    string
}

func (h *handler) run(w http.ResponseWriter, r *http.Request, base, id string) {
	v := parseView(r.URL.Query())
	d, err := h.db.Detail(r.Context(), id)
	if errors.Is(err, runs.ErrNoRun) {
		h.notFound(w, base, "There's no run "+id+".")
		return
	}
	if err != nil {
		h.failed(w, base, err)
		return
	}
	p := runPage{
		pageBase: h.base("Run "+shortRun(d.RunID), base, true), BackHref: base + v.query(),
		ID: d.RunID, Outcome: d.Outcome, Intent: d.Intent, Factory: d.Factory, Served: "served " + map[string]string{runs.ServedLocal: "locally", runs.ServedRemote: "remotely"}[d.ServedBy],
		SHA: shortSHA(d.BlueprintSHA), FullSHA: d.BlueprintSHA, Routine: d.Routine, Runtime: d.Runtime, Repo: d.Repo,
		Session: d.Session, SessionLink: sessionLink(d.Session), Stub: d.Stub, Started: timeOf(d.StartedAt),
	}
	p.RoutineHref = routineHref(base, d.Factory, d.Routine, v)
	if d.CheckpointCount == 0 {
		p.LastHeard = "no checkpoints yet"
	} else {
		p.LastHeard = fmt.Sprintf("last heard from %s UTC (%s later)", clock(d.LastAt), d.LastAt.Sub(d.StartedAt).Round(time.Second))
	}
	// People paste this into a shell, and the routine is whatever the agent
	// sent run_begin, so only a name that follows the naming rule gets one.
	if d.BlueprintSHA != "" && names.Validate(d.Routine) == nil {
		p.ServedHint = gitx.ShowCommand(d.BlueprintSHA, "routines/"+d.Routine+".md")
	}

	for _, e := range d.Timeline {
		row := tlRow{Clock: clock(e.At), ISO: e.At.UTC().Format(time.RFC3339)}
		switch e.Type {
		case runs.EventLoad:
			// Loads of one kind in a row make one line.
			if n := len(p.Timeline); n > 0 && p.Timeline[n-1].Class == "load" && p.Timeline[n-1].Prefix == e.Kind {
				p.Timeline[n-1].Items = append(p.Timeline[n-1].Items, e.Name)
				continue
			}
			row.Class, row.Label, row.Prefix, row.Items = "load", "loaded", e.Kind, []string{e.Name}
		case runs.EventCheckpoint:
			row.Class, row.Label, row.Text = e.Status, e.Status, e.Summary
			if e.Author == runs.ByFabrica {
				row.Who = "fabrica"
			}
			for _, l := range e.Links {
				row.Links = append(row.Links, tlLink{URL: l, Link: webLink(l)})
			}
		case runs.EventFriction:
			row.Class, row.Label, row.Text = "friction", "friction", e.Kind+": "+e.Note
		}
		p.Timeline = append(p.Timeline, row)
	}
	for i := range p.Timeline {
		if row := &p.Timeline[i]; row.Class == "load" && len(row.Items) > 1 {
			row.Prefix += "s"
		}
	}
	h.render(w, r, http.StatusOK, "run", p)
}
