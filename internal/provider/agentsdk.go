package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/staltz/fabrica/internal/credentials"
)

// In api mode, the cursor-agents runtime drives `agent-sdk`, the command
// line of Cursor's Agent SDK (the @grok/agent-sdk npm package). It's the
// documented way to deploy an agent, and the only one: Cursor doesn't
// document the HTTP API behind it. Every call passes --json, which prints
// one JSON document on stdout, errors included.
//
// `agent-sdk deploy` builds a pushed git ref on Cursor's side, but it also
// loads the project on this machine first (to check it, and to read the
// secret names and container image it declares), and loading TypeScript
// needs the project's dependencies. So a deploy installs them into a
// scratch copy of the project, from its lockfile, without running their
// install scripts.

const (
	// AgentSDKBinEnv says which `agent-sdk` to run. Without it, Fabrica
	// looks for agent-sdk on PATH.
	AgentSDKBinEnv = "FABRICA_AGENT_SDK_BIN"
	// CursorServiceAccountKeyEnv is the team service-account key the
	// agent-sdk command line also reads. Fabrica reads it when
	// CursorKeyEnv isn't set.
	CursorServiceAccountKeyEnv = "CURSOR_SERVICE_ACCOUNT_KEY"

	agentSDKNotFound = 4 // agent-sdk's exit status for not_found
)

// ResourceAgent is the agent a resource on an agents runtime deploys.
type ResourceAgent struct {
	// Name is the agent's name in the blueprint.
	Name string `json:"name"`
	// Slug is the deployment's name in Cursor, like k7q2_pr-reviewer.
	Slug string `json:"slug"`
	// Repo, Ref and Path are what Cursor builds: an https git URL, an exact
	// commit, and the project's folder in the repo (empty for its root).
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	Path string `json:"path,omitempty"`
	// EventRepos are the repos the deployment watches, the way Cursor
	// writes them: owner/name on GitHub, gitlab.com/<group>/<project> or
	// bitbucket.org/<workspace>/<repo>.
	EventRepos []string `json:"event_repos"`
	// Redeploy is false for an update that only changes EventRepos, which
	// needs no new build.
	Redeploy bool `json:"redeploy"`
	// Dir is the project's folder on this machine, at Ref, and InstallDir
	// the folder with its lockfile: Dir, or a parent of it in an npm
	// workspace.
	Dir        string `json:"-"`
	InstallDir string `json:"-"`
}

// AgentReport is what deploying an agent taught Fabrica about it.
type AgentReport struct {
	// Secrets are the secret names the agent declares.
	Secrets []string
	// UnsetSecrets are the ones still not set on the deployment, because
	// apply didn't have them in its environment.
	UnsetSecrets []string
	// AliasToken is the token for the deployment's alias URL, which Cursor
	// shows only on a slug's first deploy.
	AliasToken string
}

// AgentReporter is an API that learns about agents as it deploys them.
type AgentReporter interface {
	// AgentReport returns what the last create or update of a resource
	// learned, if it deployed one.
	AgentReport(identity string) (AgentReport, bool)
}

// agentSDKAPI deploys agents with the agent-sdk command line. It's a
// Lister: a deployment's slug says which factory and agent it's for.
type agentSDKAPI struct {
	bin string
	// env is added to Fabrica's own environment for every command: the
	// Cursor key, when it came from the keychain rather than from there.
	env []string
	// lookupSecret finds an agent's secret in apply's environment.
	lookupSecret func(name string) (string, bool)

	mu      sync.Mutex
	reports map[string]AgentReport
}

func newAgentSDKAPI() (RuntimeAPI, error) {
	found, err := credentials.Lookup(CursorKeyEnv, CursorServiceAccountKeyEnv)
	if err != nil {
		return nil, err
	}
	if found.Value == "" {
		return nil, fmt.Errorf("%s isn't set", CursorKeyEnv)
	}
	bin, err := findAgentSDK()
	if err != nil {
		return nil, err
	}
	api := &agentSDKAPI{bin: bin, lookupSecret: os.LookupEnv, reports: map[string]AgentReport{}}
	if !found.Env {
		api.env = append(api.env, CursorKeyEnv+"="+found.Value)
	}
	return api, nil
}

// findAgentSDK finds the agent-sdk command line.
func findAgentSDK() (string, error) {
	if bin := strings.TrimSpace(os.Getenv(AgentSDKBinEnv)); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			return "", fmt.Errorf("%s points at %s, which doesn't exist", AgentSDKBinEnv, bin)
		}
		return bin, nil
	}
	bin, err := exec.LookPath("agent-sdk")
	if err != nil {
		return "", errors.New("the agent-sdk command line isn't installed (install it with `npm install --global @grok/agent-sdk`, or point " + AgentSDKBinEnv + " at it)")
	}
	return bin, nil
}

// agentSDKError is the error document agent-sdk prints with --json.
type agentSDKError struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// agentSDKFailure is a command that failed, with its exit status.
type agentSDKFailure struct {
	command string
	status  int
	message string
}

func (e *agentSDKFailure) Error() string { return e.command + " failed: " + e.message }

// sdk runs one agent-sdk command with --json and returns what it printed
// on stdout.
func (a *agentSDKAPI) sdk(ctx context.Context, timeout time.Duration, stdin string, args ...string) ([]byte, error) {
	args = append(args, "--json")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = append(append(os.Environ(), a.env...), "AGENT_SERVE_NO_UPDATE_CHECK=1")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	command := "agent-sdk " + strings.Join(args[:min(2, len(args))], " ")
	var doc agentSDKError
	_ = json.Unmarshal(stdout.Bytes(), &doc)
	if runErr == nil && doc.Error == nil {
		return stdout.Bytes(), nil
	}
	f := &agentSDKFailure{command: command, status: -1}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		f.status = exit.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		f.message = fmt.Sprintf("timed out after %s", timeout)
	case doc.Error != nil:
		f.message = doc.Error.Message
		if doc.Error.Hint != "" {
			f.message += " (" + doc.Error.Hint + ")"
		}
	case strings.TrimSpace(stderr.String()) != "":
		f.message = strings.TrimSpace(stderr.String())
	case runErr != nil:
		f.message = runErr.Error()
	default:
		f.message = "it printed an error without a message"
	}
	return stdout.Bytes(), f
}

// notFound reports whether err is agent-sdk saying the deployment doesn't
// exist.
func notFound(err error) bool {
	var f *agentSDKFailure
	return errors.As(err, &f) && f.status == agentSDKNotFound
}

func (a *agentSDKAPI) Create(ctx context.Context, res Resource) (string, error) {
	if res.Agent == nil {
		return "", fmt.Errorf("%s deploys agents, and %s isn't one", "cursor-agents", res.Identity)
	}
	return res.Agent.Slug, a.deploy(ctx, res)
}

func (a *agentSDKAPI) Update(ctx context.Context, remoteID string, res Resource) error {
	if res.Agent == nil {
		return fmt.Errorf("%s deploys agents, and %s isn't one", "cursor-agents", res.Identity)
	}
	if remoteID != res.Agent.Slug {
		return fmt.Errorf("%s is deployed as %s, but Fabrica deploys it as %s; delete %s with `agent-sdk deployment delete %s`, then apply again", res.Identity, remoteID, res.Agent.Slug, remoteID, remoteID)
	}
	if res.Agent.Redeploy {
		return a.deploy(ctx, res)
	}
	args := []string{"deployment", "update", res.Agent.Slug}
	for _, repo := range res.Agent.EventRepos {
		args = append(args, "--event-repo", repo)
	}
	if len(res.Agent.EventRepos) == 0 {
		args = append(args, "--no-event-repos")
	}
	_, err := a.sdk(ctx, 2*time.Minute, "", args...)
	return err
}

func (a *agentSDKAPI) Delete(ctx context.Context, factory, remoteID string) error {
	_, err := a.sdk(ctx, 10*time.Minute, "", "deployment", "delete", remoteID, "--yes")
	if notFound(err) {
		return nil
	}
	return err
}

// List finds the factory's deployments: the ones whose slug starts with
// the tag in prefix. Prefixes that aren't a tag (from releases before
// tags, which never deployed agents) have none.
func (a *agentSDKAPI) List(ctx context.Context, factory, prefix string) ([]RemoteResource, error) {
	tag, ok := strings.CutSuffix(prefix, "#")
	if !ok || strings.ContainsAny(tag, "/:#_") {
		return nil, nil
	}
	var out []RemoteResource
	token := ""
	for page := 0; page < 100; page++ {
		args := []string{"deployment", "list", "--limit", "100"}
		if token != "" {
			args = append(args, "--page-token", token)
		}
		data, err := a.sdk(ctx, 2*time.Minute, "", args...)
		if err != nil {
			return nil, err
		}
		var list struct {
			Items []struct {
				Slug string `json:"slug"`
			} `json:"items"`
			NextPageToken *string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("agent-sdk deployment list printed something Fabrica can't read: %v", err)
		}
		for _, item := range list.Items {
			if t, agent, ok := splitAgentSlug(item.Slug); ok && t == tag {
				out = append(out, RemoteResource{Identity: prefix + "agents/" + agent, RemoteID: item.Slug})
			}
		}
		if list.NextPageToken == nil || *list.NextPageToken == "" {
			return out, nil
		}
		token = *list.NextPageToken
	}
	return nil, errors.New("agent-sdk deployment list kept returning more pages")
}

// splitAgentSlug is stub.AgentSlug backwards (stub imports this package,
// so it lives here). ok is false for a slug Fabrica didn't make.
func splitAgentSlug(slug string) (tag, agent string, ok bool) {
	tag, agent, ok = strings.Cut(slug, "_")
	if !ok || tag == "" || agent == "" || strings.Contains(agent, "_") {
		return "", "", false
	}
	return tag, agent, true
}

// deploy installs the project's dependencies in a scratch copy, deploys it,
// and sets the secrets it declares that apply's environment has.
func (a *agentSDKAPI) deploy(ctx context.Context, res Resource) error {
	ag := res.Agent
	scratch, err := os.MkdirTemp("", "fabrica-agent-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	dir, err := prepareAgent(ctx, ag, scratch)
	if err != nil {
		return err
	}

	data, err := a.sdk(ctx, 2*time.Minute, "", "info", "--dir", dir, "--view", "manifest")
	if err != nil {
		return err
	}
	var manifest struct {
		Info struct {
			Hosting struct {
				SecretNames []string `json:"secretNames"`
			} `json:"hosting"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("agent-sdk info printed something Fabrica can't read: %v", err)
	}

	args := []string{"deploy", "--dir", dir, "--slug", ag.Slug, "--repo", ag.Repo, "--ref", ag.Ref}
	if ag.Path != "" {
		args = append(args, "--path", ag.Path)
	}
	for _, repo := range ag.EventRepos {
		args = append(args, "--cursor-events-repo", repo)
	}
	data, err = a.sdk(ctx, 45*time.Minute, "", args...)
	var deployed struct {
		Status      string   `json:"status"`
		LastError   *string  `json:"lastError"`
		SecretNames []string `json:"secretNames"`
		AliasToken  string   `json:"aliasToken"`
	}
	_ = json.Unmarshal(data, &deployed)
	report := AgentReport{Secrets: manifest.Info.Hosting.SecretNames, AliasToken: deployed.AliasToken}
	if deployed.AliasToken != "" {
		// Cursor shows it once, so keep it even when the deploy failed.
		a.record(res.Identity, report)
	}
	switch {
	case err != nil && deployed.Status == "":
		return err
	case deployed.Status != "running":
		why := ""
		if deployed.LastError != nil && *deployed.LastError != "" {
			why = ": " + *deployed.LastError
		}
		return fmt.Errorf("the deployment of %s ended %s%s", ag.Slug, deployed.Status, why)
	}

	set := map[string]bool{}
	for _, name := range deployed.SecretNames {
		set[name] = true
	}
	var problems []string
	for _, name := range report.Secrets {
		if set[name] {
			continue
		}
		value, ok := a.lookupSecret(name)
		if !ok || value == "" {
			report.UnsetSecrets = append(report.UnsetSecrets, name)
			continue
		}
		if _, err := a.sdk(ctx, 2*time.Minute, value, "secret", "set", ag.Slug, name); err != nil {
			report.UnsetSecrets = append(report.UnsetSecrets, name)
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
		}
	}
	a.record(res.Identity, report)
	if len(problems) > 0 {
		return fmt.Errorf("%s is deployed, but setting its secrets failed: %s", ag.Slug, strings.Join(problems, "; "))
	}
	return nil
}

func (a *agentSDKAPI) record(identity string, r AgentReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if old, ok := a.reports[identity]; ok && r.AliasToken == "" {
		r.AliasToken = old.AliasToken
	}
	a.reports[identity] = r
}

func (a *agentSDKAPI) AgentReport(identity string) (AgentReport, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.reports[identity]
	return r, ok
}

// Setup lists what the agents this apply deployed still need: the secrets
// apply didn't have.
func (a *agentSDKAPI) Setup(factory string, resources []Resource) string {
	var b strings.Builder
	for _, res := range resources {
		if res.Agent == nil {
			continue
		}
		r, ok := a.AgentReport(res.Identity)
		if !ok || len(r.UnsetSecrets) == 0 {
			continue
		}
		if b.Len() == 0 {
			fmt.Fprintf(&b, "# Secrets for factory %s's agents\n\n", factory)
			b.WriteString("These agents declare secrets (in `hosting.secretNames`) that apply didn't find in its environment, so they aren't set on their deployments yet. Set each one at the hidden prompt; the agent's next task uses it, without a redeploy. Or put them in the environment of `fabrica apply`, and it sets them on the agent's next deploy.\n")
		}
		fmt.Fprintf(&b, "\n## %s (deployment `%s`)\n\n```\n", res.Agent.Name, res.Agent.Slug)
		for _, name := range r.UnsetSecrets {
			fmt.Fprintf(&b, "agent-sdk secret set %s %s\n", res.Agent.Slug, name)
		}
		b.WriteString("```\n")
	}
	return b.String()
}

// prepareAgent copies the project (from its install folder down) into
// scratch, installs its dependencies there, and returns where the project
// is in the copy.
func prepareAgent(ctx context.Context, ag *ResourceAgent, scratch string) (string, error) {
	if ag.Dir == "" || ag.InstallDir == "" {
		return "", fmt.Errorf("Fabrica doesn't have %s on this machine", ag.Name)
	}
	rel, err := filepath.Rel(ag.InstallDir, ag.Dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s isn't inside %s", ag.Dir, ag.InstallDir)
	}
	root := filepath.Join(scratch, "project")
	if err := copyTree(ag.InstallDir, root); err != nil {
		return "", fmt.Errorf("copying %s: %v", ag.Name, err)
	}
	install, err := installCommand(root)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, install[0], install[1:]...)
	cmd.Dir = root
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(output.String())
		if len(tail) > 2000 {
			tail = "…" + tail[len(tail)-2000:]
		}
		return "", fmt.Errorf("installing %s's dependencies (%s) failed: %v\n%s", ag.Name, strings.Join(install, " "), err, tail)
	}
	return filepath.Join(root, rel), nil
}

// installCommand is how to install a project's dependencies from the
// lockfile in dir, exactly, without running install scripts.
func installCommand(dir string) ([]string, error) {
	for _, c := range []struct {
		lockfile string
		command  []string
	}{
		{"package-lock.json", []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"}},
		{"npm-shrinkwrap.json", []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"}},
		{"pnpm-lock.yaml", []string{"pnpm", "install", "--frozen-lockfile", "--ignore-scripts"}},
		{"yarn.lock", []string{"yarn", "install", "--frozen-lockfile", "--ignore-scripts"}},
	} {
		if _, err := os.Stat(filepath.Join(dir, c.lockfile)); err != nil {
			continue
		}
		if _, err := exec.LookPath(c.command[0]); err != nil {
			return nil, fmt.Errorf("%s needs %s to install its dependencies, and %s isn't installed", c.lockfile, c.command[0], c.command[0])
		}
		return c.command, nil
	}
	return nil, errors.New("there's no lockfile to install the agent's dependencies from")
}

// copyTree copies a folder, leaving out node_modules.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && d.Name() == "node_modules" && p != src:
			return fs.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			return nil // agents can't have them, and an import's other files don't matter
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// SortedEventRepos is a copy of repos, sorted, for comparing lists.
func SortedEventRepos(repos []string) []string {
	out := append([]string(nil), repos...)
	sort.Strings(out)
	return out
}
