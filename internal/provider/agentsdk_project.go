package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// An Agent SDK project is a folder of TypeScript and Markdown that Cursor's
// Agent SDK (the @grok/agent-sdk package, formerly @cursor/bdk) discovers
// and deploys. Fabrica writes one per cursor-agent-sdk stub, as small as it
// can be, since everything else is served live:
//
//	package.json                     the package, pinning the Agent SDK
//	bot/agent.ts                     defineAgent: name, model, egress, secret names
//	bot/instructions.md              the stub prompt
//	bot/mcp-connections/fabrica.ts   the factory's Fabrica server, token from a secret
//	bot/mcp-connections/<name>.ts    one per MCP connector
//	bot/schedules/fabrica-<n>.md     one per schedule trigger
//	bot/channels/github.ts           the pull request triggers, if any
//	bot/extensions/cloud.ts          Cursor cloud agents, on the stub's repo
//
// A hosted agent has no checkout of the repo it works on, so the cloud
// agents extension is how it gets one: it hands coding work to a Cursor
// cloud agent on the stub's repo (and only that repo), and the extension's
// own skill teaches the model when and how.

// AgentSDKVersion is the Agent SDK release the projects are written for and
// pinned to.
const AgentSDKVersion = "0.6.0"

// agentSDKTokenSecret is the deployment secret holding the factory's token.
const agentSDKTokenSecret = "FABRICA_TOKEN"

// agentSDKProject is one stub's project, and what deploying it needs.
type agentSDKProject struct {
	// Files are the project's files, by their path inside the project.
	Files map[string]string
	// Secrets are the deployment secrets to set, by name. They hold the
	// factory's token and connector secrets, so they never go into Files.
	Secrets map[string]string
	// EventRepos are the repos whose events the deployment watches, as
	// owner/name.
	EventRepos []string
}

// AgentSDKSlug is the deployment slug of a stub: its routine, so people can
// tell deployments apart in Cursor's dashboard, and a hash of its identity,
// so each stub gets its own. Slugs are lowercase letters, digits, _ and -,
// up to 64 characters.
func AgentSDKSlug(identity, routine string) string {
	sum := sha256.Sum256([]byte(identity))
	name := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(routine), "-"), "-")
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "-")
	}
	if name == "" {
		name = "stub"
	}
	return "fab-" + name + "-" + hex.EncodeToString(sum[:5])
}

var (
	slugUnsafe       = regexp.MustCompile(`[^a-z0-9_-]+`)
	agentSDKSecretRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// agentSDKPRActions maps the pull request triggers to the GitHub actions
// that fire them.
var agentSDKPRActions = map[string]string{
	"github.pull_request.opened":           "opened",
	"github.pull_request.reopened":         "reopened",
	"github.pull_request.synchronize":      "synchronize",
	"github.pull_request.ready_for_review": "ready_for_review",
	"github.pull_request.closed":           "closed",
	"github.pull_request.labeled":          "labeled",
}

// agentSDKProjectFor writes the project of a stub. Connector secrets are
// read from Fabrica's environment by name, since the deployment needs their
// values.
func agentSDKProjectFor(slug string, res Resource) (*agentSDKProject, error) {
	p := &agentSDKProject{Files: map[string]string{}, Secrets: map[string]string{}}
	egress := map[string]bool{}

	// The Fabrica server.
	if res.Fabrica.Placement != TokenInHeader {
		return nil, fmt.Errorf("an Agent SDK deployment reaches its Fabrica server with the token in a header, not in the URL")
	}
	host, err := agentSDKHost(res.Fabrica.URL)
	if err != nil {
		return nil, fmt.Errorf("the Fabrica server's address: %w", err)
	}
	egress[host] = true
	p.Secrets[agentSDKTokenSecret] = res.Fabrica.Token
	p.Files["bot/mcp-connections/fabrica.ts"] = agentSDKConnection(res.Fabrica.URL, agentSDKTokenSecret)

	// The connectors, each an MCP server.
	for _, conn := range res.Connectors {
		if conn.Kind != "mcp" {
			return nil, fmt.Errorf("an Agent SDK deployment has no built-in %s integration; give connectors/%s.yaml a fallback: MCP server", conn.Name, conn.Name)
		}
		if conn.Name == "fabrica" {
			return nil, fmt.Errorf("connector %s: the Agent SDK connection called fabrica is the Fabrica server", conn.Name)
		}
		switch conn.Transport {
		case "http", "sse":
		default:
			return nil, fmt.Errorf("connector %s is a %s MCP server, and an Agent SDK deployment can only reach http and sse ones", conn.Name, conn.Transport)
		}
		host, err := agentSDKHost(conn.URL)
		if err != nil {
			return nil, fmt.Errorf("connector %s: %w", conn.Name, err)
		}
		egress[host] = true
		secret := ""
		switch conn.Auth {
		case "oauth":
			return nil, fmt.Errorf("connector %s signs in with OAuth, which Fabrica can't do for an Agent SDK deployment; give it a bearer secret, or list another runtime for the routine", conn.Name)
		case "bearer":
			secret = conn.Secret
			if !agentSDKSecretRe.MatchString(secret) || strings.HasPrefix(secret, "CURSOR_") || secret == agentSDKTokenSecret {
				return nil, fmt.Errorf("connector %s: its secret %s can't be an Agent SDK deployment secret, whose names are UPPER_SNAKE_CASE, not starting with CURSOR_ and not %s", conn.Name, secret, agentSDKTokenSecret)
			}
			value := os.Getenv(secret)
			if value == "" {
				return nil, fmt.Errorf("connector %s needs its secret %s, to set it on the Agent SDK deployment; set %s where you run `fabrica apply`", conn.Name, secret, secret)
			}
			p.Secrets[secret] = value
		}
		p.Files["bot/mcp-connections/"+conn.Name+".ts"] = agentSDKConnection(conn.URL, secret)
	}

	// The triggers.
	var actions []string
	schedules := 0
	for _, t := range res.Triggers {
		switch action, ok := agentSDKPRActions[t.On]; {
		case t.On == "schedule":
			schedules++
			p.Files[fmt.Sprintf("bot/schedules/fabrica-%d.md", schedules)] = fmt.Sprintf(
				"---\ncron: %s\n---\n\nYour schedule (cron `%s`, in UTC) fired. Run your routine now, as your instructions say.\n", js(t.Cron), t.Cron)
		case ok:
			actions = append(actions, action)
		default:
			return nil, fmt.Errorf("an Agent SDK deployment has no %s trigger", t.On)
		}
	}
	if len(actions) > 0 {
		owner, name, ok := githubRepo(res.Repo)
		if !ok {
			return nil, fmt.Errorf("pull request triggers come through the Cursor account's GitHub connection, so they only work on github.com repos, not %s", res.Repo)
		}
		sort.Strings(actions)
		p.EventRepos = []string{owner + "/" + name}
		p.Files["bot/channels/github.ts"] = agentSDKGitHubChannel(owner+"/"+name, actions)
	}

	// The model, as one whole variant of it, like cursor-cloud's.
	model := ""
	if sel, err := cursorModelSelectionFor(res.Model); err != nil {
		return nil, err
	} else if sel != nil {
		params := sel.Parameters
		if params == nil {
			params = []cursorModelParameter{}
		}
		model = js(map[string]any{"id": sel.ModelID, "params": params})
	}

	hosts := make([]string, 0, len(egress))
	for h := range egress {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	secrets := make([]string, 0, len(p.Secrets))
	for name := range p.Secrets {
		secrets = append(secrets, name)
	}
	sort.Strings(secrets)

	p.Files["package.json"] = agentSDKPackage(slug, res)
	p.Files["bot/agent.ts"] = agentSDKAgent(res, model, hosts, secrets)
	p.Files["bot/instructions.md"] = strings.TrimRight(res.Prompt, "\n") + "\n"
	p.Files["bot/extensions/cloud.ts"] = agentSDKCloudExtension(res.Repo)
	return p, nil
}

const agentSDKHeader = "// Written by Fabrica (`fabrica apply`), which rewrites it on every change.\n"

func agentSDKPackage(slug string, res Resource) string {
	pkg := map[string]any{
		"name":         slug,
		"private":      true,
		"type":         "module",
		"description":  fmt.Sprintf("Fabrica stub of routine %s for %s. Written by `fabrica apply`, which rewrites it on every change.", res.Routine, res.Repo),
		"dependencies": map[string]string{"@grok/agent-sdk": AgentSDKVersion},
	}
	out, _ := json.MarshalIndent(pkg, "", "  ")
	return string(out) + "\n"
}

func agentSDKAgent(res Resource, model string, hosts, secrets []string) string {
	var b strings.Builder
	b.WriteString(agentSDKHeader)
	b.WriteString("import { defineAgent } from \"@grok/agent-sdk\";\n\nexport default defineAgent({\n")
	fmt.Fprintf(&b, "  name: %s,\n", js(res.Identity))
	fmt.Fprintf(&b, "  description: %s,\n", js(fmt.Sprintf("Fabrica routine %s on %s, from factory %s.", res.Routine, res.Repo, res.Factory)))
	if model != "" {
		fmt.Fprintf(&b, "  model: %s,\n", model)
	}
	fmt.Fprintf(&b, "  hosting: {\n    egressDomains: %s,\n    secretNames: %s,\n  },\n});\n", js(hosts), js(secrets))
	return b.String()
}

// agentSDKConnection is an MCP connection to a server at url, with a bearer
// token from the deployment secret called secret, if there's one. The
// ${NAME} template is filled in when the connection opens, so the value
// never shows in the project.
func agentSDKConnection(url, secret string) string {
	var b strings.Builder
	b.WriteString(agentSDKHeader)
	b.WriteString("import { defineConnection } from \"@grok/agent-sdk/connections\";\n\nexport default defineConnection({\n")
	fmt.Fprintf(&b, "  url: %s,\n", js(url))
	if secret != "" {
		fmt.Fprintf(&b, "  headers: { Authorization: %s },\n", js("Bearer ${"+secret+"}"))
	}
	b.WriteString("});\n")
	return b.String()
}

// agentSDKGitHubChannel wakes the agent on the pull request actions of one
// repo, through the Cursor account's GitHub connection. Comments never
// wake it, and it posts nothing back on its own: routines say what to post,
// through their connectors.
func agentSDKGitHubChannel(repo string, actions []string) string {
	var b strings.Builder
	b.WriteString(agentSDKHeader)
	b.WriteString("import { defaultGitHubAuth, githubChannel } from \"@grok/agent-sdk/channels/github\";\n\n")
	fmt.Fprintf(&b, "const actions: ReadonlySet<string> = new Set(%s);\n\n", js(actions))
	b.WriteString("export default githubChannel({\n")
	fmt.Fprintf(&b, "  cursorAccount: { repos: %s },\n", js([]string{repo}))
	b.WriteString("  deliverReplies: false,\n")
	b.WriteString("  onComment: () => null,\n")
	b.WriteString("  onPullRequest: (ctx, pr) => (actions.has(pr.action) ? { auth: defaultGitHubAuth(ctx) } : null),\n")
	b.WriteString("});\n")
	return b.String()
}

func agentSDKCloudExtension(repo string) string {
	var b strings.Builder
	b.WriteString(agentSDKHeader)
	b.WriteString("import cursorCloudAgents from \"@grok/agent-sdk/extensions/cursor-cloud-agents\";\n\n")
	b.WriteString("// The agent has no checkout of its own, so it hands coding work on its\n// repo to a Cursor cloud agent.\n")
	b.WriteString("export default cursorCloudAgents({\n")
	fmt.Fprintf(&b, "  cloud: { repos: [{ url: %s }] },\n", js("https://"+repo))
	b.WriteString("});\n")
	return b.String()
}

// agentSDKHost is the egress entry for a server: its lowercase hostname.
func agentSDKHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q isn't an http(s) URL", raw)
	}
	return strings.ToLower(u.Hostname()), nil
}

// githubRepo splits github.com/owner/name.
func githubRepo(repo string) (owner, name string, ok bool) {
	parts := strings.Split(repo, "/")
	if len(parts) != 3 || parts[0] != "github.com" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// js writes v as a JavaScript literal. JSON is one.
func js(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out)
}
