package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// These tests are about the admin API as the remote server serves it: how it
// is routed, and who can reach it. The API itself is tested in
// internal/admin.

// adminDo sends a request to the remote server, with an Authorization header
// when auth isn't empty, and returns the status, headers and body.
func (rw *remoteWorld) adminDo(t *testing.T, method, path, auth string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, rw.ts.URL+path, strings.NewReader(""))
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

// seedRuns puts n finished runs in history, oldest first, and returns their IDs.
func (rw *remoteWorld) seedRuns(t *testing.T, n int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for i := range n {
		at := time.Now().Add(-time.Duration(n-i) * time.Minute)
		r := runs.Run{ID: runs.NewID(at), StartedAt: at, Intent: "seeded run", Factory: "pr-review", Routine: "pr-review", Repo: "github.com/acme/app", ServedBy: runs.ServedRemote}
		if err := rw.db.Begin(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := rw.db.AddCheckpoint(ctx, r.ID, runs.Checkpoint{At: at, Author: runs.ByAgent, Status: runs.Succeeded, Summary: "done"}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	return ids
}

func TestAdminRoutesOnTheRemoteServer(t *testing.T) {
	rw := newRemoteWorld(t)
	ids := rw.seedRuns(t, 3)
	admin := token.Admin(testkit.Secret)

	// The admin token opens every route, under any path prefix.
	for _, prefix := range []string{"", "/fabrica", "/a/b", "/pr-review"} {
		for _, route := range []string{"/_admin/runs", "/_admin/runs?limit=1", "/_admin/runs/" + ids[0]} {
			code, header, body := rw.adminDo(t, "GET", prefix+route, "Bearer "+admin)
			if code != 200 || header.Get("Cache-Control") != "no-store" || header.Get("Content-Type") != "application/json" {
				t.Errorf("GET %s: %d, %v\n%s", prefix+route, code, header, body)
			}
		}
	}

	// Nobody else does: not without a token, not with a wrong one, not with
	// a factory's, whether it's in a header or in the path.
	factory := token.For(testkit.Secret, "pr-review")
	for _, route := range []string{"/_admin/runs", "/_admin/runs/" + ids[0], "/fabrica/_admin/runs", "/pr-review/_admin/runs"} {
		for _, c := range []struct{ path, auth string }{
			{route, ""},
			{route, "Bearer " + token.Admin("x-wrong-secret-x-wrong-secret-x")},
			{route, "Bearer " + factory},
			{route, "Bearer " + token.For(testkit.Secret, "other")},
			{route, "Bearer " + token.For(testkit.Secret, "_admin")},
			{"/pr-review/t/" + factory + route, ""},
			{"/pr-review/t/" + factory + route, "Bearer " + factory},
			{"/t/" + factory + route, ""},
			{"/t/" + admin + route, ""},
			{"/pr-review/t/" + admin + route, ""},
			{strings.Replace(route, "/_admin/", "/_admin/t/"+admin+"/", 1), ""},
		} {
			code, header, body := rw.adminDo(t, "GET", c.path, c.auth)
			if code != 401 {
				t.Errorf("GET %s with %q: %d, want 401\n%s", c.path, c.auth, code, body)
			}
			if strings.Contains(body, ids[0]) || strings.Contains(body, "seeded run") {
				t.Errorf("GET %s with %q leaked run history:\n%s", c.path, c.auth, body)
			}
			if header.Get("Cache-Control") != "no-store" {
				t.Errorf("GET %s with %q: Cache-Control = %q", c.path, c.auth, header.Get("Cache-Control"))
			}
		}
	}

	// Only GET.
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		for _, route := range []string{"/_admin/runs", "/_admin/runs/" + ids[0], "/fabrica/_admin/runs"} {
			if code, _, _ := rw.adminDo(t, method, route, "Bearer "+admin); code != 405 {
				t.Errorf("%s %s: %d, want 405", method, route, code)
			}
		}
	}
	if code, _, _ := rw.adminDo(t, "POST", "/_admin/runs", ""); code != 401 {
		t.Errorf("POST without a token: %d, want 401", code)
	}

	// The admin token unlocks the admin API and nothing else.
	for _, path := range []string{"/pr-review", "/pr-review/t/" + admin} {
		if code, _, _ := rw.adminDo(t, "POST", path, "Bearer "+admin); code != 401 {
			t.Errorf("POST %s with the admin token: %d, want 401: a factory endpoint takes its own factory's token", path, code)
		}
	}
	// And the factory endpoints and the health check work as before.
	if code, _, _ := rw.adminDo(t, "GET", "/healthz", ""); code != 200 {
		t.Errorf("GET /healthz: %d", code)
	}
	if code, _, _ := rw.adminDo(t, "GET", "/nope", ""); code != 404 {
		t.Errorf("GET /nope: %d, want 404", code)
	}
	rw.connect(t, "/pr-review/t/"+factory).ok("rules_get", nil)
}

func TestAdminPagingOnTheRemoteServer(t *testing.T) {
	rw := newRemoteWorld(t)
	want := rw.seedRuns(t, 11)
	slices.Reverse(want) // newest first
	for _, prefix := range []string{"", "/fabrica"} {
		for _, limit := range []string{"1", "3", "10", "11", "500"} {
			var got []string
			before := ""
			for range 20 {
				query := "?limit=" + limit
				if before != "" {
					query += "&before=" + url.QueryEscape(before)
				}
				code, _, body := rw.adminDo(t, "GET", prefix+"/_admin/runs"+query, "Bearer "+token.Admin(testkit.Secret))
				var page runs.Page
				if err := json.Unmarshal([]byte(body), &page); code != 200 || err != nil {
					t.Fatalf("GET %s: %d %s (%v)", query, code, body, err)
				}
				for _, r := range page.Runs {
					got = append(got, r.RunID)
				}
				if before = page.Next; before == "" {
					break
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("limit %s: paging walked\n%v\nwant\n%v", limit, got, want)
			}
		}
	}
}

// Agents write run history, and a person reads it through the admin API.
func TestRunsAgentsWriteArePeoplesToRead(t *testing.T) {
	rw := newRemoteWorld(t)
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	args := map[string]any{"intent": "review PR 7 on the app", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/api", "stub": rw.remoteStub(t), "session": "https://claude.ai/code/session_7"}
	runID := c.runID(c.ok("run_begin", args))
	c.ok("routine_get", map[string]any{"name": "pr-review"})
	c.ok("skill_load", map[string]any{"name": "pr-review"})
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "read the diff", "friction": []any{map[string]any{"kind": "navigation", "note": "couldn't find the retry config"}}})
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "left a review comment", "status": "succeeded", "links": []any{"https://github.com/acme/api/pull/7"}})

	code, _, body := rw.adminDo(t, "GET", "/_admin/runs/"+runID, "Bearer "+token.Admin(testkit.Secret))
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var d runs.Detail
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	var timeline []string
	for _, e := range d.Timeline {
		timeline = append(timeline, strings.Join(strings.Fields(e.Type+" "+e.Kind+" "+e.Name+" "+e.Status+" "+e.Note), " "))
	}
	want := []string{"load routine pr-review", "load skill pr-review", "checkpoint progress", "friction navigation couldn't find the retry config", "checkpoint succeeded"}
	if !slices.Equal(timeline, want) {
		t.Errorf("timeline:\n%s\nwant\n%s", strings.Join(timeline, "\n"), strings.Join(want, "\n"))
	}
	if d.Outcome != runs.OutcomeSucceeded || d.Intent != "review PR 7 on the app" || d.Routine != "pr-review" || d.Factory != "pr-review" || d.Repo != "github.com/acme/api" ||
		d.Session != "https://claude.ai/code/session_7" || d.ServedBy != runs.ServedRemote || d.Stub == "" || d.BlueprintSHA == "" || !slices.Equal(d.PRLinks, []string{"https://github.com/acme/api/pull/7"}) {
		t.Errorf("run = %+v", d)
	}
}
