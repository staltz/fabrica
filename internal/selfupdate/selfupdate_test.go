package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const asset = "fabrica-linux-amd64"

// files is a release's files, by name.
type files map[string]string

// fakeGitHub serves releases the way GitHub does: /releases/latest
// redirects to the latest release's page, and each release's files are
// under /releases/download/<version>/.
func fakeGitHub(t *testing.T, latest string, releases map[string]files) *Source {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/staltz/fabrica/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if latest == "" { // no releases yet
			http.Redirect(w, r, srv.URL+"/staltz/fabrica/releases", http.StatusFound)
			return
		}
		http.Redirect(w, r, srv.URL+"/staltz/fabrica/releases/tag/"+latest, http.StatusFound)
	})
	mux.HandleFunc("/staltz/fabrica/releases/download/{version}/{file}", func(w http.ResponseWriter, r *http.Request) {
		content, ok := releases[r.PathValue("version")][r.PathValue("file")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, content)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Source{Client: srv.Client(), GitHub: srv.URL, OS: "linux", Arch: "amd64", UserAgent: "test"}
}

// release adds checksums.txt to a release's files, like `make dist` does.
func release(fs files) files {
	var sums strings.Builder
	for name, content := range fs {
		sums.WriteString(sha256Hex(content) + "  " + name + "\n")
	}
	fs["checksums.txt"] = sums.String()
	return fs
}

// binary is a stand-in fabrica: a script that prints its version.
func binary(version string) string {
	return "#!/bin/sh\necho 'fabrica " + version + "'\n"
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// installed writes a fabrica binary to a folder of its own.
func installed(t *testing.T, content string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binaries are shell scripts")
	}
	exe := filepath.Join(t.TempDir(), "fabrica")
	if err := os.WriteFile(exe, []byte(content), 0o750); err != nil {
		t.Fatal(err)
	}
	return exe
}

// unchanged fails unless exe is still the only file in its folder, with
// the same content.
func unchanged(t *testing.T, exe, content string) {
	t.Helper()
	if data, _ := os.ReadFile(exe); string(data) != content {
		t.Errorf("%s changed to %q", exe, data)
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Errorf("leftover files next to fabrica: %v", entries)
	}
}

func TestUpdatesToTheLatestRelease(t *testing.T) {
	ctx := context.Background()
	src := fakeGitHub(t, "v0.2.0", map[string]files{
		"v0.1.0": release(files{asset: binary("v0.1.0")}),
		"v0.2.0": release(files{asset: binary("v0.2.0"), "fabrica-darwin-arm64": binary("v0.2.0 (mac)")}),
	})
	exe := installed(t, binary("v0.1.0"))

	u, err := src.Check(ctx, exe, "v0.1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != Available || u.Version != "v0.2.0" || u.Notes != src.GitHub+"/staltz/fabrica/releases/tag/v0.2.0" {
		t.Fatalf("update = %+v", u)
	}
	got, err := src.Install(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.2.0" {
		t.Errorf("new version = %q", got)
	}
	unchanged(t, exe, binary("v0.2.0"))
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v, want the old binary's 0750", info.Mode().Perm())
	}

	// Now it's up to date, by version and by content.
	if u, err := src.Check(ctx, exe, "v0.2.0", ""); err != nil || u.Status != UpToDate {
		t.Errorf("same version: %+v, %v", u, err)
	}
	if u, err := src.Check(ctx, exe, "dev", ""); err != nil || u.Status != UpToDate {
		t.Errorf("same bytes: %+v, %v", u, err)
	}

	// Asking for a version installs it, even an older one.
	u, err = src.Check(ctx, exe, "v0.2.0", "0.1.0")
	if err != nil || u.Status != Available || u.Version != "v0.1.0" {
		t.Fatalf("--version 0.1.0: %+v, %v", u, err)
	}
	if got, err := src.Install(ctx, u); err != nil || got != "v0.1.0" {
		t.Errorf("downgrade: %q, %v", got, err)
	}
}

func TestNeverDowngradesOnItsOwn(t *testing.T) {
	src := fakeGitHub(t, "v0.2.0", map[string]files{"v0.2.0": release(files{asset: binary("v0.2.0")})})
	exe := installed(t, "old")
	for current, want := range map[string]Status{
		"v0.3.0":                               Ahead,
		"v0.2.0-4-g1a2b3c4":                    Ahead, // `make build`, four commits after v0.2.0
		"v0.2.0-4-g1a2b3c4-dirty":              Ahead,
		"v0.2.0-dirty":                         Ahead,
		"v0.2.1-0.20261010120000-1a2b3c4d5e6f": Ahead, // `go build`, after v0.2.0
		"v0.1.0-4-g1a2b3c4":                    Available,
		"v0.2.0-rc.1":                          Available,
		"v0.2.0+dirty":                         Available,
		"dev":                                  Available,
		"1a2b3c4":                              Available, // git describe without any tags
	} {
		u, err := src.Check(context.Background(), exe, current, "")
		if err != nil || u.Status != want {
			t.Errorf("%s: status %v, want %v (%v)", current, u.Status, want, err)
		}
	}
}

func TestRefusesBadDownloads(t *testing.T) {
	for name, tc := range map[string]struct {
		content, sum, wantErr string
	}{
		"checksum mismatch":  {binary("v0.2.0"), sha256Hex("something else"), "checksum mismatch for "},
		"doesn't run":        {"#!/bin/sh\nexit 3\n", "", "the new fabrica doesn't run on this machine (exit status 3)"},
		"isn't fabrica":      {"#!/bin/sh\necho hello\n", "", "printed \"hello\""},
		"no binary for this": {"", "", "there's no release binary for linux/amd64"},
	} {
		t.Run(name, func(t *testing.T) {
			sum := tc.sum
			if sum == "" {
				sum = sha256Hex(tc.content)
			}
			rel := files{"checksums.txt": sum + "  fabrica-darwin-arm64\n"}
			if tc.content != "" {
				rel = files{asset: tc.content, "checksums.txt": sum + "  *" + asset + "\n"}
			}
			src := fakeGitHub(t, "v0.2.0", map[string]files{"v0.2.0": rel})
			exe := installed(t, "old")
			u, err := src.Check(context.Background(), exe, "v0.1.0", "")
			if err == nil {
				_, err = src.Install(context.Background(), u)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want %q", err, tc.wantErr)
			}
			unchanged(t, exe, "old")
		})
	}
}

func TestReleaseErrors(t *testing.T) {
	ctx := context.Background()
	src := fakeGitHub(t, "v0.2.0", map[string]files{"v0.2.0": release(files{asset: binary("v0.2.0")})})
	exe := installed(t, "old")
	for want, wantErr := range map[string]string{
		"v9.9.9": "there's no release v9.9.9",
		"banana": `"banana" isn't a version`,
	} {
		if _, err := src.Check(ctx, exe, "v0.1.0", want); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("--version %s: %v", want, err)
		}
	}

	src = fakeGitHub(t, "", nil)
	if _, err := src.Check(ctx, exe, "v0.1.0", ""); err == nil || !strings.Contains(err.Error(), "couldn't find the latest release") {
		t.Errorf("no releases: %v", err)
	}
}

func TestUpdatesFromAMirror(t *testing.T) {
	ctx := context.Background()
	rel := release(files{asset: binary("v0.2.0")})
	srv := httptest.NewServer(http.StripPrefix("/fabrica/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if content, ok := rel[r.URL.Path]; ok {
			io.WriteString(w, content)
			return
		}
		http.NotFound(w, r)
	})))
	t.Cleanup(srv.Close)
	src := &Source{Client: srv.Client(), GitHub: "http://github.invalid", Mirror: srv.URL + "/fabrica/", OS: "linux", Arch: "amd64"}
	exe := installed(t, "old")

	if _, err := src.Check(ctx, exe, "v0.1.0", "v0.2.0"); err == nil || !strings.Contains(err.Error(), "FABRICA_DOWNLOAD_URL is set") {
		t.Errorf("--version with a mirror: %v", err)
	}
	u, err := src.Check(ctx, exe, "v0.1.0", "")
	if err != nil || u.Status != Available || u.Version != "" || u.From != srv.URL+"/fabrica" || u.Notes != "" {
		t.Fatalf("update = %+v, %v", u, err)
	}
	if got, err := src.Install(ctx, u); err != nil || got != "v0.2.0" {
		t.Fatalf("install: %q, %v", got, err)
	}
	if u, err := src.Check(ctx, exe, "v0.2.0", ""); err != nil || u.Status != UpToDate {
		t.Errorf("after: %+v, %v", u, err)
	}
}

func TestExplainsAFolderItCantWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	src := fakeGitHub(t, "v0.2.0", map[string]files{"v0.2.0": release(files{asset: binary("v0.2.0")})})
	exe := installed(t, "old")
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	u, err := src.Check(context.Background(), exe, "v0.1.0", "")
	if err == nil {
		_, err = src.Install(context.Background(), u)
	}
	if err == nil || !strings.Contains(err.Error(), "you can't write to "+dir) {
		t.Errorf("error = %v", err)
	}
	unchanged(t, exe, "old")
}
