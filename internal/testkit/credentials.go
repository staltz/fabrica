package testkit

import (
	"os"
	"testing"
)

// IsolateCredentials keeps a whole test binary away from your real
// credentials: it turns the system keychain off and points Fabrica's
// config folder, where it saves credentials without a keychain, at an
// empty temporary one. Call it from TestMain, and the cleanup it returns
// once the tests are done.
func IsolateCredentials() (cleanup func()) {
	dir, err := os.MkdirTemp("", "fabrica-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("FABRICA_KEYCHAIN", "off")
	os.Setenv("XDG_CONFIG_HOME", dir)
	return func() { os.RemoveAll(dir) }
}

// FreshCredentials gives one test its own empty credentials store, so
// what one test saves never shows up in another.
func FreshCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("FABRICA_KEYCHAIN", "off")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
