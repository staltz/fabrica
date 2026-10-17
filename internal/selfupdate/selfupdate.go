// Package selfupdate replaces the running fabrica binary with the one from
// the latest GitHub release, the same files install.sh downloads.
//
// It finds the latest release's tag, downloads the static binary for this
// OS and CPU and the release's checksums, checks one against the other, and
// swaps the new binary in with a rename, so the old one keeps working until
// the new one is fully in place.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// RepoURL is where Fabrica's releases are published.
const RepoURL = "https://github.com/staltz/fabrica"

// Updater installs the latest release over a binary.
type Updater struct {
	// RepoURL is the GitHub repo whose latest release to install.
	RepoURL string
	// Current is the version that's running now, like 0.1.0 or dev. A
	// leading v is fine too.
	Current string
	// Exe is the binary to replace. Symlinks are followed, so the file
	// they point at gets replaced and the symlink stays.
	Exe string
	// OS and Arch pick the release asset, like linux and amd64.
	OS, Arch string
	// Client makes the HTTP requests. Default: one with a generous timeout.
	Client *http.Client
	// Progress, when set, is told what's happening as it happens.
	Progress io.Writer
}

// Result says what Update did.
type Result struct {
	// From and To are the old and new versions, without a leading v, the
	// way `fabrica version` prints them. They're equal when there was
	// nothing to update.
	From, To string
	// Path is the file that was (or would have been) replaced.
	Path string
	// Updated is false when Current was already the latest release.
	Updated bool
}

// Update installs the latest release, unless it's already running.
func (u *Updater) Update(ctx context.Context) (Result, error) {
	res := Result{From: strings.TrimPrefix(u.Current, "v")}
	exe, err := filepath.EvalSymlinks(u.Exe)
	if err != nil {
		return res, fmt.Errorf("couldn't find the fabrica binary: %w", err)
	}
	res.Path = exe
	if app, ok := insideApp(exe); ok {
		return res, fmt.Errorf("this fabrica came with %s, which keeps it up to date: update the app instead, with Check for Updates… in its menu", app)
	}

	tag, err := u.latestTag(ctx)
	if err != nil {
		return res, err
	}
	res.To = strings.TrimPrefix(tag, "v")
	if res.To == res.From {
		return res, nil
	}

	asset := fmt.Sprintf("fabrica-%s-%s", u.OS, u.Arch)
	base := strings.TrimSuffix(u.RepoURL, "/") + "/releases/download/" + tag
	sums, err := u.get(ctx, base+"/checksums.txt", 1<<20)
	if err != nil {
		return res, fmt.Errorf("couldn't download the checksums: %w", err)
	}
	want, ok := checksumFor(sums, asset)
	if !ok {
		return res, fmt.Errorf("release %s has no build for %s/%s; build from source with: go install github.com/staltz/fabrica/cmd/fabrica@latest", tag, u.OS, u.Arch)
	}

	u.say("Downloading fabrica %s for %s/%s...", res.To, u.OS, u.Arch)
	if err := u.replace(ctx, exe, base+"/"+asset, want); err != nil {
		return res, err
	}
	res.Updated = true
	return res, nil
}

// insideApp reports whether exe is inside a macOS app bundle, like the
// fabrica that comes with Fabrica.app. Replacing it would break the app's
// code signature, and the app would put its own back anyway.
func insideApp(exe string) (string, bool) {
	i := strings.Index(exe, ".app/Contents/")
	if i < 0 {
		return "", false
	}
	return exe[:i+len(".app")], true
}

// latestTag asks GitHub which tag the latest release has. GitHub answers
// /releases/latest with a redirect to /releases/tag/<tag>, which, unlike
// its API, has no rate limit for anonymous use.
func (u *Updater) latestTag(ctx context.Context) (string, error) {
	url := strings.TrimSuffix(u.RepoURL, "/") + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := *u.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't find the latest release: %w", err)
	}
	resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("couldn't find the latest release at %s (%s)", url, resp.Status)
	}
	dir, tag := path.Split(loc.Path)
	if !strings.HasSuffix(dir, "/releases/tag/") || tag == "" {
		return "", fmt.Errorf("there's no release at %s yet", strings.TrimSuffix(u.RepoURL, "/")+"/releases")
	}
	return tag, nil
}

// replace downloads url next to exe, checks its SHA-256 and renames it over
// exe. On any problem, exe is left as it was.
func (u *Updater) replace(ctx context.Context, exe, url, wantSum string) (err error) {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".fabrica-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("can't write to %s, where fabrica is installed; run the update as a user who can, like with sudo", dir)
		}
		return fmt.Errorf("couldn't write the new binary next to %s: %w", exe, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	body, err := u.open(ctx, url)
	if err != nil {
		return fmt.Errorf("couldn't download %s: %w", url, err)
	}
	defer body.Close()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), body); err != nil {
		return fmt.Errorf("couldn't download %s: %w", url, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSum {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s); nothing was changed", path.Base(url), wantSum, got)
	}

	// Keep the old binary's permissions. It's running, so it's executable.
	mode := fs.FileMode(0o755)
	if info, err := os.Stat(exe); err == nil {
		mode = info.Mode().Perm()
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return fmt.Errorf("couldn't replace %s: %w", exe, err)
	}
	return nil
}

// checksumFor finds a file's SHA-256 in sha256sum's output format.
func checksumFor(sums []byte, file string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// sha256sum marks binary-mode files with a leading *.
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	body, err := u.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, limit))
}

func (u *Updater) open(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return resp.Body, nil
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (u *Updater) say(format string, args ...any) {
	if u.Progress != nil {
		fmt.Fprintf(u.Progress, format+"\n", args...)
	}
}
