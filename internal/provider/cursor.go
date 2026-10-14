package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/staltz/fabrica/internal/credentials"
)

// In api mode, the cursor-cloud runtime talks to Cursor's Automations API:
// the API behind Cursor's own automations, and the one Cursor's Terraform
// provider (github.com/cursor/terraform-provider-cursor) is a client for.
// Cursor doesn't document it otherwise, so everything here follows that
// provider: the service at https://api2.cursor.sh, its four calls (create,
// get, update and delete; there's no list), how an API key is exchanged
// for a session token, and the shape of an automation. The Terraform
// provider speaks Connect RPC with the binary protobuf encoding; Fabrica
// speaks the same protocol with its JSON encoding, so it needs neither
// Terraform nor generated code.

const (
	// CursorKeyEnv holds the Cursor API key cursor-cloud uses in api mode.
	// Without it, cursor-cloud falls back to handoffs.
	CursorKeyEnv = "CURSOR_API_KEY"
	// CursorTokenEnv is what Cursor's Terraform provider calls the same
	// key. Fabrica reads it when CursorKeyEnv isn't set.
	CursorTokenEnv = "CURSOR_TOKEN"
	// CursorEndpointEnv overrides the API's address, like the Terraform
	// provider's variable of the same name.
	CursorEndpointEnv = "CURSOR_ENDPOINT"

	cursorDefaultEndpoint = "https://api2.cursor.sh"
	cursorService         = "/aiserver.v1.AutomationsService/"
	cursorMaxAttempts     = 5
	cursorMaxRetryWait    = 2 * time.Minute
)

// CursorMCPServerName is the name of the Fabrica MCP server that a
// factory's Cursor automations use. Automations can only refer to MCP
// servers by name, from Cursor's MCP settings, and each factory has its own
// address and token, so each gets its own server.
func CursorMCPServerName(factory string) string { return "fabrica-" + factory }

// cursorAPI drives Cursor automations. It's a Getter: Cursor's API can
// read an automation back by its ID, but can't list them.
type cursorAPI struct {
	endpoint string
	key      string
	client   *http.Client
	// wait sleeps before a retry.
	wait func(context.Context, time.Duration) error

	mu      sync.Mutex
	session string // the bearer token, once the key is exchanged
}

// newCursorAPI makes the client from the key in CURSOR_API_KEY (or
// CURSOR_TOKEN), or else the one `fabrica auth login` saved.
func newCursorAPI() (RuntimeAPI, error) {
	found, err := credentials.Lookup(CursorKeyEnv, CursorTokenEnv)
	if err != nil {
		return nil, err
	}
	if found.Value == "" {
		return nil, fmt.Errorf("%s isn't set", CursorKeyEnv)
	}
	return newCursorClient(found.Value), nil
}

func newCursorClient(key string) *cursorAPI {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv(CursorEndpointEnv)), "/")
	if endpoint == "" {
		endpoint = cursorDefaultEndpoint
	}
	return &cursorAPI{endpoint: endpoint, key: key, client: &http.Client{Timeout: 30 * time.Second}, wait: sleep}
}

// verifyCursorKey checks a Cursor API key by exchanging it for a session
// token, the first thing every apply does with it.
func verifyCursorKey(ctx context.Context, key string) (string, error) {
	if !isCursorAPIKey(key) {
		return "", ErrUnverifiable // already a session token; there's nothing to exchange
	}
	if _, err := newCursorClient(key).sessionToken(ctx); err != nil {
		return "", err
	}
	return "Cursor accepted it", nil
}

// isCursorAPIKey reports whether key is an API key (key_… or crsr_…),
// rather than a session token.
func isCursorAPIKey(key string) bool {
	return strings.HasPrefix(key, "key_") || strings.HasPrefix(key, "crsr_")
}

func (c *cursorAPI) Create(ctx context.Context, res Resource) (string, error) {
	wf, err := cursorWorkflowFor(res)
	if err != nil {
		return "", err
	}
	desc := cursorDescription(res)
	var resp cursorAutomationResponse
	err = c.call(ctx, "CreateAutomation", cursorCreateRequest{Name: res.Identity, Description: &desc, Workflow: wf}, &resp)
	if err != nil {
		if cursorCreateRejected(err) {
			return "", RejectedCreate(err)
		}
		return "", err
	}
	if resp.Workflow == nil || resp.Workflow.Workflow == nil || resp.Workflow.Workflow.AutomationID == "" {
		// A success with no ID means Cursor may already have the automation.
		// Leaving this uncertain lets apply ask, instead of creating another.
		return "", fmt.Errorf("Cursor's answer to creating %s has no automation ID", res.Identity)
	}
	return resp.Workflow.Workflow.AutomationID, nil
}

func (c *cursorAPI) Update(ctx context.Context, remoteID string, res Resource) error {
	wf, err := cursorWorkflowFor(res)
	if err != nil {
		return err
	}
	name, desc := res.Identity, cursorDescription(res)
	return c.call(ctx, "UpdateAutomation", cursorUpdateRequest{AutomationID: remoteID, Name: &name, Description: &desc, Workflow: wf}, nil)
}

func (c *cursorAPI) Delete(ctx context.Context, _, remoteID string) error {
	err := c.call(ctx, "DeleteAutomation", cursorIDRequest{AutomationID: remoteID}, nil)
	if isCursorNotFound(err) {
		return nil // already gone
	}
	return err
}

func (c *cursorAPI) Get(ctx context.Context, remoteID string) (*RemoteResource, error) {
	var resp struct {
		Workflow          *cursorAutomationWithOwner `json:"workflow"`
		RestrictedSummary *struct {
			Name      string `json:"name"`
			OwnerName string `json:"ownerName"`
		} `json:"restrictedSummary"`
	}
	err := c.call(ctx, "GetAutomation", cursorIDRequest{AutomationID: remoteID}, &resp)
	if isCursorNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s := resp.RestrictedSummary; s != nil {
		return nil, fmt.Errorf("the Cursor API key can see that automation %s (%q, owned by %s) exists, but can't read it; use the key of its owner or of a team admin", remoteID, s.Name, s.OwnerName)
	}
	if resp.Workflow == nil || resp.Workflow.Workflow == nil {
		return nil, fmt.Errorf("Cursor returned an empty automation for %s", remoteID)
	}
	a := resp.Workflow.Workflow
	r := &RemoteResource{Identity: a.Name, RemoteID: remoteID}
	if a.Workflow != nil {
		for _, p := range a.Workflow.Prompts {
			if m := stubFingerprintRe.FindStringSubmatch(p.Prompt); m != nil {
				r.Fingerprint = m[1]
				break
			}
		}
	}
	return r, nil
}

// stubFingerprintRe finds the stub fingerprint in a cloud stub's prompt,
// which says "… and stub `e128f7aa2995`, …" (see stub.Prompt). It's the
// fingerprint the Fabrica server checks on every run, so it's the one that
// says which version of the stub is installed.
var stubFingerprintRe = regexp.MustCompile("stub `([0-9a-f]+)`")

// Setup describes the MCP servers a factory's Cursor automations refer
// to. Cursor's API can't create them: an automation only names an MCP
// server, and Cursor looks it up in the MCP settings of whoever owns the
// automation.
func (c *cursorAPI) Setup(factory string, resources []Resource) string {
	servers := map[string]string{}
	for _, res := range resources {
		f := res.Fabrica
		line := fmt.Sprintf("HTTP, URL `%s`", f.URL)
		if f.Placement == TokenInHeader {
			line += fmt.Sprintf(", header `Authorization: Bearer %s`", f.Token)
		}
		servers[CursorMCPServerName(factory)] = line
		for _, conn := range res.Connectors {
			if conn.Kind == "mcp" {
				servers[conn.Name] = describeCursorMCPServer(conn)
			}
		}
	}
	if len(servers) == 0 {
		return ""
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "# MCP servers for factory `%s`'s Cursor automations\n\n", factory)
	b.WriteString("Fabrica created or updated Cursor automations through Cursor's API, but the API can't set up MCP servers: an automation only names them, and Cursor looks them up in the MCP settings of the automation's owner (whoever owns the Cursor API key Fabrica used).\n\n")
	b.WriteString("So make sure Cursor's MCP settings have each of these, under exactly this name:\n\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- **%s**: %s\n", name, servers[name])
	}
	fmt.Fprintf(&b, "\nYou set these up once. Come back when this file changes: Fabrica rewrites it whenever an apply creates or updates the factory's Cursor automations, like after the server secret is rotated (which changes the token) or the factory's `server:` changes.\n")
	return b.String()
}

func describeCursorMCPServer(conn ResourceConnector) string {
	parts := []string{fmt.Sprintf("%s, URL `%s`", strings.ToUpper(conn.Transport), conn.URL)}
	switch conn.Auth {
	case "oauth":
		parts = append(parts, "signing in with OAuth when Cursor asks")
	case "bearer":
		parts = append(parts, fmt.Sprintf("header `Authorization: Bearer <the %s secret>`", conn.Secret))
	}
	return strings.Join(parts, ", ")
}

// cursorDescription is the automation's description in Cursor.
func cursorDescription(res Resource) string {
	return fmt.Sprintf("Fabrica stub for routine %s of factory %s. `fabrica apply` manages it and overwrites changes made here.", res.Routine, res.Factory)
}

// The JSON shapes of Cursor's API. Field names follow the Connect JSON
// encoding of aiserver.v1's protobuf messages (lowerCamelCase), and
// Fabrica sends only the fields it sets.

type cursorCreateRequest struct {
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Workflow    *cursorWorkflow `json:"workflow"`
}

type cursorUpdateRequest struct {
	AutomationID string          `json:"automationId"`
	Name         *string         `json:"name,omitempty"`
	Description  *string         `json:"description,omitempty"`
	Workflow     *cursorWorkflow `json:"workflow,omitempty"`
}

type cursorIDRequest struct {
	AutomationID string `json:"automationId"`
}

type cursorAutomationResponse struct {
	Workflow *cursorAutomationWithOwner `json:"workflow"`
}

type cursorAutomationWithOwner struct {
	Workflow *cursorAutomation `json:"workflow"`
}

type cursorAutomation struct {
	AutomationID string          `json:"automationId"`
	Name         string          `json:"name"`
	Workflow     *cursorWorkflow `json:"workflow"`
}

type cursorWorkflow struct {
	Triggers      []cursorTrigger  `json:"triggers"`
	Actions       []cursorAction   `json:"actions,omitempty"`
	Prompts       []cursorPrompt   `json:"prompts"`
	GitConfig     *cursorGitConfig `json:"gitConfig,omitempty"`
	MemoryEnabled *bool            `json:"memoryEnabled,omitempty"`
	// ModelSelection is the model the automation runs on. Without it,
	// Cursor picks (Auto).
	ModelSelection *cursorModelSelection `json:"modelSelection,omitempty"`
}

type cursorModelSelection struct {
	ModelID    string                 `json:"modelId"`
	Parameters []cursorModelParameter `json:"parameters,omitempty"`
}

type cursorModelParameter struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

type cursorPrompt struct {
	Prompt string `json:"prompt"`
}

type cursorGitConfig struct {
	Repo  string   `json:"repo,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

type cursorTrigger struct {
	Cron *cursorCron `json:"cron,omitempty"`
	Git  *cursorGit  `json:"git,omitempty"`
}

type cursorCron struct {
	Cron string `json:"cron"`
}

type cursorGit struct {
	PullRequest *cursorPullRequest `json:"pullRequest,omitempty"`
	Push        *cursorPush        `json:"push,omitempty"`
	Label       *cursorLabel       `json:"label,omitempty"`
}

type cursorPullRequest struct {
	Repos    []string `json:"repos"`
	PRAction string   `json:"prAction"`
}

type cursorPush struct {
	Repo string `json:"repo"`
}

type cursorLabel struct {
	Repos        []string `json:"repos"`
	OnAdded      bool     `json:"onAdded"`
	PullRequests bool     `json:"pullRequests"`
}

type cursorAction struct {
	PRComment *struct{}    `json:"prComment,omitempty"`
	Slack     *cursorSlack `json:"slack,omitempty"`
	MCP       *cursorMCP   `json:"mcp,omitempty"`
}

type cursorSlack struct {
	// Generalized lets the agent post to any channel. Routines name their
	// channels in params, which are served live, so the stub can't.
	Generalized bool `json:"generalized"`
}

type cursorMCP struct {
	Server cursorMCPServer `json:"server"`
}

type cursorMCPServer struct {
	Name string `json:"name"`
}

// cursorPRActions maps Fabrica's pull request triggers to Cursor's.
// Cursor's "opened" also fires when a draft is marked ready for review.
var cursorPRActions = map[string]string{
	"github.pull_request.opened":      "GIT_PULL_REQUEST_ACTION_OPENED",
	"github.pull_request.synchronize": "GIT_PULL_REQUEST_ACTION_PUSHED",
}

// cursorModelVariants is the default variant of each model in Cursor's
// model catalog (GET /v1/models, October 2026), by Cursor's model ID.
// Cursor only takes a model with a whole variant, every parameter of it
// set ("Model 'grok-4.6' does not match a known variant" otherwise), so a
// stub's model gets its default variant, with the effort parameter
// (effort, reasoning or reasoning_effort, whichever the model has) set to
// the stub's effort. That's a known variant for every effort the catalog
// lists. Everything else (context, fast, thinking, cyber) stays as Cursor
// sets it by default.
var cursorModelVariants = map[string]map[string]string{
	"claude-fable-5":    {"context": "1m", "thinking": "true", "effort": "high"},
	"claude-fable-5-1":  {"context": "1m", "thinking": "true", "effort": "high"},
	"claude-haiku-4-5":  {"thinking": "true"},
	"claude-haiku-5-5":  {"context": "1m", "thinking": "true", "reasoning_effort": "high"},
	"claude-opus-4-5":   {"thinking": "true"},
	"claude-opus-4-6":   {"context": "1m", "thinking": "true", "effort": "high"},
	"claude-opus-4-7":   {"context": "1m", "thinking": "true", "cyber": "false", "effort": "xhigh", "fast": "false"},
	"claude-opus-4-8":   {"context": "1m", "thinking": "true", "cyber": "false", "effort": "high", "fast": "false"},
	"claude-opus-5":     {"context": "1m", "thinking": "true", "cyber": "false", "effort": "high", "fast": "false"},
	"claude-opus-5-5":   {"context": "1m", "effort": "medium", "fast": "false"},
	"claude-sonnet-4":   {"context": "200k", "thinking": "false"},
	"claude-sonnet-4-5": {"context": "200k", "thinking": "true"},
	"claude-sonnet-4-6": {"context": "1m", "thinking": "true", "effort": "medium"},
	"claude-sonnet-5":   {"context": "1m", "thinking": "true", "effort": "high"},
	"claude-sonnet-5-5": {"context": "1m", "reasoning_effort": "high"},
	"composer-2.5":      {"fast": "true"},
	"gemini-2.5-flash":  {},
	"gemini-3-flash":    {},
	"gemini-3.1-pro":    {},
	"gemini-3.5-flash":  {},
	"gemini-3.6-flash":  {"effort": "high"},
	"gemini-3.7-flash":  {"effort": "high"},
	"gemini-3.8-flash":  {"reasoning_effort": "high"},
	"gemini-4-argon":    {"reasoning_effort": "medium"},
	"glm-5.2":           {"reasoning": "high"},
	"glm-5p3":           {"reasoning": "max"},
	"glm-5p3-flash":     {"reasoning": "max"},
	"gpt-5-mini":        {},
	"gpt-5.1":           {"reasoning": "medium"},
	"gpt-5.2":           {"reasoning": "high", "fast": "true"},
	"gpt-5.3-codex":     {"reasoning": "high", "fast": "true"},
	"gpt-5.4":           {"context": "1m", "reasoning": "medium", "fast": "false"},
	"gpt-5.4-mini":      {"reasoning": "medium"},
	"gpt-5.4-nano":      {"reasoning": "medium"},
	"gpt-5.5":           {"context": "1m", "reasoning": "medium", "fast": "false"},
	"gpt-5.6-luna":      {"context": "1m", "reasoning": "medium", "fast": "false"},
	"gpt-5.6-sol":       {"context": "1m", "reasoning": "medium", "fast": "false"},
	"gpt-5.6-terra":     {"context": "1m", "reasoning": "medium", "fast": "false"},
	"grok-4.5":          {"effort": "high", "fast": "true"},
	"grok-4.6":          {"effort": "high", "fast": "true"},
	"grok-4.7":          {"context": "500k", "reasoning_effort": "high", "fast": "true"},
	"kimi-k2.7-code":    {},
	"kimi-k3":           {"reasoning": "max"},
	"muse-spark-1.3":    {"context": "1m", "effort": "high"},
}

// cursorEffortParamNames are what Cursor's models call their effort
// parameter. Each model has at most one of them.
var cursorEffortParamNames = []string{"effort", "reasoning", "reasoning_effort"}

// cursorEffortParam is the name of a model's effort parameter in its
// variant, or empty if it has none.
func cursorEffortParam(variant map[string]string) string {
	for _, name := range cursorEffortParamNames {
		if _, ok := variant[name]; ok {
			return name
		}
	}
	return ""
}

// cursorExtraHigh lists the models whose catalog calls xhigh "extra-high".
var cursorExtraHigh = map[string]bool{
	"gpt-5.2": true, "gpt-5.3-codex": true, "gpt-5.4": true, "gpt-5.5": true,
}

// cursorModelSelectionFor is the model selection for a stub's model: a
// whole variant of it, from cursorModelVariants.
func cursorModelSelectionFor(m *ResourceModel) (*cursorModelSelection, error) {
	if m == nil {
		return nil, nil
	}
	variant, ok := cursorModelVariants[m.Name]
	if !ok {
		return nil, fmt.Errorf("Fabrica doesn't know the variants of Cursor's model %s, so it can't pick one", m.Name)
	}
	params := make(map[string]string, len(variant))
	for id, value := range variant {
		params[id] = value
	}
	if m.Effort != "" {
		param, value := cursorEffortParam(variant), m.Effort
		if param == "" {
			return nil, fmt.Errorf("Cursor's model %s has no effort setting, so it can't run at effort %s", m.Name, m.Effort)
		}
		if value == "xhigh" && cursorExtraHigh[m.Name] {
			value = "extra-high"
		}
		params[param] = value
	}
	sel := &cursorModelSelection{ModelID: m.Name}
	for id, value := range params {
		sel.Parameters = append(sel.Parameters, cursorModelParameter{ID: id, Value: value})
	}
	sort.Slice(sel.Parameters, func(i, j int) bool { return sel.Parameters[i].ID < sel.Parameters[j].ID })
	return sel, nil
}

// cursorWorkflowFor builds the automation for a stub: its trigger on the
// stub's repo, the Fabrica MCP server and the stub's connectors as
// actions, the runtime's memory setting, its model, and the stub prompt.
func cursorWorkflowFor(res Resource) (*cursorWorkflow, error) {
	wf := &cursorWorkflow{
		Prompts: []cursorPrompt{{Prompt: res.Prompt}},
		// The repo goes both ways the Terraform provider sends it: as the
		// repo non-git triggers run in, and in the list of repos.
		GitConfig: &cursorGitConfig{Repo: res.Repo, Repos: []string{res.Repo}},
	}
	repo := cursorTriggerRepo(res.Repo)
	on := res.Trigger.On
	switch {
	case on == "schedule":
		if res.Trigger.Cron == "" {
			return nil, errors.New("a schedule trigger needs a cron expression")
		}
		wf.Triggers = []cursorTrigger{{Cron: &cursorCron{Cron: res.Trigger.Cron}}}
	case cursorPRActions[on] != "":
		wf.Triggers = []cursorTrigger{{Git: &cursorGit{PullRequest: &cursorPullRequest{Repos: []string{repo}, PRAction: cursorPRActions[on]}}}}
	case on == "github.pull_request.labeled":
		wf.Triggers = []cursorTrigger{{Git: &cursorGit{Label: &cursorLabel{Repos: []string{repo}, OnAdded: true, PullRequests: true}}}}
	case on == "github.push":
		wf.Triggers = []cursorTrigger{{Git: &cursorGit{Push: &cursorPush{Repo: repo}}}}
	default:
		return nil, fmt.Errorf("Cursor automations can't be triggered by %s", on)
	}

	wf.Actions = []cursorAction{{MCP: &cursorMCP{Server: cursorMCPServer{Name: CursorMCPServerName(res.Factory)}}}}
	for _, conn := range res.Connectors {
		switch {
		case conn.Kind == "mcp":
			wf.Actions = append(wf.Actions, cursorAction{MCP: &cursorMCP{Server: cursorMCPServer{Name: conn.Name}}})
		case conn.Name == "github":
			// Reading the repo comes with it; commenting on pull requests
			// is a tool to turn on.
			wf.Actions = append(wf.Actions, cursorAction{PRComment: &struct{}{}})
		case conn.Name == "slack":
			wf.Actions = append(wf.Actions, cursorAction{Slack: &cursorSlack{Generalized: true}})
		default:
			return nil, fmt.Errorf("Cursor's API has no built-in %s integration to give an automation", conn.Name)
		}
	}

	switch res.Memory {
	case "on", "off":
		enabled := res.Memory == "on"
		wf.MemoryEnabled = &enabled
	}
	sel, err := cursorModelSelectionFor(res.Model)
	if err != nil {
		return nil, err
	}
	wf.ModelSelection = sel
	return wf, nil
}

// cursorTriggerRepo writes a repo the way Cursor's git triggers take it:
// owner/name for GitHub, the full address elsewhere.
func cursorTriggerRepo(repo string) string {
	if rest, ok := strings.CutPrefix(repo, "github.com/"); ok {
		return rest
	}
	return repo
}

// call makes one Connect RPC call with the JSON encoding. resp may be nil
// when the answer doesn't matter.
func (c *cursorAPI) call(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	session, err := c.sessionToken(ctx)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+cursorService+method, bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Connect-Protocol-Version", "1")
		httpReq.Header.Set("Authorization", "Bearer "+session)
		httpReq.Header.Set("User-Agent", "fabrica")
		httpResp, err := c.client.Do(httpReq)
		if err != nil {
			return fmt.Errorf("calling Cursor's %s: %w", method, err)
		}
		data, err := io.ReadAll(io.LimitReader(httpResp.Body, 8<<20))
		httpResp.Body.Close()
		if err != nil {
			return fmt.Errorf("reading Cursor's answer to %s: %w", method, err)
		}
		if httpResp.StatusCode == http.StatusTooManyRequests && attempt < cursorMaxAttempts {
			if wait := retryDelay(httpResp.Header, attempt, time.Now()); wait <= cursorMaxRetryWait {
				if err := c.wait(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		if httpResp.StatusCode != http.StatusOK {
			return newCursorError(method, httpResp.StatusCode, data)
		}
		if resp == nil {
			return nil
		}
		if err := json.Unmarshal(data, resp); err != nil {
			return fmt.Errorf("Cursor's answer to %s isn't the JSON Fabrica expected: %v", method, err)
		}
		return nil
	}
}

// sessionToken returns the bearer token to call the API with. A raw API
// key (key_… or crsr_…) is exchanged for a session token first, once;
// anything else is already a token.
func (c *cursorAPI) sessionToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" {
		return c.session, nil
	}
	if !isCursorAPIKey(c.key) {
		c.session = c.key
		return c.session, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/auth/exchange_user_api_key", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fabrica")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging %s for a Cursor session token: %w", CursorKeyEnv, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Cursor refused to exchange %s for a session token (HTTP %d: %s); check that it's a valid Cursor API key", CursorKeyEnv, resp.StatusCode, snippet(data))
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &out); err != nil || strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("Cursor's answer to exchanging %s for a session token has no access token", CursorKeyEnv)
	}
	c.session = strings.TrimSpace(out.AccessToken)
	return c.session, nil
}

// retryDelay is how long to wait before retrying a rate-limited call: what
// the API asks for, or exponential backoff.
func retryDelay(h http.Header, attempt int, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if at, err := http.ParseTime(v); err == nil {
			return max(at.Sub(now), 0)
		}
	}
	if strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if wait := time.Unix(reset, 0).Sub(now); wait > 0 {
				return wait
			}
		}
	}
	return min(time.Second<<(attempt-1), 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// cursorError is an error answer from Cursor's API.
type cursorError struct {
	Method string
	Status int
	// Code is the Connect error code, like "not_found". It's empty when the
	// answer wasn't a Connect error at all.
	Code    string
	Message string
}

func (e *cursorError) Error() string {
	return fmt.Sprintf("Cursor's %s failed: %s", e.Method, e.Message)
}

// cursorCreateRejected reports whether Cursor's answer proves the
// automation was not created. A code the server uses when the handler
// may already have committed (or a lost connection, which isn't a
// cursorError at all) is not a rejection: the automation may exist, and
// this API can't list them to check.
func cursorCreateRejected(err error) bool {
	var ce *cursorError
	if !errors.As(err, &ce) || ce.Code == "" {
		return false
	}
	switch ce.Code {
	case "invalid_argument", "not_found", "permission_denied", "unauthenticated",
		"resource_exhausted", "failed_precondition", "out_of_range", "unimplemented":
		return true
	default:
		return false
	}
}

// isCursorNotFound reports whether Cursor said the automation doesn't
// exist. Only a Connect not_found counts: a bare HTTP 404 more likely
// means CURSOR_ENDPOINT is wrong, and treating that as "gone" would make
// Fabrica forget automations that still exist.
func isCursorNotFound(err error) bool {
	var ce *cursorError
	return errors.As(err, &ce) && ce.Code == "not_found"
}

// newCursorError reads a Connect error. Cursor's own message is usually
// just "Error": the real one is in an aiserver.v1.ErrorDetails detail.
func newCursorError(method string, status int, body []byte) error {
	var connectErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"details"`
	}
	e := &cursorError{Method: method, Status: status}
	if json.Unmarshal(body, &connectErr) != nil || connectErr.Code == "" {
		e.Message = fmt.Sprintf("HTTP %d: %s", status, snippet(body))
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			e.Message += fmt.Sprintf(" (check %s)", CursorKeyEnv)
		}
		return e
	}
	e.Code = connectErr.Code
	for _, d := range connectErr.Details {
		if d.Type != "aiserver.v1.ErrorDetails" {
			continue
		}
		raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(d.Value, "="))
		if err != nil {
			continue
		}
		if title, detail := errorDetailsText(raw); title != "" || detail != "" {
			parts := []string{e.Code}
			for _, s := range []string{title, detail} {
				if s != "" && s != parts[len(parts)-1] {
					parts = append(parts, s)
				}
			}
			e.Message = strings.Join(parts, ": ")
			return e
		}
	}
	e.Message = e.Code
	if connectErr.Message != "" && connectErr.Message != "Error" {
		e.Message += ": " + connectErr.Message
	}
	return e
}

// errorDetailsText pulls the title and detail out of an encoded
// aiserver.v1.ErrorDetails, whose field 2 is a message holding the title
// (field 1) and the detail (field 2). It's the only protobuf Fabrica reads,
// so a few lines of wire format beat a dependency.
func errorDetailsText(b []byte) (title, detail string) {
	custom, ok := protoField(b, 2)
	if !ok {
		return "", ""
	}
	t, _ := protoField(custom, 1)
	d, _ := protoField(custom, 2)
	return string(t), string(d)
}

// protoField returns the first length-delimited field with the given
// number in an encoded protobuf message.
func protoField(b []byte, want uint64) ([]byte, bool) {
	for len(b) > 0 {
		tag, n := protoVarint(b)
		if n == 0 {
			return nil, false
		}
		b = b[n:]
		num, wire := tag>>3, tag&7
		switch wire {
		case 0:
			_, n = protoVarint(b)
			if n == 0 {
				return nil, false
			}
			b = b[n:]
		case 1, 5:
			size := 8
			if wire == 5 {
				size = 4
			}
			if len(b) < size {
				return nil, false
			}
			b = b[size:]
		case 2:
			size, n := protoVarint(b)
			if n == 0 || uint64(len(b)-n) < size {
				return nil, false
			}
			value := b[n : n+int(size)]
			if num == want {
				return value, true
			}
			b = b[n+int(size):]
		default:
			return nil, false
		}
	}
	return nil, false
}

// protoVarint reads a varint, returning how many bytes it took (0 when
// it's broken).
func protoVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// snippet shortens an answer body for an error message.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		s = "(empty)"
	}
	return s
}
