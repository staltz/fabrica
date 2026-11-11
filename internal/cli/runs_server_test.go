package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/admin"
	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/mcpserver"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// These tests are about `fabrica runs --server`: reading run history from a
// remote Fabrica server instead of this machine's database. Shared harness
// helpers live in runs_test.go.

// remote serves the world's run history the way a Fabrica server does.
func (w *runsWorld) remote(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(admin.New(w.db, testkit.Secret))
	t.Cleanup(ts.Close)
	return ts
}

func TestRunsJSONIsTheAdminAPIs(t *testing.T) {
	w := newRunsWorld(t)
	ts := w.remote(t)
	get := func(path string) any {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token.Admin(testkit.Secret))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		var v any
		if res.StatusCode != 200 || json.Unmarshal([]byte(w.normalize(string(body))), &v) != nil {
			t.Fatalf("GET %s: %d", path, res.StatusCode)
		}
		return v
	}
	parse := func(s string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, s)
		}
		return v
	}

	since := "2026-10-01T00:00:00Z"
	for _, c := range []struct {
		args  []string
		query string
	}{
		{[]string{"--since", since}, "since=" + since + "&limit=50"},
		{[]string{"--since", since, "--limit", "3"}, "since=" + since + "&limit=3"},
		{[]string{"--since", since, "--routine", "pr-review", "--outcome", "succeeded"}, "since=" + since + "&limit=50&routine=pr-review&outcome=succeeded"},
		{[]string{"--since", since, "--routine", "-", "--factory", "acme"}, "since=" + since + "&limit=50&routine=-&factory=acme"},
		{[]string{"--since", since, "--factory", "nobody"}, "since=" + since + "&limit=50&factory=nobody"},
	} {
		out, _ := w.run(t, 0, append([]string{"runs", "--json"}, c.args...)...)
		want := get("/_admin/runs?" + c.query)
		if got := parse(out); !jsonEqual(got, want) {
			t.Errorf("runs --json %s\n got %s\nwant %v", strings.Join(c.args, " "), out, want)
		}
	}
	// A page with a next: the same `next` as the API's.
	out, _ := w.run(t, 0, "runs", "--json", "--since", since, "--limit", "2")
	var page runs.Page
	if err := json.Unmarshal([]byte(out), &page); err != nil || page.Next != page.Runs[1].RunID || page.SchemaVersion != 4 {
		t.Errorf("a page: %+v, %v", page, err)
	}
	if out, _ := w.run(t, 0, "runs", "--json", "--factory", "nobody"); out != "{\n  \"schema_version\": 4,\n  \"runs\": []\n}\n" {
		t.Errorf("an empty page = %q", out)
	}

	// show --json is the detail the API sends.
	for _, name := range []string{"review", "refused", "flaky", "going"} {
		out, _ := w.run(t, 0, "runs", "show", w.ids[name][:16], "--json")
		if want := get("/_admin/runs/" + w.ids[name]); !jsonEqual(parse(out), want) {
			t.Errorf("show --json %s\n got %s\nwant %v", name, out, want)
		}
	}
	// And a remote server's JSON is the local JSON, byte for byte.
	t.Setenv(token.AdminEnvVar, token.Admin(testkit.Secret))
	local, _ := w.run(t, 0, "runs", "--json", "--since", since)
	remote, _ := w.run(t, 0, "runs", "--json", "--since", since, "--server", ts.URL)
	if local != remote {
		t.Errorf("local and remote JSON differ:\n%s\n%s", local, remote)
	}
	local, _ = w.run(t, 0, "runs", "show", "--json", w.ids["review"])
	remote, _ = w.run(t, 0, "runs", "show", "--json", w.ids["review"], "--server", ts.URL)
	if local != remote {
		t.Errorf("local and remote detail JSON differ:\n%s\n%s", local, remote)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestRunsServer(t *testing.T) {
	w := newRunsWorld(t)
	adminToken := token.Admin(testkit.Secret)

	// Behind a path prefix, like server: https://acme.dev/fabrica. It counts
	// the requests that carry the token the right way.
	var requests, good atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer "+adminToken && !strings.Contains(r.URL.String(), adminToken) {
			good.Add(1)
		}
		admin.New(w.db, testkit.Secret).ServeHTTP(rw, r)
	}))
	defer ts.Close()
	server := ts.URL + "/fabrica"

	// Without any token or secret, it says what to do.
	for _, args := range [][]string{{"runs"}, {"runs", "show", w.ids["review"]}, {"runs", "export", "--out", filepath.Join(t.TempDir(), "x")}} {
		_, errOut := w.run(t, 1, append(args, "--server", server)...)
		want := "there's no admin token for " + server + ": run `fabrica auth login " + server + "` to save the server's secret, or set FABRICA_ADMIN_TOKEN"
		if !strings.Contains(errOut, want) {
			t.Errorf("fabrica %s with no token:\n%s\nwant %s", strings.Join(args, " "), errOut, want)
		}
	}
	if requests.Load() != 0 {
		t.Errorf("nothing should be sent without a token: %d requests", requests.Load())
	}

	// With FABRICA_ADMIN_TOKEN.
	t.Setenv(token.AdminEnvVar, adminToken)
	out, _ := w.run(t, 0, "runs", "--server", server, "--since", "30d")
	here, _ := w.run(t, 0, "runs", "--since", "30d")
	if out != here || !strings.Contains(out, "8 runs") {
		t.Errorf("the list over the server differs from the list here:\n%s\n%s", out, here)
	}
	if good.Load() == 0 || good.Load() != requests.Load() {
		t.Errorf("%d of %d requests had the token in the header and not the URL", good.Load(), requests.Load())
	}
	out, _ = w.run(t, 0, "runs", "show", w.ids["autofix"][:12], "--server", server)
	local, _ := w.run(t, 0, "runs", "show", w.ids["autofix"][:12])
	if out != local {
		t.Errorf("show over the server differs from show here:\n%s\n%s", out, local)
	}
	_, errOut := w.run(t, 1, "runs", "show", w.ids["review"][:12], "--server", server)
	if !strings.Contains(errOut, "fits 2 runs") || !strings.Contains(errOut, w.ids["parallel"]) {
		t.Errorf("an ambiguous prefix over the server:\n%s", errOut)
	}
	_, errOut = w.run(t, 1, "runs", "show", "run_zzzz", "--server", server)
	if !strings.Contains(errOut, "there's no run starting with \"run_zzzz\" in "+server) {
		t.Errorf("an unknown run over the server:\n%s", errOut)
	}

	// Export reads every run in full over the server, and writes the same
	// bundle (apart from where it says the runs came from).
	remoteDir, localDir := filepath.Join(t.TempDir(), "remote"), filepath.Join(t.TempDir(), "local")
	out, _ = w.run(t, 0, "runs", "export", "--server", server, "--since", "30d", "--out", remoteDir)
	if !strings.Contains(out, "Exported 8 runs (4 succeeded, 2 failed, 1 stale, 1 in progress)") {
		t.Errorf("export over the server:\n%s", out)
	}
	w.run(t, 0, "runs", "export", "--since", "30d", "--out", localDir)
	files := func(dir string) []string {
		entries, _ := filepath.Glob(filepath.Join(dir, "runs", "*.md"))
		return entries
	}
	if len(files(remoteDir)) != 8 || len(files(localDir)) != 8 {
		t.Fatalf("each bundle has a file for each run: %d, %d", len(files(remoteDir)), len(files(localDir)))
	}
	for _, name := range []string{"runs.json"} {
		a, _ := os.ReadFile(filepath.Join(remoteDir, name))
		b, _ := os.ReadFile(filepath.Join(localDir, name))
		if string(a) != string(b) {
			t.Errorf("%s differs between the server and local bundles", name)
		}
	}
	readme, _ := os.ReadFile(filepath.Join(remoteDir, "README.md"))
	if !strings.Contains(string(readme), "read from the Fabrica server at "+server) {
		t.Errorf("the README should name the server:\n%s", readme)
	}

	// A wrong token is refused, and says so. The environment variable wins
	// over a server secret that would have worked.
	t.Setenv(token.EnvVar, testkit.Secret)
	t.Setenv(token.AdminEnvVar, "fab_admin_wrong")
	_, errOut = w.run(t, 1, "runs", "--server", server)
	want := server + " refused the admin token (from the environment variable FABRICA_ADMIN_TOKEN)"
	if !strings.Contains(errOut, want) {
		t.Errorf("a wrong token:\n%s\nwant %s", errOut, want)
	}
	// A factory's token is no better.
	t.Setenv(token.AdminEnvVar, token.For(testkit.Secret, "acme"))
	if _, errOut = w.run(t, 1, "runs", "--server", server); !strings.Contains(errOut, "refused the admin token") {
		t.Errorf("a factory's token:\n%s", errOut)
	}
	// Set to nothing, it decides too.
	t.Setenv(token.AdminEnvVar, "")
	if _, errOut = w.run(t, 1, "runs", "--server", server); !strings.Contains(errOut, "FABRICA_ADMIN_TOKEN is set to nothing") {
		t.Errorf("an empty token:\n%s", errOut)
	}

	// Without the variable, the token is made from the server secret: from
	// FABRICA_SERVER_SECRET, or from the one `fabrica auth login` saved.
	unsetenv(t, token.AdminEnvVar)
	if out, _ = w.run(t, 0, "runs", "--server", server, "--since", "30d"); out != here {
		t.Errorf("the list with a secret in the environment:\n%s", out)
	}
	unsetenv(t, token.EnvVar)
	if _, errOut = w.run(t, 1, "runs", "--server", server); !strings.Contains(errOut, "there's no admin token for "+server) {
		t.Errorf("no secret:\n%s", errOut)
	}
	if _, err := credentials.Set(credentials.ServerKey(server), testkit.Secret); err != nil {
		t.Fatal(err)
	}
	if out, _ = w.run(t, 0, "runs", "--server", server+"/", "--since", "30d"); out != here {
		t.Errorf("the list with a saved secret:\n%s", out)
	}
	// A server secret that isn't the server's makes a token it refuses.
	if _, err := credentials.Set(credentials.ServerKey(server), strings.Repeat("x", 40)); err != nil {
		t.Fatal(err)
	}
	_, errOut = w.run(t, 1, "runs", "--server", server)
	if !strings.Contains(errOut, "refused the admin token (made from the server secret in ") {
		t.Errorf("a wrong secret:\n%s", errOut)
	}
}

// A bundle writes a file for each run, named after its ID, and with --server
// the IDs come from the server. One that isn't a run ID is refused before
// anything is written, wherever the file would have gone.
func TestRunsExportRefusesRunIDsThatAreNotRunIDs(t *testing.T) {
	w := newRunsWorld(t)
	t.Setenv(token.AdminEnvVar, token.Admin(testkit.Secret))
	const evil = "../../evil"
	honest := admin.New(w.db, testkit.Secret)
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// A server that calls one of its runs by a path.
		r.URL.Path, r.URL.RawPath = strings.ReplaceAll(r.URL.Path, evil, w.ids["review"]), ""
		rec := httptest.NewRecorder()
		honest.ServeHTTP(rec, r)
		rw.Header().Set("Content-Type", rec.Header().Get("Content-Type"))
		rw.WriteHeader(rec.Code)
		rw.Write([]byte(strings.ReplaceAll(rec.Body.String(), w.ids["review"], evil)))
	}))
	defer ts.Close()

	parent := t.TempDir()
	dir := filepath.Join(parent, "nested", "bundle")
	out, errOut := w.run(t, 1, "runs", "export", "--server", ts.URL, "--since", "30d", "--out", dir)
	if out != "" || !strings.Contains(errOut, `can't export a run whose ID is "../../evil"`) || !strings.Contains(errOut, "isn't a run ID") {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
	var left []string
	_ = filepath.WalkDir(parent, func(p string, d os.DirEntry, err error) error {
		if err == nil && p != parent {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Errorf("nothing should be written: %v", left)
	}
}

// The real remote server routes the admin API, with the token made from its
// own secret.
func TestRunsThroughTheRemoteServer(t *testing.T) {
	w := newRunsWorld(t)
	cache := gitx.NewCache(t.TempDir())
	srv, err := mcpserver.NewRemote(mcpserver.RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: cache, Loader: blueprint.NewLoader(cache), Runs: w.db})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// The server secret in the environment makes the admin token.
	t.Setenv(token.EnvVar, testkit.Secret)
	out, _ := w.run(t, 0, "runs", "--server", ts.URL, "--routine", "autofix")
	if !strings.Contains(out, "Fix lint failure on main") || !strings.Contains(out, "1 run from factory acme") {
		t.Errorf("runs --server:\n%s", out)
	}
	out, _ = w.run(t, 0, "runs", "show", w.ids["autofix"][:12], "--server", ts.URL)
	if !strings.HasPrefix(out, "Run "+w.ids["autofix"]+": failed\n") {
		t.Errorf("runs show --server:\n%s", out)
	}
	dir := filepath.Join(t.TempDir(), "retro")
	w.run(t, 0, "runs", "export", "--server", ts.URL, "--out", dir, "--since", "30d")
	if entries, _ := os.ReadDir(filepath.Join(dir, "runs")); len(entries) != 8 {
		t.Errorf("export --server wrote %d run files", len(entries))
	}

	// A factory's token opens the factory, and not this.
	t.Setenv(token.AdminEnvVar, token.For(testkit.Secret, "acme"))
	if _, errOut := w.run(t, 1, "runs", "--server", ts.URL); !strings.Contains(errOut, "refused the admin token") {
		t.Errorf("a factory's token:\n%s", errOut)
	}
	// And another secret makes another token.
	unsetenv(t, token.AdminEnvVar)
	t.Setenv(token.EnvVar, strings.Repeat("s", 40))
	if _, errOut := w.run(t, 1, "runs", "--server", ts.URL); !strings.Contains(errOut, "refused the admin token (made from the server secret in the environment variable FABRICA_SERVER_SECRET)") {
		t.Errorf("another secret:\n%s", errOut)
	}
}

// A server gives 500 runs a page at most, and --limit can ask for more.
func TestRunsServerPagesPastTheServersLimit(t *testing.T) {
	w := newRunsWorld(t)
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i := range 515 {
		at := base.Add(time.Duration(i) * time.Second)
		r := runs.Run{ID: runsTestID(at, fmt.Sprintf("%016d", i)), StartedAt: at, Intent: "bulk", ServedBy: runs.ServedRemote, Factory: "acme"}
		if err := w.db.Begin(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		asked = append(asked, fmt.Sprintf("%s/%v", r.URL.Query().Get("limit"), r.URL.Query().Has("before")))
		admin.New(w.db, testkit.Secret).ServeHTTP(rw, r)
	}))
	defer ts.Close()
	t.Setenv(token.AdminEnvVar, token.Admin(testkit.Secret))

	out, _ := w.run(t, 0, "runs", "--server", ts.URL, "--limit", "600", "--since", "100d", "--json")
	if n := strings.Count(out, `"run_id"`); n != 523 {
		t.Errorf("%d runs, want all 523", n)
	}
	if strings.Contains(out, `"next"`) {
		t.Error("everything was read, so there's no next")
	}
	if want := []string{"500/false", "100/true"}; !slices.Equal(asked, want) {
		t.Errorf("the requests were %v, want %v", asked, want)
	}
	out, _ = w.run(t, 0, "runs", "--server", ts.URL, "--limit", "510", "--since", "100d", "--json")
	var page runs.Page
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Runs) != 510 || page.Next != page.Runs[509].RunID {
		t.Errorf("a limit that stops short: %d runs, next %q, %v", len(page.Runs), page.Next, err)
	}
}

func TestRunsServerByFactoryName(t *testing.T) {
	w := newRunsWorld(t)
	ts := w.remote(t)
	t.Setenv(token.AdminEnvVar, token.Admin(testkit.Secret))
	// The personal home repo has a cloud factory and a local one.
	files := map[string]string{
		"factories/acme.yaml":   "blueprint: { git: github.com/acme/blueprint, ref: main }\nserver: " + ts.URL + "\nruntimes: [claude-code-cloud]\nroutines: [pr-review]\ntargets: [github.com/acme/app]\n",
		"factories/laptop.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(w.home, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := w.run(t, 0, "runs", "--server", "acme", "--since", "30d", "--routine", "autofix")
	if !strings.Contains(out, "Fix lint failure on main") || strings.Contains(out, "billing") {
		t.Errorf("--server acme:\n%s", out)
	}
	_, errOut := w.run(t, 1, "runs", "--server", "laptop")
	if !strings.Contains(errOut, "factory laptop is a local factory, so its run history is on this machine: leave out --server") {
		t.Errorf("--server laptop:\n%s", errOut)
	}
	_, errOut = w.run(t, 1, "runs", "--server", "nope")
	if !strings.Contains(errOut, `--server "nope" isn't a server URL or a factory of the home repo`) || !strings.Contains(errOut, "or name a factory: acme\n") {
		t.Errorf("--server nope:\n%s", errOut)
	}
	// A server that has gone, and one that isn't Fabrica's.
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	_, errOut = w.run(t, 1, "runs", "--server", url)
	if !strings.Contains(errOut, url+" has no admin API there") {
		t.Errorf("a plain 404:\n%s", errOut)
	}
	gone.Close()
	if _, errOut = w.run(t, 1, "runs", "--server", url); !strings.Contains(errOut, url) {
		t.Errorf("a server that's down:\n%s", errOut)
	}
}
