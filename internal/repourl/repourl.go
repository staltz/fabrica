// Package repourl turns the many ways of writing a git repo's address into
// one canonical form: `host/owner/repo`.
//
// Factories name product repos as `github.com/acme/app`, but your laptop's
// git remote might say `git@github.com:acme/app.git` or
// `https://github.com/Acme/App`. To match them up, everything gets
// normalized to the same lowercase `host/owner/repo` form first.
package repourl

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// scpLike matches the short SSH form git accepts, like
// `git@github.com:acme/app.git`.
var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// Normalize turns a repo address into `host/owner/repo`.
//
// It accepts the forms people actually use:
//
//	github.com/acme/app
//	https://github.com/acme/app.git
//	ssh://git@github.com:22/acme/app.git
//	git@github.com:acme/app.git
//
// The result is all lowercase, because the big git hosts treat repo names
// case-insensitively and a case mismatch shouldn't stop a factory from
// matching. Nested groups (like GitLab's `gitlab.com/group/sub/repo`) are
// kept as they are.
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("the repo name is empty")
	}
	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("%q isn't a valid repo URL: %v", raw, err)
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return "", fmt.Errorf("%q uses the %q scheme; product repos must live on a git host (https or ssh)", raw, u.Scheme)
		}
		host, p = u.Hostname(), u.Path
	case scpLike.MatchString(s) && !strings.HasPrefix(s, "/"):
		m := scpLike.FindStringSubmatch(s)
		host, p = m[1], m[2]
	default:
		host, p, _ = strings.Cut(s, "/")
	}
	host = strings.ToLower(host)
	p = strings.ToLower(strings.Trim(path.Clean("/"+p), "/"))
	p = strings.TrimSuffix(p, ".git")
	if host == "" || !strings.Contains(host, ".") && host != "localhost" {
		return "", fmt.Errorf("%q doesn't start with a host; write repos as host/owner/repo, like github.com/acme/app", raw)
	}
	if strings.Count(p, "/") < 1 {
		return "", fmt.Errorf("%q needs both an owner and a repo name; write it as host/owner/repo, like github.com/acme/app", raw)
	}
	return host + "/" + p, nil
}

// MustNormalize is Normalize for values that are already known to be valid,
// like the ones in a validated factory. It panics on bad input.
func MustNormalize(raw string) string {
	n, err := Normalize(raw)
	if err != nil {
		panic(err)
	}
	return n
}

// CloneURL turns a canonical `host/owner/repo` into an HTTPS URL git can
// clone. If you'd rather use SSH, git's own `url.<base>.insteadOf` setting
// can rewrite it for you, with no Fabrica configuration needed.
func CloneURL(canonical string) string {
	return "https://" + canonical + ".git"
}

// GitURL is like CloneURL but for the `git:` field of blueprints and imports,
// which may also hold a full URL or a local path. Full URLs and paths are
// used exactly as written; only the short `host/owner/repo` form is turned
// into an HTTPS URL.
func GitURL(spec string) string {
	s := strings.TrimSpace(spec)
	switch {
	case strings.Contains(s, "://"),
		strings.HasPrefix(s, "/"),
		strings.HasPrefix(s, "./"),
		strings.HasPrefix(s, "../"),
		scpLike.MatchString(s) && strings.Contains(strings.SplitN(s, ":", 2)[0], "@"):
		return s
	}
	if strings.HasSuffix(s, ".git") {
		return "https://" + s
	}
	return "https://" + s + ".git"
}
