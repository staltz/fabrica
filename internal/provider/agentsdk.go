package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/repourl"
)

// cursor-agent-sdk deploys each stub as an Agent SDK project (see
// agentsdk_project.go) to Cursor-managed hosting, through the Agent SDK's
// own command line, `agent-sdk`: the documented way in, and the only one,
// since its HTTP API is for talking to running agents, not for deploying
// them.
//
// The Agent SDK only deploys code that's pushed to git, so Fabrica keeps
// the projects on a branch of their own, fabrica/agent-sdk, of the home
// repo's remote (or of FABRICA_AGENT_SDK_REPO): one folder per deployment,
// named after its slug. Nothing else writes to that branch, and the home
// repo's own branch never sees it. Cursor needs to be able to read it,
// through its GitHub connection, like the repos its agents work on.
//
// Next to the projects, deployed/<slug>.json records which stub each
// deployment is (its identity and stub fingerprint), written only once its
// deploy succeeded. That's how List reads identities back, so the runtime
// is find_by_identity. A deployment's slug comes from its identity, so a
// create that's tried again deploys the same deployment again instead of a
// second one.

const (
	// AgentSDKBinEnv overrides the agent-sdk command.
	AgentSDKBinEnv = "FABRICA_AGENT_SDK_BIN"
	// AgentSDKRepoEnv names the git repo to deploy from, instead of the
	// home repo's remote.
	AgentSDKRepoEnv = "FABRICA_AGENT_SDK_REPO"
	// AgentSDKBranch is the branch the projects live on.
	AgentSDKBranch = "fabrica/agent-sdk"

	agentSDKDeployedDir = "deployed"
	// agentSDKTimeout bounds one agent-sdk command. A deploy waits until
	// the deployment runs, which takes a few minutes.
	agentSDKTimeout   = 20 * time.Minute
	agentSDKPushTries = 5
)

// HomeRepoUser is an API that keeps files in git next to the home repo,
// like the Agent SDK's, which deploys only pushed code. Fabrica tells it
// where the home repo is before calling it.
type HomeRepoUser interface {
	UseHomeRepo(dir string)
}

// APIFor returns the runtime's API, told about the home repo it's used
// for, if it wants to know.
func (p *Provider) APIFor(homeDir string) (RuntimeAPI, error) {
	api, err := p.API()
	if err != nil {
		return nil, err
	}
	if u, ok := api.(HomeRepoUser); ok {
		u.UseHomeRepo(homeDir)
	}
	return api, nil
}

type agentSDKAPI struct {
	bin   string
	key   string
	cache string
	home  string

	// run runs agent-sdk with the arguments and stdin; tests replace it.
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

	source *agentSDKSource
}

// agentSDKSource is where the projects are pushed: clone says where git
// fetches and pushes, deploy is the https address Cursor builds from.
type agentSDKSource struct {
	clone, deploy string
	checkout      *gitx.BranchCheckout
}

func newAgentSDKAPI() (RuntimeAPI, error) {
	found, err := credentials.Lookup(CursorKeyEnv, CursorTokenEnv)
	if err != nil {
		return nil, err
	}
	if found.Value == "" {
		return nil, fmt.Errorf("%s isn't set", CursorKeyEnv)
	}
	bin := strings.TrimSpace(os.Getenv(AgentSDKBinEnv))
	if bin == "" {
		bin = "agent-sdk"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("the Agent SDK's command line (%s) isn't installed here; install it with `npm install --global @grok/agent-sdk@%s` (it needs Node 22.13 or newer)", bin, AgentSDKVersion)
	}
	cache, err := paths.Cache()
	if err != nil {
		return nil, err
	}
	a := &agentSDKAPI{bin: resolved, key: found.Value, cache: cache}
	a.run = a.exec
	return a, nil
}

func (a *agentSDKAPI) UseHomeRepo(dir string) { a.home = dir }

// exec runs the agent-sdk command, with the API key in its environment
// (never on its command line, where other users could see it).
func (a *agentSDKAPI) exec(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, agentSDKTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = append(os.Environ(), CursorKeyEnv+"="+a.key, "AGENT_SERVE_NO_UPDATE_CHECK=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	return stdout.Bytes(), agentSDKError(args, stdout.Bytes(), stderr.Bytes(), err)
}

// agentSDKCommandError is a failed agent-sdk command, with what it said.
type agentSDKCommandError struct {
	command string
	// code is the error's code from its JSON output, like "not_found".
	code    string
	message string
	exit    int
}

func (e *agentSDKCommandError) Error() string {
	msg := e.message
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.exit)
	}
	return fmt.Sprintf("`agent-sdk %s` failed: %s", e.command, msg)
}

// agentSDKNotFound is the exit status of a command about a deployment that
// doesn't exist.
const agentSDKNotFound = 4

func agentSDKError(args []string, stdout, stderr []byte, err error) error {
	e := &agentSDKCommandError{command: strings.Join(agentSDKCommandWords(args), " "), exit: -1}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		e.message = err.Error()
		return e
	}
	e.exit = exitErr.ExitCode()
	var out struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if json.Unmarshal(stdout, &out) == nil && out.Error != nil {
		e.code, e.message = out.Error.Code, out.Error.Message
		if out.Error.Hint != "" {
			e.message += " (" + out.Error.Hint + ")"
		}
	} else {
		e.message = snippet(stderr)
	}
	return e
}

// agentSDKCommandWords is a command's words without its flags' values,
// which can be long.
func agentSDKCommandWords(args []string) []string {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		words = append(words, a)
	}
	return words
}

func isAgentSDKNotFound(err error) bool {
	var e *agentSDKCommandError
	return errors.As(err, &e) && (e.exit == agentSDKNotFound || e.code == "not_found")
}

// agentSDKMarker is deployed/<slug>.json.
type agentSDKMarker struct {
	Identity    string `json:"identity"`
	Factory     string `json:"factory"`
	Fingerprint string `json:"fingerprint"`
	// Commit is the commit of the branch the deployment was built from.
	Commit string `json:"commit"`
}

func (a *agentSDKAPI) Create(ctx context.Context, res Resource) (string, error) {
	slug := AgentSDKSlug(res.Identity, res.Routine)
	if err := a.deploy(ctx, slug, res); err != nil {
		return "", err
	}
	return slug, nil
}

// Update deploys the stub again, under the deployment's slug, which stays
// the same when the stub's identity changes.
func (a *agentSDKAPI) Update(ctx context.Context, remoteID string, res Resource) error {
	err := a.deploy(ctx, remoteID, res)
	if IsRejectedCreate(err) {
		return errors.Unwrap(err)
	}
	return err
}

// deploy pushes the stub's project, deploys it, sets its secrets and then
// records it. A failure before anything reached Cursor is a rejected
// create; one after may have left a running deployment, which the next
// apply deploys again under the same slug.
func (a *agentSDKAPI) deploy(ctx context.Context, slug string, res Resource) error {
	project, err := agentSDKProjectFor(slug, res)
	if err != nil {
		return RejectedCreate(err)
	}
	src, err := a.sourceRepo(ctx)
	if err != nil {
		return RejectedCreate(err)
	}
	commit, err := a.publish(ctx, src, fmt.Sprintf("fabrica: deploy %s (%s)", slug, res.Identity), func(dir string) error {
		return writeAgentSDKProject(filepath.Join(dir, slug), project.Files)
	})
	if err != nil {
		return RejectedCreate(err)
	}

	args := []string{"deploy",
		"--dir", filepath.Join(src.checkout.Dir, slug),
		"--repo", src.deploy, "--ref", commit, "--path", slug, "--slug", slug,
	}
	for _, repo := range project.EventRepos {
		args = append(args, "--cursor-events-repo", repo)
	}
	if _, err := a.run(ctx, nil, append(args, "--json")...); err != nil {
		return fmt.Errorf("deploying %s from %s at %s: %w", slug, src.deploy, gitx.Short(commit), err)
	}
	names := make([]string, 0, len(project.Secrets))
	for name := range project.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := a.run(ctx, []byte(project.Secrets[name]), "secret", "set", slug, name, "--json"); err != nil {
			return fmt.Errorf("setting secret %s of %s: %w", name, slug, err)
		}
	}

	marker, _ := json.MarshalIndent(agentSDKMarker{Identity: res.Identity, Factory: res.Factory, Fingerprint: res.Fingerprint, Commit: commit}, "", "  ")
	_, err = a.publish(ctx, src, fmt.Sprintf("fabrica: deployed %s", slug), func(dir string) error {
		return writeFile(filepath.Join(dir, agentSDKDeployedDir, slug+".json"), string(marker)+"\n")
	})
	if err != nil {
		return fmt.Errorf("%s is deployed, but recording that failed (the next apply deploys it again): %w", slug, err)
	}
	return nil
}

func (a *agentSDKAPI) Delete(ctx context.Context, factory, remoteID string) error {
	if _, err := a.run(ctx, nil, "deployment", "delete", remoteID, "--yes", "--json"); err != nil && !isAgentSDKNotFound(err) {
		return err
	}
	src, err := a.sourceRepo(ctx)
	if err != nil {
		return err
	}
	_, err = a.publish(ctx, src, fmt.Sprintf("fabrica: delete %s", remoteID), func(dir string) error {
		if err := os.RemoveAll(filepath.Join(dir, remoteID)); err != nil {
			return err
		}
		err := os.Remove(filepath.Join(dir, agentSDKDeployedDir, remoteID+".json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("deleted deployment %s, but couldn't remove its project from branch %s: %w", remoteID, AgentSDKBranch, err)
	}
	return nil
}

// List reads which stubs the branch says are deployed, and keeps the ones
// whose deployment still exists.
func (a *agentSDKAPI) List(ctx context.Context, factory, prefix string) ([]RemoteResource, error) {
	src, err := a.sourceRepo(ctx)
	if err != nil {
		return nil, err
	}
	exists, err := src.checkout.Reset(ctx)
	if err != nil || !exists {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(src.checkout.Dir, agentSDKDeployedDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []RemoteResource
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src.checkout.Dir, agentSDKDeployedDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var m agentSDKMarker
		if err := json.Unmarshal(data, &m); err != nil || m.Identity == "" {
			continue // not one of ours
		}
		if strings.HasPrefix(m.Identity, prefix) {
			found = append(found, RemoteResource{Identity: m.Identity, RemoteID: slug, Fingerprint: m.Fingerprint})
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	deployed, err := a.deployments(ctx)
	if err != nil {
		return nil, err
	}
	kept := found[:0]
	for _, r := range found {
		if deployed[r.RemoteID] {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// deployments lists the slugs of every deployment the key can see.
func (a *agentSDKAPI) deployments(ctx context.Context) (map[string]bool, error) {
	slugs := map[string]bool{}
	token := ""
	for page := 0; page < 100; page++ {
		args := []string{"deployment", "list", "--limit", "100", "--json"}
		if token != "" {
			args = append(args, "--page-token", token)
		}
		out, err := a.run(ctx, nil, args...)
		if err != nil {
			return nil, err
		}
		var list struct {
			Deployments []struct {
				Slug string `json:"slug"`
			} `json:"deployments"`
			NextPageToken *string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(out, &list); err != nil {
			// Some versions print the list itself.
			if err := json.Unmarshal(out, &list.Deployments); err != nil {
				return nil, fmt.Errorf("couldn't read what `agent-sdk deployment list` printed: %v", err)
			}
		}
		for _, d := range list.Deployments {
			slugs[d.Slug] = true
		}
		if list.NextPageToken == nil || *list.NextPageToken == "" {
			return slugs, nil
		}
		token = *list.NextPageToken
	}
	return slugs, nil
}

// sourceRepo finds the repo the projects are pushed to, and a checkout of
// its branch in Fabrica's cache.
func (a *agentSDKAPI) sourceRepo(ctx context.Context) (*agentSDKSource, error) {
	if a.source != nil {
		return a.source, nil
	}
	clone := strings.TrimSpace(os.Getenv(AgentSDKRepoEnv))
	if clone == "" {
		if a.home == "" {
			return nil, fmt.Errorf("Fabrica deploys Agent SDK projects from a branch of the home repo's remote, but doesn't know the home repo; set %s to a git repo to deploy from", AgentSDKRepoEnv)
		}
		wt, err := gitx.OpenWorktree(ctx, a.home)
		if err != nil {
			return nil, err
		}
		remote, _, err := wt.Upstream(ctx)
		if err != nil {
			return nil, err
		}
		if clone = wt.RemoteURL(ctx, remote); clone == "" {
			return nil, fmt.Errorf("the home repo's remote %s has no URL", remote)
		}
	}
	canonical, err := repourl.Normalize(clone)
	if err != nil {
		return nil, fmt.Errorf("Cursor deploys Agent SDK projects from a git host it can read, and %s isn't one (%v); set %s to a GitHub repo to deploy from", clone, err, AgentSDKRepoEnv)
	}
	dir := filepath.Join(a.cache, "agent-sdk", strings.NewReplacer("/", "_", ":", "_").Replace(canonical))
	checkout, err := gitx.OpenBranch(ctx, dir, clone, AgentSDKBranch)
	if err != nil {
		return nil, err
	}
	a.source = &agentSDKSource{clone: clone, deploy: "https://" + canonical, checkout: checkout}
	return a.source, nil
}

// publish brings the branch up to date, lets change edit it, and commits
// and pushes the result, starting again when someone else pushed first. It
// returns the commit the branch is at.
func (a *agentSDKAPI) publish(ctx context.Context, src *agentSDKSource, message string, change func(dir string) error) (string, error) {
	b := src.checkout
	for try := 0; try < agentSDKPushTries; try++ {
		if _, err := b.Reset(ctx); err != nil {
			return "", err
		}
		readme := filepath.Join(b.Dir, "README.md")
		if _, err := os.Stat(readme); errors.Is(err, os.ErrNotExist) {
			if err := writeFile(readme, agentSDKBranchReadme); err != nil {
				return "", err
			}
		}
		if err := change(b.Dir); err != nil {
			return "", err
		}
		commit, made, err := b.Commit(ctx, message)
		if err != nil {
			return "", err
		}
		if !made {
			return commit, nil
		}
		err = b.Push(ctx)
		if err == nil {
			return commit, nil
		}
		if !errors.Is(err, gitx.ErrPushRejected) {
			return "", err
		}
	}
	return "", fmt.Errorf("gave up pushing to branch %s of %s after %d tries: it keeps changing under us", AgentSDKBranch, src.clone, agentSDKPushTries)
}

const agentSDKBranchReadme = `# Fabrica's Agent SDK projects

Fabrica (` + "`fabrica apply`" + `) writes this branch: one folder per Cursor Agent SDK
deployment, named after its slug, and ` + "`deployed/<slug>.json`" + `, which says
which Fabrica stub each deployment is. Cursor deploys the projects from
here. Don't edit it by hand: the next apply rewrites what it changes.
`

// writeAgentSDKProject replaces the folder with exactly the project's files.
func writeAgentSDKProject(dir string, files map[string]string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for name, content := range files {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(path.Clean(name))), content); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(file, content string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(content), 0o644)
}
