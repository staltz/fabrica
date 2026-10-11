package diag

import (
	"errors"
	"strings"
	"testing"
)

func TestListBasics(t *testing.T) {
	var l List
	l.Warnf("w.code", "b", "a warning")
	l.Errorf("e.code", "a", "an error").Hintf("fix it like %s", "this")
	if !l.HasErrors() || !l.HasCode("e.code") || l.HasCode("nope") {
		t.Fatalf("unexpected list state: %v", l)
	}
	sorted := l.Sorted()
	if sorted[0].Severity != Error {
		t.Errorf("errors should sort first: %v", sorted)
	}
	if !strings.Contains(sorted[0].String(), "hint: fix it like this") {
		t.Errorf("hint missing from %q", sorted[0].String())
	}
	err := l.Err()
	if err == nil || err.Error() != "an error" {
		t.Errorf("Err() = %v", err)
	}
	back := AsList(err, "x", "")
	if len(back) != 1 || back[0].Code != "e.code" {
		t.Errorf("AsList round trip = %v", back)
	}
	if other := AsList(errors.New("boom"), "x.boom", "here"); other[0].Code != "x.boom" || other[0].Where != "here" {
		t.Errorf("AsList(plain error) = %v", other)
	}
	if p := l.Prefixed("factory pr-review"); p[0].Where != "factory pr-review › b" {
		t.Errorf("Prefixed = %q", p[0].Where)
	}
}
