package mcpserver

import (
	"strings"
	"testing"
)

// A session the local heartbeat starts for a pace routine passes the same
// stub checks as a hook's.
func TestLocalPaceRun(t *testing.T) {
	lw := newLocalWorld(t)
	lw.w.UpdateBlueprint(map[string]string{"routines/tidy.md": "---\ntrigger: { on: pace, every: 2 }\n---\nTidy up.\n"})
	lw.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\nroutines: [tidy]\npace: 15m\n",
	})
	c, _ := lw.connect(t)
	fp := localStub(t, lw.opts, "tidy")
	c.runID(c.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "tidy on the heartbeat", "routine": "tidy", "runtime": "claude-code-local", "stub": fp}))
	if text := c.ok("routine_get", map[string]any{"name": "tidy"}); !strings.Contains(text, "Tidy up.") || !strings.Contains(text, "every 30m at the factory's pace of 15m") {
		t.Errorf("routine_get:\n%s", text)
	}

	// A new pace changes nothing for the stub: it isn't baked in.
	lw.w.Home.Write(map[string]string{
		"factories/my-default.yaml": "blueprint: { git: github.com/acme/blueprint, ref: main }\nruntimes: [claude-code-local]\ntargets: [github.com/acme/app]\nroutines: [tidy]\npace: 1h\n",
	})
	c2, _ := lw.connect(t)
	c2.runID(c2.ok("run_begin", map[string]any{"repo": "github.com/acme/app", "intent": "tidy on the heartbeat", "routine": "tidy", "runtime": "claude-code-local", "stub": fp}))
}
