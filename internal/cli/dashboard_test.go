package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

// syncBuffer is a buffer that a command can write to while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// dashboardLink is what the dashboard prints for people, and for programs
// like Fabrica.app that run it, to find.
var dashboardLink = regexp.MustCompile(`http://127\.0\.0\.1:\d+/_dashboard\?code=[0-9a-f]+`)

// startDashboard runs `fabrica dashboard` until the test cancels it, and
// returns the link it printed, what it printed, and how to stop it.
func startDashboard(t *testing.T, args ...string) (link string, out *syncBuffer, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out = &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- Main(ctx, append([]string{"dashboard"}, args...), strings.NewReader(""), out, out) }()
	stopped := false
	stop = func() int {
		stopped = true
		cancel()
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("the dashboard didn't stop")
			return -1
		}
	}
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := dashboardLink.FindString(out.String()); m != "" {
			return m, out, stop
		}
		select {
		case code := <-done:
			done <- code
			t.Fatalf("the dashboard stopped with %d before printing a link:\n%s", code, out.String())
		default:
		}
	}
	t.Fatalf("the dashboard printed no link:\n%s", out.String())
	return "", nil, nil
}

// seedHome puts a run in the runs.db of the (throwaway) personal home repo.
func seedHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("FABRICA_HOME")
	db, err := runs.Open(filepath.Join(home, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	r := runs.Run{ID: runs.NewID(now), StartedAt: now, Intent: "fix the login bug", Factory: "my-default", Routine: "review-before-push", Runtime: "claude-code-local", Repo: "github.com/acme/app", ServedBy: runs.ServedLocal}
	if err := db.Begin(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCheckpoint(context.Background(), r.ID, runs.Checkpoint{At: now, Author: runs.ByAgent, Status: runs.Succeeded, Summary: "fixed it"}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// noRedirects is a client that shows the redirects rather than following them.
func noRedirects() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestDashboardCommand(t *testing.T) {
	setupCLI(t)
	runID := seedHome(t)
	link, out, stop := startDashboard(t)

	if !strings.Contains(out.String(), "Press Ctrl-C to stop.") || !strings.Contains(out.String(), "it works once") {
		t.Errorf("the dashboard said:\n%s", out.String())
	}
	base := link[:strings.Index(link, "/_dashboard")]
	c := noRedirects()

	// Without the code, nobody gets in; the bare address goes to the dashboard.
	res, err := c.Get(base + "/_dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || !strings.Contains(string(body), "fabrica dashboard") {
		t.Errorf("without the code: %d\n%s", res.StatusCode, body)
	}
	if res, err = c.Get(base + "/"); err != nil || res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/_dashboard" {
		t.Errorf("the bare address: %v, %v", res, err)
	}

	// The link trades its code for a cookie and goes to the clean address.
	res, err = c.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/_dashboard" || len(res.Cookies()) != 1 {
		t.Fatalf("the link: %d, Location %q, %d cookies", res.StatusCode, res.Header.Get("Location"), len(res.Cookies()))
	}
	req, _ := http.NewRequest("GET", base+"/_dashboard", nil)
	req.AddCookie(res.Cookies()[0])
	res2, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != 200 || !strings.Contains(string(body), "review-before-push") || !strings.Contains(string(body), "Runs per day") {
		t.Errorf("with the cookie: %d\n%s", res2.StatusCode, body)
	}
	req, _ = http.NewRequest("GET", base+"/_dashboard/runs/"+runID, nil)
	req.AddCookie(res.Cookies()[0])
	res3, _ := c.Do(req)
	body, _ = io.ReadAll(res3.Body)
	res3.Body.Close()
	if res3.StatusCode != 200 || !strings.Contains(string(body), "fix the login bug") {
		t.Errorf("a run: %d", res3.StatusCode)
	}

	// The code worked once.
	if res, err = c.Get(link); err != nil || res.StatusCode != 403 {
		t.Errorf("the link again: %v, %v", res, err)
	}

	// Ctrl-C stops it, cleanly, and the port is closed.
	if code := stop(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	if _, err := c.Get(base + "/_dashboard"); err == nil {
		t.Error("still listening after Ctrl-C")
	}
}

func TestDashboardCommandPortAndOpen(t *testing.T) {
	setupCLI(t)
	seedHome(t)
	// A port that's free right now.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	opened := make(chan string, 4)
	old := openBrowser
	openBrowser = func(url string) bool { opened <- url; return true }
	defer func() { openBrowser = old }()

	link, _, stop := startDashboard(t, "--port", fmt.Sprint(port), "--open")
	defer stop()
	if !strings.HasPrefix(link, fmt.Sprintf("http://127.0.0.1:%d/_dashboard?code=", port)) {
		t.Errorf("link %s", link)
	}
	select {
	case got := <-opened:
		if got != link {
			t.Errorf("opened %s, want %s", got, link)
		}
	default:
		t.Error("--open opened nothing")
	}

	// Without --open, no browser.
	_, _, stop2 := startDashboard(t)
	stop2()
	select {
	case got := <-opened:
		t.Errorf("opened %s without --open", got)
	default:
	}

	// Another dashboard can't have the same port, and says what to do.
	out, code := fab(t, "", "dashboard", "--port", fmt.Sprint(port))
	if code != 1 || !strings.Contains(out, fmt.Sprintf("couldn't listen on port %d", port)) || !strings.Contains(out, "leave it out") {
		t.Errorf("a busy port: exit %d\n%s", code, out)
	}
}

func TestDashboardCommandNeedsRunHistoryAndAGoodPort(t *testing.T) {
	setupCLI(t)
	home := os.Getenv("FABRICA_HOME")
	os.Remove(filepath.Join(home, "runs.db"))
	out, code := fab(t, "", "dashboard")
	if code != 1 || !strings.Contains(out, "there's no run history at "+filepath.Join(home, "runs.db")+" yet") {
		t.Errorf("no history: exit %d\n%s", code, out)
	}
	// It didn't make one.
	if _, err := os.Stat(filepath.Join(home, "runs.db")); !os.IsNotExist(err) {
		t.Errorf("the dashboard created runs.db: %v", err)
	}
	other := t.TempDir()
	out, code = fab(t, "", "--home", other, "dashboard")
	if code != 1 || !strings.Contains(out, filepath.Join(other, "runs.db")) {
		t.Errorf("--home: exit %d\n%s", code, out)
	}
	for _, port := range []string{"-1", "70000"} {
		if out, code := fab(t, "", "dashboard", "--port", port); code != 1 || !strings.Contains(out, "isn't a port number") {
			t.Errorf("--port %s: exit %d\n%s", port, code, out)
		}
	}
}

func TestDashboardCommandReadsTheHomeYouNameWithHome(t *testing.T) {
	setupCLI(t)
	other := t.TempDir()
	db, err := runs.Open(filepath.Join(other, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Begin(context.Background(), runs.Run{ID: runs.NewID(now), StartedAt: now, Intent: "elsewhere", Routine: "elsewhere-routine", Factory: "f", ServedBy: runs.ServedLocal}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	link, _, stop := startDashboard(t, "--home", other)
	defer stop()
	c := noRedirects()
	res, err := c.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ := http.NewRequest("GET", link[:strings.Index(link, "?")], nil)
	req.AddCookie(res.Cookies()[0])
	res, _ = c.Do(req)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "elsewhere-routine") {
		t.Errorf("--home wasn't read:\n%s", body)
	}
}

// openWith trades a link for a cookie, and returns that cookie and the page
// it opens.
func openWith(t *testing.T, link string) (cookie *http.Cookie, page string) {
	t.Helper()
	c := noRedirects()
	res, err := c.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatalf("%s: %d, %d cookies", link, res.StatusCode, len(res.Cookies()))
	}
	cookie = res.Cookies()[0]
	req, _ := http.NewRequest("GET", link[:strings.Index(link, "?")], nil)
	req.AddCookie(cookie)
	res, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return cookie, string(body)
}

// seedDB puts a run of a routine in a runs.db that is not the home repo's.
func seedDB(t *testing.T, path, routine string) {
	t.Helper()
	db, err := runs.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if err := db.Begin(context.Background(), runs.Run{ID: runs.NewID(now), StartedAt: now, Intent: "elsewhere", Routine: routine, Factory: "f", ServedBy: runs.ServedRemote}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardCommandShowsTheDBYouNameWithDB(t *testing.T) {
	setupCLI(t)
	seedHome(t) // the home repo has a routine of its own, which --db must not show
	db := filepath.Join(t.TempDir(), "server-runs.db")
	seedDB(t, db, "the-servers-routine")

	link, out, stop := startDashboard(t, "--db", db)
	defer stop()
	if !strings.Contains(out.String(), "Run history from "+db) {
		t.Errorf("the dashboard said:\n%s", out.String())
	}
	_, page := openWith(t, link)
	if !strings.Contains(page, "the-servers-routine") || strings.Contains(page, "review-before-push") {
		t.Errorf("--db wasn't what was read:\n%s", page)
	}
}

func TestDashboardCommandRefusesADBThatIsntThere(t *testing.T) {
	setupCLI(t)
	seedHome(t) // the home repo's runs.db being there doesn't make up for it
	missing := filepath.Join(t.TempDir(), "nope", "server-runs.db")
	out, code := fab(t, "", "dashboard", "--db", missing)
	if code != 1 || !strings.Contains(out, "there's no run history at "+missing+" yet") {
		t.Errorf("a missing --db: exit %d\n%s", code, out)
	}
	// It didn't make one, or the folder for it.
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Errorf("the dashboard made %s: %v", filepath.Dir(missing), err)
	}
	other := filepath.Join(t.TempDir(), "server-runs.db")
	if out, code := fab(t, "", "dashboard", "--db", other); code != 1 || !strings.Contains(out, "there's no run history at "+other+" yet") {
		t.Errorf("a missing --db in a folder that exists: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("the dashboard created %s: %v", other, err)
	}
}
