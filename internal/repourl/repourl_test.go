package repourl

import "testing"

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/acme/app":                  "github.com/acme/app",
		"https://github.com/Acme/App.git":      "github.com/acme/app",
		"http://github.com/acme/app/":          "github.com/acme/app",
		"git@github.com:acme/app.git":          "github.com/acme/app",
		"ssh://git@github.com:22/acme/app.git": "github.com/acme/app",
		"gitlab.com/group/sub/repo":            "gitlab.com/group/sub/repo",
	} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "app", "github.com/acme", "file:///tmp/x", "acme/app"} {
		if got, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) = %q, want an error", bad, got)
		}
	}
}

func TestGitURL(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/acme/blueprint":         "https://github.com/acme/blueprint.git",
		"github.com/acme/blueprint.git":     "https://github.com/acme/blueprint.git",
		"https://example.com/x.git":         "https://example.com/x.git",
		"git@github.com:acme/blueprint.git": "git@github.com:acme/blueprint.git",
		"/srv/git/blueprint":                "/srv/git/blueprint",
		"file:///srv/git/blueprint":         "file:///srv/git/blueprint",
	} {
		if got := GitURL(in); got != want {
			t.Errorf("GitURL(%q) = %q, want %q", in, got, want)
		}
	}
}
