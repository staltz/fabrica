package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/staltz/fabrica/internal/repourl"
)

// claude-code-local starts init sessions with Claude Code's command line,
// on the machine that runs `fabrica apply`: `claude -p` in a fresh clone of
// the repo, in the background, so apply doesn't wait for it. The session
// gets exactly the MCP servers the init needs (--strict-mcp-config), with
// the factory's own Fabrica server among them, so it's served by the same
// server as the factory's cloud stubs, whichever local factories you have.
//
// Nobody is there to answer permission prompts, so the session runs in
// Claude Code's auto permission mode, where a classifier approves what's
// safe and refuses the rest. The session ID is derived from the init's ID,
// so a retry never starts a second session.

// ClaudeBinEnv overrides the claude command that starts init sessions.
const ClaudeBinEnv = "FABRICA_CLAUDE_BIN"

type claudeLauncher struct {
	bin string
}

func newClaudeLauncher() (Launcher, error) {
	bin := strings.TrimSpace(os.Getenv(ClaudeBinEnv))
	if bin == "" {
		bin = "claude"
	}
	found, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("Claude Code's command line (%s) isn't installed here", bin)
	}
	return &claudeLauncher{bin: found}, nil
}

// Files in a session's folder.
const (
	claudeMCPConfig  = "mcp.json"
	claudeSessionLog = "session.log"
)

func (c *claudeLauncher) Launch(ctx context.Context, s Session) (Launched, error) {
	if s.Dir == "" {
		return Launched{}, errors.New("there's no folder for the session")
	}
	id := SessionUUID(s.ID)
	logPath := filepath.Join(s.Dir, claudeSessionLog)
	launched := Launched{SessionID: id, Log: logPath}
	if _, err := os.Stat(logPath); err == nil {
		return launched, nil // an earlier try started it already
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Launched{}, err
	}

	// A fresh clone of the repo to work in.
	checkout := filepath.Join(s.Dir, path.Base(s.Repo))
	if _, err := os.Stat(checkout); errors.Is(err, fs.ErrNotExist) {
		clone := exec.CommandContext(ctx, "git", "clone", "--quiet", repourl.GitURL(s.Repo), checkout)
		clone.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := clone.CombinedOutput(); err != nil {
			_ = os.RemoveAll(checkout)
			return Launched{}, fmt.Errorf("couldn't clone %s to start the session in: %v: %s", s.Repo, err, strings.TrimSpace(string(out)))
		}
	}

	// The MCP servers, in a file only you can read: it holds the factory's
	// token, which mustn't show up in the process list.
	config, err := claudeMCPConfigFor(s)
	if err != nil {
		return Launched{}, err
	}
	configPath := filepath.Join(s.Dir, claudeMCPConfig)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		return Launched{}, err
	}

	args := []string{"-p", s.Prompt,
		"--session-id", id,
		"--name", "Fabrica init " + s.Routine + " of factory " + s.Factory,
		"--mcp-config", configPath, "--strict-mcp-config",
		"--permission-mode", "auto",
	}
	if s.Model != nil {
		args = append(args, "--model", s.Model.Name)
		if s.Model.Effort != "" {
			args = append(args, "--effort", s.Model.Effort)
		}
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Launched{}, err
	}
	defer log.Close()
	// Not CommandContext: the session outlives apply.
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = checkout
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		log.Close()
		_ = os.Remove(logPath) // nothing started, so a retry may try again
		return Launched{}, fmt.Errorf("couldn't start %s: %v", c.bin, err)
	}
	_ = cmd.Process.Release()
	return launched, nil
}

// claudeMCPConfig is the --mcp-config file of a session: the factory's
// Fabrica server, and each connector's MCP server. Claude Code fills in
// ${NAME} from the environment, so connector secrets stay out of the file.
func claudeMCPConfigFor(s Session) ([]byte, error) {
	servers := map[string]any{}
	fabrica := map[string]any{"type": "http", "url": s.Fabrica.URL}
	if s.Fabrica.Placement == TokenInHeader {
		fabrica["headers"] = map[string]string{"Authorization": "Bearer " + s.Fabrica.Token}
	}
	servers["fabrica"] = fabrica
	for _, conn := range s.Connectors {
		if conn.Kind != "mcp" {
			return nil, fmt.Errorf("Claude Code has no built-in %s integration; give connectors/%s.yaml a fallback: MCP server", conn.Name, conn.Name)
		}
		srv := map[string]any{"type": conn.Transport}
		switch conn.Transport {
		case "stdio":
			srv["command"] = conn.Command
			if len(conn.Args) > 0 {
				srv["args"] = conn.Args
			}
			if len(conn.Env) > 0 {
				env := map[string]string{}
				for _, name := range conn.Env {
					env[name] = "${" + name + "}"
				}
				srv["env"] = env
			}
		default:
			srv["url"] = conn.URL
			if conn.Auth == "bearer" {
				srv["headers"] = map[string]string{"Authorization": "Bearer ${" + conn.Secret + "}"}
			}
		}
		servers[conn.Name] = srv
	}
	return json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
}
