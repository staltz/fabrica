package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/credentials"
)

// cursor-cloud starts init sessions as Cursor cloud agents, through
// Cursor's Cloud Agents API (POST /v1/agents), which is public and
// documented, unlike the Automations API that cursor.go drives. It takes
// the same API key, as HTTP basic auth, and a launch carries everything
// the agent needs: the prompt, the repos, the model, and the MCP servers
// inline, with their headers. So unlike automations, nothing has to be set
// up by hand in Cursor's settings.
//
// The caller can pick the agent's ID, and Cursor refuses a second agent
// with an ID it already has (409 agent_id_conflict). Fabrica derives the
// ID from the init's ID, so a retried launch finds the agent the first try
// started instead of starting another.

const (
	// CursorAgentsEndpointEnv overrides the Cloud Agents API's address.
	CursorAgentsEndpointEnv = "CURSOR_AGENTS_ENDPOINT"

	cursorAgentsDefaultEndpoint = "https://api.cursor.com"
	// cursorAgentNameMax is the longest name Cursor takes for an agent.
	cursorAgentNameMax = 100
	// cursorMaxRepos is the most repos one agent can start with.
	cursorMaxRepos = 20
)

type cursorLauncher struct {
	endpoint string
	key      string
	client   *http.Client
	wait     func(context.Context, time.Duration) error
}

func newCursorLauncher() (Launcher, error) {
	found, err := credentials.Lookup(CursorKeyEnv, CursorTokenEnv)
	if err != nil {
		return nil, err
	}
	if found.Value == "" {
		return nil, fmt.Errorf("%s isn't set", CursorKeyEnv)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv(CursorAgentsEndpointEnv)), "/")
	if endpoint == "" {
		endpoint = cursorAgentsDefaultEndpoint
	}
	return &cursorLauncher{endpoint: endpoint, key: found.Value, client: &http.Client{Timeout: time.Minute}, wait: sleep}, nil
}

type cursorAgentRequest struct {
	AgentID    string            `json:"agentId"`
	Name       string            `json:"name"`
	Prompt     cursorAgentPrompt `json:"prompt"`
	Repos      []cursorAgentRepo `json:"repos"`
	MCPServers []cursorAgentMCP  `json:"mcpServers"`
	Model      *cursorAgentModel `json:"model,omitempty"`
}

type cursorAgentPrompt struct {
	Text string `json:"text"`
}

type cursorAgentRepo struct {
	URL string `json:"url"`
}

type cursorAgentMCP struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type cursorAgentModel struct {
	ID     string                 `json:"id"`
	Params []cursorModelParameter `json:"params,omitempty"`
}

type cursorAgentResponse struct {
	Agent *struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"agent"`
}

func (c *cursorLauncher) Launch(ctx context.Context, s Session) (Launched, error) {
	req, err := cursorAgentFor(s)
	if err != nil {
		return Launched{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Launched{}, err
	}
	for attempt := 1; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/agents", bytes.NewReader(body))
		if err != nil {
			return Launched{}, err
		}
		httpReq.SetBasicAuth(c.key, "")
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "fabrica")
		resp, err := c.client.Do(httpReq)
		if err != nil {
			return Launched{}, fmt.Errorf("starting a Cursor cloud agent: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return Launched{}, fmt.Errorf("reading Cursor's answer to starting a cloud agent: %w", err)
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < cursorMaxAttempts:
			if wait := retryDelay(resp.Header, attempt, time.Now()); wait <= cursorMaxRetryWait {
				if err := c.wait(ctx, wait); err != nil {
					return Launched{}, err
				}
				continue
			}
		case resp.StatusCode == http.StatusConflict && strings.Contains(string(data), "agent_id_conflict"):
			// An earlier try started it already.
			return Launched{SessionID: req.AgentID, URL: cursorAgentURL(req.AgentID)}, nil
		case resp.StatusCode/100 == 2:
			var out cursorAgentResponse
			if err := json.Unmarshal(data, &out); err != nil || out.Agent == nil || out.Agent.ID == "" {
				// It may well be running; the next try finds it by its ID.
				return Launched{}, fmt.Errorf("Cursor's answer to starting cloud agent %s has no agent in it", req.AgentID)
			}
			url := out.Agent.URL
			if url == "" {
				url = cursorAgentURL(out.Agent.ID)
			}
			return Launched{SessionID: out.Agent.ID, URL: url}, nil
		}
		return Launched{}, fmt.Errorf("Cursor refused to start a cloud agent (HTTP %d: %s)", resp.StatusCode, snippet(data))
	}
}

func cursorAgentURL(id string) string { return "https://cursor.com/agents/" + id }

// cursorAgentFor builds the launch request for an init session.
func cursorAgentFor(s Session) (*cursorAgentRequest, error) {
	name := fmt.Sprintf("Fabrica init %s of factory %s", s.Routine, s.Factory)
	if len(name) > cursorAgentNameMax {
		name = name[:cursorAgentNameMax]
	}
	req := &cursorAgentRequest{AgentID: "bc-" + SessionUUID(s.ID), Name: name, Prompt: cursorAgentPrompt{Text: s.Prompt}}
	repos := s.Repos
	if len(repos) == 0 {
		repos = []string{s.Repo}
	}
	if len(repos) > cursorMaxRepos {
		repos = repos[:cursorMaxRepos] // the first is the one it starts in
	}
	for _, repo := range repos {
		req.Repos = append(req.Repos, cursorAgentRepo{URL: "https://" + repo})
	}

	fabrica := cursorAgentMCP{Name: "fabrica", Type: "http", URL: s.Fabrica.URL}
	if s.Fabrica.Placement == TokenInHeader {
		fabrica.Headers = map[string]string{"Authorization": "Bearer " + s.Fabrica.Token}
	}
	req.MCPServers = append(req.MCPServers, fabrica)
	for _, conn := range s.Connectors {
		if conn.Kind != "mcp" {
			if conn.Name == "github" {
				continue // a cloud agent works on its repos through GitHub already
			}
			return nil, fmt.Errorf("a Cursor cloud agent started through the API can't have Cursor's built-in %s integration; give connectors/%s.yaml a fallback: MCP server, or list another runtime first in the init routine's runtimes:", conn.Name, conn.Name)
		}
		srv, err := cursorAgentMCPFor(conn)
		if err != nil {
			return nil, err
		}
		req.MCPServers = append(req.MCPServers, srv)
	}

	sel, err := cursorModelSelectionFor(s.Model)
	if err != nil {
		return nil, err
	}
	if sel != nil {
		req.Model = &cursorAgentModel{ID: sel.ModelID, Params: sel.Parameters}
	}
	return req, nil
}

// cursorAgentMCPFor turns a connector into an inline MCP server. Bearer
// secrets are read from Fabrica's environment by name, since the agent
// gets them in the launch request.
func cursorAgentMCPFor(conn ResourceConnector) (cursorAgentMCP, error) {
	srv := cursorAgentMCP{Name: conn.Name, Type: conn.Transport, URL: conn.URL, Command: conn.Command, Args: conn.Args}
	switch conn.Auth {
	case "oauth":
		return srv, fmt.Errorf("connector %s signs in with OAuth, which a Cursor cloud agent started through the API can't do", conn.Name)
	case "bearer":
		value := os.Getenv(conn.Secret)
		if value == "" {
			return srv, fmt.Errorf("connector %s needs its secret %s, to give it to the Cursor cloud agent; set %s where you run `fabrica apply`", conn.Name, conn.Secret, conn.Secret)
		}
		srv.Headers = map[string]string{"Authorization": "Bearer " + value}
	}
	if len(conn.Env) > 0 {
		srv.Env = map[string]string{}
		for _, name := range conn.Env {
			value := os.Getenv(name)
			if value == "" {
				return srv, fmt.Errorf("connector %s passes %s through, to give it to the Cursor cloud agent; set %s where you run `fabrica apply`", conn.Name, name, name)
			}
			srv.Env[name] = value
		}
	}
	return srv, nil
}
