package dashboard

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
)

// manyRuns makes a dashboard over 50,000 runs, spread over the last 90 days.
func manyRuns(tb testing.TB) (http.Handler, []*http.Cookie) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "runs.db")
	db, err := runs.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	// A second handle on the same file, to put the runs in with one statement
	// each in one transaction, which the write API doesn't offer.
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		tb.Fatal(err)
	}
	defer raw.Close()
	testkit.SeedRuns(tb, raw, 50_000, time.Now().UTC())
	h := New(db, Options{Secret: secret})
	return h, admin(tb, h, "")
}

// The overview, the page people open, renders in under a second with 50,000
// runs behind it. The limit is for a busy laptop; the benchmark has the real
// figures.
func TestOverviewIsFastWith50kRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("seeding 50,000 runs takes a few seconds")
	}
	h, cookies := manyRuns(t)
	for _, target := range []string{"/_dashboard", "/_dashboard?days=90"} {
		best := time.Hour
		for range 3 {
			start := time.Now()
			code, body := get(t, h, cookies, target)
			best = min(best, time.Since(start))
			if code != 200 {
				t.Fatalf("GET %s: %d\n%s", target, code, body)
			}
			if got := tileValue(t, body, "Runs"); got == "0" {
				t.Fatalf("GET %s counted no runs", target)
			}
		}
		t.Logf("GET %s with 50,000 runs: %v", target, best)
		// The default window is what people see, and what must be fast. The
		// longest window does three times the work, and only has to be sane.
		limit := time.Second
		if target != "/_dashboard" {
			limit = 2 * time.Second
		}
		if !raceEnabled && best > limit {
			t.Errorf("GET %s took %v with 50,000 runs, want under %v", target, best, limit)
		}
	}
	// A routine's page is smaller, but reads loads and friction as well.
	start := time.Now()
	code, _ := get(t, h, cookies, "/_dashboard/routines/acme/pr-review?days=90")
	t.Logf("a routine's page: %v", time.Since(start))
	if code != 200 {
		t.Errorf("routine page: %d", code)
	}
}

func BenchmarkOverview50kRuns(b *testing.B) {
	h, cookies := manyRuns(b)
	for _, days := range []string{"7", "30", "90"} {
		b.Run(days+" days", func(b *testing.B) {
			for b.Loop() {
				if rec := do(h, "GET", "/_dashboard?days="+days, cookies, nil); rec.Code != 200 {
					b.Fatalf("%d", rec.Code)
				}
			}
		})
	}
	b.Run("routine page, 90 days", func(b *testing.B) {
		for b.Loop() {
			if rec := do(h, "GET", "/_dashboard/routines/acme/pr-review?days=90", cookies, nil); rec.Code != 200 {
				b.Fatalf("%d", rec.Code)
			}
		}
	})
}
