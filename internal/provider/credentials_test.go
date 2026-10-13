package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/testkit"
)

func TestMain(m *testing.M) {
	cleanup := testkit.IsolateCredentials()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// unsetenv unsets a variable for one test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	os.Unsetenv(name)
}

const acmeMatrix = `kind: cloud
apply_modes: [api, handoff]
find_by_identity: true
skill_transport: text
mcp_auth: {header: native}
credentials:
  ACME_API_KEY:
    description: an Acme API key
    url: https://acme.dev/settings/keys
    instructions: Click New key.
`

func TestParseMatrixCredentials(t *testing.T) {
	m, err := ParseMatrix("acme", []byte(acmeMatrix+"  ACME_ORG:\n    description: your Acme organization's ID\n    aliases: [ACME_ORGANIZATION]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Credentials) != 2 || m.Credentials[0].Env != "ACME_API_KEY" || m.Credentials[1].Env != "ACME_ORG" {
		t.Fatalf("credentials = %+v, want ACME_API_KEY then ACME_ORG", m.Credentials)
	}
	c := m.Credentials[0]
	if c.Description != "an Acme API key" || c.URL != "https://acme.dev/settings/keys" || c.Instructions != "Click New key." {
		t.Errorf("ACME_API_KEY = %+v", c)
	}
	if got := strings.Join(m.Credentials[1].Names(), ","); got != "ACME_ORG,ACME_ORGANIZATION" {
		t.Errorf("Names = %s", got)
	}

	base := "kind: cloud\napply_modes: [api, handoff]\nfind_by_identity: true\nskill_transport: text\nmcp_auth: {header: native}\n"
	for name, yaml := range map[string]string{
		"lowercase name":      base + "credentials:\n  acme_key: {description: x}\n",
		"fabrica's own":       base + "credentials:\n  FABRICA_SERVER_SECRET: {description: x}\n",
		"no description":      base + "credentials:\n  ACME_KEY: {url: https://acme.dev}\n",
		"plain http url":      base + "credentials:\n  ACME_KEY: {description: x, url: http://acme.dev}\n",
		"alias twice":         base + "credentials:\n  ACME_KEY: {description: x, aliases: [ACME_TOKEN]}\n  ACME_TOKEN: {description: y}\n",
		"unknown key":         base + "credentials:\n  ACME_KEY: {description: x, secret: true}\n",
		"no api mode":         "kind: cloud\napply_modes: [handoff]\nskill_transport: text\nmcp_auth: {header: native}\ncredentials:\n  ACME_KEY: {description: x}\n",
		"local runtime creds": "kind: local\napply_modes: [api]\nfind_by_identity: true\nskill_transport: directory\ncredentials:\n  ACME_KEY: {description: x}\n",
	} {
		if _, err := ParseMatrix("acme", []byte(yaml)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// acmeProvider writes a folder provider whose api program answers list
// with the ACME_API_KEY it was given, so tests can see what it got.
func acmeProvider(t *testing.T, modes string) *Provider {
	t.Helper()
	dir := t.TempDir()
	matrix := strings.Replace(acmeMatrix, "[api, handoff]", modes, 1)
	files := map[string]string{
		"provider.yaml": matrix,
		"handoff.md":    "Set up {{.Identity}}, then run {{.ConfirmCommand}}.\n",
		"api":           "#!/bin/sh\nprintf '{\"resources\":[{\"identity\":\"key=%s\",\"remote_id\":\"r1\"}]}' \"$ACME_API_KEY\"\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadFolder("acme", dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFolderProviderCredentials(t *testing.T) {
	testkit.FreshCredentials(t)
	unsetenv(t, "ACME_API_KEY")
	p := acmeProvider(t, "[api, handoff]")

	// Without its credential, api mode is unavailable, so it falls back.
	if mode, _ := p.Mode(); mode != Handoff {
		t.Errorf("without ACME_API_KEY, mode = %s, want handoff", mode)
	}
	if err := p.APIUnavailable(); err == nil || err.Error() != "ACME_API_KEY isn't set" {
		t.Errorf("APIUnavailable = %v", err)
	}
	// With no fallback, the error says what's missing and how to fix it.
	only := acmeProvider(t, "[api]")
	if _, err := only.Mode(); err == nil || !strings.Contains(err.Error(), "ACME_API_KEY isn't set") || !strings.Contains(err.Error(), "fabrica auth login") {
		t.Errorf("api-only Mode = %v, want it to name ACME_API_KEY and fabrica auth login", err)
	}

	// Saved with `fabrica auth login`: api mode, and the program gets it.
	if _, err := credentials.Set("ACME_API_KEY", "acme_saved"); err != nil {
		t.Fatal(err)
	}
	if mode, err := p.Mode(); mode != API || err != nil {
		t.Errorf("with ACME_API_KEY saved, mode = %s, %v; want api", mode, err)
	}
	if got := listIdentity(t, p); got != "key=acme_saved" {
		t.Errorf("the api program got %q, want the saved key", got)
	}

	// The environment wins, and the program gets it from there.
	t.Setenv("ACME_API_KEY", "acme_env")
	if got := listIdentity(t, p); got != "key=acme_env" {
		t.Errorf("the api program got %q, want the key from the environment", got)
	}
	// Folder providers' credentials can't be checked by Fabrica.
	if _, err := p.VerifyCredential(context.Background(), "ACME_API_KEY", "acme_env"); !errors.Is(err, ErrUnverifiable) {
		t.Errorf("VerifyCredential = %v, want ErrUnverifiable", err)
	}
}

func listIdentity(t *testing.T, p *Provider) string {
	t.Helper()
	api, err := p.API()
	if err != nil {
		t.Fatal(err)
	}
	got, err := api.(Lister).List(context.Background(), "f", "https://acme.dev/f")
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %v, %v", got, err)
	}
	return got[0].Identity
}

func TestCursorKeyFromTheStoreAndVerified(t *testing.T) {
	testkit.FreshCredentials(t)
	unsetenv(t, CursorKeyEnv)
	unsetenv(t, CursorTokenEnv)
	fake := testkit.NewFakeCursor(t) // sets the key in the environment; take it out again
	unsetenv(t, CursorKeyEnv)
	unsetenv(t, CursorTokenEnv)
	p, _ := Builtin("cursor-cloud")
	if mode, _ := p.Mode(); mode != Handoff {
		t.Fatalf("with no key anywhere, mode = %s", mode)
	}
	if len(p.Matrix.Credentials) != 1 || p.Matrix.Credentials[0].URL == "" {
		t.Fatalf("cursor-cloud should declare its key, with where to make one: %+v", p.Matrix.Credentials)
	}

	ctx := context.Background()
	if _, err := p.VerifyCredential(ctx, CursorKeyEnv, "key_wrong"); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("verifying a wrong key = %v, want Cursor's refusal", err)
	}
	if said, err := p.VerifyCredential(ctx, CursorKeyEnv, testkit.FakeCursorKey); err != nil || said != "Cursor accepted it" {
		t.Errorf("verifying the right key = %q, %v", said, err)
	}
	if _, err := p.VerifyCredential(ctx, CursorKeyEnv, "a-session-token"); !errors.Is(err, ErrUnverifiable) {
		t.Errorf("verifying a session token = %v, want ErrUnverifiable", err)
	}

	if _, err := credentials.Set(CursorKeyEnv, testkit.FakeCursorKey); err != nil {
		t.Fatal(err)
	}
	if mode, _ := p.Mode(); mode != API {
		t.Errorf("with the key saved, mode = %s, want api", mode)
	}
	api, err := p.API()
	if err != nil {
		t.Fatal(err)
	}
	id, err := api.Create(ctx, cursorResource())
	if err != nil || len(fake.Automations()) != 1 {
		t.Errorf("creating with the saved key: %q, %v", id, err)
	}
}
