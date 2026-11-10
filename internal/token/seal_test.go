package token

import (
	"strings"
	"testing"
)

func TestSeal(t *testing.T) {
	secret := strings.Repeat("s", 32)
	const id = "4ckr#tidy@github.com/acme/app"
	sealed, err := Seal(secret, "acme", id, "sk-ant-oat01-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "sealed:v1:") || strings.Contains(sealed, "sk-ant") {
		t.Fatalf("sealed = %q", sealed)
	}
	if again, _ := Seal(secret, "acme", id, "sk-ant-oat01-secret"); again == sealed {
		t.Error("sealing twice should give different values (a fresh nonce each time)")
	}
	if got, err := Open(secret, "acme", id, sealed); err != nil || got != "sk-ant-oat01-secret" {
		t.Errorf("Open = %q, %v", got, err)
	}
	for name, open := range map[string]func() (string, error){
		"another secret":  func() (string, error) { return Open(strings.Repeat("x", 32), "acme", id, sealed) },
		"another factory": func() (string, error) { return Open(secret, "other", id, sealed) },
		"another stub":    func() (string, error) { return Open(secret, "acme", "4ckr#other@github.com/acme/app", sealed) },
		"not sealed":      func() (string, error) { return Open(secret, "acme", id, "sk-ant-oat01-secret") },
		"garbled":         func() (string, error) { return Open(secret, "acme", id, sealed[:len(sealed)-4]+"AAAA") },
		"too short":       func() (string, error) { return Open(secret, "acme", id, "sealed:v1:AAAA") },
	} {
		if got, err := open(); err == nil {
			t.Errorf("%s: Open should fail, got %q", name, got)
		}
	}
}
