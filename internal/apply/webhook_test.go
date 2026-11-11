package apply_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

func TestPaceRoutineWebhook(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	const tidy = "---\ntrigger: { on: pace, every: 4 }\n---\nTidy up.\n"
	h.w.UpdateBlueprint(map[string]string{"routines/tidy.md": tidy})
	const factory = "blueprint: { git: github.com/acme/blueprint, ref: stable }\nserver: https://fabrica.acme.dev\nruntimes: [claude-code-cloud]\nroutines: [tidy]\ntargets: [github.com/acme/app, github.com/acme/api]\npace: 30m\n"
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": factory}).CommitPush("add a paced factory")

	// Claude routines take handoffs, which say to add an API trigger and
	// to give Fabrica its fire URL and token.
	res := h.apply()
	if len(res.Handoffs) != 2 {
		t.Fatalf("handoffs: %v\n%s", res.Handoffs, h.out.String())
	}
	tag := h.plan().Factory("pr-review").Spec.Tag()
	id := tag + "#tidy@github.com/acme/app"
	var handoff, handoffID string
	for _, file := range res.Handoffs {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "`"+id+"`") {
			handoff, handoffID = string(data), strings.TrimSuffix(filepath.Base(file), ".md")
		}
	}
	for _, want := range []string{
		"4. Trigger: **API**, and nothing else.",
		"11. Give Fabrica the routine's API trigger",
		"fabrica webhook pr-review '" + id + "' --runtime claude-code-cloud --url '<the webhook URL>'",
	} {
		if !strings.Contains(handoff, want) {
			t.Errorf("handoff is missing %q:\n%s", want, handoff)
		}
	}

	set := func(w apply.Webhook) error {
		if w.Factory == "" {
			w.Factory, w.Runtime, w.Identity, w.URL, w.Token, w.Secret = "pr-review", "claude-code-cloud", id, "https://api.anthropic.com/v1/claude_code/routines/trig_1/fire", " sk-ant-oat01-x \n", testkit.Secret
		}
		_, err := apply.SetWebhook(ctx, h.w.Home.Dir, w, now)
		return err
	}
	if err := set(apply.Webhook{}); err == nil || !strings.Contains(err.Error(), "handoff "+handoffID+" is still pending") {
		t.Errorf("before the confirm, the stub isn't there: %v", err)
	}
	if _, err := apply.Confirm(ctx, h.w.Home.Dir, handoffID, "trig_1", false, now); err != nil {
		t.Fatal(err)
	}

	// Until Fabrica has the webhook, plan says so, with the command.
	p := h.plan()
	if fp := p.Factory("pr-review"); !fp.Diags.HasCode("webhook.missing") || !strings.Contains(render(p), "fabrica webhook pr-review '"+id+"'") {
		t.Errorf("plan should ask for the webhook:\n%s", render(p))
	}

	for name, w := range map[string]apply.Webhook{
		"no URL":        {Factory: "pr-review", Runtime: "claude-code-cloud", Identity: id, URL: "<the webhook URL>", Token: "t", Secret: testkit.Secret},
		"not a URL":     {Factory: "pr-review", Runtime: "claude-code-cloud", Identity: id, URL: "trig_1", Token: "t", Secret: testkit.Secret},
		"no token":      {Factory: "pr-review", Runtime: "claude-code-cloud", Identity: id, URL: "https://x.dev/hook", Token: "  ", Secret: testkit.Secret},
		"no secret":     {Factory: "pr-review", Runtime: "claude-code-cloud", Identity: id, URL: "https://x.dev/hook", Token: "t"},
		"another stub":  {Factory: "pr-review", Runtime: "claude-code-cloud", Identity: tag + "#nope@github.com/acme/app", URL: "https://x.dev/hook", Token: "t", Secret: testkit.Secret},
		"another place": {Factory: "pr-review", Runtime: "cursor-cloud", Identity: id, URL: "https://x.dev/hook", Token: "t", Secret: testkit.Secret},
	} {
		if err := set(w); err == nil {
			t.Errorf("%s: SetWebhook should fail", name)
		}
	}
	if err := set(apply.Webhook{}); err != nil {
		t.Fatal(err)
	}
	r := h.resource(home.Key{Runtime: "claude-code-cloud", ID: id})
	if r.Webhook == nil || r.Webhook.URL != "https://api.anthropic.com/v1/claude_code/routines/trig_1/fire" || strings.Contains(r.Webhook.Token, "sk-ant") || !r.Webhook.SetAt.Equal(now) {
		t.Fatalf("state records %+v", r.Webhook)
	}
	if got, err := token.Open(testkit.Secret, "pr-review", id, r.Webhook.Token); err != nil || got != "sk-ant-oat01-x" {
		t.Errorf("the sealed token opens to %q, %v", got, err)
	}
	if fp := h.plan().Factory("pr-review"); fp.Diags.HasCode("webhook.missing") {
		t.Errorf("with the webhook, plan is quiet about it:\n%s", fp.Diags)
	}

	// A whole auth header is kept as a header name and its sealed value.
	if err := set(apply.Webhook{Factory: "pr-review", Runtime: "claude-code-cloud", Identity: id, URL: "https://api2.cursor.sh/automations/x/webhook", Token: "Authorization: Bearer crsr_x", Secret: testkit.Secret}); err != nil {
		t.Fatal(err)
	}
	if r := h.resource(home.Key{Runtime: "claude-code-cloud", ID: id}); r.Webhook.Header != "Authorization" || strings.Contains(r.Webhook.Token, "crsr_x") {
		t.Errorf("state records %+v", r.Webhook)
	} else if got, _ := token.Open(testkit.Secret, "pr-review", id, r.Webhook.Token); got != "Bearer crsr_x" {
		t.Errorf("the sealed value opens to %q", got)
	}

	// Changing the pace or every: needs no apply: the stub doesn't bake
	// them in.
	h.w.UpdateBlueprint(map[string]string{"routines/tidy.md": strings.Replace(tidy, "every: 4", "every: 7", 1)})
	h.w.Home.Write(map[string]string{"factories/pr-review.yaml": strings.Replace(factory, "pace: 30m", "pace: 25m", 1)}).CommitPush("slower")
	if p := h.plan(); len(p.Factory("pr-review").Changes) != 0 {
		t.Errorf("a new pace or every: changes no stub:\n%s", render(p))
	}

	// An update keeps the webhook, and its handoff doesn't ask for it again.
	h.w.UpdateBlueprint(map[string]string{"routines/tidy.md": strings.Replace(tidy, "---\nTidy", "connectors: [github]\n---\nTidy", 1)})
	res = h.apply()
	for _, file := range res.Handoffs {
		data, _ := os.ReadFile(file)
		if strings.Contains(string(data), "`"+id+"`") {
			if strings.Contains(string(data), "fabrica webhook") {
				t.Errorf("an update of a stub whose webhook Fabrica has doesn't ask for it:\n%s", data)
			}
			if _, err := apply.Confirm(ctx, h.w.Home.Dir, strings.TrimSuffix(filepath.Base(file), ".md"), "", false, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if r := h.resource(home.Key{Runtime: "claude-code-cloud", ID: id}); r.Webhook == nil || r.Shape.Fingerprint == "" {
		t.Errorf("after the update, state records %+v", r)
	}

	// After the server secret is rotated, the server can't open the token.
	h.env.Secret = strings.Repeat("n", 32)
	if fp := h.plan().Factory("pr-review"); !fp.Diags.HasCode("webhook.stale") {
		t.Errorf("a rotated secret makes the webhook stale:\n%s", fp.Diags)
	}
}

// resource is a stub as the factory's state records it.
func (h *harness) resource(key home.Key) home.Resource {
	h.t.Helper()
	s := h.state()
	i, ok := s.FindResource(key)
	if !ok {
		h.t.Fatalf("state has no %v", key)
	}
	return s.Resources[i]
}

func TestSplitAuthHeader(t *testing.T) {
	for pasted, want := range map[string][2]string{
		"sk-ant-oat01-abc":                    {"", "sk-ant-oat01-abc"},
		"  sk-ant-oat01-abc \n":               {"", "sk-ant-oat01-abc"},
		"Authorization: Bearer crsr_abc":      {"Authorization", "Bearer crsr_abc"},
		"X-Webhook-Secret:   s3cr3t  ":        {"X-Webhook-Secret", "s3cr3t"},
		`-H "Authorization: Bearer crsr_abc"`: {"Authorization", "Bearer crsr_abc"},
		"'Authorization: Bearer crsr_abc'":    {"Authorization", "Bearer crsr_abc"},
		"user:pass-looking-token":             {"", "user:pass-looking-token"},
		"":                                    {"", ""},
	} {
		h, v := apply.SplitAuthHeader(pasted)
		if h != want[0] || v != want[1] {
			t.Errorf("SplitAuthHeader(%q) = %q, %q; want %q, %q", pasted, h, v, want[0], want[1])
		}
	}
}
