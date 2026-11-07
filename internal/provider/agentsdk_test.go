package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/testkit"
)

func agentSDKResource() Resource {
	return Resource{
		Identity: "k7q2#pr-review@github.com/acme/app",
		Factory:  "pr-review", Routine: "pr-review", Repo: "github.com/acme/app",
		Triggers: []ResourceTrigger{{On: "github.pull_request.synchronize"}, {On: "github.pull_request.opened"}, {On: "schedule", Cron: "0 9 * * 1-5"}},
		Connectors: []ResourceConnector{
			{Name: "linear", Kind: "mcp", URL: "https://mcp.linear.app/mcp", Transport: "http", Auth: "bearer", Secret: "LINEAR_TOKEN"},
			{Name: "docs", Kind: "mcp", URL: "https://Docs.Example.com:8443/sse", Transport: "sse"},
		},
		Fabrica:     ResourceFabrica{URL: "https://fabrica.acme.dev/pr-review", Placement: TokenInHeader, Token: "fab_tok"},
		Model:       &ResourceModel{Name: "claude-sonnet-4-6", Effort: "max"},
		Prompt:      "You are routine `pr-review`. Call `run_begin` with … and stub `e128f7aa2995`, and an intent …",
		Fingerprint: "e128f7aa2995",
	}
}

func TestAgentSDKSlug(t *testing.T) {
	slugRe := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	a := AgentSDKSlug("k7q2#pr-review@github.com/acme/app", "pr-review")
	if a != AgentSDKSlug("k7q2#pr-review@github.com/acme/app", "pr-review") {
		t.Error("the slug should only depend on the identity and routine")
	}
	if !strings.HasPrefix(a, "fab-pr-review-") || !slugRe.MatchString(a) {
		t.Errorf("slug %q", a)
	}
	if b := AgentSDKSlug("k7q2#pr-review@github.com/acme/api", "pr-review"); b == a {
		t.Error("two stubs got the same slug")
	}
	long := AgentSDKSlug("x", strings.Repeat("very-long-routine-name-", 10))
	if !slugRe.MatchString(long) {
		t.Errorf("long slug %q (%d characters)", long, len(long))
	}
}

func TestAgentSDKProject(t *testing.T) {
	t.Setenv("LINEAR_TOKEN", "lin_secret")
	p, err := agentSDKProjectFor("fab-pr-review-0123456789", agentSDKResource())
	if err != nil {
		t.Fatal(err)
	}
	names := slices.Sorted(maps.Keys(p.Files))
	want := []string{
		"bot/agent.ts", "bot/channels/github.ts", "bot/extensions/cloud.ts", "bot/instructions.md",
		"bot/mcp-connections/docs.ts", "bot/mcp-connections/fabrica.ts", "bot/mcp-connections/linear.ts",
		"bot/schedules/fabrica-1.md", "package.json",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", names, want)
	}
	for name, content := range p.Files {
		for _, secret := range []string{"fab_tok", "lin_secret"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s holds the secret %s; secrets go to the deployment, never into git", name, secret)
			}
		}
	}

	checks := map[string]string{
		"bot/agent.ts": agentSDKHeader + `import { defineAgent } from "@grok/agent-sdk";

export default defineAgent({
  name: "k7q2#pr-review@github.com/acme/app",
  description: "Fabrica routine pr-review on github.com/acme/app, from factory pr-review.",
  model: {"id":"claude-sonnet-4-6","params":[{"id":"context","value":"1m"},{"id":"effort","value":"max"},{"id":"thinking","value":"true"}]},
  hosting: {
    egressDomains: ["docs.example.com","fabrica.acme.dev","mcp.linear.app"],
    secretNames: ["FABRICA_TOKEN","LINEAR_TOKEN"],
  },
});
`,
		"bot/mcp-connections/fabrica.ts": agentSDKHeader + `import { defineConnection } from "@grok/agent-sdk/connections";

export default defineConnection({
  url: "https://fabrica.acme.dev/pr-review",
  headers: { Authorization: "Bearer ${FABRICA_TOKEN}" },
});
`,
		"bot/channels/github.ts": agentSDKHeader + `import { defaultGitHubAuth, githubChannel } from "@grok/agent-sdk/channels/github";

const actions: ReadonlySet<string> = new Set(["opened","synchronize"]);

export default githubChannel({
  cursorAccount: { repos: ["acme/app"] },
  deliverReplies: false,
  onComment: () => null,
  onPullRequest: (ctx, pr) => (actions.has(pr.action) ? { auth: defaultGitHubAuth(ctx) } : null),
});
`,
		"bot/schedules/fabrica-1.md": "---\ncron: \"0 9 * * 1-5\"\n---\n\nYour schedule (cron `0 9 * * 1-5`, in UTC) fired. Run your routine now, as your instructions say.\n",
		"bot/instructions.md":        agentSDKResource().Prompt + "\n",
	}
	for name, want := range checks {
		if got := p.Files[name]; got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
	if got := p.Files["bot/mcp-connections/docs.ts"]; strings.Contains(got, "headers") {
		t.Errorf("a connector without auth got headers:\n%s", got)
	}
	if got := p.Files["bot/extensions/cloud.ts"]; !strings.Contains(got, `cloud: { repos: [{ url: "https://github.com/acme/app" }] }`) {
		t.Errorf("the cloud agents extension should be scoped to the stub's repo:\n%s", got)
	}
	var pkg struct {
		Name         string            `json:"name"`
		Type         string            `json:"type"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(p.Files["package.json"]), &pkg); err != nil || pkg.Name != "fab-pr-review-0123456789" || pkg.Type != "module" || pkg.Dependencies["@grok/agent-sdk"] != AgentSDKVersion {
		t.Errorf("package.json = %s (%v)", p.Files["package.json"], err)
	}
	if p.Secrets["FABRICA_TOKEN"] != "fab_tok" || p.Secrets["LINEAR_TOKEN"] != "lin_secret" || len(p.Secrets) != 2 {
		t.Errorf("secrets = %v", p.Secrets)
	}
	if strings.Join(p.EventRepos, ",") != "acme/app" {
		t.Errorf("event repos = %v", p.EventRepos)
	}

	// A schedule-only stub has no channel, no event repos, and no model
	// when the routine leaves it to the runtime.
	res := agentSDKResource()
	res.Triggers, res.Connectors, res.Model = []ResourceTrigger{{On: "schedule", Cron: "0 3 * * *"}, {On: "schedule", Cron: "0 4 * * *"}}, nil, nil
	p, err = agentSDKProjectFor("s", res)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Files["bot/channels/github.ts"]; ok || len(p.EventRepos) != 0 {
		t.Error("a stub without pull request triggers got a GitHub channel")
	}
	if !strings.Contains(p.Files["bot/schedules/fabrica-2.md"], `cron: "0 4 * * *"`) {
		t.Errorf("second schedule: %q", p.Files["bot/schedules/fabrica-2.md"])
	}
	if strings.Contains(p.Files["bot/agent.ts"], "model:") {
		t.Errorf("no model was asked for, so agent.ts should leave the Agent SDK's default:\n%s", p.Files["bot/agent.ts"])
	}
}

func TestAgentSDKProjectRejects(t *testing.T) {
	t.Setenv("LINEAR_TOKEN", "")
	cases := map[string]func(*Resource){
		"native connector": func(r *Resource) { r.Connectors = []ResourceConnector{{Name: "github", Kind: "native"}} },
		"oauth connector": func(r *Resource) {
			r.Connectors = []ResourceConnector{{Name: "x", Kind: "mcp", URL: "https://x.dev/mcp", Transport: "http", Auth: "oauth"}}
		},
		"stdio connector": func(r *Resource) {
			r.Connectors = []ResourceConnector{{Name: "x", Kind: "mcp", Transport: "stdio", Command: "x"}}
		},
		"missing secret":   func(r *Resource) {},
		"reserved secret":  func(r *Resource) { r.Connectors[0].Secret = "CURSOR_KEY" },
		"token in the URL": func(r *Resource) { r.Connectors = nil; r.Fabrica.Placement = TokenInURL },
		"push trigger":     func(r *Resource) { r.Connectors = nil; r.Triggers = []ResourceTrigger{{On: "github.push"}} },
		"not on github": func(r *Resource) {
			r.Connectors, r.Repo = nil, "gitlab.com/acme/app"
		},
	}
	for name, change := range cases {
		res := agentSDKResource()
		res.Connectors = res.Connectors[:1]
		change(&res)
		if _, err := agentSDKProjectFor("s", res); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// fakeAgentSDK records the agent-sdk commands an agentSDKAPI runs, and
// answers them like a Cursor team with the deployments in deployed.
type fakeAgentSDK struct {
	calls    []string
	stdin    map[string]string
	deployed map[string]bool
	failOn   string
}

func (f *fakeAgentSDK) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if stdin != nil {
		f.stdin[call] = string(stdin)
	}
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return nil, &agentSDKCommandError{command: f.failOn, code: "failed", message: "it broke", exit: 1}
	}
	switch {
	case args[0] == "deploy":
		f.deployed[flagValue(args, "--slug")] = true
	case strings.HasPrefix(call, "deployment delete "):
		if !f.deployed[args[2]] {
			return nil, &agentSDKCommandError{command: "deployment delete", code: "not_found", exit: agentSDKNotFound}
		}
		delete(f.deployed, args[2])
	case strings.HasPrefix(call, "deployment list"):
		var list []map[string]string
		for slug := range f.deployed {
			list = append(list, map[string]string{"slug": slug, "status": "running"})
		}
		out, _ := json.Marshal(map[string]any{"deployments": list, "nextPageToken": nil})
		return out, nil
	}
	return []byte("{}"), nil
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newFakeAgentSDKAPI(t *testing.T) (*agentSDKAPI, *fakeAgentSDK, *testkit.HomeRepo) {
	t.Helper()
	host := testkit.NewHost(t)
	home := host.Home("github.com/acme/home")
	// Like a real checkout: the remote is a GitHub URL (which the test
	// host rewrites to a folder).
	testkit.Git(t, home.Dir, "remote", "set-url", "origin", "https://github.com/acme/home.git")
	fake := &fakeAgentSDK{stdin: map[string]string{}, deployed: map[string]bool{}}
	api := &agentSDKAPI{cache: t.TempDir(), run: fake.run}
	api.UseHomeRepo(home.Dir)
	return api, fake, home
}

func TestAgentSDKAPILifecycle(t *testing.T) {
	t.Setenv(AgentSDKRepoEnv, "")
	t.Setenv("LINEAR_TOKEN", "lin_secret")
	api, fake, home := newFakeAgentSDKAPI(t)
	ctx := context.Background()
	res := agentSDKResource()

	slug, err := api.Create(ctx, res)
	if err != nil {
		t.Fatal(err)
	}
	if slug != AgentSDKSlug(res.Identity, res.Routine) {
		t.Errorf("remote ID %q, want the slug", slug)
	}

	// The project is on the branch, and the deploy builds that commit.
	branch := "refs/heads/" + AgentSDKBranch
	files := testkit.Git(t, home.Bare, "ls-tree", "-r", "--name-only", branch)
	for _, want := range []string{"README.md", slug + "/bot/agent.ts", slug + "/package.json", "deployed/" + slug + ".json"} {
		if !strings.Contains(files, want+"\n") {
			t.Errorf("branch %s has no %s:\n%s", AgentSDKBranch, want, files)
		}
	}
	projectCommit := strings.TrimSpace(testkit.Git(t, home.Bare, "rev-parse", branch+"~1"))
	deploy := fake.calls[0]
	for _, want := range []string{
		"deploy --dir ", "--repo https://github.com/acme/home --ref " + projectCommit + " --path " + slug + " --slug " + slug,
		"--cursor-events-repo acme/app --json",
	} {
		if !strings.Contains(deploy, want) {
			t.Errorf("deploy command %q lacks %q", deploy, want)
		}
	}
	if fake.stdin["secret set "+slug+" FABRICA_TOKEN --json"] != "fab_tok" || fake.stdin["secret set "+slug+" LINEAR_TOKEN --json"] != "lin_secret" {
		t.Errorf("secrets set: %v", fake.stdin)
	}
	if log := testkit.Git(t, home.Bare, "log", "--format=%s", "main"); strings.TrimSpace(log) != "init" {
		t.Errorf("the home repo's own branch changed:\n%s", log)
	}

	// List reads identities back, under the prefix only.
	list, err := api.List(ctx, "pr-review", "k7q2#")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != (RemoteResource{Identity: res.Identity, RemoteID: slug, Fingerprint: "e128f7aa2995"}) {
		t.Errorf("List = %+v", list)
	}
	if list, _ := api.List(ctx, "pr-review", "zzzz#"); len(list) != 0 {
		t.Errorf("List under another prefix = %+v", list)
	}

	// An update deploys again under the same slug.
	res.Fingerprint, res.Prompt = "f00dfeed0000", "new prompt"
	if err := api.Update(ctx, slug, res); err != nil {
		t.Fatal(err)
	}
	if list, _ := api.List(ctx, "pr-review", "k7q2#"); len(list) != 1 || list[0].Fingerprint != "f00dfeed0000" {
		t.Errorf("List after update = %+v", list)
	}
	if got := testkit.Git(t, home.Bare, "show", branch+":"+slug+"/bot/instructions.md"); got != "new prompt\n" {
		t.Errorf("instructions after update = %q", got)
	}

	// A deployment deleted by hand isn't listed.
	delete(fake.deployed, slug)
	if list, _ := api.List(ctx, "pr-review", "k7q2#"); len(list) != 0 {
		t.Errorf("List after a deployment was deleted by hand = %+v", list)
	}
	fake.deployed[slug] = true

	// Delete removes the deployment and its project. Deleting it again is
	// fine.
	if err := api.Delete(ctx, "pr-review", slug); err != nil {
		t.Fatal(err)
	}
	if fake.deployed[slug] {
		t.Error("the deployment is still there")
	}
	files = testkit.Git(t, home.Bare, "ls-tree", "-r", "--name-only", branch)
	if strings.Contains(files, slug) {
		t.Errorf("the project is still on the branch:\n%s", files)
	}
	if err := api.Delete(ctx, "pr-review", slug); err != nil {
		t.Errorf("deleting a deployment that's gone: %v", err)
	}
}

func TestAgentSDKAPIFailedDeployIsNotRecorded(t *testing.T) {
	t.Setenv(AgentSDKRepoEnv, "")
	t.Setenv("LINEAR_TOKEN", "lin_secret")
	api, fake, home := newFakeAgentSDKAPI(t)
	ctx := context.Background()
	fake.failOn = "deploy"

	_, err := api.Create(ctx, agentSDKResource())
	if err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("Create = %v, want the deploy's error", err)
	}
	if IsRejectedCreate(err) {
		t.Error("a failed deploy may still have started one, so it isn't a rejected create")
	}
	files := testkit.Git(t, home.Bare, "ls-tree", "-r", "--name-only", "refs/heads/"+AgentSDKBranch)
	if strings.Contains(files, "deployed/") {
		t.Errorf("a failed deploy was recorded as deployed:\n%s", files)
	}

	// A stub that can't be written never reaches Cursor.
	fake.calls = nil
	res := agentSDKResource()
	res.Connectors = []ResourceConnector{{Name: "github", Kind: "native"}}
	if _, err := api.Create(ctx, res); !IsRejectedCreate(err) {
		t.Errorf("Create of a stub that can't be written = %v, want a rejected create", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("it still ran %v", fake.calls)
	}
}

func TestAgentSDKAPISourceRepo(t *testing.T) {
	api, _, _ := newFakeAgentSDKAPI(t)
	ctx := context.Background()

	t.Setenv(AgentSDKRepoEnv, "git@github.com:Acme/Agents.git")
	src, err := api.sourceRepo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if src.deploy != "https://github.com/acme/agents" || src.clone != "git@github.com:Acme/Agents.git" {
		t.Errorf("source = %+v", src)
	}

	api.source = nil
	t.Setenv(AgentSDKRepoEnv, "")
	if src, err := api.sourceRepo(ctx); err != nil || src.deploy != "https://github.com/acme/home" {
		t.Errorf("source from the home repo = %+v, %v", src, err)
	}

	bare := &agentSDKAPI{cache: t.TempDir()}
	if _, err := bare.sourceRepo(ctx); err == nil || !strings.Contains(err.Error(), AgentSDKRepoEnv) {
		t.Errorf("without a home repo: %v", err)
	}
}

// The real command gets the key in its environment, and its JSON errors
// come back readable.
func TestAgentSDKCommandLine(t *testing.T) {
	testkit.FreshCredentials(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "agent-sdk")
	err := os.WriteFile(script, []byte(`#!/bin/sh
echo "$@" >> "$0.args"
echo "key=$CURSOR_API_KEY" >> "$0.args"
case "$1 $2" in
  "deployment delete") echo '{"error":{"code":"not_found","message":"No deployment fab-x."}}'; exit 4 ;;
  "deployment get") echo '{"error":{"code":"permission_denied","message":"Not yours.","hint":"Ask its owner."}}'; exit 5 ;;
esac
echo '{}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentSDKBinEnv, script)
	t.Setenv(CursorKeyEnv, "")
	t.Setenv(CursorTokenEnv, "")
	if _, err := newAgentSDKAPI(); err == nil || !strings.Contains(err.Error(), "CURSOR_API_KEY isn't set") {
		t.Errorf("without a key: %v", err)
	}
	t.Setenv(CursorKeyEnv, "crsr_secret")
	t.Setenv(AgentSDKBinEnv, filepath.Join(dir, "missing"))
	if _, err := newAgentSDKAPI(); err == nil || !strings.Contains(err.Error(), "npm install --global @grok/agent-sdk@"+AgentSDKVersion) {
		t.Errorf("without the command: %v", err)
	}
	t.Setenv(AgentSDKBinEnv, script)
	made, err := newAgentSDKAPI()
	if err != nil {
		t.Fatal(err)
	}
	api := made.(*agentSDKAPI)

	_, err = api.run(context.Background(), nil, "deployment", "delete", "fab-x", "--yes", "--json")
	if !isAgentSDKNotFound(err) {
		t.Errorf("not found = %v", err)
	}
	_, err = api.run(context.Background(), nil, "deployment", "get", "fab-x", "--json")
	var cmdErr *agentSDKCommandError
	if !errors.As(err, &cmdErr) || isAgentSDKNotFound(err) || err.Error() != "`agent-sdk deployment get fab-x` failed: Not yours. (Ask its owner.)" {
		t.Errorf("permission denied = %v", err)
	}
	args, _ := os.ReadFile(script + ".args")
	if strings.Contains(strings.SplitN(string(args), "\n", 2)[0], "crsr_secret") || !strings.Contains(string(args), "key=crsr_secret") {
		t.Errorf("the key should be in the environment, not the arguments:\n%s", args)
	}
}
