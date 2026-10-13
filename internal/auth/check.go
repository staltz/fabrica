package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/token"
)

// Verdict is what the service said about a credential.
type Verdict string

const (
	// Accepted means the service accepts it.
	Accepted Verdict = "accepted"
	// Rejected means the service refuses it: apply would fail with it.
	Rejected Verdict = "rejected"
	// Unchecked means there was no way to ask, like for a folder
	// provider's credential, or a server that doesn't serve the factories
	// yet. That's not a problem in itself.
	Unchecked Verdict = "unchecked"
	// Unreachable means Fabrica tried to ask and got no clear answer,
	// like when the server is down.
	Unreachable Verdict = "unreachable"
)

// Status is how one credential stands.
type Status struct {
	Need  *Need
	Found credentials.Found
	// Err is set when looking the credential up failed, like a keychain
	// that couldn't be read.
	Err     error
	Verdict Verdict
	// Detail says what the service said, or why it couldn't be asked.
	Detail string
}

// Set reports whether there's a credential at all.
func (s *Status) Set() bool { return s.Err == nil && s.Found.Value != "" }

// Problem reports whether apply would fail because of it: it's required
// and missing, it's refused, or it can't be read.
func (s *Status) Problem() bool {
	return s.Err != nil || s.Verdict == Rejected || !s.Set() && s.Need.Required()
}

// Check looks a credential up and, when verify is set, asks its service
// whether it works.
func Check(ctx context.Context, n *Need, verify bool) *Status {
	s := &Status{Need: n, Verdict: Unchecked}
	s.Found, s.Err = n.Lookup()
	if !s.Set() || !verify {
		return s
	}
	s.Verdict, s.Detail = Verify(ctx, n, s.Found.Value)
	return s
}

// Verify asks the service a credential is for whether value works.
func Verify(ctx context.Context, n *Need, value string) (Verdict, string) {
	if n.Kind == ServerSecret {
		return VerifyServerSecret(ctx, n, value)
	}
	if n.Provider == nil {
		return Unchecked, "Fabrica has no way to check it"
	}
	said, err := n.Provider.VerifyCredential(ctx, n.Credential.Env, value)
	switch {
	case errors.Is(err, provider.ErrUnverifiable):
		if n.Provider.Builtin {
			return Unchecked, "Fabrica has no way to check it"
		}
		return Unchecked, fmt.Sprintf("Fabrica has no way to check it; only %s's api program uses it", n.Provider.Name())
	case err != nil && isNetworkError(err):
		return Unreachable, err.Error()
	case err != nil:
		return Rejected, err.Error()
	}
	return Accepted, said
}

// isNetworkError reports whether err means the service couldn't be
// reached, rather than that it said no.
func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

// HTTPClient is the client server checks use. It never follows a
// redirect: a Fabrica server doesn't send any, and the token must only
// ever go to the server's own address.
var HTTPClient = &http.Client{
	Timeout:       15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// VerifyServerSecret asks a Fabrica server whether it accepts tokens made
// with secret, without knowing the server's own secret. For one of the
// factories it serves, it first sends a token that can't be right, to make
// sure a Fabrica server answers and refuses it, then the real token made
// with secret. Both are plain GETs that change nothing on the server.
func VerifyServerSecret(ctx context.Context, n *Need, secret string) (Verdict, string) {
	probe := ProbeServer(ctx, n)
	switch {
	case probe.Err != nil:
		return Unreachable, probe.Err.Error()
	case probe.Factory == "":
		return Unchecked, probe.Detail
	}
	code, _, err := get(ctx, probe.Endpoint, token.For(secret, probe.Factory))
	switch {
	case err != nil:
		return Unreachable, fmt.Sprintf("couldn't reach %s: %v", n.Server, err)
	case code == http.StatusUnauthorized:
		return Rejected, fmt.Sprintf("the server refuses tokens made with it (checked with factory %s), so it isn't the secret the server uses", probe.Factory)
	case code == http.StatusNotFound:
		return Unreachable, fmt.Sprintf("the server stopped serving factory %s halfway through the check; try again", probe.Factory)
	}
	return Accepted, fmt.Sprintf("the server accepts it (checked with factory %s)", probe.Factory)
}

// Probe is what a server said when asked about a need's factories.
type Probe struct {
	// Factory and Endpoint are the first factory a Fabrica server answers
	// for, refusing a wrong token. Empty when there's none.
	Factory, Endpoint string
	// Detail says why there's no factory to check with.
	Detail string
	// Err is set when the server couldn't be reached, or something other
	// than a Fabrica server answered.
	Err error
}

// Running reports whether a Fabrica server serves at least one of the
// factories, which means it has a secret already.
func (p Probe) Running() bool { return p.Factory != "" }

// ProbeServer finds a factory the server serves, by sending a token that
// can't be right to each factory's endpoint in turn: a Fabrica server
// refuses it with a 401 that names Fabrica.
func ProbeServer(ctx context.Context, n *Need) Probe {
	names := make([]string, 0, len(n.Endpoints))
	for name := range n.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return Probe{Detail: "Fabrica can only check it with one of the server's factories, and none in this home repo uses this server"}
	}
	for _, name := range names {
		endpoint := n.Endpoints[name]
		code, realm, err := get(ctx, endpoint, "fab_fabrica-auth-check")
		switch {
		case err != nil:
			return Probe{Err: fmt.Errorf("couldn't reach %s: %v", n.Server, err)}
		case code == http.StatusNotFound:
			continue // the server doesn't serve this one (yet)
		case code == http.StatusUnauthorized && strings.Contains(realm, `realm="fabrica"`):
			return Probe{Factory: name, Endpoint: endpoint}
		default:
			return Probe{Err: fmt.Errorf("something other than a Fabrica server answers at %s (HTTP %d to a wrong token)", endpoint, code)}
		}
	}
	return Probe{Detail: fmt.Sprintf("couldn't check it yet: the server doesn't serve %s (it serves what's merged in its home repo, about a minute after the merge)", plural(names, "factory", "any of factories"))}
}

// get sends a GET with a bearer token, and returns the status code and
// the WWW-Authenticate header.
func get(ctx context.Context, endpoint, tok string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "fabrica")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// Access is whether git can read one repo.
type Access struct {
	Repo Repo
	// Err is what git said when it couldn't.
	Err error
}

// CheckRepos asks git whether it can read each repo, a few at a time.
func CheckRepos(ctx context.Context, repos []Repo) []Access {
	out := make([]Access, len(repos))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = Access{Repo: repo, Err: gitx.CanRead(ctx, repo.URL)}
		}()
	}
	wg.Wait()
	return out
}

// GitProblem shortens what git said when it couldn't read a repo to its
// last line, which is usually the one that explains.
func GitProblem(err error) string {
	var gitErr *gitx.Error
	if errors.As(err, &gitErr) {
		lines := strings.Split(strings.TrimSpace(gitErr.Stderr), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return last
		}
	}
	return err.Error()
}
