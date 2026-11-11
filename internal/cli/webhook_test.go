package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/token"
)

func TestWebhookCommand(t *testing.T) {
	w := setupCLI(t)
	w.UpdateBlueprint(map[string]string{"routines/tidy.md": "---\ntrigger: { on: pace, every: 4 }\n---\nTidy up.\n"})
	w.Home.Write(map[string]string{
		"factories/paced.yaml": "blueprint: { git: github.com/acme/blueprint, ref: stable }\nserver: https://fabrica.acme.dev\nruntimes: [claude-code-cloud]\nroutines: [tidy]\ntargets: [github.com/acme/app]\npace: 30m\n",
	}).CommitPush("deploy a paced routine")
	dir, handoffs := w.Home.Dir, t.TempDir()
	mustFab(t, 0, dir, "apply", "--handoff-dir", handoffs)
	files, _ := filepath.Glob(filepath.Join(handoffs, "paced", "*.md"))
	if len(files) != 1 {
		t.Fatalf("want 1 handoff, got %v", files)
	}
	text, _ := os.ReadFile(files[0])
	mustFab(t, 0, dir, "confirm", regexp.MustCompile("fabrica confirm (h_[0-9a-f]+)").FindStringSubmatch(string(text))[1], "--remote-id", "trig_1")

	// The handoff ends with the exact command, and plan asks for it too.
	cmd := regexp.MustCompile("fabrica webhook paced '([^']+)' --runtime claude-code-cloud").FindStringSubmatch(string(text))
	if cmd == nil {
		t.Fatalf("the handoff has no webhook command:\n%s", text)
	}
	id := cmd[1]
	if out, _ := fab(t, dir, "plan"); !strings.Contains(out, "fabrica webhook paced '"+id+"'") {
		t.Errorf("plan should ask for the webhook:\n%s", out)
	}

	// The token comes from stdin, never from a flag.
	webhook := func(stdin string, args ...string) (string, int) {
		t.Helper()
		t.Chdir(dir)
		var out bytes.Buffer
		code := Main(context.Background(), append([]string{"webhook"}, args...), strings.NewReader(stdin), &out, &out)
		return out.String(), code
	}
	if out, code := webhook("sk-ant-oat01-x\n", "paced", id, "--runtime", "claude-code-cloud"); code == 0 || !strings.Contains(out, "--url is missing") {
		t.Errorf("without --url: exit %d\n%s", code, out)
	}
	if out, code := webhook("sk-ant-oat01-x\n", "nope", id, "--runtime", "claude-code-cloud", "--url", "https://x.dev/fire"); code == 0 || !strings.Contains(out, "there's no factory nope") {
		t.Errorf("an unknown factory: exit %d\n%s", code, out)
	}
	out, code := webhook("sk-ant-oat01-x\n", "paced", id, "--runtime", "claude-code-cloud", "--url", "https://api.anthropic.com/v1/claude_code/routines/trig_1/fire")
	if code != 0 || !strings.Contains(out, "Recorded the webhook of tidy @ github.com/acme/app on claude-code-cloud") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	w.Home.Pull()
	s, err := home.ReadState(dir, "paced")
	if err != nil {
		t.Fatal(err)
	}
	hook := s.Resources[0].Webhook
	if hook == nil || strings.Contains(hook.Token, "sk-ant") {
		t.Fatalf("state records %+v", hook)
	}
	if tok, err := token.Open(os.Getenv(token.EnvVar), "paced", id, hook.Token); err != nil || tok != "sk-ant-oat01-x" {
		t.Errorf("the token opens to %q, %v", tok, err)
	}
	if out, _ := fab(t, dir, "plan"); strings.Contains(out, "fabrica webhook") {
		t.Errorf("with its webhook, plan stops asking:\n%s", out)
	}
}
