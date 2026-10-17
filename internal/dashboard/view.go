package dashboard

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

// The windows the dashboard offers, in days.
var windows = []int{7, 30, 90}

const defaultDays = 30

// Which runs to show.
const (
	kindBoth        = ""
	kindRoutines    = "routines"
	kindInteractive = "interactive"
)

// view is what the filters at the top of a page say. It lives in the query
// string, so every view can be bookmarked and shared.
type view struct {
	Factory string
	Days    int
	Kind    string
}

// parseView reads the filters, falling back to the defaults for anything
// that isn't one of the choices.
func parseView(q url.Values) view {
	v := view{Days: defaultDays}
	if f := q.Get("factory"); len(f) <= 100 {
		v.Factory = f
	}
	if n, err := strconv.Atoi(q.Get("days")); err == nil && slices.Contains(windows, n) {
		v.Days = n
	}
	if k := q.Get("kind"); k == kindRoutines || k == kindInteractive {
		v.Kind = k
	}
	return v
}

// query is the view as a query string, with a leading ?, or nothing when
// every filter is at its default.
func (v view) query() string {
	q := url.Values{}
	if v.Factory != "" {
		q.Set("factory", v.Factory)
	}
	if v.Days != defaultDays {
		q.Set("days", strconv.Itoa(v.Days))
	}
	if v.Kind != kindBoth {
		q.Set("kind", v.Kind)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// since is when the view's window starts.
func (v view) since(now time.Time) time.Time { return now.AddDate(0, 0, -v.Days) }

// window says the window in words.
func (v view) window() string { return "last " + strconv.Itoa(v.Days) + " days" }

// filter is the run filter for the whole dashboard.
func (v view) filter(now time.Time) runs.Filter {
	f := runs.Filter{Factory: v.Factory, Since: v.since(now)}
	switch v.Kind {
	case kindRoutines:
		f.RoutinesOnly = true
	case kindInteractive:
		f.Routine = runs.InteractiveRoutine
	}
	return f
}

// chip is one choice among the filters.
type chip struct {
	Label   string
	Href    string
	Current bool
}

// chipGroup is a row of choices for one filter.
type chipGroup struct {
	Label string
	Chips []chip
}

// filterGroups makes the rows of choices at the top of a page. link gives
// the address of the page with another view, so the links keep the other
// filters as they are. A page without a say in a filter (the routine page
// has its factory in the address) passes it in skip.
func (v view) filterGroups(link func(view) string, factories []string, skip ...string) []chipGroup {
	href := func(change func(*view)) string {
		w := v
		change(&w)
		return link(w)
	}
	var groups []chipGroup
	if !slices.Contains(skip, "factory") {
		if v.Factory != "" && !slices.Contains(factories, v.Factory) {
			factories = append(slices.Clone(factories), v.Factory) // it has no runs here, but it's what was asked for
			slices.Sort(factories)
		}
		g := chipGroup{Label: "Factory", Chips: []chip{{Label: "All", Href: href(func(w *view) { w.Factory = "" }), Current: v.Factory == ""}}}
		for _, f := range factories {
			g.Chips = append(g.Chips, chip{Label: f, Href: href(func(w *view) { w.Factory = f }), Current: v.Factory == f})
		}
		groups = append(groups, g)
	}
	g := chipGroup{Label: "Window"}
	for _, n := range windows {
		g.Chips = append(g.Chips, chip{Label: strconv.Itoa(n) + " days", Href: href(func(w *view) { w.Days = n }), Current: v.Days == n})
	}
	groups = append(groups, g)
	if !slices.Contains(skip, "kind") {
		groups = append(groups, chipGroup{Label: "Show", Chips: []chip{
			{Label: "All runs", Href: href(func(w *view) { w.Kind = kindBoth }), Current: v.Kind == kindBoth},
			{Label: "Routines", Href: href(func(w *view) { w.Kind = kindRoutines }), Current: v.Kind == kindRoutines},
			{Label: "Interactive", Href: href(func(w *view) { w.Kind = kindInteractive }), Current: v.Kind == kindInteractive},
		}})
	}
	return groups
}

// kindWords says what a view's kind shows, for the empty states.
func (v view) kindWords() string {
	switch v.Kind {
	case kindRoutines:
		return "routine runs"
	case kindInteractive:
		return "interactive runs"
	}
	return "runs"
}

// scope says in words what narrows a view, like " for factory acme", or
// nothing.
func (v view) scope() string {
	var parts []string
	if v.Factory != "" {
		parts = append(parts, "factory "+v.Factory)
	}
	switch v.Kind {
	case kindRoutines:
		parts = append(parts, "routines only")
	case kindInteractive:
		parts = append(parts, "interactive runs only")
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
