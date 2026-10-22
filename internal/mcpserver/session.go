// Package mcpserver is the Fabrica MCP server, the only way blueprint content
// reaches agents.
//
// One binary, two modes:
//
//   - `fabrica mcp` (local) talks stdio to Claude Code, Cursor or Codex on
//     your machine. It picks the blueprint from your personal home repo's
//     local factories by matching the workspace's git remote, checks the
//     workspace (the Fabrica line, skill clashes), and unpacks skill folders
//     onto disk when they're loaded.
//   - `fabrica mcp --http` (remote) serves cloud runtimes over HTTP, one
//     endpoint per cloud factory of one home repo.
//
// Each MCP session is served from one blueprint commit, fixed when the
// session starts, so a merge that lands mid-run never changes what that run
// sees.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
)

// Version is reported to MCP clients. The CLI sets it at startup.
var Version = "dev"

// Snapshot is what one session serves.
type Snapshot struct {
	// Mode is runs.ServedLocal or runs.ServedRemote.
	Mode      string
	Blueprint *blueprint.Blueprint
	Factory   *home.Factory
	// Repo and Runtime are known up front locally (from the workspace and
	// the tool). Remotely they come from each run's stub.
	Repo    string
	Runtime string
	// Workspace is the inspected product repo (local only).
	Workspace *product.Workspace
	// Refusal, when set, is why Fabrica won't serve this session at all.
	Refusal string
	// Clashes are repo skills with the same name as blueprint skills. While
	// there are any, run_begin and skill_load refuse.
	Clashes []string
	// SkillCache is where skill folders get unpacked (local only).
	SkillCache string
}

// BlueprintSHA is the exact commit served.
func (s *Snapshot) BlueprintSHA() string {
	if s.Blueprint == nil {
		return ""
	}
	return s.Blueprint.Source.Commit
}

func (s *Snapshot) remote() bool { return s.Mode == runs.ServedRemote }

func (s *Snapshot) factoryName() string {
	if s.Factory == nil {
		return ""
	}
	return s.Factory.Name
}

// Backend is what differs between the local and the remote server.
type Backend interface {
	// Resolve works out the session's snapshot. It's called once, on the
	// session's first tool call; the local server uses the MCP session to
	// ask the client for its workspace roots.
	Resolve(ctx context.Context, ss *mcp.ServerSession) *Snapshot
	// LatestFactory returns the served factory as it is in the latest
	// home repo right now, or nil and a reason if it's gone. Stale-stub
	// checks compare against this, never against state.
	LatestFactory(ctx context.Context, snap *Snapshot, repo, runtime string) (*home.Factory, string)
}

// Core holds what every session shares.
type Core struct {
	Runs    *runs.DB
	Backend Backend
	Now     func() time.Time
	// ProtocolVersions, when set, limits the MCP protocol versions the
	// server offers. Empty means every version the SDK supports.
	ProtocolVersions []string
}

func (c *Core) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// session is one MCP session's state.
type session struct {
	core *Core

	once sync.Once
	snap *Snapshot

	mu sync.Mutex
	// run is the session's current run, set by run_begin. Loads of skills,
	// rules and routines are linked to it.
	run *runs.Run
	// began are routines whose run_begin passed in this session.
	began map[string]*runs.Run
	// refused are routines whose run_begin was refused in this session;
	// routine_get won't serve them.
	refused map[string]string
}

// NewServer builds an MCP server for one session. instructions are the
// MCP instructions sent to the client when it connects.
func (c *Core) NewServer(instructions string) *mcp.Server {
	s := &session{core: c, began: map[string]*runs.Run{}, refused: map[string]string{}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fabrica", Title: "Fabrica", Version: Version}, &mcp.ServerOptions{
		Instructions: instructions,
		// Empty capabilities turn logging off. Tools is set so the server
		// advertises them; ListChanged stays false because the tool list
		// never changes mid-session. Advertising listChanged makes some
		// cloud clients wait for a GET SSE stream instead of calling tools/list.
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		SupportedProtocolVersions: c.ProtocolVersions,
	})
	s.register(srv)
	return srv
}

// snapshot resolves the session's snapshot on first use.
func (s *session) snapshot(ctx context.Context, ss *mcp.ServerSession) *Snapshot {
	s.once.Do(func() { s.snap = s.core.Backend.Resolve(ctx, ss) })
	return s.snap
}

func readOnly() *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &f}
}

func writes() *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{DestructiveHint: &f, OpenWorldHint: &f}
}

func (s *session) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "skills_list",
		Description: "List the names and descriptions of all skills in this repo's blueprint. Load the relevant ones with skill_load.",
		Annotations: readOnly(),
	}, s.skillsList)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "skill_load",
		Description: "Load one skill: its SKILL.md and the list of its files. Relative paths in a skill resolve against the skill's folder.",
		Annotations: readOnly(),
	}, s.skillLoad)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "skill_file",
		Description: "Read one text file from a skill, by its path inside the skill's folder.",
		Annotations: readOnly(),
	}, s.skillFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "rules_get",
		Description: "Get every always-on working rule in full, plus an index of the other rules. Follow them for the whole session.",
		Annotations: readOnly(),
	}, s.rulesGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "rule_load",
		Description: "Load one rule by name, when it applies (see the index from rules_get).",
		Annotations: readOnly(),
	}, s.ruleLoad)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "routine_get",
		Description: "Get a routine's full definition: its instructions, skills, allowed side effects, params and the working rules. Routine stubs call this right after run_begin.",
		Annotations: readOnly(),
	}, s.routineGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_save",
		Description: "Replace a routine's memory: the notes it keeps for its own next runs on this repo, which routine_get shows. Only for routines whose routine_get says Fabrica keeps their memory.",
		Annotations: writes(),
	}, s.memorySave)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "run_begin",
		Description: "Start a run and get its run_id. Call it first, in every session. For a routine run, pass routine, runtime, repo and stub exactly as your stub says. If you know where this session's transcript lives, pass it as session.",
		Annotations: writes(),
	}, s.runBegin)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "run_report",
		Description: "Record a checkpoint for a run: what you did (at most 20 words), its status (progress, succeeded or failed) and any links (PRs, issues, commits). Add friction for anything that slowed you down.",
		Annotations: writes(),
	}, s.runReport)
}

// refusal returns the error for a session Fabrica won't serve.
func refusal(snap *Snapshot) error {
	if snap.Refusal == "" {
		return nil
	}
	return fmt.Errorf("Fabrica can't serve this session: %s", snap.Refusal)
}

// clashError is the error while repo skills clash with blueprint skills.
func clashError(snap *Snapshot) error {
	if len(snap.Clashes) == 0 {
		return nil
	}
	return fmt.Errorf("this repo has its own skills with the same names as blueprint skills (%s), and Fabrica won't pick one; rename or remove the repo's skills, or ask the blueprint's author to add them to expects:. Tell the user", strings.Join(snap.Clashes, ", "))
}

// runtimeProvider finds the provider for a runtime: built in, or a folder
// provider from the blueprint.
func runtimeProvider(f *blueprint.Blueprint, name string) (*provider.Provider, bool) {
	if p, ok := provider.Builtin(name); ok {
		return p, true
	}
	if f != nil {
		if item := f.Providers[name]; item != nil && item.Provider != nil {
			return item.Provider, true
		}
	}
	return nil, false
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
