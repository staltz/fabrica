// Package auth works out which credentials a home repo needs before
// `fabrica apply` can do its job, and checks each one with the service
// it's for. It's what `fabrica auth` is built on.
//
// A home repo needs three kinds of access:
//
//   - The secret of each Fabrica server its cloud factories use, to make
//     each factory's token. Fabrica can make one, and can ask the server
//     whether it accepts tokens made with a secret, without knowing the
//     server's.
//   - The credentials of each runtime API it deploys through, like a
//     Cursor API key. Each runtime's capability matrix declares them, with
//     the page where you make one.
//   - Git access: apply reads blueprints and product repos, and pushes
//     state to the home repo. Fabrica runs git, so that's whatever your
//     git can do; this package only checks it.
//
// Saving credentials is the credentials package's job.
package auth

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
)

// Kind is what kind of credential a Need is.
type Kind string

const (
	// ServerSecret is the secret of a Fabrica server.
	ServerSecret Kind = "server"
	// RuntimeCredential is something a runtime's API needs, like an API
	// key.
	RuntimeCredential Kind = "runtime"
)

// Need is one credential a home repo's factories need.
type Need struct {
	Kind Kind
	// Name is what `fabrica auth login` takes to set it up: the server's
	// URL, or the credential's environment variable.
	Name string
	// Factories are the factories that need it, sorted.
	Factories []string

	// Server is the server's URL, for a server secret.
	Server string
	// Endpoints map each factory to its endpoint on the server, for a
	// server secret. Checking a secret asks one of them.
	Endpoints map[string]string

	// Credential is the credential, for a runtime credential.
	Credential provider.Credential
	// Runtimes are the runtimes that need it, sorted.
	Runtimes []string
	// Provider is one of those runtimes, which knows how to check it.
	Provider *provider.Provider
	// Fallback is the apply mode those runtimes fall back to without it,
	// or "" when they have none, which makes it required.
	Fallback provider.ApplyMode
}

// Required reports whether apply fails without it. A runtime credential
// whose runtimes can fall back to handoffs is optional.
func (n *Need) Required() bool { return n.Kind == ServerSecret || n.Fallback == "" }

// Title says what it is, for people, like "a Cursor API key, for
// cursor-cloud (factory pr-review)".
func (n *Need) Title() string {
	switch n.Kind {
	case ServerSecret:
		if len(n.Factories) == 0 {
			return "the server secret"
		}
		return "the server secret, for " + plural(n.Factories, "factory", "factories")
	default:
		what := n.Credential.Description
		if what == "" {
			what = n.Credential.Env
		}
		switch {
		case len(n.Runtimes) == 0:
			return what
		case len(n.Factories) == 0:
			return fmt.Sprintf("%s, for %s", what, strings.Join(n.Runtimes, " and "))
		}
		return fmt.Sprintf("%s, for %s (%s)", what, strings.Join(n.Runtimes, " and "), plural(n.Factories, "factory", "factories"))
	}
}

// Without says what happens without it.
func (n *Need) Without() string {
	switch {
	case n.Kind == ServerSecret:
		return "apply can't make the factories' tokens"
	case n.Fallback == provider.Handoff:
		return fmt.Sprintf("%s %s handoffs instead of %s API", strings.Join(n.Runtimes, " and "), verb(n.Runtimes, "uses", "use"), possessive(n.Runtimes))
	default:
		return fmt.Sprintf("%s can't be applied", strings.Join(n.Runtimes, " and "))
	}
}

// Lookup finds the credential: in the environment, or where `fabrica auth
// login` saved it.
func (n *Need) Lookup() (credentials.Found, error) {
	if n.Kind == ServerSecret {
		return credentials.ServerSecret(n.Server)
	}
	return credentials.Lookup(n.Credential.Names()...)
}

// Key is what the credential is saved under.
func (n *Need) Key() string {
	if n.Kind == ServerSecret {
		return credentials.ServerKey(n.Server)
	}
	return n.Credential.Env
}

// EnvVar is the environment variable that holds it.
func (n *Need) EnvVar() string {
	if n.Kind == ServerSecret {
		return "FABRICA_SERVER_SECRET"
	}
	return n.Credential.Env
}

// Repo is a git repo apply reads, or pushes to.
type Repo struct {
	// URL is what git is given.
	URL string
	// Name is how people know it: github.com/acme/app, or a path.
	Name string
	// Why says what it is to the home repo, like "blueprint of pr-review".
	Why []string
	// Home is true for the home repo itself.
	Home bool
}

// OnGitHub reports whether the repo is on github.com, where the GitHub
// CLI can sign you in.
func (r Repo) OnGitHub() bool {
	n, err := repourl.Normalize(r.URL)
	return err == nil && strings.HasPrefix(n, "github.com/")
}

// Requirements are everything a home repo's factories need.
type Requirements struct {
	Home *home.Home
	// Needs are the credentials, server secrets first.
	Needs []*Need
	// Repos are the git repos, the home repo first.
	Repos []Repo
	// Problems are things that kept Fabrica from working out everything,
	// like a blueprint it couldn't load to see its providers.
	Problems []string
}

// Env is what working out requirements needs.
type Env struct {
	Git    *gitx.Cache
	Loader *blueprint.Loader
}

// Find works out what a home repo's factories need.
func Find(ctx context.Context, env *Env, h *home.Home) *Requirements {
	r := &Requirements{Home: h}
	servers := map[string]*Need{}
	creds := map[string]*Need{}
	repos := map[string]*Repo{}
	addRepo := func(url, name, why string) {
		if repos[url] == nil {
			repos[url] = &Repo{URL: url, Name: name}
		}
		if !contains(repos[url].Why, why) {
			repos[url].Why = append(repos[url].Why, why)
		}
	}

	cloud := len(h.StateNames) > 0
	for _, name := range sortedNames(h.Factories) {
		d := h.Factories[name]
		addRepo(repourl.GitURL(d.Blueprint.Git), d.Blueprint.Git, "blueprint of "+name)
		if d.Scope != provider.Cloud {
			continue
		}
		cloud = true
		for _, repo := range d.SpecificTargets() {
			addRepo(repourl.CloneURL(repo), repo, "target of "+name)
		}

		server := credentials.NormalizeServer(d.Server)
		n := servers[server]
		if n == nil {
			n = &Need{Kind: ServerSecret, Name: server, Server: server, Endpoints: map[string]string{}}
			servers[server] = n
		}
		n.Factories = append(n.Factories, name)
		n.Endpoints[name] = d.Endpoint()

		var bp *blueprint.Blueprint
		for _, rt := range d.Runtimes {
			p, ok := provider.Builtin(rt)
			if !ok {
				if bp == nil {
					var err error
					if bp, err = loadBlueprint(ctx, env, d); err != nil {
						r.Problems = append(r.Problems, fmt.Sprintf("couldn't load the blueprint of factory %s, to see what its runtimes need: %v", name, err))
						break
					}
				}
				item := bp.Providers[rt]
				if item == nil || item.Provider == nil {
					continue // `fabrica plan` reports it
				}
				p = item.Provider
			}
			if p.IsLocal() || !p.Matrix.HasMode(provider.API) {
				continue
			}
			for _, c := range p.Matrix.Credentials {
				n := creds[c.Env]
				if n == nil {
					n = &Need{Kind: RuntimeCredential, Name: c.Env, Credential: c, Provider: p, Fallback: fallback(p)}
					creds[c.Env] = n
				} else if f := fallback(p); f == "" {
					n.Fallback = "" // required as soon as one runtime has no fallback
				}
				if !contains(n.Runtimes, rt) {
					n.Runtimes = append(n.Runtimes, rt)
				}
				if !contains(n.Factories, name) {
					n.Factories = append(n.Factories, name)
				}
			}
		}
	}

	for _, key := range sortedNames(servers) {
		r.Needs = append(r.Needs, servers[key])
	}
	for _, key := range sortedNames(creds) {
		n := creds[key]
		sort.Strings(n.Runtimes)
		r.Needs = append(r.Needs, n)
	}

	if cloud {
		// Apply pushes state to the home repo's upstream.
		if url, name, err := homeRemote(ctx, h.Dir); err != nil {
			r.Problems = append(r.Problems, err.Error())
		} else {
			repo := Repo{URL: url, Name: name, Why: []string{"the home repo, where apply records state"}, Home: true}
			if other := repos[url]; other != nil {
				repo.Why = append(repo.Why, other.Why...)
				delete(repos, url)
			}
			r.Repos = append(r.Repos, repo)
		}
	}
	for _, key := range sortedNames(repos) {
		r.Repos = append(r.Repos, *repos[key])
	}
	return r
}

// Lookup finds a need by what `fabrica auth login` was given: a server's
// URL, an environment variable (or one of its aliases), or a runtime (all
// of its credentials).
func (r *Requirements) Lookup(name string) []*Need {
	var out []*Need
	for _, n := range r.Needs {
		switch {
		case n.Kind == ServerSecret && strings.Contains(name, "://") && n.Server == credentials.NormalizeServer(name),
			n.Kind == RuntimeCredential && contains(n.Credential.Names(), name),
			n.Kind == RuntimeCredential && contains(n.Runtimes, name):
			out = append(out, n)
		}
	}
	return out
}

// Standalone works out what a name given to `fabrica auth login` means
// when the home repo doesn't need it: a server's URL, a built-in runtime
// (its credentials), one of a built-in runtime's credentials, or any
// environment variable, for a folder provider elsewhere. It returns nil
// when the name is none of those.
func Standalone(name string) []*Need {
	if strings.Contains(name, "://") {
		server := credentials.NormalizeServer(name)
		return []*Need{{Kind: ServerSecret, Name: server, Server: server, Endpoints: map[string]string{}}}
	}
	if p, ok := provider.Builtin(name); ok {
		var out []*Need
		for _, c := range p.Matrix.Credentials {
			out = append(out, &Need{Kind: RuntimeCredential, Name: c.Env, Credential: c, Provider: p, Runtimes: []string{name}, Fallback: fallback(p)})
		}
		return out
	}
	for _, rt := range provider.BuiltinNames() {
		p, _ := provider.Builtin(rt)
		for _, c := range p.Matrix.Credentials {
			if contains(c.Names(), name) {
				return []*Need{{Kind: RuntimeCredential, Name: c.Env, Credential: c, Provider: p, Runtimes: []string{rt}, Fallback: fallback(p)}}
			}
		}
	}
	if envNameRe.MatchString(name) && !strings.HasPrefix(name, "FABRICA_") {
		return []*Need{{Kind: RuntimeCredential, Name: name, Credential: provider.Credential{Env: name}}}
	}
	return nil
}

var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// fallback is the apply mode a runtime uses when its API can't be.
func fallback(p *provider.Provider) provider.ApplyMode {
	for _, m := range p.Matrix.ApplyModes {
		if m != provider.API {
			return m
		}
	}
	return ""
}

func loadBlueprint(ctx context.Context, env *Env, d *home.Factory) (*blueprint.Blueprint, error) {
	resolved, err := env.Git.Remote(repourl.GitURL(d.Blueprint.Git), gitx.Full).Resolve(ctx, d.Blueprint.Ref)
	if err != nil {
		return nil, err
	}
	f, diags := env.Loader.LoadGit(ctx, d.Blueprint.Git, resolved.Commit, d.Blueprint.Path)
	if f == nil {
		if err := diags.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("there's no blueprint in %s", d.Blueprint)
	}
	return f, nil
}

// homeRemote is the git URL apply pushes the home repo's state to.
func homeRemote(ctx context.Context, dir string) (url, name string, err error) {
	wt, err := gitx.OpenWorktree(ctx, dir)
	if err != nil {
		return "", "", fmt.Errorf("the home repo at %s isn't a git repo, and apply records state by pushing it", dir)
	}
	remote, _, err := wt.Upstream(ctx)
	if err != nil {
		return "", "", fmt.Errorf("the home repo's branch has no upstream to push to, and apply records state by pushing it: %v", err)
	}
	url = wt.RemoteURL(ctx, remote)
	if url == "" {
		return "", "", fmt.Errorf("the home repo's remote %s has no URL", remote)
	}
	name = url
	if n, err := repourl.Normalize(url); err == nil && !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "file:") {
		name = n
	}
	return url, name, nil
}

// GitHubRepo is the home repo's owner/repo on github.com, for commands
// like `gh secret set --repo`. It's "" when the home repo isn't there.
func (r *Requirements) GitHubRepo() string {
	for _, repo := range r.Repos {
		if repo.Home && repo.OnGitHub() {
			n, _ := repourl.Normalize(repo.URL)
			return strings.TrimPrefix(n, "github.com/")
		}
	}
	return ""
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func plural(names []string, one, many string) string {
	if len(names) == 1 {
		return one + " " + names[0]
	}
	return many + " " + strings.Join(names, ", ")
}

func verb(names []string, one, many string) string {
	if len(names) == 1 {
		return one
	}
	return many
}

func possessive(names []string) string {
	if len(names) == 1 {
		return "its"
	}
	return "their"
}
