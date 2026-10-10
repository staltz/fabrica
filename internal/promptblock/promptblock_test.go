package promptblock

import "testing"

func TestWrap(t *testing.T) {
	got := Wrap("  Do the thing.\nThen the other.\n")
	want := "-----BEGIN PROMPT-----\nDo the thing.\nThen the other.\n-----END PROMPT-----"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
