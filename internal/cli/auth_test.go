package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/mcpserver"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// fabIn runs the CLI in-process with input on stdin, one answer per line.
func fabIn(t *testing.T, dir, input string, wantCode int, args ...string) string {
	t.Helper()
	if dir != "" {
		t.Chdir(dir)
	}
	var out bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(input), &out, &out)
	if code != wantCode {
		t.Fatalf("fabrica %s: exit %d, want %d\n%s", strings.Join(args, " "), code, wantCode, out.String())
	}
	return out.String()
}

func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	os.Unsetenv(name)
}

// keyedProvider is the toy runtime, behind an API that needs a key: its
// api program refuses to run without TOY_API_KEY=toy_good.
var keyedProvider = map[string]string{
	"providers/keyed/provider.yaml": strings.Replace(testkit.ToyProviderFiles["providers/toy/provider.yaml"], "description: A pretend runtime used in Fabrica's tests\n",
		"description: The toy runtime, behind an API that needs a key\ncredentials:\n  TOY_API_KEY:\n    description: a toy API key\n", 1),
	"providers/keyed/api!x": "#!/bin/sh\nif [ \"$TOY_API_KEY\" != toy_good ]; then echo \"keyed: wrong key\" >&2; exit 1; fi\nexec \"$FABRICA_TOY_BIN\" \"$@\"\n",
}

func TestAuthCommands(t *testing.T) {
	w := setupCLI(t)
	unsetenv(t, token.EnvVar)
	cursor := testkit.NewFakeCursor(t)
	unsetenv(t, "CURSOR_API_KEY") // the fake sets it; this test saves it instead
	unsetenv(t, "CURSOR_TOKEN")
	unsetenv(t, "TOY_API_KEY")
	opened := filepath.Join(t.TempDir(), "opened")
	browser := filepath.Join(t.TempDir(), "browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\necho \"$1\" >> "+opened+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", browser)
	w.UpdateBlueprint(keyedProvider)

	// A real Fabrica server, with its own secret, serving the home repo.
	db, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache := gitx.NewCache(t.TempDir())
	srv, err := mcpserver.NewRemote(mcpserver.RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: cache, Loader: blueprint.NewLoader(cache), Runs: db})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	server := ts.URL
	w.Home.Write(map[string]string{
		"factories/pr-review.yaml": "blueprint: { git: github.com/acme/blueprint, ref: stable }\nserver: " + server + "\nruntimes: [claude-code-cloud, cursor-cloud, keyed]\nroutines: [pr-review]\ntargets: [github.com/acme/app]\n",
	}).CommitPush("deploy pr-review")
	if err := srv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	home := w.Home.Dir
	// Like a real home repo's remote (the fake git host still serves it).
	testkit.Git(t, home, "remote", "set-url", "origin", "https://github.com/acme/home.git")

	// Nothing is set up: the server secret and the toy key are required,
	// Cursor's key is optional (cursor-cloud falls back to handoffs).
	out := fabIn(t, home, "", 1, "auth")
	for _, want := range []string{
		"Server secrets\n  ✗ " + server + ": the server secret, for factory pr-review\n    not set, so apply can't make the factories' tokens\n",
		"  · CURSOR_API_KEY: a Cursor API key, for cursor-cloud (factory pr-review)\n    not set, so cursor-cloud uses handoffs instead of its API",
		"  ✗ TOY_API_KEY: a toy API key, for keyed (factory pr-review)\n    not set, so keyed can't be applied",
		"  ✓ github.com/acme/home: the home repo, where apply records state",
		"  ✓ github.com/acme/blueprint: blueprint of pr-review",
		"  ✓ github.com/acme/app: target of pr-review",
		"✗ 2 problems.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth is missing %q:\n%s", want, out)
		}
	}
	if out := fabIn(t, home, "", 1, "apply"); !strings.Contains(out, "runtime keyed can't use its API because TOY_API_KEY isn't set") || !strings.Contains(out, "fabrica auth login") {
		t.Errorf("apply without the toy key should say how to set it up:\n%s", out)
	}

	// A script can pipe a credential in.
	out = fabIn(t, home, "toy_good\n", 0, "auth", "login", "TOY_API_KEY")
	if !strings.Contains(out, "since FABRICA_KEYCHAIN=off.") || !strings.Contains(out, "Without it, keyed can't be applied.\n  Paste it: \n  ✓ Saved in ") {
		t.Errorf("login TOY_API_KEY:\n%s", out)
	}
	if out := fabIn(t, home, "", 1, "apply"); !strings.Contains(out, "there's no server secret for "+server) || !strings.Contains(out, "fabrica auth login") {
		t.Errorf("apply without a server secret should say how to set one up:\n%s", out)
	}

	// Log in: a wrong server secret first (the server refuses it), then
	// the right one; Cursor's key from the page the browser opens, wrong
	// then right. The toy key is set up already, so it's skipped.
	wrongSecret := strings.Repeat("w", 40)
	input := strings.Join([]string{
		wrongSecret, testkit.Secret, // server
		"", "", "key_wrong", "", testkit.FakeCursorKey, // CURSOR_API_KEY: yes, open, wrong; open again, right
	}, "\n") + "\n"
	out = fabIn(t, home, input, 0, "auth", "login")
	for _, want := range []string{
		"The server is running factory pr-review, so it has a secret already.",
		"✗ No: the server refuses tokens made with it (checked with factory pr-review), so it isn't the secret the server uses.",
		"✓ Yes: the server accepts it (checked with factory pr-review).",
		"Without it, cursor-cloud uses handoffs instead of its API.",
		"Opened it in your browser.\n  Create a new API key, call it fabrica, and copy it.",
		"✗ That didn't work: Cursor refused to exchange CURSOR_API_KEY",
		"✓ Cursor accepted it.",
		"✓ TOY_API_KEY: a toy API key, for keyed (factory pr-review)\n  saved in ",
		"✓ git can read all 3 repos apply uses.",
		"To give the home repo's GitHub Actions the same credentials:\n  fabrica auth env | gh secret set --env-file - --repo acme/home",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth login is missing %q:\n%s", want, out)
		}
	}
	if got, _ := os.ReadFile(opened); strings.Count(string(got), "https://cursor.com/dashboard/api\n") != 2 {
		t.Errorf("the browser should have opened Cursor's dashboard twice, got %q", got)
	}
	if strings.Contains(out, testkit.Secret) || strings.Contains(out, testkit.FakeCursorKey) {
		t.Errorf("login must never print a secret:\n%s", out)
	}

	out = fabIn(t, home, "", 0, "auth")
	for _, want := range []string{
		"  ✓ " + server + ": the server secret, for factory pr-review\n    saved in ",
		"the server accepts it (checked with factory pr-review)",
		"  ✓ CURSOR_API_KEY: a Cursor API key, for cursor-cloud (factory pr-review)\n    saved in ",
		"Cursor accepted it",
		"  ✓ TOY_API_KEY: a toy API key, for keyed (factory pr-review)\n    saved in ",
		"Fabrica has no way to check it; only keyed's api program uses it",
		"✓ Apply has everything it needs.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth after login is missing %q:\n%s", want, out)
		}
	}

	// With everything saved, and nothing in the environment, plan uses
	// Cursor's API and apply works: the server's token for the stubs, and
	// the keyed api program gets its key.
	out = mustFab(t, 2, home, "plan")
	if !strings.Contains(out, "+ create pr-review @ github.com/acme/app on cursor-cloud (api)") || !strings.Contains(out, "on keyed (api)") {
		t.Fatalf("plan with saved credentials:\n%s", out)
	}
	out = mustFab(t, 0, home, "apply", "--handoff-dir", t.TempDir())
	if len(cursor.Automations()) != 1 {
		t.Errorf("apply should have made a Cursor automation with the saved key:\n%s", out)
	}
	toy := w.Toy.Resources()
	if len(toy) != 1 || toy[0].Token != token.For(testkit.Secret, "pr-review") {
		t.Errorf("the keyed runtime should have the stub, with the server's token: %+v\n%s", toy, out)
	}

	// env prints them, ready for a .env file or `gh secret set`.
	out = fabIn(t, home, "", 0, "auth", "env")
	want := "CURSOR_API_KEY=" + testkit.FakeCursorKey + "\nFABRICA_SERVER_SECRET=" + testkit.Secret + "\nTOY_API_KEY=toy_good\n"
	if out != want {
		t.Errorf("auth env = %q, want %q", out, want)
	}

	// An environment variable wins over what's saved: here, a wrong one.
	t.Setenv(token.EnvVar, wrongSecret)
	out = fabIn(t, home, "", 1, "auth")
	if !strings.Contains(out, "  ✗ "+server+": the server secret, for factory pr-review\n    from the environment variable FABRICA_SERVER_SECRET; the server refuses tokens made with it") {
		t.Errorf("a wrong FABRICA_SERVER_SECRET should show up as refused:\n%s", out)
	}
	unsetenv(t, token.EnvVar)

	// Logout forgets; a runtime's name stands for its credentials.
	out = fabIn(t, home, "", 0, "auth", "logout", "cursor-cloud")
	if !regexp.MustCompile(`Forgot CURSOR_API_KEY \(it was in .*credentials\.json, a file only you can read\)\.`).MatchString(out) {
		t.Errorf("logout:\n%s", out)
	}
	if out := fabIn(t, home, "", 0, "auth", "--no-check"); !strings.Contains(out, "· CURSOR_API_KEY") || !strings.Contains(out, "can also set up the optional credential above") {
		t.Errorf("after logout, Cursor's key should be optional and missing:\n%s", out)
	}
	if out := fabIn(t, home, "", 0, "auth", "logout", "CURSOR_TOKEN"); !strings.Contains(out, "CURSOR_API_KEY wasn't saved.") {
		t.Errorf("an alias should name its credential:\n%s", out)
	}
	if out := fabIn(t, home, "", 1, "auth", "env", "CURSOR_API_KEY"); !strings.Contains(out, "CURSOR_API_KEY isn't set up; run `fabrica auth login CURSOR_API_KEY`") {
		t.Errorf("env for a missing credential:\n%s", out)
	}

	// Making a new secret for a running server asks first.
	out = fabIn(t, home, "\nn\n", 1, "auth", "login", server)
	if !strings.Contains(out, "Make a new one anyway? [y/N]") || !strings.Contains(out, "Stopped: there's no more input.") {
		t.Errorf("a new secret for a running server should need a yes:\n%s", out)
	}

	// A new server: login makes its secret, and env hands it over.
	out = fabIn(t, home, "\n", 0, "auth", "login", "https://new.acme.dev/")
	if !strings.Contains(out, "✓ Made a new secret, and saved it in ") || !strings.Contains(out, "Your server at https://new.acme.dev needs the same secret") {
		t.Errorf("login for a new server:\n%s", out)
	}
	if strings.Contains(out, "GitHub Actions") {
		t.Errorf("a server the home repo doesn't use has nothing to do with its CI:\n%s", out)
	}
	out = fabIn(t, home, "", 0, "auth", "env", "https://new.acme.dev")
	if !regexp.MustCompile(`^FABRICA_SERVER_SECRET=[0-9a-f]{64}\n$`).MatchString(out) {
		t.Errorf("env for the new server = %q", out)
	}
	if strings.Contains(out, testkit.Secret) {
		t.Error("the new server's secret should be its own")
	}

	// Names that mean nothing are an error, and git keeps its own credentials.
	if out := fabIn(t, home, "", 1, "auth", "login", "nonsense"); !strings.Contains(out, `there's no credential called "nonsense"`) {
		t.Errorf("login nonsense:\n%s", out)
	}
	if out := fabIn(t, home, "", 1, "auth", "logout", "git"); !strings.Contains(out, "gh auth logout") {
		t.Errorf("logout git:\n%s", out)
	}
}

func TestAuthOnALocalOnlyHome(t *testing.T) {
	w := setupCLI(t)
	w.MakePersonal(t)
	w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/blueprint, ref: stable }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	})
	out := fabIn(t, w.Home.Dir, "", 0, "auth")
	if strings.Contains(out, "Server secrets") || !strings.Contains(out, "✓ github.com/acme/blueprint: blueprint of mine") || !strings.Contains(out, "✓ Apply has everything it needs.") {
		t.Errorf("a local-only home needs only git access to its blueprint:\n%s", out)
	}

	// git can't read a blueprint: auth says so, and login says how to fix it.
	w.Home.Write(map[string]string{
		"factories/mine.yaml": "blueprint: { git: github.com/acme/missing, ref: stable }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	})
	out = fabIn(t, w.Home.Dir, "", 1, "auth")
	if !strings.Contains(out, "✗ github.com/acme/missing: blueprint of mine\n    git can't read it: ") {
		t.Errorf("an unreadable blueprint:\n%s", out)
	}
	out = fabIn(t, w.Home.Dir, "", 0, "auth", "login")
	if !strings.Contains(out, "✗ git can't read github.com/acme/missing (blueprint of mine)") || !strings.Contains(out, "gh") {
		t.Errorf("login with an unreadable blueprint:\n%s", out)
	}
}

func TestRemoteServerSecretFromTheStore(t *testing.T) {
	testkit.FreshCredentials(t)
	unsetenv(t, token.EnvVar)
	if _, err := remoteSecret(""); err == nil || !strings.Contains(err.Error(), "FABRICA_SERVER_SECRET isn't set") {
		t.Errorf("no secret anywhere: %v", err)
	}
	if _, err := remoteSecret("https://fabrica.acme.dev"); err == nil || !strings.Contains(err.Error(), "fabrica auth login https://fabrica.acme.dev") {
		t.Errorf("--url without a saved secret: %v", err)
	}
	if _, err := credentials.Set(credentials.ServerKey("https://fabrica.acme.dev"), testkit.Secret); err != nil {
		t.Fatal(err)
	}
	if got, err := remoteSecret("https://fabrica.acme.dev/"); err != nil || got != testkit.Secret {
		t.Errorf("--url with a saved secret = %q, %v", got, err)
	}
	other := strings.Repeat("e", 40)
	t.Setenv(token.EnvVar, other)
	if got, _ := remoteSecret("https://fabrica.acme.dev"); got != other {
		t.Errorf("FABRICA_SERVER_SECRET should win, got %q", got)
	}
}

func TestAuthAdminToken(t *testing.T) {
	w := setupCLI(t)
	factory := func(server string) map[string]string {
		return map[string]string{"factories/pr-review.yaml": "blueprint: { git: github.com/acme/blueprint, ref: stable }\nserver: " + server + "\nruntimes: [claude-code-cloud]\nroutines: [pr-review]\ntargets: [github.com/acme/app]\n"}
	}
	home := w.Home.Dir
	w.Home.Write(factory("https://fabrica.acme.dev"))

	// FABRICA_SERVER_SECRET counts for every server, so it needs no URL.
	out := fabIn(t, home, "", 0, "auth", "admin-token")
	if want := token.Admin(testkit.Secret) + "\n"; out != want {
		t.Errorf("admin-token = %q, want only %q", out, want)
	}
	if out := fabIn(t, home, "", 0, "auth", "admin-token", "https://anywhere.example"); out != token.Admin(testkit.Secret)+"\n" {
		t.Errorf("admin-token with a URL = %q", out)
	}
	if strings.Contains(out, testkit.Secret) || token.Admin(testkit.Secret) == token.For(testkit.Secret, "pr-review") {
		t.Error("the admin token isn't the secret, and isn't a factory's token")
	}
	t.Setenv(token.EnvVar, "")
	if out := fabIn(t, home, "", 1, "auth", "admin-token"); !strings.Contains(out, "FABRICA_SERVER_SECRET is set to nothing") {
		t.Errorf("an empty FABRICA_SERVER_SECRET:\n%s", out)
	}

	// Without the environment variable, it's the secret saved for the
	// server: the home repo's only one when no URL is named.
	unsetenv(t, token.EnvVar)
	out = fabIn(t, home, "", 1, "auth", "admin-token")
	if !strings.Contains(out, "there's no server secret for https://fabrica.acme.dev; run `fabrica auth login https://fabrica.acme.dev`") {
		t.Errorf("no saved secret:\n%s", out)
	}
	if _, err := credentials.Set(credentials.ServerKey("https://fabrica.acme.dev"), testkit.Secret); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"auth", "admin-token"}, {"auth", "admin-token", "https://fabrica.acme.dev"}, {"auth", "admin-token", "https://Fabrica.acme.dev/"}} {
		if out := fabIn(t, home, "", 0, args...); out != token.Admin(testkit.Secret)+"\n" {
			t.Errorf("fabrica %s = %q", strings.Join(args, " "), out)
		}
	}
	if out := fabIn(t, home, "", 1, "auth", "admin-token", "https://other.acme.dev"); !strings.Contains(out, "there's no server secret for https://other.acme.dev") {
		t.Errorf("a server with no saved secret:\n%s", out)
	}

	// Several servers: name one. Each has its own secret, so its own token.
	otherSecret := strings.Repeat("o", 40)
	if _, err := credentials.Set(credentials.ServerKey("https://other.acme.dev"), otherSecret); err != nil {
		t.Fatal(err)
	}
	w.Home.Write(map[string]string{"factories/autofix.yaml": strings.Replace(factory("https://other.acme.dev")["factories/pr-review.yaml"], "pr-review]", "autofix]", 1)})
	out = fabIn(t, home, "", 1, "auth", "admin-token")
	if !strings.Contains(out, "use 2 servers (https://fabrica.acme.dev, https://other.acme.dev); name the one you want, like `fabrica auth admin-token https://fabrica.acme.dev`") {
		t.Errorf("several servers:\n%s", out)
	}
	if out := fabIn(t, home, "", 0, "auth", "admin-token", "https://other.acme.dev"); out != token.Admin(otherSecret)+"\n" {
		t.Errorf("the other server's token = %q", out)
	}

	// Names that aren't servers, and homes that have none.
	if out := fabIn(t, home, "", 1, "auth", "admin-token", "acme"); !strings.Contains(out, `"acme" isn't a server URL`) {
		t.Errorf("admin-token acme:\n%s", out)
	}
	if out := fabIn(t, home, "", 1, "auth", "admin-token", "a", "b"); !strings.Contains(out, "accepts at most 1 arg") {
		t.Errorf("too many arguments:\n%s", out)
	}
	w.Home.Write(map[string]string{
		"factories/pr-review.yaml": "blueprint: { git: github.com/acme/blueprint, ref: stable }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\n",
	})
	if err := os.Remove(filepath.Join(home, "factories", "autofix.yaml")); err != nil {
		t.Fatal(err)
	}
	if out := fabIn(t, home, "", 1, "auth", "admin-token"); !strings.Contains(out, "use no Fabrica server; name one") {
		t.Errorf("a home repo with no servers:\n%s", out)
	}
}

// The token the CLI prints is the one a real server takes, and the secret
// the server uses stays out of it.
func TestAuthAdminTokenOpensTheServer(t *testing.T) {
	w := setupCLI(t)
	db, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cache := gitx.NewCache(t.TempDir())
	srv, err := mcpserver.NewRemote(mcpserver.RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: cache, Loader: blueprint.NewLoader(cache), Runs: db})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	adminToken := strings.TrimSpace(fabIn(t, w.Home.Dir, "", 0, "auth", "admin-token", ts.URL))
	for _, c := range []struct {
		auth string
		want int
	}{
		{"Bearer " + adminToken, 200},
		{"Bearer " + token.For(testkit.Secret, "pr-review"), 401},
		{"Bearer " + testkit.Secret, 401},
		{"", 401},
	} {
		req, _ := http.NewRequest("GET", ts.URL+"/_admin/runs", nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("GET /_admin/runs with %q: %d, want %d", c.auth, res.StatusCode, c.want)
		}
	}
}
