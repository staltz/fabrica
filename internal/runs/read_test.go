package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seed is a run to put in a database: a run, and its latest checkpoint.
type seed struct {
	Run
	status string // of one checkpoint at the run's start time; empty for none
}

// put begins the runs, each with a checkpoint when it has a status. It
// returns the run IDs in the order given.
func put(t *testing.T, db *DB, seeds ...seed) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for _, s := range seeds {
		if s.ServedBy == "" {
			s.ServedBy = ServedRemote
		}
		if s.Intent == "" {
			s.Intent = "do a thing"
		}
		if err := db.Begin(ctx, s.Run); err != nil {
			t.Fatal(err)
		}
		if s.status != "" {
			if err := db.AddCheckpoint(ctx, s.ID, Checkpoint{At: s.StartedAt.Add(time.Second), Author: ByAgent, Status: s.status, Summary: "did " + s.ID}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, s.ID)
	}
	return ids
}

func listIDs(t *testing.T, db *DB, f Filter) []string {
	t.Helper()
	list, _, err := db.List(context.Background(), f)
	if err != nil {
		t.Fatalf("List(%+v): %v", f, err)
	}
	ids := []string{}
	for _, s := range list {
		ids = append(ids, s.RunID)
	}
	return ids
}

func TestListFilters(t *testing.T) {
	db := openTest(t)
	now := time.Now()
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	mk := func(name string, ago time.Duration, routine, factory, repo, runtime, status string) seed {
		s := seed{Run: Run{ID: NewID(at(ago)), StartedAt: at(ago), Intent: name, Routine: routine, Factory: factory, Repo: repo, Runtime: runtime}, status: status}
		return s
	}
	// Oldest first, so that newest-first order is the reverse.
	seeds := []seed{
		mk("a", 9*24*time.Hour, "pr-review", "acme", "github.com/acme/app", "claude-code-cloud", Succeeded), // 9 days ago
		mk("b", 3*24*time.Hour, "pr-review", "acme", "github.com/acme/api", "claude-code-cloud", Failed),
		mk("c", 2*24*time.Hour, "autofix", "acme", "github.com/acme/app", "cursor-cloud", Succeeded),
		mk("d", 20*time.Hour, "", "acme", "github.com/acme/app", "claude-code-local", Succeeded), // interactive
		mk("e", 12*time.Hour, "pr-review", "other", "github.com/other/app", "claude-code-cloud", Progress),
		mk("f", 8*time.Hour, "pr-review", "acme", "github.com/acme/app", "claude-code-cloud", Progress), // quiet for 8 hours: stale
		mk("g", 3*time.Hour, "", "other", "github.com/other/app", "claude-code-local", Progress),        // in progress
		mk("h", time.Hour, "autofix", "acme", "github.com/acme/api", "cursor-cloud", ""),                // in progress, no checkpoints
	}
	s := map[string]string{} // intent → run ID
	for _, id := range put(t, db, seeds...) {
		d, err := db.Detail(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		s[d.Intent] = id
	}
	ids := func(intents ...string) []string {
		out := []string{}
		for _, in := range intents {
			out = append(out, s[in])
		}
		return out
	}

	for _, c := range []struct {
		name string
		f    Filter
		want []string // intents, newest first
	}{
		{"everything", Filter{}, []string{"h", "g", "f", "e", "d", "c", "b", "a"}},
		{"factory", Filter{Factory: "other"}, []string{"g", "e"}},
		{"routine", Filter{Routine: "pr-review"}, []string{"f", "e", "b", "a"}},
		{"interactive", Filter{Routine: "-"}, []string{"g", "d"}},
		{"repo", Filter{Repo: "github.com/acme/api"}, []string{"h", "b"}},
		{"runtime", Filter{Runtime: "cursor-cloud"}, []string{"h", "c"}},
		{"one outcome", Filter{Outcomes: []string{"failed"}}, []string{"b"}},
		{"stale", Filter{Outcomes: []string{"stale"}}, []string{"f", "e"}},
		{"several outcomes", Filter{Outcomes: []string{"failed", "in_progress"}}, []string{"h", "g", "b"}},
		{"since", Filter{Since: at(30 * time.Hour)}, []string{"h", "g", "f", "e", "d"}},
		{"since is inclusive", Filter{Since: seeds[3].StartedAt}, []string{"h", "g", "f", "e", "d"}},
		{"since a week", Filter{Since: now.Add(-DefaultSince)}, []string{"h", "g", "f", "e", "d", "c", "b"}},
		{"nothing is that old", Filter{Since: now.Add(time.Hour)}, []string{}},
		{"everything at once", Filter{Factory: "acme", Routine: "pr-review", Repo: "github.com/acme/app", Runtime: "claude-code-cloud", Outcomes: []string{"stale"}, Since: at(24 * time.Hour)}, []string{"f"}},
		{"limit", Filter{Limit: 3}, []string{"h", "g", "f"}},
		{"before", Filter{Before: s["e"]}, []string{"d", "c", "b", "a"}},
		{"nothing matches", Filter{Factory: "nope"}, []string{}},
		{"a quote is just text", Filter{Factory: "acme' OR '1'='1", Routine: "x'; DROP TABLE runs; --", Outcomes: []string{"stale"}}, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, want := listIDs(t, db, c.f), ids(c.want...); !slices.Equal(got, want) {
				t.Errorf("List(%+v)\n got %v\nwant %v (%v)", c.f, got, want, c.want)
			}
		})
	}

	// A quote in a filter doesn't hurt the database.
	if _, err := db.Get(context.Background(), s["a"]); err != nil {
		t.Errorf("the runs table should be fine: %v", err)
	}

	// What List says about a run is what run_overview says.
	list, _, _ := db.List(context.Background(), Filter{Runtime: "claude-code-local", Routine: "-", Factory: "acme"})
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	got := list[0]
	if got.Outcome != OutcomeSucceeded || got.Routine != "" || got.Intent != "d" || got.CheckpointCount != 1 || got.LastSummary != "did "+s["d"] || got.ServedBy != ServedRemote {
		t.Errorf("summary = %+v", got)
	}
}

func TestListInvalidFilters(t *testing.T) {
	db := openTest(t)
	for _, f := range []Filter{{Outcomes: []string{"done"}}, {Outcomes: []string{"failed", ""}}, {Limit: -1}, {Limit: MaxLimit + 1}} {
		if _, _, err := db.List(context.Background(), f); err == nil {
			t.Errorf("List(%+v) should refuse", f)
		}
	}
	if _, _, err := db.List(context.Background(), Filter{Limit: MaxLimit, Outcomes: Outcomes}); err != nil {
		t.Errorf("the biggest limit is fine: %v", err)
	}
	err := Filter{Outcomes: []string{"done"}}.Validate()
	if err == nil || !strings.Contains(err.Error(), `outcome "done" isn't one of succeeded, failed, in_progress, stale`) {
		t.Errorf("Validate = %v", err)
	}
	if err := CheckLimit(0); err == nil || !strings.Contains(err.Error(), "from 1 to 500") {
		t.Errorf("CheckLimit(0) = %v", err)
	}
}

func TestListPaging(t *testing.T) {
	db := openTest(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var want []string
	for i := range 25 {
		s := seed{Run: Run{ID: NewID(base.Add(time.Duration(i) * time.Minute)), StartedAt: base.Add(time.Duration(i) * time.Minute)}}
		want = append(want, put(t, db, s)...)
	}
	slices.Reverse(want) // newest first

	for _, limit := range []int{1, 7, 10, 25, 26, 500, 0} {
		var got []string
		before, pages := "", 0
		for {
			list, next, err := db.List(context.Background(), Filter{Before: before, Limit: limit})
			if err != nil {
				t.Fatal(err)
			}
			pages++
			for _, s := range list {
				got = append(got, s.RunID)
			}
			if next == "" {
				break
			}
			if len(list) != limit {
				t.Fatalf("limit %d: a page with a next has %d runs", limit, len(list))
			}
			if before = next; pages > 30 {
				t.Fatal("paging doesn't end")
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("limit %d: paging gave\n%v\nwant\n%v", limit, got, want)
		}
	}

	// A page that ends exactly at the last run has no next.
	if _, next, _ := db.List(context.Background(), Filter{Limit: 25}); next != "" {
		t.Errorf("a full last page has no next, got %q", next)
	}
	if _, next, _ := db.List(context.Background(), Filter{Limit: 24}); next != want[23] {
		t.Errorf("next = %q, want the oldest run of the page, %q", next, want[23])
	}
}

func TestDetailTimeline(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	// Recent, so the run isn't stale.
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(500 * time.Millisecond)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	id := NewID(t0)
	if err := db.Begin(ctx, Run{ID: id, StartedAt: t0, Intent: "Review PR 412", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/acme/app",
		Factory: "acme", BlueprintSHA: "ee8f142", Stub: "7f3a9c7f3a9c", ServedBy: ServedRemote, Session: "https://claude.ai/code/session_1"}); err != nil {
		t.Fatal(err)
	}
	// Written out of order, on purpose: the timeline sorts by time.
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(db.AddCheckpoint(ctx, id, Checkpoint{At: at(50 * time.Second), Author: ByAgent, Status: Succeeded, Summary: "posted review", Links: []string{"https://github.com/acme/app/pull/412", "https://github.com/acme/app/issues/3"}}))
	check(db.Loaded(ctx, id, LoadSkill, "pr-review", at(4*time.Second)))
	check(db.Loaded(ctx, id, LoadRoutine, "pr-review", at(time.Second)))
	check(db.AddCheckpoint(ctx, id, Checkpoint{At: at(30 * time.Second), Author: ByAgent, Status: Progress, Summary: "read the diff",
		Friction: []Friction{{Kind: "navigation", Note: "couldn't find the backoff config"}, {Kind: "checks", Note: "no lint for this"}}}))
	check(db.Loaded(ctx, id, LoadRule, "product-knowledge-only", at(2*time.Second)))
	check(db.AddCheckpoint(ctx, id, Checkpoint{At: at(40 * time.Second), Author: ByFabrica, Status: Failed, Summary: "refused something", Friction: []Friction{{Kind: "other", Note: "x"}}}))
	// The agent said it was done, then carried on.
	check(db.AddCheckpoint(ctx, id, Checkpoint{At: at(time.Minute), Author: ByAgent, Status: Progress, Summary: "one more thing"}))

	d, err := db.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range d.Timeline {
		switch e.Type {
		case EventLoad:
			got = append(got, "load "+e.Kind+" "+e.Name)
		case EventCheckpoint:
			got = append(got, "checkpoint "+e.Author+" "+e.Status+" "+e.Summary)
		case EventFriction:
			got = append(got, "friction "+e.Kind+" "+e.Note)
		}
	}
	want := []string{
		"load routine pr-review",
		"load rule product-knowledge-only",
		"load skill pr-review",
		"checkpoint agent progress read the diff",
		"friction navigation couldn't find the backoff config",
		"friction checks no lint for this",
		"checkpoint fabrica failed refused something",
		"friction other x",
		"checkpoint agent succeeded posted review",
		"checkpoint agent progress one more thing",
	}
	if !slices.Equal(got, want) {
		t.Errorf("timeline:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if d.Stub != "7f3a9c7f3a9c" || d.Session != "https://claude.ai/code/session_1" || d.Outcome != OutcomeInProgress || d.CheckpointCount != 4 || d.FrictionCount != 3 {
		t.Errorf("detail = %+v", d.Summary)
	}
	if !d.StartedAt.Equal(t0.Truncate(time.Second)) || !d.LastAt.Equal(at(time.Minute).Truncate(time.Second)) {
		t.Errorf("times are cut to whole seconds: started %v, last %v", d.StartedAt, d.LastAt)
	}
	if !reflect.DeepEqual(d.PRLinks, []string{"https://github.com/acme/app/pull/412"}) {
		t.Errorf("pr_links = %v", d.PRLinks)
	}
	if first := d.Timeline[0]; !first.At.Equal(at(time.Second).Truncate(time.Second)) {
		t.Errorf("first event at %v", first.At)
	}
	if cp := d.Timeline[8]; !reflect.DeepEqual(cp.Links, []string{"https://github.com/acme/app/pull/412", "https://github.com/acme/app/issues/3"}) {
		t.Errorf("a checkpoint keeps all its links, not only PRs: %v", cp.Links)
	}

	if _, err := db.Detail(ctx, "run_nope"); !errors.Is(err, ErrNoRun) {
		t.Errorf("Detail of a missing run = %v", err)
	}
}

// Friction is stamped with its checkpoint's time and comes right after it,
// even when other things happened at that very time.
func TestDetailTimelineTies(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	db.Begin(ctx, Run{ID: "run_tie", StartedAt: at, Intent: "x", ServedBy: ServedLocal})
	db.AddCheckpoint(ctx, "run_tie", Checkpoint{At: at, Author: ByAgent, Status: Progress, Summary: "working", Friction: []Friction{{Kind: "tools", Note: "slow"}}})
	db.Loaded(ctx, "run_tie", LoadSkill, "s", at)
	db.AddCheckpoint(ctx, "run_tie", Checkpoint{At: at.Add(time.Second), Author: ByAgent, Status: Progress, Summary: "later"})
	d, err := db.Detail(ctx, "run_tie")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range d.Timeline {
		types = append(types, e.Type)
	}
	if want := "load,checkpoint,friction,checkpoint"; strings.Join(types, ",") != want {
		t.Errorf("timeline = %v, want %s", types, want)
	}
}

func TestDetailOfARunWithNothingYet(t *testing.T) {
	db := openTest(t)
	db.Begin(context.Background(), Run{ID: "run_bare", StartedAt: time.Now(), Intent: "just started", ServedBy: ServedLocal})
	d, err := db.Detail(context.Background(), "run_bare")
	if err != nil {
		t.Fatal(err)
	}
	if d.Timeline == nil || len(d.Timeline) != 0 || d.PRLinks == nil || d.Outcome != OutcomeInProgress || !d.LastAt.Equal(d.StartedAt) {
		t.Errorf("detail = %+v", d)
	}
	data, _ := json.Marshal(d)
	if !strings.Contains(string(data), `"timeline":[]`) || !strings.Contains(string(data), `"pr_links":[]`) {
		t.Errorf("empty lists are arrays: %s", data)
	}
}

// The JSON is a public interface: the spec's examples, field for field.
func TestJSONShape(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 41, 2, 0, time.UTC)
	sum := Summary{
		RunID: "run_06gmjeps8jf8zfjjrfmydev50g", StartedAt: t0, LastAt: t0.Add(17*time.Minute + 38*time.Second), Outcome: OutcomeSucceeded,
		Intent: "Review PR 412 for the billing retry change", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/acme/app",
		Factory: "acme", BlueprintSHA: "ee8f142", ServedBy: ServedRemote, Session: "https://claude.ai/code/session_1",
		LastSummary: "Posted review with two blocking comments", CheckpointCount: 3, FrictionCount: 1,
		PRLinks: []string{"https://github.com/acme/app/pull/412"},
	}
	page, _ := json.Marshal(NewPage([]Summary{sum}, "run_06gm"))
	if want := `{"schema_version":3,"runs":[{"run_id":"run_06gmjeps8jf8zfjjrfmydev50g","started_at":"2026-10-17T09:41:02Z","last_at":"2026-10-17T09:58:40Z",` +
		`"outcome":"succeeded","intent":"Review PR 412 for the billing retry change","routine":"pr-review","runtime":"claude-code-cloud",` +
		`"repo":"github.com/acme/app","factory":"acme","blueprint_sha":"ee8f142","served_by":"remote","session":"https://claude.ai/code/session_1",` +
		`"last_summary":"Posted review with two blocking comments","checkpoint_count":3,"friction_count":1,"pr_links":["https://github.com/acme/app/pull/412"]}],` +
		`"next":"run_06gm"}`; string(page) != want {
		t.Errorf("page JSON:\n%s\nwant\n%s", page, want)
	}

	// An interactive run with nothing else known leaves the optional fields
	// out, and the last page has no next.
	bare, _ := json.Marshal(NewPage(nil, ""))
	if string(bare) != `{"schema_version":3,"runs":[]}` {
		t.Errorf("an empty page = %s", bare)
	}
	min, _ := json.Marshal(Summary{RunID: "run_x", StartedAt: t0, LastAt: t0, Outcome: OutcomeStale, Intent: "x", ServedBy: ServedLocal, PRLinks: []string{}})
	if want := `{"run_id":"run_x","started_at":"2026-10-17T09:41:02Z","last_at":"2026-10-17T09:41:02Z","outcome":"stale","intent":"x","served_by":"local","checkpoint_count":0,"friction_count":0,"pr_links":[]}`; string(min) != want {
		t.Errorf("minimal summary:\n%s\nwant\n%s", min, want)
	}

	detail, _ := json.Marshal(Detail{Summary: sum, Stub: "7f3a9c7f3a9c", Timeline: []Event{
		{At: t0, Type: EventLoad, Kind: "routine", Name: "pr-review"},
		{At: t0, Type: EventCheckpoint, Author: ByAgent, Status: Progress, Summary: "read the diff"},
		{At: t0, Type: EventFriction, Kind: "navigation", Note: "couldn't find it"},
		{At: t0, Type: EventCheckpoint, Author: ByAgent, Status: Succeeded, Summary: "done", Links: []string{"https://github.com/acme/app/pull/412"}},
	}})
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(detail, &obj); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := "blueprint_sha checkpoint_count factory friction_count intent last_at last_summary outcome pr_links repo routine run_id runtime served_by session started_at stub timeline"; strings.Join(keys, " ") != want {
		t.Errorf("detail fields: %s\nwant %s", strings.Join(keys, " "), want)
	}
	if want := `[{"at":"2026-10-17T09:41:02Z","type":"load","kind":"routine","name":"pr-review"},` +
		`{"at":"2026-10-17T09:41:02Z","type":"checkpoint","author":"agent","status":"progress","summary":"read the diff","links":[]},` +
		`{"at":"2026-10-17T09:41:02Z","type":"friction","kind":"navigation","note":"couldn't find it"},` +
		`{"at":"2026-10-17T09:41:02Z","type":"checkpoint","author":"agent","status":"succeeded","summary":"done","links":["https://github.com/acme/app/pull/412"]}]`; string(obj["timeline"]) != want {
		t.Errorf("timeline JSON:\n%s\nwant\n%s", obj["timeline"], want)
	}

	// It reads back into the same thing, for clients of the admin API.
	var back Detail
	if err := json.Unmarshal(detail, &back); err != nil {
		t.Fatal(err)
	}
	if again, _ := json.Marshal(back); string(again) != string(detail) {
		t.Errorf("JSON doesn't round trip:\n%s\n%s", detail, again)
	}
	if _, err := json.Marshal(Event{Type: "nonsense"}); err == nil {
		t.Error("an event of an unknown type should not marshal")
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"1d", now.Add(-24 * time.Hour)},
		{"48h", now.Add(-48 * time.Hour)},
		{"1h", now.Add(-time.Hour)},
		{" 3d ", now.Add(-72 * time.Hour)},
		{"65535d", now.Add(-65535 * 24 * time.Hour)},
		{"2026-10-10T00:00:00Z", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
		{"2026-10-10T02:00:00+02:00", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
		{"2026-10-10T00:00:00.5Z", time.Date(2026, 10, 10, 0, 0, 0, 500_000_000, time.UTC)},
	} {
		got, err := ParseSince(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "d", "h", "0d", "0h", "-1d", "+1d", "1.5d", "7", "7 d", "7w", "7D", "65536d", "99999999999999999999d", "yesterday", "2026-10-10", "2026-10-10 00:00:00", "10/10/2026"} {
		_, err := ParseSince(in, now)
		if err == nil || !strings.Contains(err.Error(), "7d or 48h") || !strings.Contains(err.Error(), "RFC 3339") {
			t.Errorf("ParseSince(%q) should say what it takes, got %v", in, err)
		}
	}
}

func TestSinceSecondPrecision(t *testing.T) {
	db := openTest(t)
	started := time.Date(2026, 10, 17, 9, 41, 2, 500_000_000, time.UTC) // 02.5
	put(t, db, seed{Run: Run{ID: "run_half", StartedAt: started}})
	for _, c := range []struct {
		since time.Time
		want  int
	}{
		{started.Add(-time.Second), 1},
		{started.Truncate(time.Second), 1},       // the same second counts
		{started.Add(500 * time.Millisecond), 0}, // 03.0 is after it
		{started.Add(time.Second), 0},
	} {
		if got := len(listIDs(t, db, Filter{Since: c.since})); got != c.want {
			t.Errorf("since %s: %d runs, want %d", c.since.Format(time.RFC3339Nano), got, c.want)
		}
	}
	// A zone doesn't matter.
	if got := listIDs(t, db, Filter{Since: started.In(time.FixedZone("x", 3*3600)).Add(-time.Second)}); len(got) != 1 {
		t.Errorf("since in another zone: %v", got)
	}
}

func TestListWithManyRuns(t *testing.T) {
	// Not a benchmark, just a check that paging holds up on a few thousand
	// runs with a filter on top.
	db := openTest(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		id := fmt.Sprintf("run_%06d", i)
		routine := []string{"pr-review", "autofix", ""}[i%3]
		if _, err := tx.Exec(`INSERT INTO runs (run_id, started_at, intent, routine, factory, served_by) VALUES (?, ?, 'x', ?, 'acme', 'remote')`, id, stamp(base.Add(time.Duration(i)*time.Second)), null(routine)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	list, next, err := db.List(ctx, Filter{Routine: "-", Limit: 50})
	if err != nil || len(list) != 50 || next == "" || list[0].RunID != "run_001997" {
		t.Errorf("List = %d runs, next %q, %v", len(list), next, err)
	}
}
