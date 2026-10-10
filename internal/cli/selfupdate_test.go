package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSelfUpdate updates fabrica from a mirror, for real: the test binary
// stands in for the old fabrica, and a script for the new one.
func TestSelfUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the new fabrica is a shell script")
	}
	newBin := "#!/bin/sh\necho 'fabrica v9.9.9'\n"
	sum := sha256.Sum256([]byte(newBin))
	asset := "fabrica-" + runtime.GOOS + "-" + runtime.GOARCH
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			io.WriteString(w, hex.EncodeToString(sum[:])+"  "+asset+"\n")
		case "/" + asset:
			io.WriteString(w, newBin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()
	t.Setenv("FABRICA_DOWNLOAD_URL", mirror.URL)

	// --check only looks, so it's safe to run in-process, on the test binary.
	if out := mustFab(t, 2, "", "self-update", "--check"); !strings.Contains(out, "The fabrica at "+mirror.URL+" is different from yours") {
		t.Errorf("--check:\n%s", out)
	}
	if out := mustFab(t, 1, "", "self-update", "--version", "v1.0.0"); !strings.Contains(out, "--version can't pick another") {
		t.Errorf("--version with a mirror:\n%s", out)
	}

	// A real update replaces the running binary, so it runs on a copy.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fabrica")
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(bin) // macOS temp folders are behind a symlink
	cmd := exec.Command(bin, "self-update")
	cmd.Env = append(os.Environ(), "FABRICA_TEST_AS_CLI=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "✓ Updated "+resolved+" to v9.9.9.") {
		t.Fatalf("self-update: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(bin); string(got) != newBin {
		t.Errorf("the binary wasn't replaced: %.40q", got)
	}
}
