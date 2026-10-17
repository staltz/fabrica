package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

func (w *world) client() *Client { return NewClient(w.ts.URL, token.Admin(secret)) }

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestClientListsLikeTheDatabase(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client()
	now := time.Now()
	for _, f := range []runs.Filter{
		{Since: now.Add(-runs.DefaultSince)},
		{}, // every run, however old: not the API's last 7 days
		{Factory: "acme", Since: now.Add(-100 * 24 * time.Hour)},
		{Routine: "pr-review"},
		{Routine: runs.InteractiveRoutine},
		{Repo: "github.com/acme/api"},
		{Runtime: "cursor-cloud"},
		{Outcomes: []string{runs.OutcomeFailed, runs.OutcomeInProgress}},
		{Limit: 2},
		{Before: w.ids["r4"], Limit: 2},
		{Factory: "nobody"},
	} {
		want, wantNext, err := w.db.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		got, gotNext, err := c.List(ctx, f)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		if asJSON(t, got) != asJSON(t, want) || gotNext != wantNext {
			t.Errorf("List(%+v)\n got %s (next %q)\nwant %s (next %q)", f, asJSON(t, got), gotNext, asJSON(t, want), wantNext)
		}
	}
	if all, _, _ := c.List(ctx, runs.Filter{}); len(all) != 5 {
		t.Errorf("a filter without Since should see all 5 runs, saw %d", len(all))
	}
}

func TestClientPages(t *testing.T) {
	w := newWorld(t)
	var asked []string
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("limit")+"/"+r.URL.Query().Get("before"))
		mu.Unlock()
		New(w.db, secret).ServeHTTP(rw, r)
	}))
	defer ts.Close()
	c := NewClient(ts.URL, token.Admin(secret))

	// runs.ListAll follows next, a page at a time.
	got := []string{}
	before := ""
	for {
		list, next, err := c.List(context.Background(), runs.Filter{Limit: 2, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			got = append(got, w.name(s.RunID))
		}
		if next == "" {
			break
		}
		before = next
	}
	if want := []string{"r5", "r4", "r3", "r2", "r1"}; !slices.Equal(got, want) {
		t.Errorf("paging gave %v, want %v", got, want)
	}
	list, next, err := runs.ListAll(context.Background(), c, runs.Filter{Limit: 3})
	if err != nil || len(list) != 3 || next != list[2].RunID {
		t.Errorf("ListAll = %d runs, next %q, %v", len(list), next, err)
	}
}

func TestClientDetails(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client()
	d, err := c.Detail(ctx, w.ids["r2"])
	if err != nil {
		t.Fatal(err)
	}
	want, _ := w.db.Detail(ctx, w.ids["r2"])
	if asJSON(t, d) != asJSON(t, want) {
		t.Errorf("Detail\n got %s\nwant %s", asJSON(t, d), asJSON(t, want))
	}
	if len(d.Timeline) != 3 || d.Stub != "7f3a9c7f3a9c" {
		t.Errorf("detail = %+v", d)
	}

	// Several at once come back in the order asked for.
	ids := []string{w.ids["r5"], w.ids["r1"], w.ids["r3"], w.ids["r2"], w.ids["r4"]}
	all, err := c.Details(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range all {
		if d.RunID != ids[i] {
			t.Errorf("Details[%d] = %s, want %s", i, w.name(d.RunID), w.name(ids[i]))
		}
	}
	if none, err := c.Details(ctx, nil); err != nil || len(none) != 0 {
		t.Errorf("Details(nil) = %v, %v", none, err)
	}
}

func TestClientReadsAtMostFourAtATime(t *testing.T) {
	w := newWorld(t)
	var inFlight, most atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond) // long enough for the others to pile up
		New(w.db, secret).ServeHTTP(rw, r)
	}))
	defer ts.Close()

	ids := make([]string, 0, 20)
	for range 4 {
		for _, name := range []string{"r1", "r2", "r3", "r4", "r5"} {
			ids = append(ids, w.ids[name])
		}
	}
	all, err := NewClient(ts.URL, token.Admin(secret)).Details(context.Background(), ids)
	if err != nil || len(all) != len(ids) {
		t.Fatalf("Details = %d runs, %v", len(all), err)
	}
	if got := most.Load(); got > 4 || got < 2 {
		t.Errorf("%d requests at once, want several, but at most 4", got)
	}
}

func TestClientStopsAtTheFirstError(t *testing.T) {
	w := newWorld(t)
	_, err := w.client().Details(context.Background(), []string{w.ids["r1"], "run_nope", w.ids["r2"]})
	if !IsStatus(err, 404) {
		t.Errorf("Details with an unknown run = %v, want a 404", err)
	}
}

func TestClientSendsTheTokenInAHeader(t *testing.T) {
	w := newWorld(t)
	adminToken := token.Admin(secret)
	var urls, auths []string
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		urls = append(urls, r.URL.String())
		auths = append(auths, r.Header.Get("Authorization"))
		New(w.db, secret).ServeHTTP(rw, r)
	}))
	defer ts.Close()

	// Under a path prefix, and with a slash on the end of the address.
	c := NewClient(ts.URL+"/fabrica/", adminToken)
	if _, _, err := c.List(context.Background(), runs.Filter{Routine: "pr-review", Outcomes: []string{"failed", "stale"}, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Detail(context.Background(), w.ids["r1"]); err != nil {
		t.Fatal(err)
	}
	for i, u := range urls {
		if !strings.HasPrefix(u, "/fabrica/_admin/runs") || strings.Contains(u, adminToken) || strings.Contains(u, "/t/") {
			t.Errorf("request %d went to %s", i, u)
		}
		if auths[i] != "Bearer "+adminToken {
			t.Errorf("request %d: Authorization = %q", i, auths[i])
		}
	}
	for _, want := range []string{"routine=pr-review", "outcome=failed%2Cstale", "limit=5", "since=1970-01-01T00%3A00%3A00Z"} {
		if !strings.Contains(urls[0], want) {
			t.Errorf("the list request %s should have %s", urls[0], want)
		}
	}
}

func TestClientErrors(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	for name, c := range map[string]*Client{
		"a wrong token":     NewClient(w.ts.URL, "fab_admin_nope"),
		"a factory's token": NewClient(w.ts.URL, token.For(secret, "acme")),
		"no token":          NewClient(w.ts.URL, ""),
	} {
		if _, _, err := c.List(ctx, runs.Filter{}); !IsStatus(err, 401) || !strings.Contains(err.Error(), "admin token") {
			t.Errorf("%s: List = %v, want a 401 that says why", name, err)
		}
		if _, err := c.Detail(ctx, w.ids["r1"]); !IsStatus(err, 401) {
			t.Errorf("%s: Detail = %v, want a 401", name, err)
		}
	}
	if _, _, err := w.client().List(ctx, runs.Filter{Outcomes: []string{"done"}}); !IsStatus(err, 400) || !strings.Contains(err.Error(), `outcome "done"`) {
		t.Errorf("a bad outcome = %v, want the server's 400", err)
	}
	if _, err := w.client().Detail(ctx, "run_nope"); !IsStatus(err, 404) || !strings.Contains(err.Error(), "there's no run run_nope") {
		t.Errorf("an unknown run = %v", err)
	}

	// A server that isn't Fabrica's.
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte("<html>hello</html>")) }))
	defer other.Close()
	if _, _, err := NewClient(other.URL, "x").List(ctx, runs.Filter{}); err == nil || !strings.Contains(err.Error(), "isn't what a Fabrica admin API sends") {
		t.Errorf("a web page = %v", err)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	if _, _, err := NewClient(gone.URL, "x").List(ctx, runs.Filter{}); !IsStatus(err, 404) || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("a plain 404 = %v", err)
	}
	gone.Close()
	if _, _, err := NewClient(gone.URL, "x").List(ctx, runs.Filter{}); err == nil {
		t.Error("a server that's down should be an error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.client().Details(cancelled, []string{w.ids["r1"]}); err == nil {
		t.Error("a cancelled context should be an error")
	}
}

// A redirect would send the admin token wherever the server (or whoever is
// in the way) points, so the client never follows one. It says where the
// server pointed, because the address is probably just not the final one.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	var gotAuth atomic.Value
	elsewhere := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		rw.Write([]byte(`{"runs":[]}`))
	}))
	defer elsewhere.Close()

	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for name, location := range map[string]string{"another host": elsewhere.URL + "/_admin/runs", "a path": "/fabrica/_admin/runs", "nowhere": ""} {
			redirecting := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if location != "" {
					rw.Header().Set("Location", location)
				}
				rw.WriteHeader(status)
			}))
			c := NewClient(redirecting.URL, token.Admin(secret))
			ctx := context.Background()
			_, _, err := c.List(ctx, runs.Filter{})
			_, err2 := c.Detail(ctx, "run_a")
			for _, err := range []error{err, err2} {
				if err == nil || !IsStatus(err, status) || !strings.Contains(err.Error(), "the server answered with a redirect") || !strings.Contains(err.Error(), "use its final address as --server") {
					t.Errorf("%d to %s: error = %v", status, name, err)
				}
				switch {
				case err != nil && location == elsewhere.URL+"/_admin/runs" && !strings.Contains(err.Error(), "to "+location):
					t.Errorf("%d to %s: the error should say where to: %v", status, name, err)
				case err != nil && location == "/fabrica/_admin/runs" && !strings.Contains(err.Error(), "to "+redirecting.URL+location):
					t.Errorf("%d to %s: the error should say where to: %v", status, name, err)
				}
			}
			redirecting.Close()
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the client followed a redirect to another server %d times, and sent it %q", n, gotAuth.Load())
	}
}

// The run's file in a bundle is named after its ID, so a server can't be
// allowed to answer for a run with another run's ID, or with a made-up one.
func TestClientChecksTheRunItGot(t *testing.T) {
	w := newWorld(t)
	for name, answer := range map[string]string{
		"another run":    `{"run_id":"` + w.ids["r2"] + `","outcome":"succeeded"}`,
		"a path":         `{"run_id":"../../../evil","outcome":"succeeded"}`,
		"no ID":          `{"outcome":"succeeded"}`,
		"a different ID": `{"run_id":"run_other","outcome":"succeeded"}`,
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write([]byte(answer)) }))
		c := NewClient(ts.URL, token.Admin(secret))
		d, err := c.Detail(context.Background(), w.ids["r1"])
		if err == nil || d != nil || !strings.Contains(err.Error(), "a different run") {
			t.Errorf("%s: Detail = %v, %v; want an error that says the server answered for a different run", name, d, err)
		}
		if _, err := c.Details(context.Background(), []string{w.ids["r1"]}); err == nil {
			t.Errorf("%s: Details should fail too", name)
		}
		ts.Close()
	}
	// The right run still comes back.
	if d, err := w.client().Detail(context.Background(), w.ids["r1"]); err != nil || d.RunID != w.ids["r1"] {
		t.Errorf("Detail = %v, %v", d, err)
	}
}
