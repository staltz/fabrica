package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

// RemoteOptions configure `fabrica mcp --http`.
type RemoteOptions struct {
	// Home is the home repo to serve: a git URL (or a local path to a git
	// repo). The server serves exactly what its cloud deployments deploy.
	Home string
	// HomeRef is the branch to read. Empty means the default branch.
	HomeRef string
	// PublicURL, when set, limits the server to deployments whose server:
	// is this URL. That lets one home repo point different deployments at
	// different servers (staging and production, say).
	PublicURL string
	// Secret is the server secret. Each deployment's token is
	// HMAC(secret, deployment name).
	Secret string
	// RefreshEvery is how often to re-fetch the home repo and factories.
	RefreshEvery time.Duration
	Git          *gitx.Cache
	Loader       *factory.Loader
	Runs         *runs.DB
	Logger       *slog.Logger
	Now          func() time.Time
}

// Remote is the remote Fabrica server.
type Remote struct {
	opts RemoteOptions

	mu          sync.RWMutex
	state       *remoteState
	lastRefresh time.Time
	lastErr     error
	handlers    map[string]*mcp.StreamableHTTPHandler
}

// remoteState is one reading of the home repo.
type remoteState struct {
	homeCommit  string
	deployments map[string]*remoteDeployment
	// routes maps a URL path (like /pr-review) to a deployment name.
	routes map[string]string
}

type remoteDeployment struct {
	deployment *home.Deployment
	factory    *factory.Factory
	// refusal is set when the deployment can't be served, like when its
	// factory has errors. Sessions still connect, so agents can say why.
	refusal string
}

// NewRemote makes a remote server. Call Refresh at least once before
// serving.
func NewRemote(opts RemoteOptions) (*Remote, error) {
	if opts.Secret == "" {
		return nil, errors.New("the server secret is empty; set " + token.EnvVar)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.RefreshEvery == 0 {
		opts.RefreshEvery = time.Minute
	}
	return &Remote{opts: opts, handlers: map[string]*mcp.StreamableHTTPHandler{}, state: &remoteState{deployments: map[string]*remoteDeployment{}, routes: map[string]string{}}}, nil
}

// Refresh re-reads the home repo and the factories it deploys. When it
// fails, the server keeps serving what it had.
func (s *Remote) Refresh(ctx context.Context) error {
	st, err := s.read(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRefresh, s.lastErr = time.Now(), err
	if err != nil {
		s.opts.Logger.Error("refresh failed; still serving the last good home repo", "error", err)
		return err
	}
	if st.homeCommit != s.state.homeCommit {
		s.opts.Logger.Info("serving home repo", "commit", gitx.Short(st.homeCommit), "deployments", len(st.deployments))
	}
	s.state = st
	return nil
}

func (s *Remote) read(ctx context.Context) (*remoteState, error) {
	remote := s.opts.Git.Remote(repourl.GitURL(s.opts.Home), gitx.Full)
	if err := remote.Fetch(ctx); err != nil {
		return nil, err
	}
	var commit string
	if s.opts.HomeRef != "" {
		resolved, err := remote.Resolve(ctx, s.opts.HomeRef)
		if err != nil {
			return nil, err
		}
		commit = resolved.Commit
	} else {
		_, c, err := remote.DefaultBranch(ctx)
		if err != nil {
			return nil, err
		}
		commit = c
	}
	tree, err := remote.Tree(ctx, commit)
	if err != nil {
		return nil, err
	}
	h, err := home.Open(tree)
	if err != nil {
		return nil, err
	}
	for _, d := range h.Diags.Only(diag.Error) {
		s.opts.Logger.Warn("home repo problem", "where", d.Where, "problem", d.Message)
	}

	s.mu.RLock()
	prev := s.state
	s.mu.RUnlock()
	st := &remoteState{homeCommit: commit, deployments: map[string]*remoteDeployment{}, routes: map[string]string{}}
	for _, name := range h.Names() {
		d := h.Deployments[name]
		if d.Scope != provider.Cloud {
			continue // local deployments are for `fabrica mcp` on laptops
		}
		if s.opts.PublicURL != "" && strings.TrimRight(d.Server, "/") != strings.TrimRight(s.opts.PublicURL, "/") {
			continue
		}
		rd := &remoteDeployment{deployment: d}
		resolved, err := s.opts.Git.Remote(repourl.GitURL(d.Factory.Git), gitx.Full).Resolve(ctx, d.Factory.Ref)
		switch {
		case err != nil:
			rd.refusal = fmt.Sprintf("the server couldn't read the factory of deployment %s: %v", name, err)
		case prev.deployments[name] != nil && prev.deployments[name].factory != nil && prev.deployments[name].factory.Source.Commit == resolved.Commit && prev.deployments[name].deployment.Factory == d.Factory:
			rd.factory = prev.deployments[name].factory // same commit, nothing to reload
		default:
			f, diags := s.opts.Loader.LoadGit(ctx, d.Factory.Git, resolved.Commit, d.Factory.Path)
			if errs := diags.Only(diag.Error); len(errs) > 0 || f == nil {
				rd.refusal = fmt.Sprintf("the factory of deployment %s (%s at %s) has problems: %s", name, d.Factory, gitx.Short(resolved.Commit), firstLines(errs, 3))
			} else {
				rd.factory = f
			}
		}
		st.deployments[name] = rd
		st.routes[endpointPath(d.Endpoint())] = name
	}
	return st, nil
}

// endpointPath is the URL path of an endpoint, like /pr-review (or
// /fabrica/pr-review when server: has a path).
func endpointPath(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return path.Clean("/" + u.Path)
}

func (s *Remote) current() *remoteState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// RunRefresher refreshes on a timer until ctx is done.
func (s *Remote) RunRefresher(ctx context.Context) {
	t := time.NewTicker(s.opts.RefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Refresh(ctx)
		}
	}
}

// ServeHTTP routes requests to deployment endpoints:
//
//	/healthz                       health check (no token needed)
//	/<deployment>                  token in an Authorization: Bearer header
//	/<deployment>/t/<token>        token in the URL, for runtimes that only take a URL
func (s *Remote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.health(w)
		return
	}
	p, tok := splitToken(path.Clean("/" + r.URL.Path))
	st := s.current()
	name, ok := st.routes[p]
	if !ok {
		httpError(w, http.StatusNotFound, "there's no Fabrica deployment at "+p)
		return
	}
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if tok == "" || !token.Valid(s.opts.Secret, name, strings.TrimSpace(tok)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fabrica"`)
		httpError(w, http.StatusUnauthorized, "missing or wrong token for deployment "+name)
		return
	}
	s.handlerFor(name).ServeHTTP(w, r)
}

// splitToken pulls a /t/<token> suffix off a path.
func splitToken(p string) (string, string) {
	i := strings.LastIndex(p, "/t/")
	if i < 0 {
		return p, ""
	}
	tok := p[i+len("/t/"):]
	if tok == "" || strings.Contains(tok, "/") {
		return p, ""
	}
	return p[:i], tok
}

// handlerFor returns the MCP handler of one deployment. Each deployment
// gets its own handler, so sessions of one deployment can never be used
// through another deployment's endpoint.
func (s *Remote) handlerFor(name string) *mcp.StreamableHTTPHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handlers[name]; ok {
		return h
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.newSession(name) }, &mcp.StreamableHTTPOptions{
		SessionTimeout: time.Hour,
		Logger:         s.opts.Logger,
		// Every request already needs the deployment's secret token, which
		// DNS rebinding can't get at, and Fabrica usually runs behind a
		// reverse proxy that connects over localhost.
		DisableLocalhostProtection: true,
	})
	s.handlers[name] = h
	return h
}

// newSession starts a session for a deployment, fixed to the factory
// commit the server has right now.
func (s *Remote) newSession(name string) *mcp.Server {
	rd := s.current().deployments[name]
	if rd == nil {
		return nil
	}
	snap := &Snapshot{Mode: runs.ServedRemote, Deployment: rd.deployment, Factory: rd.factory, Refusal: rd.refusal}
	core := &Core{Runs: s.opts.Runs, Backend: &remoteBackend{server: s, snap: snap}, Now: s.opts.Now}
	instructions := Instructions(snap)
	if snap.Refusal != "" {
		instructions = RefusalInstructions(snap.Refusal)
	}
	return core.NewServer(instructions)
}

func (s *Remote) health(w http.ResponseWriter) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := map[string]any{"ok": s.lastErr == nil && s.state.homeCommit != "", "home_commit": gitx.Short(s.state.homeCommit)}
	if !s.lastRefresh.IsZero() {
		status["last_refresh"] = s.lastRefresh.UTC().Format(time.RFC3339)
	}
	if s.lastErr != nil {
		status["error"] = "the last refresh failed; see the server log"
	}
	w.Header().Set("Content-Type", "application/json")
	if status["ok"] != true {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Serve listens on addr until ctx is done.
func (s *Remote) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	s.opts.Logger.Info("listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// remoteBackend serves one session from a fixed snapshot, but checks stubs
// against whatever the server read from the home repo most recently.
type remoteBackend struct {
	server *Remote
	snap   *Snapshot
}

func (b *remoteBackend) Resolve(context.Context, *mcp.ServerSession) *Snapshot { return b.snap }

func (b *remoteBackend) LatestDeployment(_ context.Context, snap *Snapshot, _, _ string) (*home.Deployment, string) {
	rd := b.server.current().deployments[snap.deploymentName()]
	if rd == nil {
		return nil, fmt.Sprintf("deployment %s isn't in the home repo anymore", snap.deploymentName())
	}
	return rd.deployment, ""
}
