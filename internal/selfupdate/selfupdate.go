// Package selfupdate replaces the running fabrica binary with a release.
//
// It uses the same files as install.sh: the static binary for this OS and
// CPU (fabrica-<os>-<arch>) and checksums.txt, from a GitHub release or
// from a mirror folder ($FABRICA_DOWNLOAD_URL). The new binary is
// downloaded next to the old one, checked against checksums.txt, and run
// once (`fabrica version`) to make sure it works on this machine. Only then
// is it renamed over the old one. The rename is atomic, so nothing ever
// sees a half-written binary, and anything still running the old one (like
// the MCP servers your AI tools started) keeps working until it restarts.
//
// The binary is replaced at the same path on purpose: `fabrica apply`
// writes that path into your local tools' configs.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Repo is the GitHub repo that releases come from.
const Repo = "staltz/fabrica"

// MirrorEnvVar points at a mirror of one release's files, for both
// install.sh and self-update.
const MirrorEnvVar = "FABRICA_DOWNLOAD_URL"

const (
	// maxChecksums and maxBinary cap downloads, so a broken mirror can't
	// fill your disk. Release binaries are around 20 MB.
	maxChecksums = 1 << 20
	maxBinary    = 512 << 20
	// smokeTimeout is how long the new binary gets to print its version.
	smokeTimeout = 30 * time.Second
)

// Source is where releases come from.
type Source struct {
	Client *http.Client
	// GitHub is GitHub's address, https://github.com. Tests point it at a
	// fake.
	GitHub string
	// Mirror, when set, is a folder URL with one release's files in it. It
	// replaces GitHub completely.
	Mirror string
	// OS and Arch pick the binary, in Go's names (linux, arm64).
	OS, Arch string
	// UserAgent is sent with every request.
	UserAgent string
}

// New returns a Source for the OS and CPU this binary was built for.
// mirror is $FABRICA_DOWNLOAD_URL, or empty for GitHub releases.
func New(version, mirror string) *Source {
	return &Source{
		Client:    &http.Client{Timeout: 10 * time.Minute},
		GitHub:    "https://github.com",
		Mirror:    mirror,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		UserAgent: "fabrica/" + version,
	}
}

// Status is how the running binary compares to a release.
type Status int

const (
	// Available means the release is different from what's running.
	Available Status = iota
	// UpToDate means what's running is that release.
	UpToDate
	// Ahead means what's running is newer than the latest release, like a
	// build from source.
	Ahead
)

// Update describes one possible update, as Check found it.
type Update struct {
	// Exe is the binary that would be replaced.
	Exe string
	// Current is the running version.
	Current string
	// Version is the release's version, like v0.2.0. It's empty for a
	// mirror, which doesn't say until its binary runs.
	Version string
	// Asset is the release file for this OS and CPU.
	Asset string
	// From is the folder URL the release's files are in.
	From string
	// Notes is the release's page on GitHub, when it's from GitHub.
	Notes  string
	Status Status

	sum string // the asset's SHA-256, from checksums.txt
}

// Executable returns the path of the running fabrica, with symlinks
// resolved: updating replaces the real file, so links to it keep working.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("couldn't find the running fabrica binary: %w", err)
	}
	return filepath.EvalSymlinks(exe)
}

// Check works out which release to install over exe, and whether that's
// an update. want is a version like v0.2.0, or empty for the latest
// release. Only the latest is ever compared by age: asking for a version
// installs it even when it's older.
func (s *Source) Check(ctx context.Context, exe, current, want string) (*Update, error) {
	u := &Update{Exe: exe, Current: current, Asset: "fabrica-" + s.OS + "-" + s.Arch}
	switch {
	case s.Mirror != "" && want != "":
		return nil, fmt.Errorf("%s is set, and it points at one release's files, so --version can't pick another; unset it to install from GitHub", MirrorEnvVar)
	case s.Mirror != "":
		u.From = strings.TrimRight(s.Mirror, "/")
	case want != "":
		v, err := ParseVersion(want)
		if err != nil {
			return nil, err
		}
		u.Version = v
	default:
		v, err := s.latest(ctx)
		if err != nil {
			return nil, err
		}
		u.Version = v
	}
	if u.Version != "" {
		u.From = s.GitHub + "/" + Repo + "/releases/download/" + u.Version
		u.Notes = s.GitHub + "/" + Repo + "/releases/tag/" + u.Version
	}

	switch {
	case u.Version != "" && u.Version == current:
		u.Status = UpToDate
		return u, nil
	case want == "" && u.Version != "" && ahead(current, u.Version):
		u.Status = Ahead
		return u, nil
	}
	sum, err := s.checksum(ctx, u)
	if err != nil {
		return nil, err
	}
	u.sum = sum
	// Same bytes means up to date, even when the versions don't say so (a
	// mirror never does).
	if have, err := fileSHA256(exe); err == nil && have == sum {
		u.Status = UpToDate
	}
	return u, nil
}

// Install downloads u's binary, checks it and puts it in place of u.Exe.
// It returns the new binary's version. When anything fails, u.Exe is left
// as it was.
func (s *Source) Install(ctx context.Context, u *Update) (string, error) {
	if u.sum == "" {
		sum, err := s.checksum(ctx, u)
		if err != nil {
			return "", err
		}
		u.sum = sum
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(u.Exe); err == nil {
		mode = info.Mode().Perm()
	}

	// The new binary goes next to the old one, so the rename stays on one
	// file system and is atomic.
	dir := filepath.Dir(u.Exe)
	tmp, err := os.CreateTemp(dir, ".fabrica-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("you can't write to %s, where fabrica is installed; run this again as someone who can (for example with sudo)", dir)
		}
		return "", fmt.Errorf("couldn't write the new fabrica next to the old one: %w", err)
	}
	defer os.Remove(tmp.Name()) // a no-op once it's renamed
	err = s.download(ctx, u, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return "", err
	}
	version, err := smokeTest(ctx, tmp.Name())
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), u.Exe); err != nil {
		return "", fmt.Errorf("couldn't replace %s: %w", u.Exe, err)
	}
	return version, nil
}

// ParseVersion turns "0.2.0" or "v0.2.0" into a release tag.
func ParseVersion(v string) (string, error) {
	tag := v
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if !semver.IsValid(tag) {
		return "", fmt.Errorf("%q isn't a version; use a release tag like v0.2.0 (see https://github.com/%s/releases)", v, Repo)
	}
	return tag, nil
}

// gitDescribe matches the versions `make build` gives builds between
// releases: v0.1.0-5-g1a2b3c4 is five commits after v0.1.0, and -dirty
// means it has changes that aren't committed.
var gitDescribe = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+)(?:-[0-9]+-g[0-9a-f]+(?:-dirty)?|-dirty)$`)

// ahead reports whether the current version is newer than the release.
// Anything that isn't a version (like "dev") counts as older.
func ahead(current, release string) bool {
	if m := gitDescribe.FindStringSubmatch(current); m != nil {
		// Semver reads these as pre-releases of the tag, but they come
		// after it.
		return semver.Compare(m[1], release) >= 0
	}
	return semver.Compare(current, release) > 0
}

// latest asks GitHub for the newest release's tag. GitHub redirects
// /releases/latest to that release's page. That's the same redirect
// install.sh relies on, and unlike GitHub's API it has no rate limit.
func (s *Source) latest(ctx context.Context) (string, error) {
	page := s.GitHub + "/" + Repo + "/releases/latest"
	req, err := s.request(ctx, http.MethodHead, page)
	if err != nil {
		return "", err
	}
	client := *s.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't ask GitHub for the latest release: %w", err)
	}
	resp.Body.Close()
	_, tag, ok := strings.Cut(resp.Header.Get("Location"), "/releases/tag/")
	if resp.StatusCode/100 != 3 || !ok {
		return "", fmt.Errorf("couldn't find the latest release at %s (got %s)", page, resp.Status)
	}
	if t, err := url.PathUnescape(tag); err == nil {
		tag = t
	}
	if !semver.IsValid(tag) {
		return "", fmt.Errorf("the latest release of %s is %q, which isn't a version", Repo, tag)
	}
	return tag, nil
}

// checksum reads u's asset's SHA-256 from the release's checksums.txt.
func (s *Source) checksum(ctx context.Context, u *Update) (string, error) {
	file := u.From + "/checksums.txt"
	resp, err := s.get(ctx, file)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && u.Version != "" {
		return "", fmt.Errorf("there's no release %s; see https://github.com/%s/releases for the versions there are", u.Version, Repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("couldn't download %s (got %s)", file, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksums))
	if err != nil {
		return "", fmt.Errorf("couldn't download %s: %w", file, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		// sha256sum's format: the hash, then the name (with a * in binary mode).
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == u.Asset {
			sum := strings.ToLower(f[0])
			if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
				return "", fmt.Errorf("%s has a broken checksum for %s", file, u.Asset)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("there's no release binary for %s/%s (%s doesn't list %s); build from source with: go install github.com/%s/cmd/fabrica@latest", s.OS, s.Arch, file, u.Asset, Repo)
}

// download writes u's binary to w, and fails unless its checksum matches.
func (s *Source) download(ctx context.Context, u *Update, w io.Writer) error {
	file := u.From + "/" + u.Asset
	resp, err := s.get(ctx, file)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("couldn't download %s (got %s)", file, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxBinary+1))
	if err != nil {
		return fmt.Errorf("couldn't download %s: %w", file, err)
	}
	if n > maxBinary {
		return fmt.Errorf("%s is bigger than %d MB, which can't be right", file, maxBinary>>20)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != u.sum {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", file, u.sum, got)
	}
	return nil
}

// smokeTest runs the new binary once, before it replaces anything, and
// returns the version it reports.
func smokeTest(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, smokeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", fmt.Errorf("the new fabrica doesn't run on this machine (%v)", err)
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "fabrica ")
	if !ok || v == "" {
		return "", fmt.Errorf("the new fabrica doesn't look like fabrica: `fabrica version` printed %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

func (s *Source) request(ctx context.Context, method, addr string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, addr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	return req, nil
}

func (s *Source) get(ctx context.Context, addr string) (*http.Response, error) {
	req, err := s.request(ctx, http.MethodGet, addr)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't download %s: %w", addr, err)
	}
	return resp, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
