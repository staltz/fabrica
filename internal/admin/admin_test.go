package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

const secret = "an-admin-test-secret-of-32-chars-or-more"

// world is a server with a few runs in it.
type world struct {
	db  *runs.DB
	ts  *httptest.Server
	ids map[string]string // name → run ID
}

// do sends a request with an Authorization header (when auth isn't empty)
// and returns the status, headers and body.
func (w *world) do(t *testing.T, method, path, auth string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, w.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(body)
}

// get reads a path with the admin token.
func (w *world) get(t *testing.T, path string) (int, string) {
	t.Helper()
	code, _, body := w.do(t, "GET", path, "Bearer "+token.Admin(secret))
	return code, body
}

// list reads a page of runs through the API and returns the names of the
// runs, newest first, and next.
func (w *world) list(t *testing.T, query string) (names []string, next string) {
	t.Helper()
	code, body := w.get(t, "/_admin/runs?"+query)
	if code != 200 {
		t.Fatalf("GET /_admin/runs?%s: %d %s", query, code, body)
	}
	var page runs.Page
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	names = []string{}
	for _, r := range page.Runs {
		names = append(names, w.name(r.RunID))
	}
	return names, page.Next
}

func (w *world) name(id string) string {
	for name, v := range w.ids {
		if v == id {
			return name
		}
	}
	return id
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w := &world{db: db, ids: map[string]string{}}
	ctx := context.Background()
	now := time.Now()
	seed := func(name string, ago time.Duration, r runs.Run, status string, links ...string) {
		t.Helper()
		r.ID, r.StartedAt, r.Intent, r.ServedBy = runs.NewID(now.Add(-ago)), now.Add(-ago), "intent of "+name, runs.ServedRemote
		w.ids[name] = r.ID
		if err := db.Begin(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := db.Loaded(ctx, r.ID, runs.LoadSkill, "pr-review", r.StartedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		cp := runs.Checkpoint{At: r.StartedAt.Add(2 * time.Second), Author: runs.ByAgent, Status: status, Summary: "summary of " + name, Links: links}
		if name == "r2" {
			cp.Friction = []runs.Friction{{Kind: "tools", Note: "the linter was slow"}}
		}
		if err := db.AddCheckpoint(ctx, r.ID, cp); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	seed("r1", 10*day, runs.Run{Factory: "acme", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/acme/app"}, runs.Succeeded)
	seed("r2", 3*day, runs.Run{Factory: "acme", Routine: "autofix", Runtime: "cursor-cloud", Repo: "github.com/acme/api", BlueprintSHA: "ee8f142", Stub: "7f3a9c7f3a9c", Session: "https://claude.ai/code/session_2"},
		runs.Failed, "https://github.com/acme/api/pull/9")
	seed("r3", 2*day, runs.Run{Factory: "other", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/other/app"}, runs.Succeeded)
	seed("r4", day, runs.Run{Factory: "acme", Runtime: "claude-code-local", Repo: "github.com/acme/app"}, runs.Succeeded)
	seed("r5", 30*time.Minute, runs.Run{Factory: "acme", Routine: "pr-review", Runtime: "claude-code-cloud", Repo: "github.com/acme/app"}, runs.Progress)

	w.ts = httptest.NewServer(New(db, secret))
	t.Cleanup(w.ts.Close)
	return w
}

func TestRoutesNeedTheAdminToken(t *testing.T) {
	w := newWorld(t)
	for _, prefix := range []string{"", "/fabrica", "/a/b/c", "/pr-review"} {
		routes := []string{prefix + "/_admin/runs", prefix + "/_admin/runs/" + w.ids["r2"], prefix + "/_admin/runs?outcome=failed", prefix + "/_admin/runs/run_unknown", prefix + "/_admin/nope"}
		for _, route := range routes {
			wantAdmin := 200
			switch {
			case strings.HasSuffix(route, "run_unknown"), strings.HasSuffix(route, "nope"):
				wantAdmin = 404
			}
			code, _, body := w.do(t, "GET", route, "Bearer "+token.Admin(secret))
			if code != wantAdmin {
				t.Errorf("GET %s with the admin token: %d, want %d\n%s", route, code, wantAdmin, body)
			}

			// Anything else is refused with one and the same answer: a
			// factory's token (good for its own endpoint) is no better than
			// garbage.
			var refusals []string
			for _, auth := range []string{
				"",
				"Bearer",
				"Bearer ",
				"Bearer nope",
				"Bearer " + token.Admin("another-secret-another-secret-another-secret"),
				"Bearer " + token.For(secret, "pr-review"),
				"Bearer " + token.For(secret, "acme"),
				"Bearer " + token.For(secret, "admin"),
				"Bearer " + token.For(secret, "_admin"),
				"Basic " + token.Admin(secret),
				"bearer " + token.Admin(secret),
				token.Admin(secret),
				"Bearer " + token.Admin(secret) + "x",
				"Bearer " + strings.TrimPrefix(token.Admin(secret), "fab_admin_"),
			} {
				code, header, body := w.do(t, "GET", route, auth)
				if code != 401 {
					t.Errorf("GET %s with %q: %d, want 401", route, auth, code)
				}
				if header.Get("WWW-Authenticate") == "" {
					t.Errorf("GET %s with %q: a 401 should say what it wants", route, auth)
				}
				refusals = append(refusals, body)
			}
			for _, body := range refusals {
				if body != refusals[0] {
					t.Errorf("GET %s: refusals differ:\n%s\n%s", route, refusals[0], body)
				}
			}
		}
	}
}

func TestTheTokenNeverGoesInTheURL(t *testing.T) {
	w := newWorld(t)
	admin, factory := token.Admin(secret), token.For(secret, "pr-review")
	for _, route := range []string{
		"/_admin/runs/t/" + admin,
		"/t/" + admin + "/_admin/runs",
		"/pr-review/t/" + factory + "/_admin/runs",
		"/pr-review/t/" + admin + "/_admin/runs",
		"/_admin/runs?token=" + url.QueryEscape(admin),
		"/_admin/runs?access_token=" + url.QueryEscape(admin),
		"/_admin/runs?admin_token=" + url.QueryEscape(admin),
	} {
		if code, _, body := w.do(t, "GET", route, ""); code != 401 {
			t.Errorf("GET %s: %d, want 401\n%s", route, code, body)
		}
	}
}

func TestOnlyGet(t *testing.T) {
	w := newWorld(t)
	for _, route := range []string{"/_admin/runs", "/_admin/runs/" + w.ids["r1"], "/x/_admin/runs"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
			code, header, _ := w.do(t, method, route, "Bearer "+token.Admin(secret))
			if code != 405 || header.Get("Allow") != "GET" {
				t.Errorf("%s %s: %d (Allow %q), want 405 and Allow: GET", method, route, code, header.Get("Allow"))
			}
			// Not knowing the token comes first: a stranger learns nothing.
			if code, _, _ := w.do(t, method, route, ""); code != 401 {
				t.Errorf("%s %s without a token: %d, want 401", method, route, code)
			}
		}
	}
	if code, _ := w.get(t, "/_admin/runs"); code != 200 {
		t.Error("GET should still work")
	}
	if n := len(listAll(t, w)); n != 5 {
		t.Errorf("the failed writes changed history: %d runs", n)
	}
}

func listAll(t *testing.T, w *world) []string {
	t.Helper()
	names, _ := w.list(t, "since=100d&limit=500")
	return names
}

func TestNoStore(t *testing.T) {
	w := newWorld(t)
	good := "Bearer " + token.Admin(secret)
	for _, c := range []struct {
		method, route, auth string
		want                int
	}{
		{"GET", "/_admin/runs", good, 200},
		{"GET", "/_admin/runs/" + w.ids["r1"], good, 200},
		{"GET", "/_admin/runs?limit=0", good, 400},
		{"GET", "/_admin/runs/run_unknown", good, 404},
		{"GET", "/_admin/elsewhere", good, 404},
		{"POST", "/_admin/runs", good, 405},
		{"HEAD", "/_admin/runs", good, 405},
		{"GET", "/_admin/runs", "", 401},
		{"GET", "/_admin/runs", "Bearer " + token.For(secret, "pr-review"), 401},
		{"POST", "/_admin/runs", "", 401},
		{"GET", "/_admin", "", 401},
		{"GET", "/_admin/", good, 404},
	} {
		code, header, _ := w.do(t, c.method, c.route, c.auth)
		if code != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.route, code, c.want)
		}
		if got := header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s (%d): Cache-Control = %q, want no-store", c.method, c.route, code, got)
		}
		if ct := header.Get("Content-Type"); c.method != "HEAD" && ct != "application/json" {
			t.Errorf("%s %s (%d): Content-Type = %q", c.method, c.route, code, ct)
		}
	}
}

func TestListFieldNames(t *testing.T) {
	w := newWorld(t)
	code, body := w.get(t, "/_admin/runs?routine=autofix")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if string(page["schema_version"]) != "3" {
		t.Errorf("schema_version = %s", page["schema_version"])
	}
	if _, ok := page["next"]; ok {
		t.Error("the last page has no next")
	}
	if len(page) != 2 {
		t.Errorf("a page has schema_version, runs and (sometimes) next: %v", keys(page))
	}
	var list []map[string]any
	if err := json.Unmarshal(page["runs"], &list); err != nil || len(list) != 1 {
		t.Fatalf("runs = %s, %v", page["runs"], err)
	}
	want := "blueprint_sha checkpoint_count factory friction_count intent last_at last_summary outcome pr_links repo routine run_id runtime served_by session started_at"
	if got := strings.Join(keys(list[0]), " "); got != want {
		t.Errorf("a run has fields\n%s\nwant\n%s", got, want)
	}
	r := list[0]
	if r["run_id"] != w.ids["r2"] || r["outcome"] != "failed" || r["intent"] != "intent of r2" || r["routine"] != "autofix" || r["runtime"] != "cursor-cloud" ||
		r["repo"] != "github.com/acme/api" || r["factory"] != "acme" || r["blueprint_sha"] != "ee8f142" || r["served_by"] != "remote" ||
		r["session"] != "https://claude.ai/code/session_2" || r["last_summary"] != "summary of r2" || r["checkpoint_count"] != 1.0 || r["friction_count"] != 1.0 {
		t.Errorf("run = %v", r)
	}
	if links, _ := r["pr_links"].([]any); len(links) != 1 || links[0] != "https://github.com/acme/api/pull/9" {
		t.Errorf("pr_links = %v", r["pr_links"])
	}
	for _, f := range []string{"started_at", "last_at"} {
		if s, _ := r[f].(string); !regexpTime.MatchString(s) {
			t.Errorf("%s = %q, want a time like 2026-10-17T09:41:02Z", f, s)
		}
	}

	// An interactive run with no PR has no routine, and its pr_links is
	// still an array.
	_, body = w.get(t, "/_admin/runs?routine=-&limit=1")
	if !strings.Contains(body, `"pr_links":[]`) || strings.Contains(body, `"routine"`) || strings.Contains(body, `"session"`) {
		t.Errorf("an interactive run: %s", body)
	}
	// Nothing found is an empty array, not null.
	_, body = w.get(t, "/_admin/runs?factory=nobody")
	if body != `{"schema_version":3,"runs":[]}`+"\n" {
		t.Errorf("an empty page = %s", body)
	}
}

func TestDetailFieldNames(t *testing.T) {
	w := newWorld(t)
	code, body := w.get(t, "/_admin/runs/"+w.ids["r2"])
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	want := "blueprint_sha checkpoint_count factory friction_count intent last_at last_summary outcome pr_links repo routine run_id runtime served_by session started_at stub timeline"
	if got := strings.Join(keys(d), " "); got != want {
		t.Errorf("a run in full has fields\n%s\nwant\n%s", got, want)
	}
	if string(d["stub"]) != `"7f3a9c7f3a9c"` {
		t.Errorf("stub = %s", d["stub"])
	}
	var events []map[string]any
	if err := json.Unmarshal(d["timeline"], &events); err != nil {
		t.Fatal(err)
	}
	var shapes []string
	for _, e := range events {
		shapes = append(shapes, e["type"].(string)+": "+strings.Join(keys(e), " "))
	}
	if want := []string{
		"load: at kind name type",
		"checkpoint: at author links status summary type",
		"friction: at kind note type",
	}; !slices.Equal(shapes, want) {
		t.Errorf("timeline events:\n%s\nwant\n%s", strings.Join(shapes, "\n"), strings.Join(want, "\n"))
	}
	if events[0]["kind"] != "skill" || events[0]["name"] != "pr-review" || events[1]["status"] != "failed" || events[1]["author"] != "agent" || events[2]["note"] != "the linter was slow" {
		t.Errorf("timeline = %v", events)
	}
	if links, ok := events[1]["links"].([]any); !ok || len(links) != 1 {
		t.Errorf("a checkpoint's links = %v", events[1]["links"])
	}
}

var regexpTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestFilters(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{"r5", "r4", "r3", "r2"}}, // 7 days by default
		{"since=100d", []string{"r5", "r4", "r3", "r2", "r1"}},
		{"since=47h", []string{"r5", "r4"}},
		{"since=" + url.QueryEscape(time.Now().Add(-26*time.Hour).UTC().Format(time.RFC3339)), []string{"r5", "r4"}},
		{"since=" + url.QueryEscape(time.Now().Add(-4*24*time.Hour).Format(time.RFC3339)), []string{"r5", "r4", "r3", "r2"}},
		{"factory=other", []string{"r3"}},
		{"routine=pr-review", []string{"r5", "r3"}},
		{"routine=-", []string{"r4"}},
		{"repo=github.com/acme/app", []string{"r5", "r4"}},
		{"runtime=cursor-cloud", []string{"r2"}},
		{"outcome=failed", []string{"r2"}},
		{"outcome=failed,in_progress", []string{"r5", "r2"}},
		{"outcome=failed&outcome=succeeded", []string{"r4", "r3", "r2"}},
		{"outcome=failed%2C%20succeeded", []string{"r4", "r3", "r2"}},
		{"outcome=", []string{"r5", "r4", "r3", "r2"}},
		{"factory=acme&routine=pr-review&outcome=in_progress", []string{"r5"}},
		{"limit=2", []string{"r5", "r4"}},
		{"before=" + w.ids["r4"], []string{"r3", "r2"}},
		{"factory=nobody", []string{}},
		{"factory=acme'+OR+'1'%3D'1", []string{}},
		{"surprise=ignored", []string{"r5", "r4", "r3", "r2"}},
	} {
		got, _ := w.list(t, c.query)
		if !slices.Equal(got, c.want) {
			t.Errorf("?%s\n got %v\nwant %v", c.query, got, c.want)
		}
	}
}

func TestBadQueries(t *testing.T) {
	w := newWorld(t)
	for query, mention := range map[string]string{
		"since=yesterday":       "7d or 48h",
		"since=0d":              "7d or 48h",
		"since=2026-10-10":      "RFC 3339",
		"limit=0":               "from 1 to 500",
		"limit=-3":              "from 1 to 500",
		"limit=501":             "from 1 to 500",
		"limit=lots":            "isn't a number",
		"limit=1.5":             "isn't a number",
		"outcome=done":          `outcome "done" isn't one of succeeded, failed, in_progress, stale`,
		"outcome=failed,Done":   `"Done"`,
		"outcome=failed,,stale": "", // empty items are skipped
	} {
		code, body := w.get(t, "/_admin/runs?"+query)
		if mention == "" {
			if code != 200 {
				t.Errorf("?%s: %d %s", query, code, body)
			}
			continue
		}
		var e struct{ Error string }
		if code != 400 || json.Unmarshal([]byte(body), &e) != nil || !strings.Contains(e.Error, mention) {
			t.Errorf("?%s: %d %s, want 400 and an error that mentions %q", query, code, body, mention)
		}
	}
	if names, _ := w.list(t, "limit=500"); len(names) != 4 {
		t.Errorf("the biggest limit works: %v", names)
	}
}

func TestUnknownRoutes(t *testing.T) {
	w := newWorld(t)
	for _, route := range []string{
		"/_admin/runs/run_unknown",
		"/_admin/runs/" + w.ids["r1"] + "/extra",
		"/_admin/runs/" + w.ids["r1"] + "/timeline",
		"/_admin/run",
		"/_admin/nope",
		"/_admin/",
		"/_admin",
		"/_admin/stats",
	} {
		code, body := w.get(t, route)
		var e struct{ Error string }
		if code != 404 || json.Unmarshal([]byte(body), &e) != nil || e.Error == "" {
			t.Errorf("GET %s: %d %s, want 404 and an error", route, code, body)
		}
	}
	// The run ID is looked up as plain text.
	if code, _ := w.get(t, "/_admin/runs/"+url.PathEscape("x' OR '1'='1")); code != 404 {
		t.Errorf("an injected run ID: %d", code)
	}
	// A trailing slash and a doubled one are the same route.
	for _, route := range []string{"/_admin/runs/", "//_admin//runs", "/x/../_admin/runs"} {
		if code, _ := w.get(t, route); code != 200 {
			t.Errorf("GET %s: %d, want 200", route, code)
		}
	}
}

func TestPagingWalksEveryRunOnce(t *testing.T) {
	w := newWorld(t)
	// 41 more runs, so there are several pages at every limit.
	ctx := context.Background()
	for i := range 41 {
		at := time.Now().Add(-time.Duration(i+1) * time.Minute)
		r := runs.Run{ID: runs.NewID(at), StartedAt: at, Intent: "bulk", ServedBy: runs.ServedRemote, Factory: "bulk"}
		if err := w.db.Begin(ctx, r); err != nil {
			t.Fatal(err)
		}
		w.ids["bulk"+r.ID] = r.ID
	}
	all, next := w.list(t, "limit=500")
	if len(all) != 45 || next != "" {
		t.Fatalf("%d runs, next %q", len(all), next)
	}
	if !slices.IsSortedFunc(all, func(a, b string) int { return -strings.Compare(w.ids[a], w.ids[b]) }) {
		t.Errorf("newest first by run_id: %v", all)
	}

	for _, limit := range []string{"1", "4", "10", "44", "45", "46", "100"} {
		var got []string
		before, pages := "", 0
		for {
			query := "limit=" + limit
			if before != "" {
				query += "&before=" + url.QueryEscape(before)
			}
			page, next := w.list(t, query)
			got = append(got, page...)
			if pages++; pages > 50 {
				t.Fatalf("limit %s: paging never ends", limit)
			}
			if next == "" {
				break
			}
			before = next
		}
		if !slices.Equal(got, all) {
			t.Errorf("limit %s: paging gave %d runs, want the same %d in the same order", limit, len(got), len(all))
		}
	}
}

func TestMatches(t *testing.T) {
	for p, want := range map[string]bool{
		"/_admin/runs":                 true,
		"/_admin/runs/run_x":           true,
		"/_admin":                      true,
		"/_admin/":                     true,
		"/fabrica/_admin/runs":         true,
		"/a/b/_admin/runs/run_x":       true,
		"/pr-review/t/tok/_admin/runs": true,
		"/x/_admin/runs/y/_admin/runs": true,
		"//_admin//runs":               true,
		"_admin/runs":                  true,
		"/healthz":                     false,
		"/pr-review":                   false,
		"/pr-review/t/fab_admin_xyz":   false,
		"/admin/runs":                  false,
		"/_adminx/runs":                false,
		"/x_admin/runs":                false,
		"/_dashboard":                  false,
		"/_admin_runs":                 false,
		"/fabrica/_admin/../pr-review": false,
		"/_admin/runs/../../pr-review": false,
		"/":                            false,
		"":                             false,
	} {
		if got := Matches(p); got != want {
			t.Errorf("Matches(%q) = %v, want %v", p, got, want)
		}
	}
}
