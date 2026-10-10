package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/runs"
)

// LocalOptions configure `fabrica mcp`.
type LocalOptions struct {
	// Runtime is the local tool this server is registered in, like
	// claude-code-local. Deployments are matched per runtime.
	Runtime string
	// HomeDir is your personal home repo.
	HomeDir string
	// Workspace, when set, is the workspace to serve. Otherwise the server
	// asks the client for its roots, and falls back to the project folder
	// the tool says (CLAUDE_PROJECT_DIR) or the working directory.
	Workspace  string
	Git        *gitx.Cache
	Loader     *factory.Loader
	Runs       *runs.DB
	SkillCache string
}

type localBackend struct {
	opts LocalOptions
	// initial is the snapshot worked out at startup, before the client has
	// told us anything. It decides the MCP instructions.
	initial    *Snapshot
	initialTop string
}

// NewLocal prepares the local server and works out its first snapshot.
func NewLocal(ctx context.Context, opts LocalOptions) (*Core, string) {
	b := &localBackend{opts: opts}
	dir := opts.Workspace
	if dir == "" {
		dir = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	b.initial = b.resolveDir(ctx, dir)
	b.initialTop = toplevel(ctx, dir)
	core := &Core{Runs: opts.Runs, Backend: b}

	instructions := Instructions(b.initial)
	switch {
	case b.initial.Refusal != "" && b.initialTop == "" && opts.Workspace == "":
		// We don't know the workspace yet; the client may tell us its
		// roots on the first tool call.
		instructions = Instructions(&Snapshot{Mode: runs.ServedLocal}) +
			"\n\nIf Fabrica can't serve this workspace, its tools will say why; tell the user if that happens."
	case b.initial.Refusal != "":
		instructions = RefusalInstructions(b.initial.Refusal)
	}
	return core, instructions
}

// RunLocal serves MCP over stdio until the client goes away.
func RunLocal(ctx context.Context, opts LocalOptions) error {
	core, instructions := NewLocal(ctx, opts)
	return core.NewServer(instructions).Run(ctx, &mcp.StdioTransport{})
}

// Resolve prefers the workspace the client reports through MCP roots (the
// first root is the main workspace). The session keeps whatever this
// returns, so the factory commit stays fixed for the whole session.
func (b *localBackend) Resolve(ctx context.Context, ss *mcp.ServerSession) *Snapshot {
	if b.opts.Workspace != "" || ss == nil {
		return b.initial
	}
	dir := firstRoot(ctx, ss)
	if dir == "" {
		return b.initial
	}
	if top := toplevel(ctx, dir); top != "" && top == b.initialTop {
		return b.initial
	}
	return b.resolveDir(ctx, dir)
}

// firstRoot asks the client for its roots and returns the first local
// folder, or "".
func firstRoot(ctx context.Context, ss *mcp.ServerSession) string {
	// MCP roots are deprecated as of protocol 2026-07-28, but they still
	// work during the deprecation window, and Claude Code and Cursor send
	// them. When a client stops, we fall back to the project folder.
	params := ss.InitializeParams()
	//lint:ignore SA1019 see above
	if params == nil || params.Capabilities == nil || params.Capabilities.RootsV2 == nil {
		return ""
	}
	//lint:ignore SA1019 see above
	res, err := ss.ListRoots(ctx, nil)
	if err != nil || len(res.Roots) == 0 {
		return ""
	}
	u, err := url.Parse(res.Roots[0].URI)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return filepath.FromSlash(u.Path)
}

func toplevel(ctx context.Context, dir string) string {
	wt, err := gitx.OpenWorktree(ctx, dir)
	if err != nil {
		return ""
	}
	return wt.Dir
}

// resolveDir works out what to serve for a workspace folder, refusing
// (with a reason) whenever something is wrong.
func (b *localBackend) resolveDir(ctx context.Context, dir string) *Snapshot {
	snap := &Snapshot{Mode: runs.ServedLocal, Runtime: b.opts.Runtime, SkillCache: b.opts.SkillCache}
	refuse := func(format string, args ...any) *Snapshot {
		snap.Refusal = fmt.Sprintf(format, args...)
		return snap
	}
	repo, top, err := product.RepoName(ctx, dir)
	if err != nil {
		return refuse("%v", err)
	}
	snap.Repo = repo
	h, err := home.Open(b.opts.HomeDir)
	if err != nil {
		return refuse("couldn't read your home repo: %v", err)
	}
	d, why := h.MatchLocal(repo, b.opts.Runtime)
	if d == nil {
		return refuse("%s", why)
	}
	snap.Deployment = d
	ws, err := product.InspectDir(ctx, top)
	if err != nil {
		return refuse("couldn't inspect the workspace at %s: %v", top, err)
	}
	ws.Repo = repo
	snap.Workspace = ws
	if !ws.HasLine() {
		return refuse("%s. %s", strings.Join(ws.LineProblems, "; "), LineHint())
	}
	resolved, err := b.opts.Git.Remote(repourl.GitURL(d.Factory.Git), gitx.Full).Resolve(ctx, d.Factory.Ref)
	if err != nil {
		return refuse("couldn't reach the factory of deployment %s: %v", d.Name, err)
	}
	f, diags := b.opts.Loader.LoadGit(ctx, d.Factory.Git, resolved.Commit, d.Factory.Path)
	if errs := diags.Only(diag.Error); len(errs) > 0 || f == nil {
		return refuse("the factory of deployment %s (%s at %s) has problems: %s. Run `fabrica check` in the factory repo to see them all",
			d.Name, d.Factory, gitx.Short(resolved.Commit), firstLines(errs, 3))
	}
	snap.Factory = f
	snap.Clashes = product.Clashes(ws, f)
	return snap
}

// LatestDeployment re-reads your home repo: if you changed or removed the
// local deployment since the session started, stubs notice right away.
func (b *localBackend) LatestDeployment(ctx context.Context, snap *Snapshot, repo, runtime string) (*home.Deployment, string) {
	h, err := home.Open(b.opts.HomeDir)
	if err != nil {
		return nil, err.Error()
	}
	d, why := h.MatchLocal(repo, runtime)
	if d == nil {
		return nil, why
	}
	if d.Name != snap.deploymentName() {
		return nil, fmt.Sprintf("this workspace is now served by deployment %s, not %s", d.Name, snap.deploymentName())
	}
	return d, ""
}

func firstLines(diags diag.List, n int) string {
	var out []string
	for i, d := range diags {
		if i == n {
			out = append(out, fmt.Sprintf("and %d more", len(diags)-n))
			break
		}
		out = append(out, d.Where+": "+d.Message)
	}
	return strings.Join(out, "; ")
}
