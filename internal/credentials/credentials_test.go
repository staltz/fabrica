package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/token"
)

// isolate points the store at a fresh config folder, with the system
// keychain off, so tests never touch your real credentials.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(KeychainEnv, "off")
	return filepath.Join(dir, "fabrica", fileName)
}

// unsetenv unsets a variable for one test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "") // registers the cleanup that restores it
	os.Unsetenv(name)
}

func TestLookupPrefersTheEnvironment(t *testing.T) {
	isolate(t)
	unsetenv(t, "CURSOR_API_KEY")
	unsetenv(t, "CURSOR_TOKEN")

	if f, err := Lookup("CURSOR_API_KEY", "CURSOR_TOKEN"); err != nil || f.Value != "" || f.Env {
		t.Fatalf("nothing saved: Lookup = %+v, %v", f, err)
	}
	where, err := Set("CURSOR_API_KEY", "crsr_saved")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "credentials.json") || InKeychain() {
		t.Errorf("Set saved in %q, want the credentials file", where)
	}
	f, err := Lookup("CURSOR_API_KEY", "CURSOR_TOKEN")
	if err != nil || f.Value != "crsr_saved" || f.Env || f.From != where {
		t.Errorf("saved: Lookup = %+v, %v", f, err)
	}

	// An alias in the environment beats the store.
	t.Setenv("CURSOR_TOKEN", "crsr_alias")
	if f, _ := Lookup("CURSOR_API_KEY", "CURSOR_TOKEN"); f.Value != "crsr_alias" || !f.Env || f.From != "the environment variable CURSOR_TOKEN" {
		t.Errorf("alias set: Lookup = %+v", f)
	}
	// The first variable beats the alias.
	t.Setenv("CURSOR_API_KEY", " crsr_env\n")
	if f, _ := Lookup("CURSOR_API_KEY", "CURSOR_TOKEN"); f.Value != "crsr_env" || f.From != "the environment variable CURSOR_API_KEY" {
		t.Errorf("both set: Lookup = %+v", f)
	}
	// A variable set to nothing means "no credential", without asking the store.
	t.Setenv("CURSOR_API_KEY", "")
	unsetenv(t, "CURSOR_TOKEN")
	if f, _ := Lookup("CURSOR_API_KEY", "CURSOR_TOKEN"); f.Value != "" || !f.Env {
		t.Errorf("set to nothing: Lookup = %+v, want the environment to decide there's none", f)
	}
}

func TestFileIsPrivate(t *testing.T) {
	path := isolate(t)
	if _, err := Set("CURSOR_API_KEY", "crsr_x"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v, want 0600", info.Mode().Perm())
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("config folder mode = %v, want 0700", dir.Mode().Perm())
	}

	// Others can read it: refuse, and say how to fix it.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	forget("CURSOR_API_KEY")
	if _, _, err := Get("CURSOR_API_KEY"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("Get on a world-readable file = %v, want a refusal that says chmod 600", err)
	}
}

func TestDeleteRemovesTheFileWhenEmpty(t *testing.T) {
	path := isolate(t)
	if _, err := Set("A_KEY", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := Set("B_KEY", "two"); err != nil {
		t.Fatal(err)
	}
	where, err := Delete("A_KEY")
	if err != nil || len(where) != 1 {
		t.Fatalf("Delete = %v, %v", where, err)
	}
	if v, _, _ := Get("A_KEY"); v != "" {
		t.Errorf("A_KEY still there: %q", v)
	}
	if v, _, _ := Get("B_KEY"); v != "two" {
		t.Errorf("B_KEY = %q, want two", v)
	}
	if where, _ := Delete("A_KEY"); len(where) != 0 {
		t.Errorf("deleting twice found it in %v", where)
	}
	if _, err := Delete("B_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the empty credentials file is still there: %v", err)
	}
}

func TestServerSecretsAreKeptPerServer(t *testing.T) {
	isolate(t)
	unsetenv(t, token.EnvVar)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if _, err := Set(ServerKey("https://fabrica.acme.dev/"), a); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(ServerKey("https://staging.acme.dev"), b); err != nil {
		t.Fatal(err)
	}
	if f, err := ServerSecret("https://Fabrica.ACME.dev"); err != nil || f.Value != a {
		t.Errorf("ServerSecret(acme) = %+v, %v", f, err)
	}
	if f, err := ServerSecret("https://staging.acme.dev/"); err != nil || f.Value != b {
		t.Errorf("ServerSecret(staging) = %+v, %v", f, err)
	}
	if f, err := ServerSecret("https://other.dev"); err != nil || f.Value != "" {
		t.Errorf("ServerSecret(unknown) = %+v, %v", f, err)
	}

	// FABRICA_SERVER_SECRET counts for every server.
	c := strings.Repeat("c", 40)
	t.Setenv(token.EnvVar, c)
	if f, _ := ServerSecret("https://staging.acme.dev"); f.Value != c || !f.Env {
		t.Errorf("with %s set, ServerSecret = %+v", token.EnvVar, f)
	}
	t.Setenv(token.EnvVar, "short")
	if _, err := ServerSecret("https://staging.acme.dev"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("a short %s = %v, want an error", token.EnvVar, err)
	}
}

func TestCheck(t *testing.T) {
	for _, ok := range []string{"crsr_abc123", "key_ABC-def.ghi", strings.Repeat("f", 64), "a+b/c=="} {
		if err := Check(ok); err != nil {
			t.Errorf("Check(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "two words", "line\nbreak", "it's", `say "hi"`, `back\slash`, strings.Repeat("x", 2000), "naïve"} {
		if err := Check(bad); err == nil {
			t.Errorf("Check(%q) = nil, want an error", bad)
		}
	}
	isolate(t)
	if _, err := Set("A_KEY", "has space"); err == nil {
		t.Error("Set saved a value with a space")
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"https://fabrica.acme.dev":          "https://fabrica.acme.dev",
		"https://fabrica.acme.dev/":         "https://fabrica.acme.dev",
		"HTTPS://Fabrica.Acme.Dev//":        "https://fabrica.acme.dev",
		"https://acme.dev/Fabrica/":         "https://acme.dev/Fabrica",
		" http://localhost:8080 ":           "http://localhost:8080",
		"https://fabrica.acme.dev/fabrica/": "https://fabrica.acme.dev/fabrica",
	} {
		if got := NormalizeServer(in); got != want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeSecurity is a stand-in for macOS's /usr/bin/security that keeps items
// in files. Like the real one, `-i` reads commands from stdin and splits
// them like a shell. It logs its command lines, so tests can check that no
// secret ever appears on one.
const fakeSecurity = `#!/bin/sh
dir="$FAKE_KEYCHAIN"
echo "$*" >> "$dir/argv.log"
hex() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n'; }
parse() {
	s= a= w=
	while [ $# -gt 0 ]; do
		case "$1" in
			-s) s=$2; shift 2 ;;
			-a) a=$2; shift 2 ;;
			-l) shift 2 ;;
			-w) if [ $# -gt 1 ]; then w=$2; shift 2; else shift; fi ;;
			*) shift ;;
		esac
	done
	f="$dir/item-$(hex "$s")-$(hex "$a")"
}
case "$1" in
-i)
	while IFS= read -r line; do
		eval "set -- $line"
		cmd=$1; shift
		parse "$@"
		[ "$cmd" = add-generic-password ] && printf '%s' "$w" > "$f"
	done ;;
find-generic-password)
	shift; parse "$@"
	if [ ! -f "$f" ]; then echo "security: The specified item could not be found in the keychain." >&2; exit 44; fi
	cat "$f"; echo ;;
delete-generic-password)
	shift; parse "$@"
	if [ ! -f "$f" ]; then echo "security: The specified item could not be found in the keychain." >&2; exit 44; fi
	rm "$f" ;;
esac
`

// fakeSecretTool is a stand-in for libsecret's secret-tool, which reads
// the secret to store from stdin.
const fakeSecretTool = `#!/bin/sh
dir="$FAKE_KEYCHAIN"
echo "$*" >> "$dir/argv.log"
if [ -n "$FAKE_NO_DBUS" ]; then echo "secret-tool: Cannot autolaunch D-Bus without X11 \$DISPLAY" >&2; exit 1; fi
hex() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n'; }
cmd=$1; shift
case "$cmd" in store) shift ;; esac
f="$dir/item-$(hex "$*")"
case "$cmd" in
store) cat > "$f" ;;
lookup) [ -f "$f" ] || exit 1; cat "$f" ;;
clear) rm -f "$f" ;;
esac
`

func writeTool(t *testing.T, script string) (path, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake keychain tools are shell scripts")
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "tool")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KEYCHAIN", dir)
	return path, dir
}

// exerciseBackend runs a backend through saving, reading, replacing and
// deleting, and checks the secret never showed up on a command line.
func exerciseBackend(t *testing.T, b backend, dir string) {
	t.Helper()
	key := ServerKey("https://fabrica.acme.dev")
	if _, ok, err := b.Get(key); ok || err != nil {
		t.Fatalf("Get before Set = %v, %v", ok, err)
	}
	secret := "s3cr3t+/=_" + strings.Repeat("9", 40)
	if err := b.Set(key, secret); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := b.Get(key); !ok || err != nil || v != secret {
		t.Fatalf("Get = %q, %v, %v; want %q", v, ok, err, secret)
	}
	if err := b.Set(key, "replaced"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := b.Get(key); v != "replaced" {
		t.Errorf("after replacing, Get = %q", v)
	}
	if removed, err := b.Delete(key); !removed || err != nil {
		t.Errorf("Delete = %v, %v", removed, err)
	}
	if removed, err := b.Delete(key); removed || err != nil {
		t.Errorf("second Delete = %v, %v", removed, err)
	}
	if _, ok, err := b.Get(key); ok || err != nil {
		t.Errorf("Get after Delete = %v, %v", ok, err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "argv.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), secret) || strings.Contains(string(log), "replaced") {
		t.Errorf("a secret showed up on a command line:\n%s", log)
	}
}

func TestMacKeychain(t *testing.T) {
	path, dir := writeTool(t, fakeSecurity)
	exerciseBackend(t, &macKeychain{path: path}, dir)
}

func TestSecretService(t *testing.T) {
	path, dir := writeTool(t, fakeSecretTool)
	s := &secretService{path: path}
	if !s.usable() {
		t.Fatal("secret-tool with a working keyring isn't usable")
	}
	exerciseBackend(t, s, dir)

	t.Setenv("FAKE_NO_DBUS", "1")
	other, _ := writeTool(t, fakeSecretTool)
	if (&secretService{path: other}).usable() {
		t.Error("secret-tool without D-Bus counts as usable")
	}
}
