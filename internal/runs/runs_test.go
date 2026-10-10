package runs

import (
	"context"
	"database/sql"
	"path/filepath"
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
	if err := db.Begin(ctx, Run{ID: id, StartedAt: now, Intent: "review the new PR", Routine: "pr-review", Runtime: "claude",
		Repo: "github.com/acme/app", Deployment: "pr-review", FactorySHA: strings.Repeat("a", 40), Stub: "7f3a9c7f3a9c", ServedBy: ServedRemote}); err != nil {
		t.Fatal(err)
	}
	if err := db.SkillLoaded(ctx, id, "pr-review", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCheckpoint(ctx, id, Checkpoint{At: now.Add(2 * time.Second), Author: ByAgent, Status: Succeeded, Summary: "left one comment", Links: []string{"https://github.com/acme/app/pull/1"}}); err != nil {
		t.Fatal(err)
	}

	r, err := db.Get(ctx, id)
	if err != nil || r.Routine != "pr-review" || r.ServedBy != ServedRemote || !r.StartedAt.Equal(now) {
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
	var status, skills string
	if err := raw.QueryRow("SELECT last_status, skills_loaded FROM run_overview WHERE run_id = ?", id).Scan(&status, &skills); err != nil {
		t.Fatal(err)
	}
	if status != Succeeded || skills != "pr-review" {
		t.Errorf("run_overview = %q, %q", status, skills)
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

func TestIDsSortByTime(t *testing.T) {
	a := NewID(time.Unix(1000, 0))
	b := NewID(time.Unix(2000, 0))
	if len(a) != 30 || !strings.HasPrefix(a, "run_") || a >= b {
		t.Errorf("ids %q, %q", a, b)
	}
}
