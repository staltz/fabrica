package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func cursorResource() Resource {
	return Resource{
		Identity: "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app",
		Factory:  "pr-review", Routine: "pr-review", Repo: "github.com/acme/app",
		Trigger: ResourceTrigger{On: "github.pull_request.opened"},
		Connectors: []ResourceConnector{
			{Name: "github", Kind: "native"},
			{Name: "slack", Kind: "native"},
			{Name: "linear", Kind: "mcp", URL: "https://mcp.linear.app/mcp", Transport: "http", Auth: "oauth"},
		},
		Fabrica: ResourceFabrica{URL: "https://fabrica.acme.dev/pr-review", Placement: TokenInHeader, Token: "fab_tok"},
		Memory:  "off",
		Prompt:  "You are routine `pr-review`. Call `run_begin` with … and stub `e128f7aa2995`, and an intent …",
	}
}

func TestCursorWorkflowJSON(t *testing.T) {
	// This is the JSON encoding of an aiserver.v1.Workflow, as the protobuf
	// schema vendored in Cursor's Terraform provider reads it.
	wf, err := cursorWorkflowFor(cursorResource())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(wf)
	want := `{"triggers":[{"git":{"pullRequest":{"repos":["acme/app"],"prAction":"GIT_PULL_REQUEST_ACTION_OPENED"}}}],` +
		`"actions":[{"mcp":{"server":{"name":"fabrica-pr-review"}}},{"prComment":{}},{"slack":{"generalized":true}},{"mcp":{"server":{"name":"linear"}}}],` +
		`"prompts":[{"prompt":"You are routine ` + "`pr-review`. Call `run_begin` with … and stub `e128f7aa2995`" + `, and an intent …"}],` +
		`"gitConfig":{"repo":"github.com/acme/app","repos":["github.com/acme/app"]},"memoryEnabled":false}`
	if string(got) != want {
		t.Errorf("workflow JSON:\n got %s\nwant %s", got, want)
	}

	triggers := map[string]string{
		"github.pull_request.synchronize": `{"git":{"pullRequest":{"repos":["acme/app"],"prAction":"GIT_PULL_REQUEST_ACTION_PUSHED"}}}`,
		"github.pull_request.labeled":     `{"git":{"label":{"repos":["acme/app"],"onAdded":true,"pullRequests":true}}}`,
		"github.push":                     `{"git":{"push":{"repo":"acme/app"}}}`,
		"schedule":                        `{"cron":{"cron":"0 3 * * *"}}`,
	}
	for on, want := range triggers {
		res := cursorResource()
		res.Trigger = ResourceTrigger{On: on, Cron: "0 3 * * *"}
		wf, err := cursorWorkflowFor(res)
		if err != nil {
			t.Fatalf("%s: %v", on, err)
		}
		if got, _ := json.Marshal(wf.Triggers[0]); string(got) != want {
			t.Errorf("%s: trigger = %s, want %s", on, got, want)
		}
	}

	res := cursorResource()
	res.Trigger.On = "github.pull_request.reopened"
	if _, err := cursorWorkflowFor(res); err == nil {
		t.Error("Cursor has no reopened trigger, so that should be an error")
	}
	res = cursorResource()
	res.Connectors = []ResourceConnector{{Name: "notion", Kind: "native"}}
	if _, err := cursorWorkflowFor(res); err == nil {
		t.Error("Cursor has no built-in notion tool, so that should be an error")
	}
	res = cursorResource()
	res.Memory = "on"
	if wf, _ := cursorWorkflowFor(res); wf.MemoryEnabled == nil || !*wf.MemoryEnabled {
		t.Error("memory on should turn the automation's memory tool on")
	}
}

func TestCursorAPILifecycle(t *testing.T) {
	fake := testkit.NewFakeCursor(t)
	p, _ := Builtin("cursor-cloud")
	if mode, _ := p.Mode(); mode != API {
		t.Fatalf("with a key, cursor-cloud should use api mode, not %s", mode)
	}
	client, err := p.API()
	if err != nil {
		t.Fatal(err)
	}
	getter, ok := client.(Getter)
	if !ok {
		t.Fatalf("Cursor's API can't list, so its client should be a Getter")
	}
	ctx := context.Background()
	res := cursorResource()

	id, err := client.Create(ctx, res)
	if err != nil || id != "auto_1" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	a := fake.Automations()[0]
	if a.Name != res.Identity || !strings.Contains(a.Description, "`fabrica apply` manages it") {
		t.Errorf("automation = %+v", a)
	}

	got, err := getter.Get(ctx, id)
	if err != nil || got == nil || got.Identity != res.Identity || got.Fingerprint != "e128f7aa2995" {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	res.Trigger.On = "github.push"
	if err := client.Update(ctx, id, res); err != nil {
		t.Fatal(err)
	}
	if trig := fake.Automations()[0].Workflow["triggers"]; !strings.Contains(toJSON(trig), `"push"`) {
		t.Errorf("Update didn't change the trigger: %s", toJSON(trig))
	}

	fake.FailNext("UpdateAutomation", "permission_denied")
	if err := client.Update(ctx, id, res); err == nil || !strings.Contains(err.Error(), "permission_denied") {
		t.Errorf("a failed update should say why, got %v", err)
	}

	if err := client.Delete(ctx, "pr-review", id); err != nil {
		t.Fatal(err)
	}
	if got, err := getter.Get(ctx, id); got != nil || err != nil {
		t.Errorf("Get after Delete = %+v, %v; want nothing", got, err)
	}
	if err := client.Delete(ctx, "pr-review", id); err != nil {
		t.Errorf("deleting what's already gone should be fine, got %v", err)
	}
}

func TestCursorModeNeedsAKey(t *testing.T) {
	t.Setenv(CursorKeyEnv, "")
	t.Setenv(CursorTokenEnv, "")
	p, _ := Builtin("cursor-cloud")
	if mode, _ := p.Mode(); mode != Handoff {
		t.Errorf("without a key, cursor-cloud should fall back to handoffs, not %s", mode)
	}
	if err := p.APIUnavailable(); err == nil || !strings.Contains(err.Error(), CursorKeyEnv) {
		t.Errorf("APIUnavailable = %v, want it to name %s", err, CursorKeyEnv)
	}
	t.Setenv(CursorTokenEnv, "key_from_terraform")
	if mode, _ := p.Mode(); mode != API {
		t.Errorf("the Terraform provider's %s should work too, but mode = %s", CursorTokenEnv, mode)
	}
}

func TestCursorAPIErrorsAndRetries(t *testing.T) {
	// aiserver.v1.ErrorDetails{error: 5, details: {title, detail}}, the way
	// Cursor puts the real message in a Connect error.
	inner := append(append([]byte{0x0a, 11}, "Not allowed"...), append([]byte{0x12, 24}, "Ask a team admin for it."...)...)
	details := append([]byte{0x08, 5, 0x12, byte(len(inner))}, inner...)
	value := base64.RawStdEncoding.EncodeToString(details)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("a token that isn't an API key should be used as is, got %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetAutomation") && calls == 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case strings.HasSuffix(r.URL.Path, "/GetAutomation") && calls == 2:
			w.Write([]byte(`{"restrictedSummary":{"automationId":"a1","name":"someone's","ownerName":"Ada"}}`))
		case strings.HasSuffix(r.URL.Path, "/CreateAutomation"):
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"permission_denied","message":"Error","details":[{"type":"aiserver.v1.ErrorDetails","value":"` + value + `"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`<html>no such page</html>`))
		}
	}))
	defer srv.Close()
	t.Setenv(CursorKeyEnv, "session-token")
	t.Setenv(CursorEndpointEnv, srv.URL)
	client, err := newCursorAPI()
	if err != nil {
		t.Fatal(err)
	}
	api := client.(*cursorAPI)
	ctx := context.Background()

	_, err = api.Get(ctx, "a1")
	if calls != 2 || err == nil || !strings.Contains(err.Error(), "owned by Ada") {
		t.Errorf("a rate-limited call should be retried, and a restricted automation is an error; calls=%d, err=%v", calls, err)
	}
	_, err = api.Create(ctx, cursorResource())
	if err == nil || err.Error() != "Cursor's CreateAutomation failed: permission_denied: Not allowed: Ask a team admin for it." {
		t.Errorf("Create error = %v", err)
	}
	// A plain 404 means a wrong address, not a deleted automation:
	// treating it as "gone" would make Fabrica forget what it made.
	got, err := api.Get(ctx, "a1")
	if got != nil || err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("Get on a bare 404 = %+v, %v; want an error", got, err)
	}
}

func TestCursorSetupListsTheMCPServers(t *testing.T) {
	t.Setenv(CursorKeyEnv, "key_x")
	client, _ := newCursorAPI()
	other := cursorResource()
	other.Connectors = nil
	text := client.(ManualSetup).Setup("pr-review", []Resource{cursorResource(), other})
	for _, want := range []string{
		"- **fabrica-pr-review**: HTTP, URL `https://fabrica.acme.dev/pr-review`, header `Authorization: Bearer fab_tok`",
		"- **linear**: HTTP, URL `https://mcp.linear.app/mcp`, signing in with OAuth when Cursor asks",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("setup is missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "fabrica-pr-review") != 1 {
		t.Errorf("each server should be listed once:\n%s", text)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
