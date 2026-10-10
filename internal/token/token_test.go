package token

import (
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	secret := strings.Repeat("s", 32)
	tok := For(secret, "pr-review")
	if !strings.HasPrefix(tok, "fab_") || tok != For(secret, "pr-review") {
		t.Fatalf("token %q should be stable and prefixed", tok)
	}
	if !Valid(secret, "pr-review", tok) {
		t.Error("the right token should be valid")
	}
	if Valid(secret, "other", tok) || Valid(strings.Repeat("x", 32), "pr-review", tok) || Valid(secret, "pr-review", tok+"x") {
		t.Error("a token must only work for its own factory and secret")
	}
	if len(Hash(tok)) != 12 || Hash(tok) == Hash(For(secret, "other")) {
		t.Error("Hash should be short and differ between tokens")
	}
}

func TestSecretFromEnv(t *testing.T) {
	t.Setenv(EnvVar, "")
	if _, err := SecretFromEnv(); err == nil {
		t.Error("a missing secret should be an error")
	}
	t.Setenv(EnvVar, "short")
	if _, err := SecretFromEnv(); err == nil {
		t.Error("a short secret should be an error")
	}
	t.Setenv(EnvVar, strings.Repeat("a", 40))
	if s, err := SecretFromEnv(); err != nil || len(s) != 40 {
		t.Errorf("SecretFromEnv = %q, %v", s, err)
	}
}
