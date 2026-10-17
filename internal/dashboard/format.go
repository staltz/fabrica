package dashboard

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// timeCell is a time, ready for a <time> element. Everything on the
// dashboard is in UTC, because the days on its charts are.
type timeCell struct {
	ISO, Short string
}

func timeOf(t time.Time) timeCell {
	if t.IsZero() {
		return timeCell{}
	}
	t = t.UTC()
	return timeCell{ISO: t.Format(time.RFC3339), Short: t.Format("Jan 2 15:04")}
}

func clock(t time.Time) string { return t.UTC().Format("15:04:05") }

// successRate is succeeded ÷ finished, where finished means succeeded or
// failed, and what to say about how it was worked out.
func successRate(succeeded, failed int) (text, note string) {
	finished := succeeded + failed
	if finished == 0 {
		return "–", "no finished runs yet"
	}
	return fmt.Sprintf("%.0f%%", float64(succeeded)/float64(finished)*100), fmt.Sprintf("%d of %s finished", succeeded, plural(finished, "run", "runs"))
}

// shortRun is the start of a run ID, enough to tell runs apart.
func shortRun(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// repoName drops the host from a repo, like github.com/acme/app → acme/app.
func repoName(repo string) string {
	if _, rest, ok := strings.Cut(repo, "/"); ok {
		return rest
	}
	return repo
}

// webLink says whether run data holds something we may make a link of: an
// http(s) URL. Agents write run data, so it's never trusted further than
// that.
func webLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// sessionLink says whether a session pointer may be a link: only an
// https:// URL, because it can be a file path or an ID too.
func sessionLink(raw string) bool {
	return strings.HasPrefix(raw, "https://") && webLink(raw)
}

// prLabel names a PR or merge request by its URL: acme/app#412, or
// acme/app!9 for GitLab's. It falls back to the URL without its scheme.
//
// Agents write the links, and the label is what a person reads before
// deciding to trust one, so any host but GitHub and GitLab's own is part of
// it: https://evil.example/acme/app/pull/412 is evil.example/acme/app#412.
func prLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := ""
	if h := strings.ToLower(u.Host); h != "github.com" && h != "gitlab.com" {
		host = u.Host + "/"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if (p == "pull" || p == "merge_requests") && i+1 < len(parts) && i >= 2 {
			repo := strings.Join(parts[:i], "/")
			repo = strings.TrimSuffix(repo, "/-")
			sep := "#"
			if p == "merge_requests" {
				sep = "!"
			}
			return host + repo + sep + parts[i+1]
		}
	}
	return u.Host + u.Path
}

// frictionGloss says what each kind of friction means.
var frictionGloss = map[string]string{
	"navigation": "Hard to find code or docs",
	"checks":     "A lint, type check or test that was missing or broken",
	"standards":  "A rule that was unclear, wrong or missing",
	"tools":      "A slow, failing or wasteful tool",
	"info":       "Information the agent needed but couldn't get",
	"other":      "Something else",
}

// staleStub says whether a refusal was because the agent's stub is out of
// date, which `fabrica apply` fixes. These are the reasons the MCP server
// writes in that case.
func staleStub(reason string) bool {
	return strings.Contains(reason, "stub is no longer deployed") || strings.Contains(reason, "stub is outdated")
}
