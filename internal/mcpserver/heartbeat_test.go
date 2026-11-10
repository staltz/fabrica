package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

// webhookCall is one call a fake runtime's webhook got.
type webhookCall struct {
	Path, Auth, Version, Beta, Text string
}

type fakeWebhooks struct {
	mu    sync.Mutex
	calls []webhookCall
	ts    *httptest.Server
}

func newFakeWebhooks(t *testing.T) *fakeWebhooks {
	f := &fakeWebhooks{}
	f.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		f.mu.Lock()
		f.calls = append(f.calls, webhookCall{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Version: r.Header.Get("anthropic-version"), Beta: r.Header.Get("anthropic-beta"), Text: body.Text})
		f.mu.Unlock()
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "want a JSON POST", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"type":"routine_fire"}`))
	}))
	t.Cleanup(f.ts.Close)
	return f
}

// take returns the calls since the last take, sorted by path.
func (f *fakeWebhooks) take() []webhookCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	slices.SortFunc(out, func(a, b webhookCall) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func paths(calls []webhookCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Path)
	}
	return out
}

const pacedFactory = `blueprint: { git: github.com/acme/blueprint, ref: stable }
server: https://fabrica.acme.dev
runtimes: [claude-code-cloud, cursor-cloud]
routines: [tidy, sweep]
targets: [github.com/acme/app]
pace: 30m
`

func TestHeartbeat(t *testing.T) {
	rw := newRemoteWorld(t)
	ctx := context.Background()
	hooks := newFakeWebhooks(t)
	rw.w.UpdateBlueprint(map[string]string{
		"routines/tidy.md":  "---\ntrigger: { on: pace, every: 4 }\n---\nTidy up.\n",
		"routines/sweep.md": "---\ntrigger: { on: pace }\n---\nSweep.\n",
	})
	hook := func(identity, path, tok, secret string) *home.Webhook {
		sealed, err := token.Seal(secret, "paced", identity, tok)
		if err != nil {
			t.Fatal(err)
		}
		return &home.Webhook{URL: hooks.ts.URL + path, Token: sealed}
	}
	stub := func(routine, runtime, repo string, wh *home.Webhook) home.Resource {
		return home.Resource{ID: "x#" + routine + "@" + repo, Runtime: runtime, Routine: routine, Repo: repo, RemoteID: "r", Mode: "api",
			Shape: home.Shape{Trigger: "webhook", Fingerprint: "f"}, Webhook: wh}
	}
	const app = "github.com/acme/app"
	state := &home.State{Resources: []home.Resource{
		stub("tidy", "claude-code-cloud", app, hook("x#tidy@"+app, "/claude/tidy", "tok-claude", testkit.Secret)),
		stub("sweep", "cursor-cloud", app, hook("x#sweep@"+app, "/cursor/sweep", "tok-cursor", testkit.Secret)),
		// Sealed with another server secret: recorded as failed, never called.
		stub("sweep", "claude-code-cloud", app, hook("x#sweep@"+app, "/claude/sweep", "tok-old", strings.Repeat("o", 32))),
		// No webhook yet: nothing to call.
		stub("tidy", "cursor-cloud", app, nil),
		// A repo the factory doesn't deploy to anymore: left for apply to delete.
		stub("tidy", "cursor-cloud", "github.com/acme/gone", hook("x#tidy@github.com/acme/gone", "/gone", "tok-gone", testkit.Secret)),
	}}
	if err := home.WriteState(rw.w.Home.Dir, "paced", state); err != nil {
		t.Fatal(err)
	}
	rw.w.Home.Write(map[string]string{"factories/paced.yaml": pacedFactory}).CommitPush("add paced")
	if err := rw.srv.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// 12:00 UTC is a beat that every: 4 runs on (at 30m, every 2h from
	// midnight), and every: 1 runs on every beat.
	rw.srv.Beat(ctx, at("2026-11-10T12:00:07Z"))
	calls := hooks.take()
	if got := paths(calls); !slices.Equal(got, []string{"/claude/tidy", "/cursor/sweep"}) {
		t.Fatalf("calls at 12:00 = %v", got)
	}
	claude, cursor := calls[0], calls[1]
	if claude.Auth != "Bearer tok-claude" || claude.Version != "2023-06-01" || claude.Beta != "experimental-cc-routine-2026-04-01" {
		t.Errorf("Claude's fire call = %+v", claude)
	}
	if !strings.Contains(claude.Text, "routine tidy on the heartbeat of factory paced: the beat at 2026-11-10T12:00:00Z") || !strings.Contains(claude.Text, "every 4 paces, every 2h at the factory's pace of 30m") {
		t.Errorf("text = %q", claude.Text)
	}
	if cursor.Auth != "Bearer tok-cursor" || cursor.Version != "" || cursor.Beta != "" {
		t.Errorf("Cursor's webhook call = %+v", cursor)
	}

	// Later in the same beat, nothing fires again, even from a new server
	// (a restart) on the same database.
	rw.srv.Beat(ctx, at("2026-11-10T12:20:00Z"))
	restarted, err := NewRemote(RemoteOptions{Home: "github.com/acme/home", Secret: testkit.Secret, Git: rw.cache, Loader: rw.loader, Runs: rw.db})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Beat(ctx, at("2026-11-10T12:29:59Z"))
	if got := hooks.take(); len(got) != 0 {
		t.Fatalf("a beat fires once: %v", paths(got))
	}

	// The next beat only runs every: 1.
	rw.srv.Beat(ctx, at("2026-11-10T12:30:00Z"))
	if got := paths(hooks.take()); !slices.Equal(got, []string{"/cursor/sweep"}) {
		t.Fatalf("calls at 12:30 = %v", got)
	}

	fires, err := rw.db.PaceFires(ctx, at("2026-11-10T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fires {
		got = append(got, f.BeatAt.Format("15:04")+" "+f.Runtime+" "+f.Routine+" "+f.Status)
		if f.Status == runs.FireFailed && !strings.Contains(f.Error, "sealed with another server secret") {
			t.Errorf("a token that won't open says why: %q", f.Error)
		}
	}
	slices.Sort(got)
	want := []string{
		"12:00 claude-code-cloud sweep failed",
		"12:00 claude-code-cloud tidy fired",
		"12:00 cursor-cloud sweep fired",
		"12:30 claude-code-cloud sweep failed",
		"12:30 cursor-cloud sweep fired",
	}
	if !slices.Equal(got, want) {
		t.Errorf("fires:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The heartbeat wakes up when the next beat starts.
	if got := rw.srv.untilNextBeat(at("2026-11-10T12:59:30Z")); got != 30*time.Second {
		t.Errorf("untilNextBeat = %s", got)
	}

	// Changing every: needs no apply: the server reads it at its next
	// refresh. And a factory without a pace has no heartbeat.
	rw.w.UpdateBlueprint(map[string]string{"routines/tidy.md": "---\ntrigger: { on: pace, every: 3 }\n---\nTidy up.\n"})
	rw.w.Home.Write(map[string]string{"factories/paced.yaml": pacedFactory}).CommitPush("touch")
	if err := rw.srv.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rw.srv.Beat(ctx, at("2026-11-10T13:30:00Z")) // beat 27 of the day: every 3, not every 4
	if got := paths(hooks.take()); !slices.Equal(got, []string{"/claude/tidy", "/cursor/sweep"}) {
		t.Fatalf("calls at 13:30 with every: 3 = %v", got)
	}
	rw.w.Home.Write(map[string]string{"factories/paced.yaml": strings.Replace(pacedFactory, "pace: 30m\n", "", 1)}).CommitPush("no pace")
	if err := rw.srv.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rw.srv.Beat(ctx, at("2026-11-10T14:00:00Z"))
	if got := hooks.take(); len(got) != 0 {
		t.Errorf("without a pace, nothing fires: %v", paths(got))
	}
	if got := rw.srv.untilNextBeat(at("2026-11-10T14:00:00Z")); got != heartbeatMaxSleep {
		t.Errorf("without a pace, the heartbeat just checks back: %s", got)
	}
}
