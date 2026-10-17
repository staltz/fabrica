package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/testkit"
)

func initSession() Session {
	return Session{
		ID: "i_3f2a1c9e04", Identity: "fabrica.acme.dev/acme-app#onboard", Factory: "acme-app", Routine: "onboard",
		Repo: "github.com/acme/app", Repos: []string{"github.com/acme/app", "github.com/acme/api"},
		Connectors: []ResourceConnector{
			{Name: "github", Kind: "native"},
			{Name: "linear", Kind: "mcp", URL: "https://mcp.linear.app/mcp", Transport: "http", Auth: "bearer", Secret: "LINEAR_API_KEY"},
		},
		Fabrica:     ResourceFabrica{URL: "https://fabrica.acme.dev/acme-app", Placement: TokenInHeader, Token: "fab_tok"},
		Model:       &ResourceModel{Name: "claude-opus-5-5", Effort: "high"},
		Prompt:      "You are the init session of Fabrica factory `acme-app`.",
		Fingerprint: "a1b2c3d4e5f6",
	}
}

func TestLaunchModes(t *testing.T) {
	for _, bad := range []string{
		"kind: cloud\napply_modes: [handoff]\nlaunch_modes: [maybe]\nskill_transport: text\nmcp_auth: {url: native}\n",
		"kind: cloud\napply_modes: [handoff]\nlaunch_modes: [api, api]\nskill_transport: text\nmcp_auth: {url: native}\n",
	} {
		if _, err := ParseMatrix("x", []byte(bad)); err == nil {
			t.Errorf("want an error for %q", bad)
		}
	}
	want := map[string]string{
		"claude-code-cloud": "handoff", "claude-code-local": "api", "codex": "handoff", "cursor-cloud": "api,handoff",
		"codex-cli": "", "copilot-cloud": "", "copilot-local": "", "cursor-local": "",
	}
	for name, modes := range want {
		p, _ := Builtin(name)
		var got []string
		for _, m := range p.Matrix.LaunchModes {
			got = append(got, string(m))
		}
		if strings.Join(got, ",") != modes {
			t.Errorf("%s: launch_modes = %v, want %s", name, got, modes)
		}
		if p.CanHandOffLaunch() != strings.Contains(modes, "handoff") {
			t.Errorf("%s: a launch handoff exists = %v, want it with launch_modes %q", name, p.CanHandOffLaunch(), modes)
		}
	}
}

func TestFolderProviderLaunch(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("provider.yaml", "kind: cloud\napply_modes: [handoff]\nlaunch_modes: [api, handoff]\nskill_transport: text\nmcp_auth: {header: native}\n", 0o644)
	write("handoff.md", "{{.ConfirmCommand}}\n", 0o644)
	if _, err := LoadFolder("acme", dir); err == nil || !strings.Contains(err.Error(), "api program") {
		t.Errorf("launch_modes: [api] needs an api program, got %v", err)
	}
	// The api program echoes what it got, so the test can see the request.
	write("api", "#!/bin/sh\ncat > \"$(dirname \"$0\")/request.json\"\necho '{\"session_id\": \"acme_s1\", \"url\": \"https://acme.dev/s/1\"}'\n", 0o755)
	if _, err := LoadFolder("acme", dir); err == nil || !strings.Contains(err.Error(), "launch.md") {
		t.Errorf("launch_modes: [handoff] needs a launch.md, got %v", err)
	}
	write("launch.md", "Start {{.Routine}} on {{.Repo}}, then {{.ConfirmCommand}}\n", 0o644)
	p, err := LoadFolder("acme", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LaunchUnavailable(); err != nil {
		t.Fatalf("a folder provider with an api program can start sessions: %v", err)
	}
	l, err := p.Launcher()
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Launch(context.Background(), initSession())
	if err != nil || got.SessionID != "acme_s1" || got.URL != "https://acme.dev/s/1" {
		t.Fatalf("Launch = %+v, %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "request.json"))
	var req struct {
		Version int     `json:"version"`
		Op      string  `json:"op"`
		Session Session `json:"session"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	if req.Version != ProtocolVersion || req.Op != "launch" || req.Session.ID != "i_3f2a1c9e04" || req.Session.Fabrica.Token != "fab_tok" || len(req.Session.Repos) != 2 {
		t.Errorf("the launch request = %s", data)
	}
	text, err := p.RenderLaunch(LaunchData{Routine: "onboard", Repo: "github.com/acme/app", ConfirmCommand: "fabrica confirm i_1"})
	if err != nil || text != "Start onboard on github.com/acme/app, then fabrica confirm i_1\n" {
		t.Errorf("RenderLaunch = %q, %v", text, err)
	}
}

func TestRenderBuiltinLaunchHandoffs(t *testing.T) {
	data := LaunchData{
		ID: "i_3f2a1c9e04", Runtime: "x", Factory: "acme-app", Routine: "onboard", Identity: "fabrica.acme.dev/acme-app#onboard",
		Repo: "github.com/acme/app", Repos: []string{"github.com/acme/app", "github.com/acme/api", "github.com/acme/web"},
		Connectors:     []HandoffConnector{{Name: "linear", Setup: "MCP server at `https://mcp.linear.app/mcp` (http)"}},
		Model:          HandoffModel{Setup: "pick model `claude-opus-5-5`, with effort `high`"},
		Fabrica:        HandoffFabrica{URL: "https://fabrica.acme.dev/acme-app", Header: "Bearer fab_tok"},
		Prompt:         "line one\nline two",
		ConfirmCommand: "fabrica confirm i_3f2a1c9e04 --remote-id <the session's ID or URL>",
	}
	for _, name := range []string{"claude-code-cloud", "codex", "cursor-cloud"} {
		p, _ := Builtin(name)
		text, err := p.RenderLaunch(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"`onboard`", "`github.com/acme/app`", "`github.com/acme/api`, `github.com/acme/web`", "Bearer fab_tok", "**linear**", "   line one\n   line two", data.ConfirmCommand} {
			if !strings.Contains(text, want) {
				t.Errorf("%s's launch.md is missing %q:\n%s", name, want, text)
			}
		}
	}
}

func TestSessionUUIDIsStable(t *testing.T) {
	a, b := SessionUUID("i_1"), SessionUUID("i_1")
	if a != b || a == SessionUUID("i_2") {
		t.Errorf("SessionUUID should be the same for the same ID only: %s %s %s", a, b, SessionUUID("i_2"))
	}
	if len(a) != 36 || a[14] != '5' || !strings.ContainsRune("89ab", rune(a[19])) {
		t.Errorf("SessionUUID = %s, want a version 5 UUID", a)
	}
}

func TestCursorLauncher(t *testing.T) {
	var requests []map[string]any
	conflict := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _, ok := r.BasicAuth(); !ok || user != "key_x" || r.URL.Path != "/v1/agents" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s (basic auth user %q)", r.Method, r.URL.Path, user)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		requests = append(requests, req)
		if conflict {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"code":"agent_id_conflict","message":"An agent with this ID already exists"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"agent":{"id":"` + req["agentId"].(string) + `","url":"https://cursor.com/agents/` + req["agentId"].(string) + `","status":"ACTIVE"},"run":{"status":"CREATING"}}`))
	}))
	defer srv.Close()
	t.Setenv(CursorKeyEnv, "key_x")
	t.Setenv(CursorAgentsEndpointEnv, srv.URL)
	t.Setenv("LINEAR_API_KEY", "lin_secret")
	p, _ := Builtin("cursor-cloud")
	if err := p.LaunchUnavailable(); err != nil {
		t.Fatal(err)
	}
	l, err := p.Launcher()
	if err != nil {
		t.Fatal(err)
	}
	s := initSession()
	got, err := l.Launch(context.Background(), s)
	agentID := "bc-" + SessionUUID(s.ID)
	if err != nil || got.SessionID != agentID || got.URL != "https://cursor.com/agents/"+agentID {
		t.Fatalf("Launch = %+v, %v", got, err)
	}
	req := toJSON(requests[0])
	for _, want := range []string{
		`"agentId":"` + agentID + `"`,
		`"prompt":{"text":"You are the init session of Fabrica factory ` + "`acme-app`" + `."}`,
		`"repos":[{"url":"https://github.com/acme/app"},{"url":"https://github.com/acme/api"}]`,
		`{"headers":{"Authorization":"Bearer fab_tok"},"name":"fabrica","type":"http","url":"https://fabrica.acme.dev/acme-app"}`,
		`{"headers":{"Authorization":"Bearer lin_secret"},"name":"linear","type":"http","url":"https://mcp.linear.app/mcp"}`,
		`"model":{"id":"claude-opus-5-5","params":[{"id":"context","value":"1m"},{"id":"effort","value":"high"},{"id":"fast","value":"false"}]}`,
	} {
		if !strings.Contains(req, want) {
			t.Errorf("the launch request is missing %s:\n%s", want, req)
		}
	}

	// A retry finds the agent the first try started.
	conflict = true
	got, err = l.Launch(context.Background(), s)
	if err != nil || got.SessionID != agentID {
		t.Errorf("a launch Cursor already has should be fine: %+v, %v", got, err)
	}
	if toJSON(requests[1]["agentId"]) != toJSON(requests[0]["agentId"]) {
		t.Errorf("a retry should ask for the same agent ID")
	}

	// A connector secret it doesn't have is an error, before any request.
	t.Setenv("LINEAR_API_KEY", "")
	if _, err := l.Launch(context.Background(), s); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") || len(requests) != 2 {
		t.Errorf("a missing connector secret should fail before calling Cursor, got %v", err)
	}
}

func TestCursorLauncherNeedsAKey(t *testing.T) {
	t.Setenv(CursorKeyEnv, "")
	t.Setenv(CursorTokenEnv, "")
	p, _ := Builtin("cursor-cloud")
	if err := p.LaunchUnavailable(); err == nil || !strings.Contains(err.Error(), CursorKeyEnv) {
		t.Errorf("LaunchUnavailable = %v, want it to name %s", err, CursorKeyEnv)
	}
}

func TestClaudeLauncher(t *testing.T) {
	host := testkit.NewHost(t)
	host.Repo("github.com/acme/app").Write(map[string]string{"AGENTS.md": "# App\n"}).Commit("init")

	// A pretend claude that writes down how it was started.
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$(dirname \"$0\")/args\"\necho started\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ClaudeBinEnv, "fabrica-test-no-such-claude")
	p, _ := Builtin("claude-code-local")
	if err := p.LaunchUnavailable(); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("without claude, LaunchUnavailable = %v", err)
	}
	t.Setenv(ClaudeBinEnv, bin)
	l, err := p.Launcher()
	if err != nil {
		t.Fatal(err)
	}
	s := initSession()
	s.Connectors = s.Connectors[1:] // locally, GitHub would need a fallback
	s.Model = nil
	s.Dir = filepath.Join(t.TempDir(), "acme-app", s.ID)
	got, err := l.Launch(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != SessionUUID(s.ID) || got.Log != filepath.Join(s.Dir, "session.log") {
		t.Errorf("Launch = %+v", got)
	}
	argsFile := filepath.Join(filepath.Dir(bin), "args")
	var args []byte
	for i := 0; i < 100; i++ { // it runs in the background
		if args, _ = os.ReadFile(argsFile); len(args) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) == 0 || lines[0] != filepath.Join(s.Dir, "app") {
		t.Fatalf("claude should run in a clone of the repo, got %q", args)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "app", "AGENTS.md")); err != nil {
		t.Errorf("the repo wasn't cloned: %v", err)
	}
	cmdline := strings.Join(lines[1:], " ")
	for _, want := range []string{"-p " + s.Prompt, "--session-id " + SessionUUID(s.ID), "--mcp-config " + filepath.Join(s.Dir, "mcp.json") + " --strict-mcp-config", "--permission-mode auto"} {
		if !strings.Contains(cmdline, want) {
			t.Errorf("claude's arguments are missing %q: %s", want, cmdline)
		}
	}
	if strings.Contains(cmdline, "fab_tok") {
		t.Errorf("the token must stay out of the command line: %s", cmdline)
	}
	config, _ := os.ReadFile(filepath.Join(s.Dir, "mcp.json"))
	info, _ := os.Stat(filepath.Join(s.Dir, "mcp.json"))
	if info == nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mcp.json holds the token, so only you may read it: %v", info)
	}
	for _, want := range []string{`"Authorization": "Bearer fab_tok"`, `"url": "https://fabrica.acme.dev/acme-app"`, `"Authorization": "Bearer ${LINEAR_API_KEY}"`} {
		if !strings.Contains(string(config), want) {
			t.Errorf("mcp.json is missing %s:\n%s", want, config)
		}
	}

	// A retry doesn't start a second session.
	os.Remove(argsFile)
	if again, err := l.Launch(context.Background(), s); err != nil || again.SessionID != got.SessionID {
		t.Errorf("retry = %+v, %v", again, err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("a retry started claude again")
	}
}
