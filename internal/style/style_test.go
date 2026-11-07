package style

import (
	"bytes"
	"testing"

	"github.com/staltz/fabrica/internal/diag"
)

func TestOffLeavesTextAlone(t *testing.T) {
	t.Setenv("FORCE_COLOR", "")
	st := For(&bytes.Buffer{})
	if st.On() {
		t.Fatal("a buffer isn't a terminal, so there should be no color")
	}
	d := diag.Diagnostic{Severity: diag.Error, Where: "f.yaml", Message: "bad", Hint: "fix it"}
	if got := st.Diag(d); got != d.String() {
		t.Errorf("Diag = %q, want %q", got, d.String())
	}
	if got := st.Mark("+ create x"); got != "+ create x" {
		t.Errorf("Mark = %q", got)
	}
}

func TestForceColorAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	if !For(&bytes.Buffer{}).On() {
		t.Error("FORCE_COLOR should turn color on")
	}
	t.Setenv("FORCE_COLOR", "0")
	if For(&bytes.Buffer{}).On() {
		t.Error("FORCE_COLOR=0 shouldn't")
	}
}

func TestMark(t *testing.T) {
	st := Style{on: true}
	for line, want := range map[string]string{
		"+ create pr-review @ x": "\x1b[32m+ create\x1b[0m pr-review @ x",
		"~ update MCP server":    "\x1b[33m~ update\x1b[0m MCP server",
		"- delete it":            "\x1b[31m- delete\x1b[0m it",
		"? unknown":              "? unknown",
		"plain":                  "plain",
	} {
		if got := st.Mark(line); got != want {
			t.Errorf("Mark(%q) = %q, want %q", line, got, want)
		}
	}
}
