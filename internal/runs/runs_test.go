package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRecordAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "runs.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	id := NewID(now)
	if err := db.Begin(ctx, Run{ID: id, StartedAt: now, Intent: "review the new PR", Routine: "pr-review", Runtime: "claude-code-cloud",
		Repo: "github.com/acme/app", Factory: "pr-review", BlueprintSHA: strings.Repeat("a", 40), Stub: "7f3a9c7f3a9c", ServedBy: ServedRemote,
		Session: "https://claude.ai/code/session_123"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Loaded(ctx, id, LoadSkill, "pr-review", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCheckpoint(ctx, id, Checkpoint{At: now.Add(2 * time.Second), Author: ByAgent, Status: Succeeded, Summary: "left one comment", Links: []string{"https://github.com/acme/app/pull/1"}}); err != nil {
		t.Fatal(err)
	}

	r, err := db.Get(ctx, id)
	if err != nil || r.Routine != "pr-review" || r.ServedBy != ServedRemote || !r.StartedAt.Equal(now) || r.Session != "https://claude.ai/code/session_123" {
		t.Fatalf("Get = %+v, %v", r, err)
	}
	cps, _ := db.Checkpoints(ctx, id)
	if len(cps) != 1 || cps[0].Links[0] != "https://github.com/acme/app/pull/1" {
		t.Errorf("checkpoints = %+v", cps)
	}
	if _, err := db.Get(ctx, "run_nope"); err != ErrNoRun {
		t.Errorf("missing run: %v", err)
	}

	// The overview view is part of the public schema.
	raw, _ := sql.Open("sqlite", path)
	defer raw.Close()
	var status, skills, outcome string
	if err := raw.QueryRow("SELECT last_status, skills_loaded, outcome FROM run_overview WHERE run_id = ?", id).Scan(&status, &skills, &outcome); err != nil {
		t.Fatal(err)
	}
	if status != Succeeded || skills != "pr-review" || outcome != "succeeded" {
		t.Errorf("run_overview = %q, %q, %q", status, skills, outcome)
	}
	var version int
	raw.QueryRow("PRAGMA user_version").Scan(&version)
	if version != SchemaVersion {
		t.Errorf("user_version = %d", version)
	}

	// Reopening an existing database is fine.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db2.Close()
}

func TestMemories(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	id := NewID(now)
	db.Begin(ctx, Run{ID: id, StartedAt: now, Intent: "check deps", Routine: "deps", ServedBy: ServedRemote})

	if m, err := db.Memory(ctx, "d", "deps", "github.com/acme/app"); err != nil || m.Content != "" {
		t.Fatalf("a routine that saved nothing has an empty memory: %+v, %v", m, err)
	}
	if err := db.SaveMemory(ctx, "d", "deps", "github.com/acme/app", "- reported lodash", id, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMemory(ctx, "d", "deps", "github.com/acme/app", "- reported lodash and left", id, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	m, _ := db.Memory(ctx, "d", "deps", "github.com/acme/app")
	if m.Content != "- reported lodash and left" || m.UpdatedBy != id || !m.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("memory = %+v", m)
	}
	if other, _ := db.Memory(ctx, "d", "deps", "github.com/acme/api"); other.Content != "" {
		t.Errorf("memories are per repo: %+v", other)
	}
	if err := db.SaveMemory(ctx, "d", "deps", "github.com/acme/app", strings.Repeat("x", MaxMemoryBytes+1), id, now); err == nil {
		t.Error("an oversized memory should be refused")
	}
	db.SaveMemory(ctx, "d", "deps", "github.com/acme/app", "  ", id, now)
	if m, _ := db.Memory(ctx, "d", "deps", "github.com/acme/app"); m.Content != "" {
		t.Errorf("saving nothing should forget: %+v", m)
	}
}

func TestMigratesFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	raw, _ := sql.Open("sqlite", path)
	if _, err := raw.Exec(schemaV1 + "PRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	raw.Exec(`INSERT INTO runs (run_id, started_at, intent, served_by) VALUES ('run_old', '2026-10-01T00:00:00Z', 'old run', 'local')`)
	raw.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if r, err := db.Get(context.Background(), "run_old"); err != nil || r.Intent != "old run" {
		t.Errorf("old runs survive the migration: %+v, %v", r, err)
	}
	if err := db.SaveMemory(context.Background(), "d", "r", "github.com/acme/app", "note", "run_old", time.Now()); err != nil {
		t.Errorf("the migration adds memories: %v", err)
	}
}

// dump runs a query on a raw connection and returns its rows as strings,
// with NULL as "<nil>".
func dump(t *testing.T, raw *sql.DB, query string, args ...any) [][]string {
	t.Helper()
	rows, err := raw.Query(query, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = "<nil>"
			if v.Valid {
				row[i] = v.String
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

const overviewV1Columns = "run_id, started_at, intent, routine, runtime, repo, factory, blueprint_sha, served_by, last_status, last_summary, checkpoint_count, skills_loaded"

func TestMigratesFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	raw, _ := sql.Open("sqlite", path)
	defer raw.Close()
	if _, err := raw.Exec(schemaV1 + schemaV2 + "PRAGMA user_version = 2;"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339Nano) }
	// Runs as a v2 Fabrica left them: one of each outcome, and some in
	// between.
	for _, r := range []struct{ id, started string }{
		{"run_1_succeeded", at(20 * time.Hour)},
		{"run_2_failed", at(19 * time.Hour)},
		{"run_3_progress", at(time.Hour)},
		{"run_4_abandoned", at(240 * time.Hour)},
		{"run_5_silent", at(30 * time.Minute)},
		{"run_6_never_reported", at(240 * time.Hour)},
		{"run_7_reopened", at(10 * time.Hour)},
		{"run_8_quiet_for_days", at(48 * time.Hour)},
	} {
		if _, err := raw.Exec(`INSERT INTO runs (run_id, started_at, intent, routine, runtime, repo, factory, blueprint_sha, served_by)
			VALUES (?, ?, 'an intent', 'pr-review', 'claude-code-cloud', 'github.com/acme/app', 'acme', 'abc123', 'remote')`, r.id, r.started); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := func(run string, ago time.Duration, status, summary string, links ...string) {
		t.Helper()
		data, _ := json.Marshal(append([]string{}, links...))
		if _, err := raw.Exec(`INSERT INTO checkpoints (run_id, at, author, status, summary, links) VALUES (?, ?, 'agent', ?, ?, ?)`, run, at(ago), status, summary, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint("run_1_succeeded", 19*time.Hour, "progress", "started")
	checkpoint("run_1_succeeded", 18*time.Hour, "succeeded", "left a review",
		"https://github.com/acme/app/pull/1", "https://github.com/acme/app/pull/1", "https://github.com/acme/app/issues/5", "https://gitlab.com/acme/app/-/merge_requests/9")
	checkpoint("run_2_failed", 18*time.Hour, "failed", "tests are red")
	checkpoint("run_3_progress", 30*time.Minute, "progress", "reading the diff")
	checkpoint("run_4_abandoned", 239*time.Hour, "progress", "reading the diff")
	// run_6 never reported anything.
	checkpoint("run_7_reopened", 9*time.Hour, "succeeded", "done")
	checkpoint("run_7_reopened", 2*time.Hour, "progress", "one more thing")
	checkpoint("run_8_quiet_for_days", 47*time.Hour, "progress", "reading the diff")
	for i, l := range []struct{ run, skill string }{
		{"run_1_succeeded", "pr-review"}, {"run_1_succeeded", "testing"}, {"run_1_succeeded", "pr-review"}, {"run_2_failed", "testing"}, {"run_3_progress", "pr-review"},
	} {
		if _, err := raw.Exec(`INSERT INTO skill_loads (run_id, at, skill) VALUES (?, ?, ?)`, l.run, at(time.Duration(30-i)*time.Hour), l.skill); err != nil {
			t.Fatal(err)
		}
	}
	oldOverview := dump(t, raw, "SELECT "+overviewV1Columns+" FROM run_overview ORDER BY run_id")
	oldSkillLoads := dump(t, raw, "SELECT id, run_id, at, skill FROM skill_loads ORDER BY id")
	oldRuns := dump(t, raw, "SELECT * FROM runs ORDER BY run_id")
	oldCheckpoints := dump(t, raw, "SELECT * FROM checkpoints ORDER BY id")
	if len(oldSkillLoads) != 5 {
		t.Fatalf("v2 skill loads = %v", oldSkillLoads)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if v := dump(t, raw, "PRAGMA user_version"); v[0][0] != "3" {
		t.Errorf("user_version = %v, want 3", v)
	}
	// The public interfaces keep their rows.
	if got := dump(t, raw, "SELECT id, run_id, at, skill FROM skill_loads ORDER BY id"); !reflect.DeepEqual(got, oldSkillLoads) {
		t.Errorf("skill_loads changed:\nwas %v\nnow %v", oldSkillLoads, got)
	}
	if got := dump(t, raw, "SELECT "+overviewV1Columns+" FROM run_overview ORDER BY run_id"); !reflect.DeepEqual(got, oldOverview) {
		t.Errorf("run_overview's first 13 columns changed:\nwas %v\nnow %v", oldOverview, got)
	}
	// Select * lists the old columns first, in the same order, then the new ones.
	rows, err := raw.Query("SELECT * FROM run_overview")
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	rows.Close()
	if want := overviewV1Columns + ", session, last_at, outcome, friction_count, pr_links"; strings.Join(cols, ", ") != want {
		t.Errorf("run_overview columns = %v, want %s", cols, want)
	}
	// runs only gains its session column; checkpoints don't change.
	var oldRunsPlusSession [][]string
	for _, row := range oldRuns {
		oldRunsPlusSession = append(oldRunsPlusSession, append(append([]string{}, row...), "<nil>"))
	}
	if got := dump(t, raw, "SELECT * FROM runs ORDER BY run_id"); !reflect.DeepEqual(got, oldRunsPlusSession) {
		t.Errorf("runs = %v", got)
	}
	if got := dump(t, raw, "SELECT * FROM checkpoints ORDER BY id"); !reflect.DeepEqual(got, oldCheckpoints) {
		t.Errorf("checkpoints changed: %v", got)
	}
	if got := dump(t, raw, "SELECT run_id, kind, name FROM loads ORDER BY id"); len(got) != 5 || got[0][1] != "skill" {
		t.Errorf("loads = %v", got)
	}

	// The new columns.
	outcomes := map[string]string{}
	for _, row := range dump(t, raw, "SELECT run_id, outcome FROM run_overview") {
		outcomes[row[0]] = row[1]
	}
	wantOutcomes := map[string]string{
		"run_1_succeeded": "succeeded", "run_2_failed": "failed",
		"run_3_progress": "in_progress", "run_4_abandoned": "stale",
		"run_5_silent": "in_progress", "run_6_never_reported": "stale",
		"run_7_reopened": "in_progress", "run_8_quiet_for_days": "stale",
	}
	if !reflect.DeepEqual(outcomes, wantOutcomes) {
		t.Errorf("outcomes = %v\nwant       %v", outcomes, wantOutcomes)
	}
	row := dump(t, raw, "SELECT last_at, pr_links, friction_count, session FROM run_overview WHERE run_id = 'run_1_succeeded'")[0]
	var links []string
	if err := json.Unmarshal([]byte(row[1]), &links); err != nil {
		t.Fatalf("pr_links = %q: %v", row[1], err)
	}
	slices.Sort(links)
	if want := []string{"https://github.com/acme/app/pull/1", "https://gitlab.com/acme/app/-/merge_requests/9"}; !slices.Equal(links, want) {
		t.Errorf("pr_links = %v, want the distinct PR and merge request links %v", links, want)
	}
	if row[0] != at(18*time.Hour) || row[2] != "0" || row[3] != "<nil>" {
		t.Errorf("last_at, friction_count, session = %v", row)
	}
	if got := dump(t, raw, "SELECT last_at, pr_links FROM run_overview WHERE run_id = 'run_6_never_reported'")[0]; got[0] != at(240*time.Hour) || got[1] != "[]" {
		t.Errorf("a run without checkpoints has last_at = started_at and no PRs: %v", got)
	}

	// New code writes the new things.
	ctx := context.Background()
	if err := db.Loaded(ctx, "run_3_progress", LoadRule, "tests-first", now); err != nil {
		t.Fatal(err)
	}
	if err := db.Loaded(ctx, "run_3_progress", LoadRoutine, "pr-review", now); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCheckpoint(ctx, "run_3_progress", Checkpoint{At: now, Author: ByAgent, Status: Progress, Summary: "still reading",
		Friction: []Friction{{Kind: "navigation", Note: "couldn't find the retry config"}, {Kind: "other", Note: "slow clone"}}}); err != nil {
		t.Fatal(err)
	}
	if got := dump(t, raw, "SELECT skills_loaded, friction_count FROM run_overview WHERE run_id = 'run_3_progress'")[0]; got[0] != "pr-review" || got[1] != "2" {
		t.Errorf("rules and routines aren't skills_loaded: %v", got)
	}
	if got := dump(t, raw, "SELECT skill FROM skill_loads WHERE run_id = 'run_3_progress'"); len(got) != 1 {
		t.Errorf("the skill_loads view only shows skills: %v", got)
	}
	// Reopening a v3 database is fine.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db2.Close()
}

// Several editor sessions can open an old database at the same moment, like
// right after an upgrade. Only one of them migrates it, and none fail.
func TestConcurrentOpensMigrateOnce(t *testing.T) {
	for round := 0; round < 5; round++ {
		path := filepath.Join(t.TempDir(), "runs.db")
		raw, _ := sql.Open("sqlite", path)
		if _, err := raw.Exec("PRAGMA journal_mode=WAL;" + schemaV1 + schemaV2 + "PRAGMA user_version = 2;"); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		errs := make(chan error, 4)
		for range cap(errs) {
			go func() {
				db, err := Open(path)
				if err == nil {
					err = db.Close()
				}
				errs <- err
			}()
		}
		for range cap(errs) {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}

func TestFreshDatabaseMatchesMigratedOne(t *testing.T) {
	const schemaOf = `SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	migratedPath := filepath.Join(t.TempDir(), "migrated.db")
	raw, _ := sql.Open("sqlite", migratedPath)
	defer raw.Close()
	if _, err := raw.Exec(schemaV1 + schemaV2 + "PRAGMA user_version = 2;"); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(migratedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	// The public Schema constant is the same thing, run in one go.
	schemaPath := filepath.Join(t.TempDir(), "schema.db")
	schemaRaw, _ := sql.Open("sqlite", schemaPath)
	defer schemaRaw.Close()
	if _, err := schemaRaw.Exec(Schema); err != nil {
		t.Fatal(err)
	}

	want := dump(t, fresh.db, schemaOf)
	if len(want) < 8 {
		t.Fatalf("fresh schema = %v", want)
	}
	if got := dump(t, migrated.db, schemaOf); !reflect.DeepEqual(got, want) {
		t.Errorf("a migrated database differs from a fresh one:\nfresh    %v\nmigrated %v", want, got)
	}
	if got := dump(t, schemaRaw, schemaOf); !reflect.DeepEqual(got, want) {
		t.Errorf("Schema differs from a fresh database:\nfresh  %v\nSchema %v", want, got)
	}
}

func TestCheckpointAndItsFrictionAreAtomic(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	db.Begin(ctx, Run{ID: "run_a", StartedAt: now, Intent: "x", ServedBy: ServedLocal})

	good := Checkpoint{At: now, Author: ByAgent, Status: Progress, Summary: "reading", Friction: []Friction{{Kind: "tools", Note: "grep was slow"}, {Kind: "info", Note: "no staging logs"}}}
	if err := db.AddCheckpoint(ctx, "run_a", good); err != nil {
		t.Fatal(err)
	}
	// A friction row the database refuses takes its checkpoint with it.
	bad := good
	bad.Friction = []Friction{{Kind: "tools", Note: "fine"}, {Kind: "nonsense", Note: "refused by the table"}}
	if err := db.AddCheckpoint(ctx, "run_a", bad); err == nil {
		t.Fatal("a bad friction kind should fail")
	}
	var checkpoints, friction int
	db.db.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&checkpoints)
	db.db.QueryRow(`SELECT COUNT(*) FROM friction`).Scan(&friction)
	if checkpoints != 1 || friction != 2 {
		t.Errorf("checkpoints = %d, friction = %d; want 1 and 2", checkpoints, friction)
	}
	var at, checkpointAt string
	db.db.QueryRow(`SELECT at FROM friction ORDER BY id LIMIT 1`).Scan(&at)
	db.db.QueryRow(`SELECT at FROM checkpoints`).Scan(&checkpointAt)
	if at != checkpointAt {
		t.Errorf("friction is stamped %s, but its checkpoint %s", at, checkpointAt)
	}
}

func TestOutcomeGoesStaleAfterSixHours(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	for _, c := range []struct {
		id  string
		ago time.Duration
	}{{"run_5h", 5 * time.Hour}, {"run_7h", 7 * time.Hour}} {
		db.Begin(ctx, Run{ID: c.id, StartedAt: now.Add(-c.ago), Intent: "x", ServedBy: ServedLocal})
		db.AddCheckpoint(ctx, c.id, Checkpoint{At: now.Add(-c.ago), Author: ByAgent, Status: Progress, Summary: "working"})
	}
	// Silent for 7 hours, but then it said it was done.
	db.AddCheckpoint(ctx, "run_7h", Checkpoint{At: now, Author: ByAgent, Status: Succeeded, Summary: "done"})
	db.Begin(ctx, Run{ID: "run_7h_silent", StartedAt: now.Add(-7 * time.Hour), Intent: "x", ServedBy: ServedLocal})

	got := map[string]string{}
	for _, row := range dump(t, db.db, "SELECT run_id, outcome FROM run_overview") {
		got[row[0]] = row[1]
	}
	if want := map[string]string{"run_5h": "in_progress", "run_7h": "succeeded", "run_7h_silent": "stale"}; !reflect.DeepEqual(got, want) {
		t.Errorf("outcomes = %v, want %v", got, want)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	raw, _ := sql.Open("sqlite", path)
	raw.Exec("PRAGMA user_version = 99")
	raw.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer Fabrica") {
		t.Errorf("want a newer-schema error, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	if err := CheckWords("intent", ""); err == nil {
		t.Error("empty intent should fail")
	}
	if err := CheckWords("intent", strings.Repeat("word ", 21)); err == nil || !strings.Contains(err.Error(), "21 words") {
		t.Errorf("21 words should fail: %v", err)
	}
	if err := CheckWords("intent", strings.Repeat("word ", 20)); err != nil {
		t.Error(err)
	}
	if s, err := CheckStatus(""); s != Progress || err != nil {
		t.Errorf("default status = %q, %v", s, err)
	}
	if _, err := CheckStatus("done"); err == nil {
		t.Error("unknown status should fail")
	}
	if err := CheckLinks([]string{"javascript:alert(1)"}); err == nil {
		t.Error("non-http links should fail")
	}
}

func TestCheckSession(t *testing.T) {
	for _, ok := range []string{"", "https://claude.ai/code/session_01ABC", "/home/me/.claude/projects/app/3f2a.jsonl", "copilot-local:3f2a", "naïve ünïcode path", strings.Repeat("x", MaxSessionBytes)} {
		if err := CheckSession(ok); err != nil {
			t.Errorf("CheckSession(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{strings.Repeat("x", MaxSessionBytes+1), strings.Repeat("é", MaxSessionBytes/2+1), "two\nlines", "carriage\rreturn", "tab\tbed", "bell\a", "nul\x00", "del\x7f", "c1\u0085control"} {
		if err := CheckSession(bad); err == nil {
			t.Errorf("CheckSession(%q) should fail", bad)
		}
	}
}

func TestCheckFriction(t *testing.T) {
	note := func(kind, text string) Friction { return Friction{Kind: kind, Note: text} }
	if err := CheckFriction(nil); err != nil {
		t.Error(err)
	}
	all := []Friction{note("navigation", "a"), note("checks", "b"), note("standards", "c"), note("tools", "d"), note("info", "e")}
	if err := CheckFriction(all); err != nil {
		t.Error(err)
	}
	if err := CheckFriction([]Friction{note("other", strings.Repeat("word ", 20))}); err != nil {
		t.Error(err)
	}
	if err := CheckFriction(append(all, note("other", "f"))); err == nil || !strings.Contains(err.Error(), "at most 5") {
		t.Errorf("six notes should fail: %v", err)
	}
	if err := CheckFriction([]Friction{note("speed", "slow")}); err == nil || !strings.Contains(err.Error(), `"speed"`) {
		t.Errorf("an unknown kind should fail: %v", err)
	}
	if err := CheckFriction([]Friction{note("", "slow")}); err == nil {
		t.Error("an empty kind should fail")
	}
	if err := CheckFriction([]Friction{note("tools", "  ")}); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("an empty note should fail: %v", err)
	}
	if err := CheckFriction([]Friction{note("tools", "fine"), note("tools", strings.Repeat("word ", 21))}); err == nil || !strings.Contains(err.Error(), "friction note 2 has 21 words") {
		t.Errorf("a long note should fail: %v", err)
	}
}

func TestIDsSortByTime(t *testing.T) {
	a := NewID(time.Unix(1000, 0))
	b := NewID(time.Unix(2000, 0))
	if len(a) != 30 || !strings.HasPrefix(a, "run_") || a >= b {
		t.Errorf("ids %q, %q", a, b)
	}
}

// TestDocQueries runs the queries from manual/run-history.md, so the docs
// can't drift from the schema.
func TestDocQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	id := NewID(now)
	db.Begin(ctx, Run{ID: id, StartedAt: now, Intent: "review", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/a/b", ServedBy: ServedRemote})
	db.Loaded(ctx, id, LoadSkill, "pr-review", now)
	db.Loaded(ctx, id, LoadRule, "tests-first", now)
	db.AddCheckpoint(ctx, id, Checkpoint{At: now, Author: ByAgent, Status: Failed, Summary: "tests are red", Links: []string{"https://github.com/a/b/pull/3"},
		Friction: []Friction{{Kind: "checks", Note: "the lint config was missing"}}})
	// A run from two days ago that never reported is stale.
	old := now.Add(-48 * time.Hour)
	db.Begin(ctx, Run{ID: NewID(old), StartedAt: old, Intent: "fix a flaky test", Repo: "github.com/a/b", ServedBy: ServedLocal})
	db.Close()

	raw, _ := sql.Open("sqlite", path)
	defer raw.Close()
	queries := []string{
		`SELECT started_at, runtime, repo, routine, intent, outcome FROM run_overview ORDER BY started_at DESC LIMIT 10`,
		`SELECT routine, COUNT(*) AS runs, SUM(outcome = 'failed') AS failed, SUM(outcome = 'stale') AS stale FROM run_overview WHERE routine IS NOT NULL AND started_at >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-28 days') GROUP BY routine ORDER BY failed DESC`,
		`SELECT started_at, last_at, routine, repo, intent, session FROM run_overview WHERE outcome = 'stale' ORDER BY started_at DESC`,
		`SELECT r.started_at, r.repo, c.author, c.summary FROM checkpoints c JOIN runs r USING (run_id) WHERE c.status = 'failed' ORDER BY c.at DESC`,
		`SELECT r.started_at, r.factory, r.routine, r.runtime, r.repo, c.summary FROM checkpoints c JOIN runs r USING (run_id) WHERE c.author = 'fabrica' ORDER BY c.at DESC`,
		`SELECT kind, name, COUNT(DISTINCT run_id) AS runs FROM loads GROUP BY kind, name ORDER BY runs DESC`,
		// Not in the docs anymore, but queries written for schema 2 must keep working.
		`SELECT skill, COUNT(DISTINCT run_id) AS runs FROM skill_loads GROUP BY skill ORDER BY runs DESC`,
		`SELECT r.started_at, r.routine, f.kind, f.note FROM friction f JOIN runs r USING (run_id) ORDER BY f.at DESC`,
		`SELECT kind, COUNT(*) AS notes FROM friction GROUP BY kind ORDER BY notes DESC`,
		`SELECT o.routine, o.outcome, l.value AS pr FROM run_overview o, json_each(o.pr_links) l`,
	}
	for _, q := range queries {
		rows, err := raw.Query(q)
		if err != nil {
			t.Errorf("%v\n%s", err, q)
			continue
		}
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		if n == 0 && !strings.Contains(q, "author = 'fabrica'") {
			t.Errorf("query returned nothing:\n%s", q)
		}
	}
}
