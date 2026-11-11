package heartbeat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/pace"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
)

func TestMain(m *testing.M) {
	cleanup := testkit.IsolateCredentials()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// fakeClaude is a pretend claude that writes down how each session was
// started, in a file named after the session's folder.
func fakeClaude(t *testing.T) (records string) {
	t.Helper()
	dir := t.TempDir()
	records = filepath.Join(dir, "records")
	if err := os.MkdirAll(records, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"" + records + "/$(basename \"$(dirname \"$PWD\")\")\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.ClaudeBinEnv, bin)
	return records
}

// waitFor waits for a session to write down how it started.
func waitFor(t *testing.T, file string) []string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if data, _ := os.ReadFile(file); len(data) > 0 {
			return strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no session started for %s", file)
	return nil
}

func TestTick(t *testing.T) {
	w := testkit.NewWorld(t)
	w.MakePersonal(t)
	records := fakeClaude(t)
	ctx := context.Background()
	w.Blueprint.Write(map[string]string{"routines/tidy.md": "---\ntrigger: { on: pace, every: 2 }\n---\nTidy up.\n"}).Commit("tidy")
	w.Home.Write(map[string]string{
		"factories/mine.yaml":  "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\nroutines: [tidy]\npace: 15m\n",
		"factories/other.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [\"*\"]\nroutines: [review-before-push]\n",
	})
	cache := gitx.NewCache(t.TempDir())
	db, err := runs.Open(filepath.Join(w.Home.Dir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	o := Options{HomeDir: w.Home.Dir, Binary: "/usr/local/bin/fabrica", Git: cache, Loader: blueprint.NewLoader(cache), Runs: db, CacheDir: t.TempDir()}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tick := func(now string) []Started {
		t.Helper()
		started, err := Tick(ctx, o, at(now))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range started {
			if s.Err != nil {
				t.Fatalf("%+v", s)
			}
		}
		return started
	}

	// A session of its own that's done and a day old goes; the heartbeat
	// leaves other folders alone.
	old := filepath.Join(o.CacheDir, "sessions", "mine", "p_0000000000")
	initDir := filepath.Join(o.CacheDir, "sessions", "mine", "i_0000000000")
	for _, dir := range []string{old, initDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "session.log"), []byte("done\n"), 0o600)
		os.Chtimes(filepath.Join(dir, "session.log"), at("2026-11-08T12:00:00Z"), at("2026-11-08T12:00:00Z"))
	}

	// 12:00 is the 48th beat of the day at 15m, so every: 2 runs on it.
	started := tick("2026-11-10T12:00:31Z")
	if len(started) != 1 || started[0].Routine != "tidy" || started[0].Repo != "github.com/acme/app" || !started[0].BeatAt.Equal(at("2026-11-10T12:00:00Z")) {
		t.Fatalf("started = %+v", started)
	}
	session := filepath.Base(filepath.Dir(started[0].Log))
	lines := waitFor(t, filepath.Join(records, session))
	if lines[0] != filepath.Join(o.CacheDir, "sessions", "mine", session, "app") {
		t.Errorf("claude should run in a clone of the repo, got %q", lines[0])
	}
	cmdline := strings.Join(lines[1:], " ")
	for _, want := range []string{
		"Fabrica routine `tidy` (from your local factory `mine`) should run now. Call `run_begin` with routine `tidy`, runtime `claude-code-local`, repo `github.com/acme/app`",
		"the beat at 2026-11-10T12:00:00Z (routine tidy runs every 2 paces, every 30m at the factory's pace of 15m)",
		"--name Fabrica routine tidy of factory mine",
		"--strict-mcp-config",
	} {
		if !strings.Contains(cmdline, want) {
			t.Errorf("claude's arguments are missing %q:\n%s", want, cmdline)
		}
	}
	var config struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(started[0].Log), "mcp.json"))
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if f := config.MCPServers["fabrica"]; f.Type != "stdio" || f.Command != "/usr/local/bin/fabrica" || strings.Join(f.Args, " ") != "mcp --runtime claude-code-local --home "+w.Home.Dir {
		t.Errorf("the session should be served by your local fabrica mcp: %+v", f)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("an old session's folder should go")
	}
	if _, err := os.Stat(initDir); err != nil {
		t.Error("an init session's folder is apply's, not the heartbeat's")
	}

	// Later in the beat, nothing starts again: not even when the marker
	// is gone, since runs.db remembers the start.
	if got := tick("2026-11-10T12:05:00Z"); len(got) != 0 {
		t.Errorf("a beat starts once: %+v", got)
	}
	os.RemoveAll(filepath.Join(o.CacheDir, "heartbeat"))
	if got := tick("2026-11-10T12:06:00Z"); len(got) != 0 {
		t.Errorf("runs.db keeps a beat from starting twice: %+v", got)
	}

	// The next beat doesn't run every: 2; the one after does.
	if got := tick("2026-11-10T12:15:00Z"); len(got) != 0 {
		t.Errorf("12:15 isn't an every: 2 beat: %+v", got)
	}
	if got := tick("2026-11-10T12:30:00Z"); len(got) != 1 {
		t.Errorf("12:30 is: %+v", got)
	}
	fires, err := db.PaceFires(ctx, at("2026-11-10T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 2 || fires[0].Status != runs.Fired || fires[0].Runtime != "claude-code-local" || fires[1].BeatAt.Format("15:04") != "12:30" {
		t.Errorf("fires = %+v", fires)
	}

	// A start that fails is recorded, with why.
	o.Launcher = func(*provider.Provider) (provider.Launcher, error) { return nil, os.ErrPermission }
	started, err = Tick(ctx, o, at("2026-11-10T13:00:00Z"))
	if err != nil || len(started) != 1 || started[0].Err == nil {
		t.Fatalf("started = %+v, %v", started, err)
	}
	fires, _ = db.PaceFires(ctx, at("2026-11-10T13:00:00Z"))
	if len(fires) != 1 || fires[0].Status != runs.FireFailed || fires[0].Error == "" {
		t.Errorf("a failed start: %+v", fires)
	}
}

func TestSessionID(t *testing.T) {
	a := SessionID("mine", "claude-code-local", "x#tidy@github.com/acme/app", paceAt(t, 30, "2026-11-10T12:00:00Z"))
	if !strings.HasPrefix(a, "p_") || len(a) != 12 || a != SessionID("mine", "claude-code-local", "x#tidy@github.com/acme/app", paceAt(t, 30, "2026-11-10T12:10:00Z")) {
		t.Errorf("a beat's session ID should be stable: %q", a)
	}
	if a == SessionID("mine", "claude-code-local", "x#tidy@github.com/acme/app", paceAt(t, 30, "2026-11-10T12:30:00Z")) {
		t.Error("each beat gets a session of its own")
	}
}

func paceAt(t *testing.T, minutes int, s string) pace.Beat {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return pace.Pace(minutes).BeatAt(v)
}
