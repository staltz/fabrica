package runs

// This file is the dashboard's side of the read API: Stats, which sums up a
// stretch of run history in one go. Like List and Detail, only
// operator-plane code may call it (internal/mcpserver has a test that keeps
// it that way).

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// MaxStatsDays is the longest stretch of history Stats looks at. It keeps
// the dashboard fast on a database with tens of thousands of runs.
const MaxStatsDays = 90

// How many items the lists in Stats hold, unless the filter's Limit says
// otherwise.
const (
	defaultRecentNotes = 10
	defaultPRs         = 10
	maxLoads           = 50
	maxRefusals        = 100
)

// Stats is a stretch of run history, summed up for the dashboard. Counts
// are of the runs that started in the window (see Filter.Since), and the
// per-outcome maps hold the outcomes that have a run, apart from Totals and
// Friction, which always hold every outcome and every friction kind.
type Stats struct {
	Totals    map[string]int       // runs per outcome
	PerDay    []DayCounts          // oldest first; only days that have runs
	Routines  []RoutineStats       // most runs first
	Friction  map[string]int       // friction notes per kind
	Recent    []FrictionNote       // newest first
	Loads     []LoadCount          // most runs first
	Refusals  []RefusalGroup       // newest first
	PRs       []PRLink             // newest first
	PRCount   int                  // distinct PR links in the window, however many PRs lists
	Blueprint []BlueprintFirstSeen // oldest first
	// Factories is every factory with runs in the window, whatever
	// Filter.Factory says, so a page can offer to switch between them.
	Factories []string
}

// DayCounts is the runs that started on one day (UTC).
type DayCounts struct {
	Day      time.Time // midnight UTC
	Runs     int
	Outcomes map[string]int
}

// RoutineStats is the runs of one routine in one factory.
type RoutineStats struct {
	Factory string
	Routine string // empty for interactive runs
	Runs    int
	// Outcomes counts the routine's runs per outcome.
	Outcomes map[string]int
	// TopFriction is the kind of friction its agents reported most, or empty
	// when they reported none. TopFrictionCount is how many notes that was.
	TopFriction      string
	TopFrictionCount int
	LastRun          time.Time // when its newest run started
}

// FrictionNote is one friction note, with the run it came from.
type FrictionNote struct {
	At      time.Time
	Kind    string
	Note    string
	RunID   string
	Factory string
	Routine string
}

// LoadCount is how many runs loaded one skill, rule or routine.
type LoadCount struct {
	Kind string // LoadSkill, LoadRule or LoadRoutine
	Name string
	Runs int
}

// RefusalGroup is the times Fabrica turned runs away for the same reason: a
// failed checkpoint that Fabrica wrote itself, not an agent.
type RefusalGroup struct {
	Factory     string
	Routine     string
	Runtime     string
	Summary     string // Fabrica's reason
	Count       int
	Latest      time.Time
	LatestRunID string
}

// PRLink is a PR or merge request that a run linked to.
type PRLink struct {
	URL     string
	RunID   string // the newest run that linked to it
	Factory string
	Routine string
	Outcome string // of that run
	At      time.Time
}

// BlueprintFirstSeen is when a routine was first served from a blueprint
// commit.
type BlueprintFirstSeen struct {
	Factory string
	Routine string
	SHA     string
	At      time.Time // when the first run that got it started
}

// FrictionKinds lists the kinds of friction, in the order the docs give
// them.
func FrictionKinds() []string { return slices.Clone(frictionKinds) }

// statsWhere is the SQL condition on the runs table for a filter, and its
// arguments. The outcome only exists in run_overview, so a filter on it
// becomes a subquery. A joined query has to call the runs table "runs".
func (f Filter) statsWhere() (string, []any) {
	g := f
	g.Outcomes, g.Before = nil, ""
	cond, args := g.where()
	if len(f.Outcomes) > 0 {
		clause := "runs.run_id IN (SELECT o.run_id FROM run_overview o WHERE o.outcome IN (?" + strings.Repeat(", ?", len(f.Outcomes)-1) + "))"
		for _, o := range f.Outcomes {
			args = append(args, o)
		}
		if cond != "" {
			cond += " AND "
		}
		cond += clause
	}
	if cond == "" {
		cond = "1"
	}
	return cond, args
}

// StatsOption changes what Stats works out.
type StatsOption func(*statsOptions)

type statsOptions struct{ skipLoads bool }

// WithoutLoads leaves Loads empty. It's the costliest piece, because every
// run loads several things, so pages that don't show it can skip it.
func WithoutLoads() StatsOption { return func(o *statsOptions) { o.skipLoads = true } }

// Stats sums up the runs the filter picks. The window is Filter.Since, and
// never longer than MaxStatsDays. Before is ignored, and Limit, when set,
// is the most items to put in Recent and PRs.
//
// Each piece is a single parameterized query, and all of them are read in
// one transaction, so the numbers agree with each other.
func (d *DB) Stats(ctx context.Context, f Filter, opts ...StatsOption) (*Stats, error) {
	return d.stats(ctx, f, time.Now(), opts)
}

func (d *DB) stats(ctx context.Context, f Filter, now time.Time, opts []StatsOption) (*Stats, error) {
	var o statsOptions
	for _, opt := range opts {
		opt(&o)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if oldest := now.AddDate(0, 0, -MaxStatsDays); f.Since.IsZero() || f.Since.Before(oldest) {
		f.Since = oldest
	}
	notes, prs := defaultRecentNotes, defaultPRs
	if f.Limit > 0 {
		notes, prs = f.Limit, f.Limit
	}
	since := f.Since.UTC().Format(sinceLayout)

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	s := &Stats{Totals: map[string]int{}, Friction: map[string]int{}}
	for _, outcome := range Outcomes {
		s.Totals[outcome] = 0
	}
	for _, k := range frictionKinds {
		s.Friction[k] = 0
	}

	// The runs table's condition, for the pieces that don't need the
	// outcome (unless the filter does).
	cond, args := f.statsWhere()
	with := func(extra ...any) []any { return append(slices.Clone(args), extra...) }

	// Runs by day, factory, routine and outcome: one pass over the view,
	// because working out an outcome is the costly part. The totals, the
	// days and the routines all come from it.
	days := map[string]*DayCounts{}
	type key struct{ factory, routine string }
	routines := map[key]*RoutineStats{}
	overview := func() error {
		g := f
		g.Before = ""
		viewCond, viewArgs := g.where()
		if viewCond == "" {
			viewCond = "1"
		}
		return query(ctx, tx, `
			SELECT substr(started_at, 1, 10), COALESCE(factory, ''), COALESCE(routine, ''), outcome, COUNT(*), MAX(started_at)
			FROM run_overview WHERE `+viewCond+` GROUP BY 1, 2, 3, 4`, viewArgs, func(rows *sql.Rows) error {
			var day, factory, routine, outcome, last string
			var n int
			if err := rows.Scan(&day, &factory, &routine, &outcome, &n, &last); err != nil {
				return err
			}
			s.Totals[outcome] += n
			dc := days[day]
			if dc == nil {
				t, _ := time.Parse("2006-01-02", day)
				dc = &DayCounts{Day: t, Outcomes: map[string]int{}}
				days[day] = dc
			}
			dc.Runs += n
			dc.Outcomes[outcome] += n
			rs := routines[key{factory, routine}]
			if rs == nil {
				rs = &RoutineStats{Factory: factory, Routine: routine, Outcomes: map[string]int{}}
				routines[key{factory, routine}] = rs
			}
			rs.Runs += n
			rs.Outcomes[outcome] += n
			if t := parseTime(last); t.After(rs.LastRun) {
				rs.LastRun = t.Truncate(time.Second)
			}
			return nil
		})
	}

	// Friction per routine and kind, then the newest notes.
	type frictionCount struct {
		key
		kind string
		n    int
	}
	var frictionCounts []frictionCount
	friction := func() error {
		err := query(ctx, tx, `
			SELECT COALESCE(runs.factory, ''), COALESCE(runs.routine, ''), fr.kind, COUNT(*)
			FROM runs JOIN friction fr ON fr.run_id = runs.run_id WHERE `+cond+` GROUP BY 1, 2, 3`, args, func(rows *sql.Rows) error {
			var c frictionCount
			if err := rows.Scan(&c.factory, &c.routine, &c.kind, &c.n); err != nil {
				return err
			}
			frictionCounts = append(frictionCounts, c)
			return nil
		})
		if err != nil {
			return err
		}
		return query(ctx, tx, `
			SELECT fr.at, fr.kind, fr.note, runs.run_id, COALESCE(runs.factory, ''), COALESCE(runs.routine, '')
			FROM runs JOIN friction fr ON fr.run_id = runs.run_id WHERE `+cond+` ORDER BY fr.at DESC, fr.id DESC LIMIT ?`, with(notes), func(rows *sql.Rows) error {
			var n FrictionNote
			var at string
			if err := rows.Scan(&at, &n.Kind, &n.Note, &n.RunID, &n.Factory, &n.Routine); err != nil {
				return err
			}
			n.At = parseTime(at).Truncate(time.Second)
			s.Recent = append(s.Recent, n)
			return nil
		})
	}

	// How many runs loaded each skill, rule and routine.
	loads := func() error {
		if o.skipLoads {
			return nil
		}
		return query(ctx, tx, `
			SELECT l.kind, l.name, COUNT(DISTINCT l.run_id)
			FROM runs JOIN loads l ON l.run_id = runs.run_id WHERE `+cond+` GROUP BY l.kind, l.name
			ORDER BY 3 DESC, l.kind, l.name LIMIT ?`, with(maxLoads), func(rows *sql.Rows) error {
			var l LoadCount
			if err := rows.Scan(&l.Kind, &l.Name, &l.Runs); err != nil {
				return err
			}
			s.Loads = append(s.Loads, l)
			return nil
		})
	}

	// Refusals are failed checkpoints that Fabrica wrote
	// itself. SQLite takes the bare run_id from the row that has the newest
	// checkpoint.
	rest := func() error {
		err := query(ctx, tx, `
			SELECT COALESCE(runs.factory, ''), COALESCE(runs.routine, ''), COALESCE(runs.runtime, ''), c.summary, COUNT(*), MAX(c.at), runs.run_id
			FROM runs JOIN checkpoints c ON c.run_id = runs.run_id
			WHERE c.author = 'fabrica' AND c.status = 'failed' AND `+cond+`
			GROUP BY 1, 2, 3, 4 ORDER BY 6 DESC LIMIT ?`, with(maxRefusals), func(rows *sql.Rows) error {
			var g RefusalGroup
			var latest string
			if err := rows.Scan(&g.Factory, &g.Routine, &g.Runtime, &g.Summary, &g.Count, &latest, &g.LatestRunID); err != nil {
				return err
			}
			g.Latest = parseTime(latest).Truncate(time.Second)
			s.Refusals = append(s.Refusals, g)
			return nil
		})
		if err != nil {
			return err
		}

		// PRs and merge requests, the same links the view's pr_links holds.
		// A PR that several runs linked to shows once, with the newest of
		// them. Most checkpoints have no links, which is worth skipping
		// before unpacking any JSON. The window function counts the PRs
		// before the limit cuts the list.
		err = query(ctx, tx, `
			SELECT url, run_id, factory, routine, (SELECT o.outcome FROM run_overview o WHERE o.run_id = x.run_id), at, total FROM (
				SELECT l.value AS url, runs.run_id AS run_id, COALESCE(runs.factory, '') AS factory, COALESCE(runs.routine, '') AS routine,
				       MAX(c.at) AS at, COUNT(*) OVER () AS total
				FROM runs JOIN checkpoints c ON c.run_id = runs.run_id, json_each(c.links) l
				WHERE c.links <> '[]' AND (l.value LIKE '%/pull/%' OR l.value LIKE '%/merge_requests/%') AND `+cond+`
				GROUP BY l.value ORDER BY MAX(c.at) DESC LIMIT ?) x
			ORDER BY at DESC`, with(prs), func(rows *sql.Rows) error {
			var p PRLink
			var at string
			if err := rows.Scan(&p.URL, &p.RunID, &p.Factory, &p.Routine, &p.Outcome, &at, &s.PRCount); err != nil {
				return err
			}
			p.At = parseTime(at).Truncate(time.Second)
			s.PRs = append(s.PRs, p)
			return nil
		})
		if err != nil {
			return err
		}

		// When each blueprint commit was first served to each routine.
		// That's a fact about the commit, not about how its runs ended, so
		// outcomes don't matter, and it looks at every run there has been,
		// not just the window's: a commit that came before the window still
		// shows up if the window's runs got it, and the dashboard marks the
		// ones that came in the window.
		g := f
		g.Outcomes, g.Before, g.Since = nil, "", time.Time{}
		bpCond, bpArgs := g.where()
		if bpCond == "" {
			bpCond = "1"
		}
		err = query(ctx, tx, `
			SELECT COALESCE(factory, ''), routine, blueprint_sha, MIN(started_at)
			FROM runs WHERE blueprint_sha IS NOT NULL AND routine IS NOT NULL AND `+bpCond+`
			GROUP BY 1, 2, 3 HAVING MAX(started_at) >= ? ORDER BY 4, 1, 2, 3`, append(bpArgs, since), func(rows *sql.Rows) error {
			var b BlueprintFirstSeen
			var at string
			if err := rows.Scan(&b.Factory, &b.Routine, &b.SHA, &at); err != nil {
				return err
			}
			b.At = parseTime(at).Truncate(time.Second)
			s.Blueprint = append(s.Blueprint, b)
			return nil
		})
		if err != nil {
			return err
		}

		// Every factory with runs in the window.
		return query(ctx, tx, `SELECT DISTINCT factory FROM runs WHERE factory IS NOT NULL AND started_at >= ? ORDER BY factory`, []any{since}, func(rows *sql.Rows) error {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			s.Factories = append(s.Factories, name)
			return nil
		})
	}

	for _, piece := range []func() error{overview, friction, loads, rest} {
		if err := piece(); err != nil {
			return nil, err
		}
	}

	// The kinds come in a fixed order, so a tie goes to the earlier one.
	for _, c := range frictionCounts {
		s.Friction[c.kind] += c.n
		if rs := routines[c.key]; rs != nil &&
			(c.n > rs.TopFrictionCount || c.n == rs.TopFrictionCount && slices.Index(frictionKinds, c.kind) < slices.Index(frictionKinds, rs.TopFriction)) {
			rs.TopFriction, rs.TopFrictionCount = c.kind, c.n
		}
	}
	for _, dc := range days {
		s.PerDay = append(s.PerDay, *dc)
	}
	slices.SortFunc(s.PerDay, func(a, b DayCounts) int { return a.Day.Compare(b.Day) })
	for _, rs := range routines {
		s.Routines = append(s.Routines, *rs)
	}
	slices.SortFunc(s.Routines, func(a, b RoutineStats) int {
		return cmpDesc(a.Runs, b.Runs, a.Factory+"/"+a.Routine, b.Factory+"/"+b.Routine)
	})
	return s, nil
}

// sinceLayout is how Filter.where writes the earliest start time.
const sinceLayout = "2006-01-02T15:04:05"

// query runs a query and calls each for every row.
func query(ctx context.Context, q queryer, text string, args []any, each func(rows *sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// cmpDesc orders by a number, biggest first, then by a name.
func cmpDesc(a, b int, nameA, nameB string) int {
	if a != b {
		return b - a
	}
	return strings.Compare(nameA, nameB)
}
