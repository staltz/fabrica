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

func TestAdminToken(t *testing.T) {
	secret := strings.Repeat("s", 32)
	admin := Admin(secret)
	if !strings.HasPrefix(admin, "fab_admin_") || admin != Admin(secret) {
		t.Fatalf("the admin token %q should be stable and prefixed", admin)
	}
	if !ValidAdmin(secret, admin) {
		t.Error("the right admin token should be valid")
	}
	if ValidAdmin(strings.Repeat("x", 32), admin) || ValidAdmin(secret, admin+"x") || ValidAdmin(secret, "") || ValidAdmin(secret, strings.TrimPrefix(admin, "fab_admin_")) {
		t.Error("an admin token must only work for its own secret")
	}
	if Admin(secret) == Admin(strings.Repeat("x", 32)) {
		t.Error("each secret should have its own admin token")
	}

	if AdminEnvVar != "FABRICA_ADMIN_TOKEN" {
		t.Errorf("AdminEnvVar = %q", AdminEnvVar)
	}

	// It's HMAC(secret, "_admin") with its own prefix.
	if want := "fab_admin_" + strings.TrimPrefix(For(secret, "_admin"), "fab_"); admin != want {
		t.Errorf("admin token = %q, want %q", admin, want)
	}

	// No factory token is the admin token, and no factory token passes as it
	// (the other way around too), whatever the factory is called.
	for _, factory := range []string{"admin", "_admin", "pr-review", "acme", "a", "0", "fab-admin"} {
		if tok := For(secret, factory); tok == admin || ValidAdmin(secret, tok) || Valid(secret, factory, admin) {
			t.Errorf("factory %q and the admin token must never be interchangeable", factory)
		}
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
