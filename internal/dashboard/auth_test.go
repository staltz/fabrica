package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/token"
)

func TestRemoteShowsALoginPageWithoutTheCookie(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	for _, target := range []string{
		"/_dashboard", "/_dashboard/", "/_dashboard?days=7", "/_dashboard/routines/acme/pr-review", "/_dashboard/runs/" + w.ids["fifth"],
		"/_dashboard/runs/run_does_not_exist", "/_dashboard/nope", "/fabrica/_dashboard",
	} {
		rec := do(h, "GET", target, nil, nil)
		body := rec.Body.String()
		if rec.Code != 200 {
			t.Errorf("GET %s: %d", target, rec.Code)
		}
		prefix, _, _ := route(target)
		// One password field, labeled "Admin token", that posts back here.
		wantIn(t, body, `<label for="token">Admin token</label>`, `type="password"`, `action="`+prefix+`/_dashboard/login"`, `method="post"`)
		if n := strings.Count(body, "<input"); n != 1 && n != 2 { // the field, and where to go afterwards
			t.Errorf("GET %s: %d inputs", target, n)
		}
		// Nothing about run history, not even whether the page exists.
		wantNotIn(t, body, "intent of", "summary of", "Runs per day", "Log out", "Success rate")
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("GET %s set a cookie", target)
		}
	}

	// Only the cookie counts: not the admin API's header, not a token in the address.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"bearer":         do(h, "GET", "/_dashboard", nil, nil, "Authorization", "Bearer "+token.Admin(secret)),
		"in query":       do(h, "GET", "/_dashboard?token="+token.Admin(secret), nil, nil),
		"in path":        do(h, "GET", "/_dashboard/t/"+token.Admin(secret), nil, nil),
		"wrong cookie":   do(h, "GET", "/_dashboard", []*http.Cookie{{Name: "fabrica_admin", Value: "fab_admin_nope"}}, nil),
		"factory cookie": do(h, "GET", "/_dashboard", []*http.Cookie{{Name: "fabrica_admin", Value: token.For(secret, "acme")}}, nil),
		"other name":     do(h, "GET", "/_dashboard", []*http.Cookie{{Name: "session", Value: token.Admin(secret)}}, nil),
	} {
		if body := rec.Body.String(); !strings.Contains(body, `<label for="token">Admin token</label>`) || strings.Contains(body, "Runs per day") {
			t.Errorf("%s got in: %d", name, rec.Code)
		}
	}
}

func TestRemoteLogin(t *testing.T) {
	w := newWorld(t)
	h := w.remote()

	// The admin token logs you in, and sets the cookie.
	rec := do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/_dashboard" {
		t.Fatalf("login: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	c := rec.Result().Cookies()[0]
	if c.Name != "fabrica_admin" || c.Value != token.Admin(secret) || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/_dashboard" || c.Secure || c.MaxAge != 0 {
		t.Errorf("the cookie is %+v", c)
	}
	if code, body := get(t, h, rec.Result().Cookies(), "/_dashboard"); code != 200 || !strings.Contains(body, "Runs per day") || !strings.Contains(body, "Log out") {
		t.Errorf("with the cookie: %d", code)
	}

	// Over HTTPS the cookie is Secure: the request says so itself, or a proxy does.
	req := httptest.NewRequest("POST", "https://example.com/_dashboard/login", strings.NewReader(url.Values{"token": {token.Admin(secret)}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secure := httptest.NewRecorder()
	h.ServeHTTP(secure, req) // httptest.NewRequest gives an https target a TLS state
	if c := secure.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Errorf("a TLS request got %+v", c)
	}
	for proto, want := range map[string]bool{"https": true, "HTTPS": true, "https, http": true, "http": false, "": false} {
		rec := do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}}, "X-Forwarded-Proto", proto)
		if got := rec.Result().Cookies()[0].Secure; got != want {
			t.Errorf("X-Forwarded-Proto %q: Secure is %v, want %v", proto, got, want)
		}
	}

	// Under a prefix, the cookie only goes to the dashboard's own pages.
	rec = do(h, "POST", "/fabrica/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}})
	if c := rec.Result().Cookies()[0]; c.Path != "/fabrica/_dashboard" || rec.Header().Get("Location") != "/fabrica/_dashboard" {
		t.Errorf("under a prefix: path %q, Location %q", c.Path, rec.Header().Get("Location"))
	}
}

func TestRemoteLoginRefusesOtherTokens(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	for name, tok := range map[string]string{
		"nothing":         "",
		"garbage":         "nope",
		"a factory token": token.For(secret, "acme"),
		"the secret":      secret,
		"another server":  token.Admin(strings.Repeat("x", 48)),
		"admin prefix":    "fab_admin_",
	} {
		rec := do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {tok}})
		body := rec.Body.String()
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
		// The login page again, with the same message whatever was wrong.
		wantIn(t, body, `<label for="token">Admin token</label>`, "That isn&#39;t the admin token for this server.")
		wantNotIn(t, body, "Runs per day")
		if tok != "" && strings.Contains(body, tok) {
			t.Errorf("%s: the page says the token back", name)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: got a cookie", name)
		}
	}
	// Garbled and oversized forms don't get in either.
	rec := do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {strings.Repeat("a", 10000)}})
	if rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 {
		t.Errorf("an oversized form: %d", rec.Code)
	}
}

func TestRemoteLoginGoesBackWhereYouWere(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	// A page that needs a login remembers itself.
	target := "/fabrica/_dashboard/runs/" + w.ids["fifth"] + "?days=7"
	body := do(h, "GET", target, nil, nil).Body.String()
	wantIn(t, body, `name="next" value="`+target+`"`)

	ok := url.Values{"token": {token.Admin(secret)}}
	for next, want := range map[string]string{
		target:                target,
		"/fabrica/_dashboard": "/fabrica/_dashboard",
		"/fabrica/_dashboard/routines/acme/pr-review?days=90": "/fabrica/_dashboard/routines/acme/pr-review?days=90",
		// Nothing that leaves the dashboard.
		"":                                  "/fabrica/_dashboard",
		"//evil.example/fabrica/_dashboard": "/fabrica/_dashboard",
		"https://evil.example/fabrica/_dashboard": "/fabrica/_dashboard",
		"/elsewhere":                               "/fabrica/_dashboard",
		"/fabrica/_dashboardx":                     "/fabrica/_dashboard",
		"/fabrica/_dashboard/../x":                 "/fabrica/_dashboard",
		"/fabrica/_dashboard\\@evil":               "/fabrica/_dashboard",
		"javascript:alert(1)":                      "/fabrica/_dashboard",
		"/fabrica/_dashboard/x\r\nSet-Cookie: a=b": "/fabrica/_dashboard",
	} {
		form := url.Values{"token": ok["token"], "next": {next}}
		rec := do(h, "POST", "/fabrica/_dashboard/login", nil, form)
		if got := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || got != want {
			t.Errorf("next %q: %d, Location %q, want %q", next, rec.Code, got, want)
		}
	}
	// A failed login keeps the destination too.
	rec := do(h, "POST", "/fabrica/_dashboard/login", nil, url.Values{"token": {"nope"}, "next": {target}})
	wantIn(t, rec.Body.String(), `name="next" value="`+target+`"`)
}

func TestRemoteLogout(t *testing.T) {
	w := newWorld(t)
	h := w.remote()
	cookies := admin(t, h, "/fabrica")
	rec := do(h, "POST", "/fabrica/_dashboard/logout", cookies, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/fabrica/_dashboard" {
		t.Errorf("logout: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	c := rec.Result().Cookies()
	if len(c) != 1 || c[0].Name != "fabrica_admin" || c[0].Value != "" || c[0].MaxAge >= 0 || c[0].Path != "/fabrica/_dashboard" || !c[0].HttpOnly {
		t.Errorf("logout set %+v", c)
	}
	// Logging out is a button, not a link.
	if rec := do(h, "GET", "/fabrica/_dashboard/logout", cookies, nil); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET logout: %d", rec.Code)
	}
	// Opening the login address by hand just goes to the dashboard.
	if rec := do(h, "GET", "/fabrica/_dashboard/login", nil, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("GET login: %d", rec.Code)
	}
}

func TestLocalCodeTradesForACookie(t *testing.T) {
	w := newWorld(t)
	h := New(w.db, Options{Code: "the-one-time-code", Now: func() time.Time { return w.now }})

	// No code and no cookie: the page that says to run the command again.
	for _, target := range []string{"/_dashboard", "/_dashboard/runs/" + w.ids["fifth"], "/_dashboard/routines/acme/pr-review"} {
		rec := do(h, "GET", target, nil, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: %d, want 403", target, rec.Code)
		}
		wantIn(t, rec.Body.String(), "This dashboard is locked", "<code>fabrica dashboard</code> again")
		wantNotIn(t, rec.Body.String(), "intent of", "Runs per day", "Log out")
	}
	// A wrong code is the same.
	for _, code := range []string{"nope", "the-one-time-cod", "the-one-time-code!", "THE-ONE-TIME-CODE"} {
		rec := do(h, "GET", "/_dashboard?code="+code, nil, nil)
		if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
			t.Errorf("code %q: %d", code, rec.Code)
		}
	}

	// The right code gives a cookie, and goes to the clean address.
	rec := do(h, "GET", "/_dashboard?code=the-one-time-code", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/_dashboard" {
		t.Fatalf("the code: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies", len(cookies))
	}
	c := cookies[0]
	if !strings.HasPrefix(c.Name, "fabrica_dashboard_") || c.Value == "" || strings.Contains(c.Value, "the-one-time-code") || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/_dashboard" || c.Secure {
		t.Errorf("the cookie is %+v", c)
	}
	if code, body := get(t, h, cookies, "/_dashboard"); code != 200 || !strings.Contains(body, "Runs per day") {
		t.Errorf("with the cookie: %d", code)
	}
	// There's nothing to log out of, so no button.
	if _, body := get(t, h, cookies, "/_dashboard"); strings.Contains(body, "Log out") {
		t.Error("a local dashboard has a log out button")
	}

	// The code works once. Someone who's already in just gets the clean address.
	if rec := do(h, "GET", "/_dashboard?code=the-one-time-code", nil, nil); rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
		t.Errorf("the code a second time: %d", rec.Code)
	}
	if rec := do(h, "GET", "/_dashboard/runs/x?days=7&code=anything", cookies, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/_dashboard/runs/x?days=7" {
		t.Errorf("with a cookie and a code: %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	// Another browser, or a made-up cookie, isn't in.
	for _, cookie := range []*http.Cookie{{Name: c.Name, Value: "made-up"}, {Name: "fabrica_admin", Value: c.Value}, {Name: "fabrica_dashboard_other", Value: c.Value}} {
		if rec := do(h, "GET", "/_dashboard", []*http.Cookie{cookie}, nil); rec.Code != http.StatusForbidden {
			t.Errorf("cookie %+v: %d", cookie, rec.Code)
		}
	}
	// There's no login to try: the token of a remote server means nothing here.
	if rec := do(h, "POST", "/_dashboard/login", nil, url.Values{"token": {token.Admin(secret)}}); rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
		t.Errorf("local login: %d", rec.Code)
	}
}

func TestLocalCodeKeepsTheRestOfTheAddress(t *testing.T) {
	w := newWorld(t)
	h := New(w.db, Options{Code: "c0de"})
	rec := do(h, "GET", "/fabrica/_dashboard/routines/acme/pr-review?code=c0de&days=7&factory=acme", nil, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/fabrica/_dashboard/routines/acme/pr-review?days=7&factory=acme" {
		t.Errorf("Location is %q", got)
	}
	if c := rec.Result().Cookies()[0]; c.Path != "/fabrica/_dashboard" {
		t.Errorf("the cookie's path is %q", c.Path)
	}
}

// Two dashboards on 127.0.0.1 share cookies (they ignore ports), so each has
// a cookie of its own.
func TestLocalDashboardsDontLogEachOtherOut(t *testing.T) {
	w := newWorld(t)
	a, b := New(w.db, Options{Code: "code-a"}), New(w.db, Options{Code: "code-b"})
	ca := do(a, "GET", "/_dashboard?code=code-a", nil, nil).Result().Cookies()
	cb := do(b, "GET", "/_dashboard?code=code-b", nil, nil).Result().Cookies()
	if ca[0].Name == cb[0].Name {
		t.Fatalf("both use the cookie %q", ca[0].Name)
	}
	both := append(append([]*http.Cookie{}, ca...), cb...)
	for name, h := range map[string]http.Handler{"a": a, "b": b} {
		if code, _ := get(t, h, both, "/_dashboard"); code != 200 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := get(t, a, cb, "/_dashboard"); code != http.StatusForbidden {
		t.Errorf("b's cookie opened a: %d", code)
	}
}

func TestNoWayInMeansNoOneIn(t *testing.T) {
	w := newWorld(t)
	h := New(w.db, Options{})
	for _, req := range []struct{ method, target string }{{"GET", "/_dashboard"}, {"GET", "/_dashboard?code="}, {"POST", "/_dashboard/login"}, {"GET", "/_dashboard/runs/" + w.ids["fifth"]}} {
		rec := do(h, req.method, req.target, []*http.Cookie{{Name: "fabrica_admin", Value: ""}, {Name: "fabrica_dashboard_", Value: ""}}, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", req.method, req.target, rec.Code)
		}
	}
}

func TestOnlyTheRightKindOfServerLogsIn(t *testing.T) {
	w := newWorld(t)
	// A server with a secret doesn't take a code.
	h := New(w.db, Options{Secret: secret, Code: "both"})
	if rec := do(h, "GET", "/_dashboard?code=both", nil, nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Runs per day") || len(rec.Result().Cookies()) != 0 {
		t.Errorf("a remote dashboard took a code: %d", rec.Code)
	}
}
