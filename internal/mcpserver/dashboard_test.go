package mcpserver

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// These tests are about the dashboard as the remote server serves it: how
// it's routed, and who can reach it. The pages themselves are tested in
// internal/dashboard.

// browser is a client that keeps cookies, like one.
func (rw *remoteWorld) browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func fetch(t *testing.T, c *http.Client, method, target string, form url.Values, header ...string) (int, http.Header, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(data)
}

func TestDashboardOnTheRemoteServer(t *testing.T) {
	rw := newRemoteWorld(t)
	ids := rw.seedRuns(t, 3)
	adminToken := token.Admin(testkit.Secret)

	// Under any prefix: first the login page, and then, with the admin token,
	// the dashboard. A factory's name as a prefix doesn't get in the way.
	for _, prefix := range []string{"", "/fabrica", "/a/b", "/pr-review"} {
		c := rw.browser(t)
		base := rw.ts.URL + prefix + "/_dashboard"
		code, header, body := fetch(t, c, "GET", base, nil)
		if code != 200 || !strings.Contains(body, `<label for="token">Admin token</label>`) || !strings.Contains(body, `action="`+prefix+`/_dashboard/login"`) {
			t.Fatalf("GET %s: %d\n%s", base, code, body)
		}
		if header.Get("Cache-Control") != "no-store" || !strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("GET %s: headers %v", base, header)
		}
		// Not with a factory's token, nor a wrong one.
		for _, tok := range []string{token.For(testkit.Secret, "pr-review"), "nope", ""} {
			if code, _, body := fetch(t, c, "POST", base+"/login", url.Values{"token": {tok}}); code != 401 || !strings.Contains(body, "Admin token") {
				t.Errorf("logging in to %s with %q: %d", base, tok, code)
			}
		}
		// With the admin token: redirected to the overview, which is now open.
		code, _, body = fetch(t, c, "POST", base+"/login", url.Values{"token": {adminToken}})
		if code != 200 || !strings.Contains(body, "Runs per day") || !strings.Contains(body, "Log out") || !strings.Contains(body, `href="`+prefix+`/_dashboard/routines/pr-review/pr-review"`) {
			t.Fatalf("after logging in to %s: %d\n%s", base, code, body)
		}
		if code, _, body := fetch(t, c, "GET", base+"/runs/"+ids[0], nil); code != 200 || !strings.Contains(body, "seeded run") {
			t.Errorf("a run page under %q: %d", prefix, code)
		}
		if code, _, _ := fetch(t, c, "GET", base+"/runs/run_nope", nil); code != 404 {
			t.Errorf("an unknown run under %q: %d", prefix, code)
		}
		// Logging out closes it again.
		if code, _, body := fetch(t, c, "POST", base+"/logout", url.Values{}); code != 200 || !strings.Contains(body, "Admin token") {
			t.Errorf("after logging out of %s: %d", base, code)
		}
		if _, _, body := fetch(t, c, "GET", base, nil); strings.Contains(body, "Runs per day") {
			t.Errorf("still in after logging out of %s", base)
		}
	}
}

// The dashboard takes the cookie it hands out, and nothing else: not the
// header or the /t/ form that the other parts of the server take.
func TestDashboardTakesNoOtherCredentials(t *testing.T) {
	rw := newRemoteWorld(t)
	rw.seedRuns(t, 1)
	c := rw.browser(t)
	for name, target := range map[string][2]string{
		"admin bearer":    {rw.ts.URL + "/_dashboard", "Bearer " + token.Admin(testkit.Secret)},
		"factory bearer":  {rw.ts.URL + "/_dashboard", "Bearer " + token.For(testkit.Secret, "pr-review")},
		"admin in a path": {rw.ts.URL + "/_dashboard/t/" + token.Admin(testkit.Secret), ""},
		"under a factory": {rw.ts.URL + "/pr-review/t/" + token.For(testkit.Secret, "pr-review") + "/_dashboard", ""},
	} {
		var headers []string
		if target[1] != "" {
			headers = []string{"Authorization", target[1]}
		}
		_, _, body := fetch(t, c, "GET", target[0], nil, headers...)
		if strings.Contains(body, "Runs per day") || strings.Contains(body, "seeded run") {
			t.Errorf("%s got in", name)
		}
	}
	// A factory's token as a cookie doesn't either.
	req, _ := http.NewRequest("GET", rw.ts.URL+"/_dashboard", nil)
	req.AddCookie(&http.Cookie{Name: "fabrica_admin", Value: token.For(testkit.Secret, "pr-review")})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(data), "Runs per day") {
		t.Error("a factory token as a cookie got in")
	}
}

// A path with both markers goes to the one that comes last.
func TestAdminAndDashboardMarkersTogether(t *testing.T) {
	rw := newRemoteWorld(t)
	c := rw.browser(t)
	code, header, _ := fetch(t, c, "GET", rw.ts.URL+"/_dashboard/x/_admin/runs", nil)
	if code != 401 || header.Get("Content-Type") != "application/json" {
		t.Errorf("…/_dashboard/x/_admin/runs: %d, %s", code, header.Get("Content-Type"))
	}
	code, header, body := fetch(t, c, "GET", rw.ts.URL+"/_admin/x/_dashboard", nil)
	if code != 200 || !strings.Contains(header.Get("Content-Type"), "text/html") || !strings.Contains(body, "Admin token") {
		t.Errorf("…/_admin/x/_dashboard: %d, %s", code, header.Get("Content-Type"))
	}
}

// A routine's page marks the blueprint commits its runs were served, and
// says what each commit was about when the server's git cache has it.
func TestDashboardNamesBlueprintCommits(t *testing.T) {
	rw := newRemoteWorld(t)
	c := rw.connect(t, "/pr-review/t/"+token.For(testkit.Secret, "pr-review"))
	args := map[string]any{"intent": "review PR 7", "routine": "pr-review", "runtime": "claude-code-cloud", "repo": "github.com/acme/api", "stub": rw.remoteStub(t)}
	runID := c.runID(c.ok("run_begin", args))
	c.ok("run_report", map[string]any{"run_id": runID, "summary": "left a review", "status": "succeeded"})

	b := rw.browser(t)
	fetch(t, b, "POST", rw.ts.URL+"/_dashboard/login", url.Values{"token": {token.Admin(testkit.Secret)}})
	code, _, body := fetch(t, b, "GET", rw.ts.URL+"/_dashboard/routines/pr-review/pr-review", nil)
	if code != 200 {
		t.Fatalf("the routine page: %d\n%s", code, body)
	}
	// The commit that "first blueprint" made.
	if !strings.Contains(body, ": first blueprint") || !strings.Contains(body, "<td class=\"wrapcell\">first blueprint</td>") {
		t.Errorf("the blueprint commit's subject isn't on the page:\n%s", body)
	}
	if !strings.Contains(body, "review PR 7") {
		t.Errorf("the run isn't listed")
	}
}
