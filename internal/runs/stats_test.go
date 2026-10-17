package runs

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/testkit"
)

// A little history to sum up. Times are relative to now, because the view
// works out "stale" from the clock.
type statsWorld struct {
	db  *DB
	now time.Time
	ids map[string]string // by name
}

func (w *statsWorld) at(ago time.Duration) time.Time { return w.now.Add(-ago) }

const day = 24 * time.Hour

// run begins a run and gives it checkpoints (oldest first) and loads, which
// are written like "skill:pr-review".
func (w *statsWorld) run(t *testing.T, name string, ago time.Duration, r Run, loads []string, cps ...Checkpoint) {
	t.Helper()
	ctx := context.Background()
	r.ID, r.StartedAt, r.Intent, r.ServedBy = NewID(w.at(ago)), w.at(ago), "intent of "+name, ServedRemote
	if err := w.db.Begin(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, l := range loads {
		kind, name, _ := cutKind(l)
		if err := w.db.Loaded(ctx, r.ID, kind, name, w.at(ago)); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cps {
		if c.Author == "" {
			c.Author = ByAgent
		}
		if c.Summary == "" {
			c.Summary = "did something"
		}
		if c.At.IsZero() {
			c.At = w.at(ago).Add(time.Duration(i+1) * time.Minute)
		}
		if err := w.db.AddCheckpoint(ctx, r.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	w.ids[name] = r.ID
}

func cutKind(s string) (kind, name string, ok bool) {
	for i := range s {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", s, false
}

func newStatsWorld(t *testing.T) *statsWorld {
	t.Helper()
	w := &statsWorld{db: openTest(t), now: time.Now().UTC(), ids: map[string]string{}}
	pr := func(n int) []string { return []string{fmt.Sprintf("https://github.com/acme/app/pull/%d", n)} }
	acme := func(routine, sha string) Run {
		return Run{Factory: "acme", Routine: routine, Runtime: "claude-code-cloud", Repo: "github.com/acme/app", BlueprintSHA: sha}
	}
	// pr-review: its first blueprint commit (aaaa) came 10 days ago, the
	// second (bbbb) 2 days ago.
	w.run(t, "old-ok", 10*day, acme("pr-review", "aaaa"), []string{"skill:pr-review"}, Checkpoint{Status: Succeeded, Links: pr(1)})
	w.run(t, "ok", 3*day, acme("pr-review", "aaaa"), []string{"skill:pr-review", "rule:typescript"}, Checkpoint{Status: Succeeded, Links: pr(2)})
	w.run(t, "failed", 2*day, acme("pr-review", "bbbb"), []string{"skill:pr-review", "routine:pr-review"},
		Checkpoint{Status: Progress, Friction: []Friction{{Kind: "navigation", Note: "couldn't find the retry config"}, {Kind: "navigation", Note: "docs were scattered"}}},
		Checkpoint{Status: Failed, Links: pr(2)}) // the same PR as "ok"
	w.run(t, "stale", 10*time.Hour, acme("pr-review", "bbbb"), nil, Checkpoint{Status: Progress})
	w.run(t, "going", time.Minute, acme("pr-review", "bbbb"), []string{"skill:pr-review"})
	// autofix has been refused twice for an old stub, and once for another reason.
	w.run(t, "refused1", 5*day, acme("autofix", "cccc"), nil,
		Checkpoint{Status: Failed, Author: ByFabrica, Summary: "Refused: stub is outdated; the factory needs fabrica apply"})
	w.run(t, "refused2", day, acme("autofix", "cccc"), nil,
		Checkpoint{Status: Failed, Author: ByFabrica, Summary: "Refused: stub is outdated; the factory needs fabrica apply"})
	w.run(t, "refused3", 2*time.Hour, acme("autofix", "cccc"), nil,
		Checkpoint{Status: Failed, Author: ByFabrica, Summary: "Refused: the repo's AGENTS.md is missing the Fabrica line"})
	// An agent that failed on its own isn't a refusal.
	w.run(t, "agent-failed", 4*time.Hour, acme("autofix", "cccc"), nil,
		Checkpoint{Status: Failed, Friction: []Friction{{Kind: "tools", Note: "the linter was slow"}}})
	// Interactive, in another factory, and outside the windows.
	w.run(t, "interactive", day, Run{Factory: "acme", Repo: "github.com/acme/app", Runtime: "claude-code-local", BlueprintSHA: "aaaa"}, []string{"skill:pdf"},
		Checkpoint{Status: Succeeded})
	w.run(t, "other", 4*day, Run{Factory: "other", Routine: "nightly", Runtime: "codex-cli", Repo: "github.com/o/o", BlueprintSHA: "dddd"}, nil,
		Checkpoint{Status: Succeeded, Links: []string{"https://gitlab.com/o/o/-/merge_requests/9", "https://github.com/o/o/issues/3"}})
	w.run(t, "sixty", 60*day, acme("pr-review", "aaaa"), nil, Checkpoint{Status: Succeeded})
	w.run(t, "ancient", 100*day, acme("pr-review", "aaaa"), nil, Checkpoint{Status: Succeeded})
	return w
}

func (w *statsWorld) stats(t *testing.T, f Filter) *Stats {
	t.Helper()
	s, err := w.db.stats(context.Background(), f, w.now, nil)
	if err != nil {
		t.Fatalf("Stats(%+v): %v", f, err)
	}
	return s
}

func TestStatsTotalsAndDays(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	// 11 runs began in the last 30 days: old-ok (10d), ok, failed, stale,
	// going, three refusals, agent-failed, interactive and other. The
	// 60-day-old run and the ancient one are outside.
	want := map[string]int{OutcomeSucceeded: 4, OutcomeFailed: 5, OutcomeInProgress: 1, OutcomeStale: 1}
	if !reflect.DeepEqual(s.Totals, want) {
		t.Errorf("Totals = %v, want %v", s.Totals, want)
	}

	var total int
	for i, d := range s.PerDay {
		sum := 0
		for _, n := range d.Outcomes {
			sum += n
		}
		if sum != d.Runs {
			t.Errorf("day %v: %d runs but outcomes add up to %d", d.Day, d.Runs, sum)
		}
		if i > 0 && !s.PerDay[i-1].Day.Before(d.Day) {
			t.Errorf("days aren't oldest first: %v then %v", s.PerDay[i-1].Day, d.Day)
		}
		if d.Day != d.Day.Truncate(24*time.Hour) {
			t.Errorf("day %v isn't midnight UTC", d.Day)
		}
		total += d.Runs
	}
	if total != 11 {
		t.Errorf("days hold %d runs, want 11", total)
	}
	// Two days ago: "failed" began then, and "going" began a minute ago.
	byDay := map[string]DayCounts{}
	for _, d := range s.PerDay {
		byDay[d.Day.Format("2006-01-02")] = d
	}
	if d := byDay[w.at(2*day).Format("2006-01-02")]; d.Outcomes[OutcomeFailed] < 1 {
		t.Errorf("the day of the failed run says %+v", d)
	}
	if d := byDay[w.at(time.Minute).Format("2006-01-02")]; d.Outcomes[OutcomeInProgress] != 1 {
		t.Errorf("today says %+v", d)
	}
}

func TestStatsWindowIsAtMost90Days(t *testing.T) {
	w := newStatsWorld(t)
	for _, f := range []Filter{{}, {Since: w.at(1000 * day)}} {
		s := w.stats(t, f)
		sum := 0
		for _, n := range s.Totals {
			sum += n
		}
		if sum != 12 { // the 60-day-old run is in, the ancient one is out
			t.Errorf("Stats(%+v) counted %d runs, want 12", f, sum)
		}
	}
	// A shorter window is respected.
	s := w.stats(t, Filter{Since: w.at(7 * day)})
	sum := 0
	for _, n := range s.Totals {
		sum += n
	}
	if sum != 10 {
		t.Errorf("7 days counted %d runs, want 10", sum)
	}
}

func TestStatsRoutines(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	type row struct {
		factory, routine string
		runs             int
		outcomes         map[string]int
		friction         string
		frictionN        int
	}
	var got []row
	for _, r := range s.Routines {
		got = append(got, row{r.Factory, r.Routine, r.Runs, r.Outcomes, r.TopFriction, r.TopFrictionCount})
	}
	want := []row{
		{"acme", "pr-review", 5, map[string]int{OutcomeSucceeded: 2, OutcomeFailed: 1, OutcomeStale: 1, OutcomeInProgress: 1}, "navigation", 2},
		{"acme", "autofix", 4, map[string]int{OutcomeFailed: 4}, "tools", 1},
		{"acme", "", 1, map[string]int{OutcomeSucceeded: 1}, "", 0},
		{"other", "nightly", 1, map[string]int{OutcomeSucceeded: 1}, "", 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Routines =\n%+v\nwant\n%+v", got, want)
	}
	for _, r := range s.Routines {
		if r.Routine == "pr-review" {
			// Its newest run began a minute ago.
			if d := w.at(time.Minute).Sub(r.LastRun); d < -2*time.Second || d > time.Second {
				t.Errorf("pr-review last ran %v, want about %v", r.LastRun, w.at(time.Minute))
			}
		}
	}
}

func TestStatsTopFrictionTiesGoToTheEarlierKind(t *testing.T) {
	w := newStatsWorld(t)
	w.run(t, "tie", time.Hour, Run{Factory: "acme", Routine: "tied"}, nil,
		Checkpoint{Status: Succeeded, Friction: []Friction{{Kind: "tools", Note: "one"}, {Kind: "standards", Note: "two"}}})
	for _, r := range w.stats(t, Filter{Since: w.at(30 * day), Routine: "tied"}).Routines {
		if r.TopFriction != "standards" || r.TopFrictionCount != 1 {
			t.Errorf("a tie between tools and standards went to %s (%d)", r.TopFriction, r.TopFrictionCount)
		}
	}
}

func TestStatsFrictionAndLoads(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	wantFriction := map[string]int{"navigation": 2, "checks": 0, "standards": 0, "tools": 1, "info": 0, "other": 0}
	if !reflect.DeepEqual(s.Friction, wantFriction) {
		t.Errorf("Friction = %v, want %v", s.Friction, wantFriction)
	}
	// Newest first: the linter note came from a run 4 hours ago, the others
	// two days ago, and notes from one checkpoint keep their order.
	var notes []string
	for _, n := range s.Recent {
		notes = append(notes, n.Note)
	}
	if want := []string{"the linter was slow", "docs were scattered", "couldn't find the retry config"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("Recent = %v, want %v", notes, want)
	}
	if r := s.Recent[0]; r.RunID != w.ids["agent-failed"] || r.Kind != "tools" || r.Factory != "acme" || r.Routine != "autofix" {
		t.Errorf("the newest note is %+v", r)
	}

	// Limit shortens the list of notes.
	if short := w.stats(t, Filter{Since: w.at(30 * day), Limit: 2}); len(short.Recent) != 2 {
		t.Errorf("with a limit of 2, Recent has %d notes", len(short.Recent))
	}

	type load struct {
		kind, name string
		runs       int
	}
	var loads []load
	for _, l := range s.Loads {
		loads = append(loads, load{l.Kind, l.Name, l.Runs})
	}
	want := []load{
		{"skill", "pr-review", 4}, // "old-ok", "ok", "failed" and "going"
		{"routine", "pr-review", 1}, {"rule", "typescript", 1}, {"skill", "pdf", 1},
	}
	if !reflect.DeepEqual(loads, want) {
		t.Errorf("Loads = %v, want %v", loads, want)
	}
}

func TestStatsRefusals(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	if len(s.Refusals) != 2 {
		t.Fatalf("Refusals = %+v, want 2 groups", s.Refusals)
	}
	// Newest first.
	if g := s.Refusals[0]; g.Summary != "Refused: the repo's AGENTS.md is missing the Fabrica line" || g.Count != 1 || g.LatestRunID != w.ids["refused3"] {
		t.Errorf("first refusal = %+v", g)
	}
	g := s.Refusals[1]
	if g.Factory != "acme" || g.Routine != "autofix" || g.Runtime != "claude-code-cloud" || g.Count != 2 || g.LatestRunID != w.ids["refused2"] {
		t.Errorf("second refusal = %+v", g)
	}
	if d := w.at(day).Add(time.Minute).Sub(g.Latest); d < -2*time.Second || d > time.Second {
		t.Errorf("latest = %v", g.Latest)
	}
	// A shorter window leaves out the 5-day-old refusal.
	short := w.stats(t, Filter{Since: w.at(3 * day)})
	if len(short.Refusals) != 2 || short.Refusals[1].Count != 1 {
		t.Errorf("Refusals in 3 days = %+v", short.Refusals)
	}
}

func TestStatsPRs(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	// Three PRs: acme/app 1 and 2, and a GitLab merge request. Issues don't
	// count, and PR 2, linked by two runs, counts once.
	if s.PRCount != 3 {
		t.Errorf("PRCount = %d, want 3", s.PRCount)
	}
	var urls []string
	for _, p := range s.PRs {
		urls = append(urls, p.URL)
	}
	// Newest link first: "failed" (2 days ago) linked PR 2, "other" (4 days)
	// the merge request, "ok" (3 days) PR 2 too, "old-ok" (10 days) PR 1.
	want := []string{"https://github.com/acme/app/pull/2", "https://gitlab.com/o/o/-/merge_requests/9", "https://github.com/acme/app/pull/1"}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("PRs = %v, want %v", urls, want)
	}
	if p := s.PRs[0]; p.RunID != w.ids["failed"] || p.Outcome != OutcomeFailed || p.Routine != "pr-review" || p.Factory != "acme" {
		t.Errorf("the newest PR is %+v", p)
	}
	if short := w.stats(t, Filter{Since: w.at(30 * day), Limit: 1}); len(short.PRs) != 1 || short.PRCount != 3 {
		t.Errorf("with a limit of 1: %d PRs listed, %d counted", len(short.PRs), short.PRCount)
	}
}

func TestStatsBlueprint(t *testing.T) {
	w := newStatsWorld(t)
	s := w.stats(t, Filter{Since: w.at(30 * day)})
	type first struct{ factory, routine, sha string }
	var got []first
	for _, b := range s.Blueprint {
		got = append(got, first{b.Factory, b.Routine, b.SHA})
	}
	// Oldest first: aaaa 100 days ago, cccc 5 days ago, dddd 4 days ago and
	// bbbb 2 days ago. The interactive run's commit isn't a routine's.
	want := []first{{"acme", "pr-review", "aaaa"}, {"acme", "autofix", "cccc"}, {"other", "nightly", "dddd"}, {"acme", "pr-review", "bbbb"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Blueprint = %v, want %v", got, want)
	}
	if d := w.at(2 * day).Sub(s.Blueprint[3].At); d < -time.Second || d > time.Second {
		t.Errorf("bbbb first served %v, want about %v", s.Blueprint[3].At, w.at(2*day))
	}

	// A commit from before the window shows up if the window's runs got it,
	// with the date it was really first served: aaaa began 100 days ago.
	short := w.stats(t, Filter{Since: w.at(5 * day), Factory: "acme", Routine: "pr-review"})
	got = nil
	for _, b := range short.Blueprint {
		got = append(got, first{b.Factory, b.Routine, b.SHA})
	}
	if want := []first{{"acme", "pr-review", "aaaa"}, {"acme", "pr-review", "bbbb"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Blueprint for 5 days = %v, want %v", got, want)
	}
	if d := w.at(100 * day).Sub(short.Blueprint[0].At); d < -time.Second || d > time.Second {
		t.Errorf("aaaa first served %v, want about %v", short.Blueprint[0].At, w.at(100*day))
	}
	// Interactive runs have no routine to mark.
	if inter := w.stats(t, Filter{Routine: InteractiveRoutine}); len(inter.Blueprint) != 0 {
		t.Errorf("Blueprint for interactive runs = %+v", inter.Blueprint)
	}
}

func TestStatsFilters(t *testing.T) {
	w := newStatsWorld(t)
	since := w.at(30 * day)
	total := func(s *Stats) (n int) {
		for _, c := range s.Totals {
			n += c
		}
		return n
	}
	for _, tc := range []struct {
		name string
		f    Filter
		runs int
	}{
		{"factory", Filter{Factory: "other"}, 1},
		{"routine", Filter{Factory: "acme", Routine: "autofix"}, 4},
		{"interactive", Filter{Routine: InteractiveRoutine}, 1},
		{"routines only", Filter{RoutinesOnly: true}, 10},
		{"routine beats routines only", Filter{Routine: "nightly", RoutinesOnly: true}, 1},
		{"repo", Filter{Repo: "github.com/o/o"}, 1},
		{"runtime", Filter{Runtime: "claude-code-local"}, 1},
		{"outcome", Filter{Outcomes: []string{OutcomeStale, OutcomeInProgress}}, 2},
		{"outcome and factory", Filter{Factory: "acme", Outcomes: []string{OutcomeSucceeded}}, 3},
		{"nothing", Filter{Factory: "nope"}, 0},
		{"paging is ignored", Filter{Before: "run_0", Limit: 3}, 11},
	} {
		tc.f.Since = since
		s := w.stats(t, tc.f)
		if got := total(s); got != tc.runs {
			t.Errorf("%s: %d runs, want %d", tc.name, got, tc.runs)
		}
		// Every piece follows the filter, so the pieces agree: what the
		// routines add up to is what the days add up to.
		fromRoutines, fromDays := 0, 0
		for _, r := range s.Routines {
			fromRoutines += r.Runs
		}
		for _, d := range s.PerDay {
			fromDays += d.Runs
		}
		if fromRoutines != tc.runs || fromDays != tc.runs {
			t.Errorf("%s: routines add up to %d and days to %d, want %d", tc.name, fromRoutines, fromDays, tc.runs)
		}
	}

	// The outcome filter reaches the pieces that don't read the outcome.
	s := w.stats(t, Filter{Since: since, Outcomes: []string{OutcomeStale}})
	if len(s.Recent) != 0 || len(s.PRs) != 0 || len(s.Refusals) != 0 || s.PRCount != 0 {
		t.Errorf("stale runs have no notes, PRs or refusals, but: %+v", s)
	}
	s = w.stats(t, Filter{Since: since, Outcomes: []string{OutcomeFailed}})
	if len(s.Refusals) != 2 || len(s.Recent) != 3 {
		t.Errorf("failed runs: %d refusal groups and %d notes", len(s.Refusals), len(s.Recent))
	}

	// Factories ignores the factory filter.
	if s := w.stats(t, Filter{Since: since, Factory: "other"}); !slices.Equal(s.Factories, []string{"acme", "other"}) {
		t.Errorf("Factories = %v", s.Factories)
	}
	// ...but not the window: a factory with no runs in it isn't listed.
	w.run(t, "long ago", 80*day, Run{Factory: "dormant", Routine: "x"}, nil, Checkpoint{Status: Succeeded})
	if s := w.stats(t, Filter{Since: since}); !slices.Equal(s.Factories, []string{"acme", "other"}) {
		t.Errorf("Factories in 30 days = %v", s.Factories)
	}
	if s := w.stats(t, Filter{Since: w.at(90 * day)}); !slices.Equal(s.Factories, []string{"acme", "dormant", "other"}) {
		t.Errorf("Factories in 90 days = %v", s.Factories)
	}
}

func TestStatsOfNothing(t *testing.T) {
	db := openTest(t)
	s, err := db.Stats(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range Outcomes {
		if n, ok := s.Totals[o]; !ok || n != 0 {
			t.Errorf("Totals[%s] = %d, %v", o, n, ok)
		}
	}
	for _, k := range FrictionKinds() {
		if n, ok := s.Friction[k]; !ok || n != 0 {
			t.Errorf("Friction[%s] = %d, %v", k, n, ok)
		}
	}
	if len(s.PerDay)+len(s.Routines)+len(s.Recent)+len(s.Loads)+len(s.Refusals)+len(s.PRs)+len(s.Blueprint)+len(s.Factories)+s.PRCount != 0 {
		t.Errorf("an empty database has stats: %+v", s)
	}
}

func TestStatsRejectsBadFilters(t *testing.T) {
	db := openTest(t)
	for _, f := range []Filter{{Outcomes: []string{"great"}}, {Limit: -1}, {Limit: 501}} {
		if _, err := db.Stats(context.Background(), f); err == nil {
			t.Errorf("Stats(%+v) worked", f)
		}
	}
}

// Filter values only ever travel as arguments.
func TestStatsFilterValuesAreArguments(t *testing.T) {
	w := newStatsWorld(t)
	nasty := "x' OR '1'='1"
	s := w.stats(t, Filter{Factory: nasty, Routine: nasty, Repo: nasty, Runtime: nasty})
	if s.Totals[OutcomeSucceeded]+s.Totals[OutcomeFailed] != 0 {
		t.Errorf("a quoted filter matched runs: %v", s.Totals)
	}
	if _, err := w.db.stats(context.Background(), Filter{Outcomes: []string{nasty}}, w.now, nil); err == nil {
		t.Error("a quoted outcome was accepted")
	}
}

func TestStatsWithoutLoads(t *testing.T) {
	w := newStatsWorld(t)
	f := Filter{Since: w.at(30 * day)}
	all := w.stats(t, f)
	lean, err := w.db.stats(context.Background(), f, w.now, []StatsOption{WithoutLoads()})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Loads) == 0 || len(lean.Loads) != 0 {
		t.Errorf("loads: %d with everything, %d without", len(all.Loads), len(lean.Loads))
	}
	lean.Loads = all.Loads
	if !reflect.DeepEqual(all, lean) {
		t.Errorf("the rest differs:\n%+v\n%+v", all, lean)
	}
}

// openMany makes a database with n seeded runs.
func openMany(tb testing.TB, n int) *DB {
	tb.Helper()
	db, err := Open(filepath.Join(tb.TempDir(), "runs.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	testkit.SeedRuns(tb, db.db, n, time.Now().UTC())
	return db
}

// BenchmarkStats50kRuns is Stats over the longest window of a busy server's
// history. The dashboard's tests check that its overview, which uses a
// shorter window and skips the loads, renders in under a second.
func BenchmarkStats50kRuns(b *testing.B) {
	db := openMany(b, 50_000)
	f := Filter{Since: time.Now().AddDate(0, 0, -MaxStatsDays)}
	for _, name := range []string{"everything", "without loads"} {
		var opts []StatsOption
		if name == "without loads" {
			opts = append(opts, WithoutLoads())
		}
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := db.Stats(context.Background(), f, opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// RoutinesOnly is for List too, since both read the same filter.
func TestListRoutinesOnly(t *testing.T) {
	w := newStatsWorld(t)
	list, _, err := w.db.List(context.Background(), Filter{Since: w.at(30 * day), RoutinesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Routine == "" {
			t.Errorf("RoutinesOnly listed an interactive run: %+v", s)
		}
	}
	if len(list) != 10 {
		t.Errorf("%d runs, want 10", len(list))
	}
}
