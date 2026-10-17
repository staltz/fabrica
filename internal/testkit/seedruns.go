package testkit

import (
	"database/sql"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// SeedRuns puts n runs into a run-history database, spread over the last 90
// days, the way a busy server's history looks: a few factories and
// routines, a couple of checkpoints and several loads per run, some
// friction, PRs, and now and then a refusal. It writes straight to the
// tables of the current schema, in one transaction, because going through
// the write API for 50,000 runs takes minutes. The same seed makes the same
// history.
func SeedRuns(tb testing.TB, db *sql.DB, n int, now time.Time) {
	tb.Helper()
	const day = 24 * time.Hour
	rng := rand.New(rand.NewSource(1))
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	prepare := func(q string) *sql.Stmt {
		st, err := tx.Prepare(q)
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { st.Close() })
		return st
	}
	insRun := prepare(`INSERT INTO runs (run_id, started_at, intent, routine, runtime, repo, factory, blueprint_sha, served_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'remote')`)
	insCp := prepare(`INSERT INTO checkpoints (run_id, at, author, status, summary, links) VALUES (?, ?, ?, ?, ?, ?)`)
	insLoad := prepare(`INSERT INTO loads (run_id, at, kind, name) VALUES (?, ?, ?, ?)`)
	insFriction := prepare(`INSERT INTO friction (run_id, at, kind, note) VALUES (?, ?, ?, ?)`)

	factories := []string{"acme", "globex", "initech"}
	routines := []string{"pr-review", "autofix", "triage", "release-notes", "nightly-audit", "docs-sync", "", ""} // "" is interactive
	skills := []string{"pr-review", "testing", "pdf", "git-hygiene", "release", "triage"}
	rules := []string{"typescript", "commit-messages", "product-knowledge-only"}
	kinds := []string{"navigation", "checks", "standards", "tools", "info", "other"}
	stamp := func(t time.Time) string { return t.Format(time.RFC3339Nano) }
	for range n {
		start := now.Add(-time.Duration(rng.Int63n(int64(90 * day))))
		id := fmt.Sprintf("run_%016x%010x", start.UnixNano(), rng.Int63n(1<<40)) // sorts by time, like the real ones
		var routine any
		if r := routines[rng.Intn(len(routines))]; r != "" {
			routine = r
		}
		factory := factories[rng.Intn(len(factories))]
		sha := fmt.Sprintf("%040x", start.Unix()/(7*86400)) // a new blueprint commit every week
		if _, err := insRun.Exec(id, stamp(start), "intent of "+id, routine, "claude-code-cloud",
			fmt.Sprintf("github.com/%s/app%d", factory, rng.Intn(4)), factory, sha); err != nil {
			tb.Fatal(err)
		}
		if routine != nil {
			if _, err := insLoad.Exec(id, stamp(start), "routine", routine); err != nil {
				tb.Fatal(err)
			}
		}
		for _, name := range rules[:2] {
			if _, err := insLoad.Exec(id, stamp(start), "rule", name); err != nil {
				tb.Fatal(err)
			}
		}
		for range 2 {
			if _, err := insLoad.Exec(id, stamp(start), "skill", skills[rng.Intn(len(skills))]); err != nil {
				tb.Fatal(err)
			}
		}
		at := start
		final := []string{"succeeded", "succeeded", "succeeded", "failed", "progress"}[rng.Intn(5)]
		if rng.Intn(50) == 0 { // refused: Fabrica's own failed checkpoint
			at = at.Add(time.Second)
			if _, err := insCp.Exec(id, stamp(at), "fabrica", "failed", "Refused: stub is outdated; the factory needs fabrica apply", "[]"); err != nil {
				tb.Fatal(err)
			}
		} else {
			at = at.Add(time.Minute)
			if _, err := insCp.Exec(id, stamp(at), "agent", "progress", "did something", "[]"); err != nil {
				tb.Fatal(err)
			}
			links := "[]"
			if rng.Intn(4) == 0 {
				links = fmt.Sprintf(`["https://github.com/%s/app/pull/%d"]`, factory, rng.Intn(5000))
			}
			at = at.Add(time.Minute)
			if _, err := insCp.Exec(id, stamp(at), "agent", final, "did something else", links); err != nil {
				tb.Fatal(err)
			}
			if rng.Intn(4) == 0 {
				if _, err := insFriction.Exec(id, stamp(at), kinds[rng.Intn(len(kinds))], "something slowed me down"); err != nil {
					tb.Fatal(err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}
