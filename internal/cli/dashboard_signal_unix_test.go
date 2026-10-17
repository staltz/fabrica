//go:build unix

package cli

import (
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGUSR1 is how Fabrica.app asks a running dashboard for another link. The
// test sends it to itself, which is the process the dashboard runs in.
func TestDashboardPrintsAFreshLinkOnSIGUSR1(t *testing.T) {
	setupCLI(t)
	seedHome(t)
	first, out, stop := startDashboard(t)
	defer stop()
	base := first[:strings.Index(first, "/_dashboard")]

	linksNow := func() []string { return dashboardLink.FindAllString(out.String(), -1) }
	// Each signal makes one more link, on a line of its own.
	for want := 2; want <= 3; want++ {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(linksNow()) < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(linksNow()) != want {
			t.Fatalf("after %d signal(s) the dashboard printed %d links:\n%s", want-1, len(linksNow()), out.String())
		}
	}
	links := linksNow()
	for _, link := range links {
		if !strings.HasPrefix(link, base+"/_dashboard?code=") {
			t.Errorf("link %s isn't on %s", link, base)
		}
		onItsOwnLine := false
		for _, line := range strings.Split(out.String(), "\n") {
			onItsOwnLine = onItsOwnLine || strings.TrimSpace(line) == link
		}
		if !onItsOwnLine {
			t.Errorf("%s isn't on a line of its own:\n%s", link, out.String())
		}
	}
	if links[0] != first || links[1] == links[0] || links[2] == links[1] || links[2] == links[0] {
		t.Errorf("the links aren't three different ones, the first first: %v", links)
	}

	// All of them work, once each, from different browsers, and it's the same
	// cookie that gets each of them in.
	var cookies []*http.Cookie
	for _, link := range links {
		cookie, page := openWith(t, link)
		if !strings.Contains(page, "review-before-push") {
			t.Errorf("%s opened:\n%s", link, page)
		}
		cookies = append(cookies, cookie)
	}
	for _, c := range cookies[1:] {
		if c.Name != cookies[0].Name || c.Value != cookies[0].Value {
			t.Errorf("another link got another cookie: %s and %s", cookies[0].Name, c.Name)
		}
	}
	for _, link := range links {
		res, err := noRedirects().Get(link)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s a second time: %d", link, res.StatusCode)
		}
	}
}
