package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/token"
)

const secret = "the-right-secret-the-right-secret-0123"

// fakeServer answers like a Fabrica server serving the given factories:
// 404 for anything else, and 401 naming Fabrica for a wrong token.
func fakeServer(t *testing.T, factories ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		served := false
		for _, f := range factories {
			served = served || f == name
		}
		if !served {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token.For(secret, name) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fabrica"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Error(w, "GET requires an Mcp-Session-Id header", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func need(server string, factories ...string) *Need {
	n := &Need{Kind: ServerSecret, Name: server, Server: server, Endpoints: map[string]string{}}
	for _, f := range factories {
		n.Factories = append(n.Factories, f)
		n.Endpoints[f] = server + "/" + f
	}
	return n
}

func TestVerifyServerSecret(t *testing.T) {
	ctx := context.Background()
	srv := fakeServer(t, "deps")
	n := need(srv.URL, "a-new-one", "deps")

	if v, detail := VerifyServerSecret(ctx, n, secret); v != Accepted || detail != "the server accepts it (checked with factory deps)" {
		t.Errorf("the right secret: %s, %q", v, detail)
	}
	if v, detail := VerifyServerSecret(ctx, n, strings.Repeat("x", 40)); v != Rejected || !strings.Contains(detail, "isn't the secret the server uses") {
		t.Errorf("a wrong secret: %s, %q", v, detail)
	}
	if v, detail := VerifyServerSecret(ctx, need(srv.URL, "a-new-one"), secret); v != Unchecked || !strings.Contains(detail, "doesn't serve factory a-new-one") {
		t.Errorf("a factory the server doesn't serve yet: %s, %q", v, detail)
	}
	if v, detail := VerifyServerSecret(ctx, need(srv.URL), secret); v != Unchecked || !strings.Contains(detail, "none in this home repo uses this server") {
		t.Errorf("a server with no factories here: %s, %q", v, detail)
	}

	// Something that isn't a Fabrica server: it accepts anything.
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer open.Close()
	if v, detail := VerifyServerSecret(ctx, need(open.URL, "deps"), secret); v != Unreachable || !strings.Contains(detail, "something other than a Fabrica server") {
		t.Errorf("not a Fabrica server: %s, %q", v, detail)
	}

	// A redirect is never followed, so a token never goes elsewhere.
	sent := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent = true }))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	defer redirect.Close()
	if v, _ := VerifyServerSecret(ctx, need(redirect.URL, "deps"), secret); v != Unreachable || sent {
		t.Errorf("a redirect: %s, followed=%v", v, sent)
	}

	// A server that's down.
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	if v, detail := VerifyServerSecret(ctx, need(url, "deps"), secret); v != Unreachable || !strings.Contains(detail, "couldn't reach") {
		t.Errorf("a server that's down: %s, %q", v, detail)
	}
}

func TestNeedWords(t *testing.T) {
	n := need("https://fabrica.acme.dev", "deps", "pr-review")
	if got := n.Title(); got != "the server secret, for factories deps, pr-review" {
		t.Errorf("Title = %q", got)
	}
	std := Standalone("CURSOR_TOKEN")
	if len(std) != 1 || std[0].Name != "CURSOR_API_KEY" || std[0].Required() || std[0].Without() != "cursor-cloud uses handoffs instead of its API" {
		t.Errorf("Standalone(CURSOR_TOKEN) = %+v", std)
	}
	if got := Standalone("https://Fabrica.acme.dev/"); len(got) != 1 || got[0].Server != "https://fabrica.acme.dev" || got[0].Key() != "FABRICA_SERVER_SECRET@https://fabrica.acme.dev" {
		t.Errorf("Standalone(url) = %+v", got)
	}
	if got := Standalone("cursor-cloud"); len(got) != 1 || got[0].Credential.Env != "CURSOR_API_KEY" {
		t.Errorf("Standalone(cursor-cloud) = %+v", got)
	}
	for _, none := range []string{"claude-code-cloud", "FABRICA_HOME", "lowercase"} {
		if got := Standalone(none); len(got) != 0 {
			t.Errorf("Standalone(%s) = %+v, want nothing", none, got)
		}
	}
}
