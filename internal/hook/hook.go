// Package hook runs local routines. `fabrica apply` installs one tool hook
// per local routine, and the tool runs `fabrica hook …` when the hook's
// event fires. It also installs a session pointer hook per tool, which
// tells every session where its own transcript is.
//
// A hook doesn't run the routine itself. It checks that the routine still
// applies to this workspace, and if so tells the agent to run it: call
// run_begin with the routine's stub, then load it with routine_get, just
// like a cloud stub. The Fabrica MCP server does the stale-stub checks.
//
// Hooks never block you because of Fabrica's own problems: when something
// is off (no git repo, no matching factory, no Fabrica line), the hook
// quietly does nothing, and the MCP server explains what's wrong.
package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/localtools"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/stub"
)

// Options describe one hook firing.
type Options struct {
	Runtime     string
	Factory     string
	Routine     string
	Fingerprint string
	Event       string
	// Session runs the session pointer hook instead of a routine's: only
	// Runtime is set.
	Session bool
	// HomeDir is your personal home repo.
	HomeDir string
	// MarkerDir remembers which pushes and commits were already held back
	// once, so the agent can go ahead after running the routine.
	MarkerDir string
	Stdin     io.Reader
	Stdout    io.Writer
	// Getenv reads environment variables (replaceable in tests).
	Getenv func(string) string
}

// input is what the tools send on stdin. Claude Code and Codex CLI use
// the same fields; Cursor and Copilot CLI have their own.
type input struct {
	SessionID      string   `json:"session_id"`
	ConversationID string   `json:"conversation_id"`
	CopilotSession string   `json:"sessionId"`
	TranscriptPath string   `json:"transcript_path"`
	Cwd            string   `json:"cwd"`
	WorkspaceRoots []string `json:"workspace_roots"`
	StopHookActive bool     `json:"stop_hook_active"`
	LoopCount      int      `json:"loop_count"`
	Status         string   `json:"status"`
	Command        string   `json:"command"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	// ToolArgs is Copilot CLI's tool input: a JSON object, encoded as a
	// string.
	ToolArgs json.RawMessage `json:"toolArgs"`
}

// command is the shell command a pre-tool hook is about.
func (in input) command() string {
	if in.ToolInput.Command != "" {
		return in.ToolInput.Command
	}
	if len(in.ToolArgs) > 0 {
		args := []byte(in.ToolArgs)
		var encoded string
		if json.Unmarshal(args, &encoded) == nil {
			args = []byte(encoded)
		}
		var v struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &v) == nil && v.Command != "" {
			return v.Command
		}
	}
	return in.Command
}

// Run handles one hook firing.
func Run(ctx context.Context, o Options) error {
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	// Cursor also runs the hooks in ~/.claude/settings.json. Fabrica gives
	// Cursor its own hooks, so the Claude Code ones stay quiet there and
	// nothing runs twice.
	if o.Runtime == "claude-code-local" && getenv("CURSOR_VERSION") != "" {
		return nil
	}
	var in input
	if data, err := io.ReadAll(o.Stdin); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &in)
	}
	// VS Code also runs the hooks in ~/.copilot/hooks, but Fabrica's MCP
	// server is only registered in Copilot CLI. Copilot CLI sends sessionId
	// with every event; VS Code sends its own session_id instead.
	if o.Runtime == "copilot-local" && in.CopilotSession == "" {
		return nil
	}

	dir := in.Cwd
	if len(in.WorkspaceRoots) > 0 {
		dir = in.WorkspaceRoots[0]
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	repo, top, err := product.RepoName(ctx, dir)
	if err != nil {
		return nil // not a repo Fabrica can serve
	}
	h, err := home.Open(o.HomeDir)
	if err != nil {
		return nil
	}
	d, _ := h.MatchLocal(repo, o.Runtime)
	if d == nil || !o.Session && (d.Name != o.Factory || !d.Runs(o.Routine, o.Runtime)) {
		return nil // no factory matches, or this routine doesn't apply here (anymore)
	}
	if ws, err := product.InspectDir(ctx, top); err != nil || !ws.HasLine() {
		return nil // the MCP server tells the agent what's missing
	}
	if o.Session {
		return sessionPointer(o, in)
	}
	prompt := stub.LocalPrompt(o.Routine, o.Factory, o.Runtime, repo, o.Fingerprint)

	switch o.Event {
	case localtools.SessionStart:
		if o.Runtime == "copilot-local" {
			return respond(o.Stdout, map[string]any{"additionalContext": prompt})
		}
		return respond(o.Stdout, claudeContext("SessionStart", prompt))
	case localtools.PromptSubmit:
		return respond(o.Stdout, claudeContext("UserPromptSubmit", prompt))
	case localtools.SessionStop:
		if o.Runtime == "cursor-local" {
			if in.LoopCount > 0 || in.Status != "" && in.Status != "completed" {
				return nil // already followed up once, or the agent didn't finish normally
			}
			return respond(o.Stdout, map[string]any{"followup_message": prompt})
		}
		if in.StopHookActive {
			return nil // we already made the agent continue once
		}
		return respond(o.Stdout, map[string]any{"decision": "block", "reason": prompt})
	case localtools.GitPreCommit, localtools.GitPrePush:
		cmd := in.command()
		want := "commit"
		if o.Event == localtools.GitPrePush {
			want = "push"
		}
		if !RunsGit(cmd, want) {
			return nil
		}
		// Hold the command back once per session and commit, so the agent
		// runs the routine and then tries again. A new commit gets reviewed
		// again.
		session := in.SessionID + in.ConversationID + in.CopilotSession
		marker := filepath.Join(o.MarkerDir, markerKey(o, session, headCommit(ctx, top)))
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
		if err := os.MkdirAll(o.MarkerDir, 0o755); err == nil {
			_ = os.WriteFile(marker, nil, 0o644)
		}
		reason := prompt + " When the routine is done, run the same git command again."
		if o.Runtime == "cursor-local" {
			return respond(o.Stdout, map[string]any{
				"permission":    "deny",
				"user_message":  "Fabrica is running routine " + o.Routine + " first.",
				"agent_message": reason,
			})
		}
		if o.Runtime == "copilot-local" {
			return respond(o.Stdout, map[string]any{"permissionDecision": "deny", "permissionDecisionReason": reason})
		}
		return respond(o.Stdout, map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}})
	}
	return nil
}

// sessionPointer tells the agent where its own transcript lives, so it can
// pass that to run_begin. The pointer is the transcript's path when the
// tool sends one, else the tool's name and session ID. Without either,
// there's nothing to say.
func sessionPointer(o Options, in input) error {
	pointer := in.TranscriptPath
	if pointer == "" {
		id := in.SessionID
		if o.Runtime == "copilot-local" {
			id = in.CopilotSession
		}
		if id == "" {
			return nil
		}
		pointer = o.Runtime + ":" + id
	}
	text := `Fabrica: when you call run_begin, pass session: "` + pointer + `".`
	if o.Runtime == "copilot-local" {
		return respond(o.Stdout, map[string]any{"additionalContext": text})
	}
	return respond(o.Stdout, claudeContext("SessionStart", text))
}

func claudeContext(event, text string) map[string]any {
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
}

func respond(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

var shellSeparators = regexp.MustCompile(`&&|\|\||[;|\n&]`)

// RunsGit reports whether a shell command line runs `git <sub>`, like
// `git push origin main` or `cd app && git -C . push`.
func RunsGit(cmd, sub string) bool {
	for _, segment := range shellSeparators.Split(cmd, -1) {
		fields := strings.Fields(segment)
		i := 0
		for i < len(fields) && strings.Contains(fields[i], "=") {
			i++ // environment assignments, like GIT_TRACE=1 git push
		}
		if i >= len(fields) || fields[i] != "git" && !strings.HasSuffix(fields[i], "/git") {
			continue // the command isn't git
		}
		// Skip git's global options, like -C <dir> or --no-pager.
		j := i + 1
		for j < len(fields) && strings.HasPrefix(fields[j], "-") {
			if fields[j] == "-C" || fields[j] == "-c" {
				j++
			}
			j++
		}
		if j < len(fields) && fields[j] == sub {
			return true
		}
	}
	return false
}

func headCommit(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func markerKey(o Options, session, head string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{o.Runtime, o.Factory, o.Routine, o.Event, session, head}, "\x00")))
	return hex.EncodeToString(sum[:16])
}
