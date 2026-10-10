package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/selfupdate"
)

func TestUpdateFlag(t *testing.T) {
	asset := fmt.Sprintf("fabrica-%s-%s", runtime.GOOS, runtime.GOARCH)
	newBinary := "the v0.2.0 binary"
	h := sha256.Sum256([]byte(newBinary))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v0.2.0", http.StatusFound)
		case "/releases/download/v0.2.0/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(h[:]), asset)
		case "/releases/download/v0.2.0/" + asset:
			fmt.Fprint(w, newBinary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fabrica")
	if err := os.WriteFile(exe, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := version
	orig := updater
	t.Cleanup(func() { updater = orig })
	updater = func(cmd *cobra.Command) (*selfupdate.Updater, error) {
		u, err := orig(cmd)
		if err != nil {
			return nil, err
		}
		u.RepoURL, u.Exe, u.Current = srv.URL, exe, current
		return u, nil
	}

	out := mustFab(t, 0, "", "-u")
	if !strings.Contains(out, "Downloading "+asset+" (v0.2.0)...") || !strings.Contains(out, "Updated fabrica from dev to v0.2.0, in "+exe) {
		t.Errorf("fabrica -u:\n%s", out)
	}
	if b, _ := os.ReadFile(exe); string(b) != newBinary {
		t.Errorf("binary = %q, want the new one", b)
	}

	current = "v0.2.0"
	if out := mustFab(t, 0, "", "--update"); out != "fabrica v0.2.0 is already the latest version.\n" {
		t.Errorf("fabrica --update when up to date:\n%s", out)
	}

	// Without the flag, fabrica still just shows its help. (Not through fab:
	// cobra reads os.Args when args is nil.)
	var help bytes.Buffer
	if code := Main(context.Background(), []string{}, strings.NewReader(""), &help, &help); code != 0 || !strings.Contains(help.String(), "Typical workflow:") {
		t.Errorf("fabrica with no arguments: exit %d\n%s", code, help.String())
	}
	if out := mustFab(t, 1, "", "-u", "extra"); !strings.Contains(out, `unknown command "extra"`) {
		t.Errorf("fabrica -u extra:\n%s", out)
	}
}
