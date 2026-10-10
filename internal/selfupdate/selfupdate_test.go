package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves releases the way github.com does: /releases/latest
// redirects to the latest tag, and each release's files are under
// /releases/download/<tag>/.
type fakeGitHub struct {
	*httptest.Server
	latest string
	files  map[string]string // "<tag>/<file>" to content

	mu   sync.Mutex
	hits []string
}

func newFakeGitHub(t *testing.T, latest string, files map[string]string) *fakeGitHub {
	t.Helper()
	gh := &fakeGitHub{latest: latest, files: files}
	gh.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gh.mu.Lock()
		gh.hits = append(gh.hits, r.URL.Path)
		gh.mu.Unlock()
		switch {
		case r.URL.Path == "/staltz/fabrica/releases/latest" && gh.latest == "":
			http.Redirect(w, r, "/staltz/fabrica/releases", http.StatusFound)
		case r.URL.Path == "/staltz/fabrica/releases/latest":
			http.Redirect(w, r, "/staltz/fabrica/releases/tag/"+gh.latest, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/staltz/fabrica/releases/download/"):
			content, ok := gh.files[strings.TrimPrefix(r.URL.Path, "/staltz/fabrica/releases/download/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, content)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	return gh
}

func (gh *fakeGitHub) downloaded(file string) bool {
	gh.mu.Lock()
	defer gh.mu.Unlock()
	for _, p := range gh.hits {
		if strings.HasSuffix(p, "/"+file) {
			return true
		}
	}
	return false
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// release makes the files of one release, with a correct checksums.txt.
func release(tag string, binaries map[string]string) map[string]string {
	files := map[string]string{}
	var sums strings.Builder
	for name, content := range binaries {
		files[tag+"/"+name] = content
		fmt.Fprintf(&sums, "%s  %s\n", sum(content), name)
	}
	files[tag+"/checksums.txt"] = sums.String()
	return files
}

// installed puts an old binary in a temp folder, the way install.sh does.
func installed(t *testing.T) string {
	t.Helper()
	// Resolve symlinks in the temp folder itself (like macOS's /var), so
	// paths compare equal to what Update reports.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fabrica")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func updater(gh *fakeGitHub, exe, current string) *Updater {
	return &Updater{
		RepoURL: gh.URL + "/staltz/fabrica",
		Current: current,
		Exe:     exe,
		OS:      "linux",
		Arch:    "arm64",
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// leftovers lists anything in dir besides the binary itself.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != "fabrica" {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestUpdateInstallsTheLatestRelease(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0", release("v0.2.0", map[string]string{
		"fabrica-linux-amd64": "new amd64 binary",
		"fabrica-linux-arm64": "new arm64 binary",
	}))
	exe := installed(t)
	if err := os.Chmod(exe, 0o700); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	u := updater(gh, exe, "v0.1.0")
	u.Progress = &progress

	res, err := u.Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Result{From: "v0.1.0", To: "v0.2.0", Path: exe, Updated: true}
	if res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	if got := readFile(t, exe); got != "new arm64 binary" {
		t.Errorf("binary = %q, want the arm64 build", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want the old binary's 0700", info.Mode().Perm())
	}
	if got := progress.String(); got != "Downloading fabrica-linux-arm64 (v0.2.0)...\n" {
		t.Errorf("progress = %q", got)
	}
	if extra := leftovers(t, filepath.Dir(exe)); len(extra) > 0 {
		t.Errorf("left files behind: %v", extra)
	}
}

func TestUpdateDoesNothingWhenAlreadyLatest(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0", release("v0.2.0", map[string]string{"fabrica-linux-arm64": "new binary"}))
	exe := installed(t)

	res, err := updater(gh, exe, "v0.2.0").Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated || res.To != "v0.2.0" {
		t.Errorf("result = %+v, want nothing updated", res)
	}
	if readFile(t, exe) != "old binary" {
		t.Error("the binary was replaced")
	}
	if gh.downloaded("checksums.txt") || gh.downloaded("fabrica-linux-arm64") {
		t.Error("downloaded release files it didn't need")
	}
}

func TestUpdateReplacesWhatASymlinkPointsAt(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0", release("v0.2.0", map[string]string{"fabrica-linux-arm64": "new binary"}))
	exe := installed(t)
	link := filepath.Join(t.TempDir(), "fabrica")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}

	res, err := updater(gh, link, "v0.1.0").Update(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != exe {
		t.Errorf("path = %s, want the symlink's target %s", res.Path, exe)
	}
	if target, err := os.Readlink(link); err != nil || target != exe {
		t.Errorf("the symlink changed: %q, %v", target, err)
	}
	if got := readFile(t, link); got != "new binary" {
		t.Errorf("binary = %q", got)
	}
}

func TestUpdateRefusesABadChecksum(t *testing.T) {
	files := release("v0.2.0", map[string]string{"fabrica-linux-arm64": "new binary"})
	files["v0.2.0/fabrica-linux-arm64"] = "tampered binary"
	gh := newFakeGitHub(t, "v0.2.0", files)
	exe := installed(t)

	_, err := updater(gh, exe, "v0.1.0").Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for fabrica-linux-arm64") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if readFile(t, exe) != "old binary" {
		t.Error("the binary was replaced")
	}
	if extra := leftovers(t, filepath.Dir(exe)); len(extra) > 0 {
		t.Errorf("left files behind: %v", extra)
	}
}

func TestUpdateErrors(t *testing.T) {
	tests := []struct {
		name   string
		latest string
		files  map[string]string
		want   string
	}{
		{
			name:   "no build for this platform",
			latest: "v0.2.0",
			files:  release("v0.2.0", map[string]string{"fabrica-darwin-arm64": "mac binary"}),
			want:   "release v0.2.0 has no build for linux/arm64; build from source with: go install",
		},
		{
			name:   "no checksums",
			latest: "v0.2.0",
			files:  map[string]string{"v0.2.0/fabrica-linux-arm64": "new binary"},
			want:   "couldn't download the checksums: 404 Not Found",
		},
		{
			name:   "checksum but no binary",
			latest: "v0.2.0",
			files: map[string]string{
				"v0.2.0/checksums.txt": sum("new binary") + "  fabrica-linux-arm64\n",
			},
			want: "fabrica-linux-arm64: 404 Not Found",
		},
		{
			name:   "no releases yet",
			latest: "",
			want:   "there's no release at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(t, tt.latest, tt.files)
			exe := installed(t)
			_, err := updater(gh, exe, "v0.1.0").Update(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if readFile(t, exe) != "old binary" {
				t.Error("the binary was replaced")
			}
			if extra := leftovers(t, filepath.Dir(exe)); len(extra) > 0 {
				t.Errorf("left files behind: %v", extra)
			}
		})
	}
}

func TestUpdateWithoutTheRepo(t *testing.T) {
	gh := newFakeGitHub(t, "v0.2.0", nil)
	u := updater(gh, installed(t), "v0.1.0")
	u.RepoURL = gh.URL + "/someone/else"
	_, err := u.Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "couldn't find the latest release at") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("AAAA  fabrica-linux-amd64\nbbbb *fabrica-linux-arm64\n\nnot a line\n")
	for file, want := range map[string]string{"fabrica-linux-amd64": "aaaa", "fabrica-linux-arm64": "bbbb"} {
		if got, ok := checksumFor(sums, file); !ok || got != want {
			t.Errorf("checksumFor(%s) = %q, %v; want %q", file, got, ok, want)
		}
	}
	if _, ok := checksumFor(sums, "fabrica-darwin-arm64"); ok {
		t.Error("found a checksum for a file that isn't listed")
	}
}
