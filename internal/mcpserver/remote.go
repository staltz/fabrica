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

	"github.com/staltz/fabrica/internal/admin"
	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/dashboard"
	"github.com/staltz/fabrica/internal/diag"
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
	// repo). The server serves exactly what its cloud factories deploy.
	Home string
	// HomeRef is the branch to read. Empty means the default branch.
	HomeRef string
	// PublicURL, when set, limits the server to factories whose server:
	// is this URL. That lets one home repo point different factories at
	// different servers (staging and production, say).
	PublicURL string
	// Secret is the server secret. Each factory's token is
	// HMAC(secret, factory name).
	Secret string
	// RefreshEvery is how often to re-fetch the home repo and blueprints.
	RefreshEvery time.Duration
	Git          *gitx.Cache
	Loader       *blueprint.Loader
	Runs         *runs.DB
	Logger       *slog.Logger
	Now          func() time.Time
}

// Remote is the remote Fabrica server.
type Remote struct {
	opts RemoteOptions
	// admin is the admin API, which people use to read run history. It's
	// the operator plane: agents never reach it, and it's not in this
	// package's tools.
	admin http.Handler
	// dashboard is the run-history dashboard, also for people only. This
	// package just routes to it.
	dashboard http.Handler

	mu          sync.RWMutex
	state       *remoteState
	lastRefresh time.Time
	lastErr     error
	handlers    map[string]*mcp.StreamableHTTPHandler
}

// remoteState is one reading of the home repo.
type remoteState struct {
	homeCommit string
	factories  map[string]*remoteFactory
	// routes maps a URL path (like /pr-review) to a factory name.
	routes map[string]string
}

type remoteFactory struct {
	factory   *home.Factory
	blueprint *blueprint.Blueprint
	// refusal is set when the factory can't be served, like when its
	// blueprint has errors. Sessions still connect, so agents can say why.
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
	s := &Remote{opts: opts, admin: admin.New(opts.Runs, opts.Secret), handlers: map[string]*mcp.StreamableHTTPHandler{}, state: &remoteState{factories: map[string]*remoteFactory{}, routes: map[string]string{}}}
	s.dashboard = dashboard.New(opts.Runs, dashboard.Options{Secret: opts.Secret, Subject: s.blueprintSubject, Now: opts.Now, Logger: opts.Logger})
	return s, nil
}

// blueprintSubject finds the subject line of a commit of a factory's
// blueprint, for the dashboard's charts. It only looks in the git cache the
// server already has, and says nothing when the commit isn't there.
func (s *Remote) blueprintSubject(ctx context.Context, factory, sha string) string {
	rd := s.current().factories[factory]
	if rd == nil || s.opts.Git == nil {
		return ""
	}
	subject, err := s.opts.Git.Remote(repourl.GitURL(rd.factory.Blueprint.Git), gitx.Full).Subject(ctx, sha)
	if err != nil {
		return ""
	}
	return subject
}

// Refresh re-reads the home repo and the blueprints it deploys. When it
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
		s.opts.Logger.Info("serving home repo", "commit", gitx.Short(st.homeCommit), "factories", len(st.factories))
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
	st := &remoteState{homeCommit: commit, factories: map[string]*remoteFactory{}, routes: map[string]string{}}
	for _, name := range h.Names() {
		d := h.Factories[name]
		if d.Scope != provider.Cloud {
			continue // local factories are for `fabrica mcp` on laptops
		}
		if s.opts.PublicURL != "" && strings.TrimRight(d.Server, "/") != strings.TrimRight(s.opts.PublicURL, "/") {
			continue
		}
		rd := &remoteFactory{factory: d}
		resolved, err := s.opts.Git.Remote(repourl.GitURL(d.Blueprint.Git), gitx.Full).Resolve(ctx, d.Blueprint.Ref)
		switch {
		case err != nil:
			rd.refusal = fmt.Sprintf("the server couldn't read the blueprint of factory %s: %v", name, err)
		case prev.factories[name] != nil && prev.factories[name].blueprint != nil && prev.factories[name].blueprint.Source.Commit == resolved.Commit && prev.factories[name].factory.Blueprint == d.Blueprint:
			rd.blueprint = prev.factories[name].blueprint // same commit, nothing to reload
		default:
			f, diags := s.opts.Loader.LoadGit(ctx, d.Blueprint.Git, resolved.Commit, d.Blueprint.Path)
			if errs := diags.Only(diag.Error); len(errs) > 0 || f == nil {
				rd.refusal = fmt.Sprintf("the blueprint of factory %s (%s at %s) has problems: %s", name, d.Blueprint, gitx.Short(resolved.Commit), firstLines(errs, 3))
			} else {
				rd.blueprint = f
			}
		}
		st.factories[name] = rd
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

// ServeHTTP routes requests to factory endpoints, and to the operator plane:
//
//	/healthz                       health check (no token needed)
//	/<factory>                  token in an Authorization: Bearer header
//	/<factory>/t/<token>        token in the URL, for runtimes that only take a URL
//	…/_admin/runs               the admin API: only the admin token, only in a header
//	…/_dashboard                the dashboard: log in with the admin token, then a cookie
func (s *Remote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.health(w)
		return
	}
	if h := s.operator(r.URL.Path); h != nil {
		h.ServeHTTP(w, r)
		return
	}
	p, tok := splitToken(path.Clean("/" + r.URL.Path))
	st := s.current()
	name, ok := st.routes[p]
	if !ok {
		httpError(w, http.StatusNotFound, "there's no Fabrica factory at "+p)
		return
	}
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if tok == "" || !token.Valid(s.opts.Secret, name, strings.TrimSpace(tok)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fabrica"`)
		httpError(w, http.StatusUnauthorized, "missing or wrong token for factory "+name)
		return
	}
	s.handlerFor(name).ServeHTTP(w, r)
}

// operator returns the handler for a path of the operator plane, the part
// of the server that people use and agents never do, or nil for any other
// path. Each part is found by its marker segment, the last one in the path,
// so it works under a path prefix too. Factory names never contain an
// underscore, so no factory endpoint can look like one.
func (s *Remote) operator(urlPath string) http.Handler {
	isAdmin, isDashboard := admin.Matches(urlPath), dashboard.Matches(urlPath)
	switch {
	case isAdmin && isDashboard:
		// A path with both is the one whose marker comes last.
		p := path.Clean("/" + urlPath)
		if strings.LastIndex(p, "/_admin") > strings.LastIndex(p, "/_dashboard") {
			return s.admin
		}
		return s.dashboard
	case isAdmin:
		return s.admin
	case isDashboard:
		return s.dashboard
	}
	return nil
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

// handlerFor returns the MCP handler of one factory. Each factory
// gets its own handler, so sessions of one factory can never be used
// through another factory's endpoint.
func (s *Remote) handlerFor(name string) *mcp.StreamableHTTPHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handlers[name]; ok {
		return h
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.newSession(name) }, &mcp.StreamableHTTPOptions{
		SessionTimeout: time.Hour,
		Logger:         s.opts.Logger,
		// Every request already needs the factory's secret token, which
		// DNS rebinding can't get at, and Fabrica usually runs behind a
		// reverse proxy that connects over localhost.
		DisableLocalhostProtection: true,
	})
	s.handlers[name] = h
	return h
}

// newSession starts a session for a factory, fixed to the blueprint
// commit the server has right now.
func (s *Remote) newSession(name string) *mcp.Server {
	rd := s.current().factories[name]
	if rd == nil {
		return nil
	}
	snap := &Snapshot{Mode: runs.ServedRemote, Factory: rd.factory, Blueprint: rd.blueprint, Refusal: rd.refusal}
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

func (b *remoteBackend) LatestFactory(_ context.Context, snap *Snapshot, _, _ string) (*home.Factory, string) {
	rd := b.server.current().factories[snap.factoryName()]
	if rd == nil {
		return nil, fmt.Sprintf("factory %s isn't in the home repo anymore", snap.factoryName())
	}
	return rd.factory, ""
}
