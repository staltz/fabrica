package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/stub"
)

// AgentDeploy is a blueprint agent's deployment that should exist: one
// per agent, per factory, per runtime that deploys agents. It watches all
// of the factory's repos.
type AgentDeploy struct {
	Agent *blueprint.Agent
	// Slug is the deployment's name in Cursor.
	Slug string
	// Shape is what state records once it's deployed. Its Commit is the
	// blueprint's commit, or the one already deployed when the agent's
	// files didn't change since.
	Shape home.AgentShape
	// Redeploy is false for an update that only changes the repos it
	// watches, which needs no new build.
	Redeploy bool
}

// Resource is the deployment for a provider's API.
func (a *AgentDeploy) Resource() *provider.ResourceAgent {
	return &provider.ResourceAgent{
		Name: a.Agent.Ref.Name, Slug: a.Slug, Repo: a.Shape.Git, Ref: a.Shape.Commit, Path: a.Shape.Path,
		EventRepos: a.Shape.EventRepos, Redeploy: a.Redeploy, Dir: a.Agent.Dir, InstallDir: a.Agent.InstallDir,
	}
}

// agentsRuntime reports whether a runtime deploys agents.
func agentsRuntime(rt *Runtime) bool { return rt.Provider.Matrix.Agents }

// checkAgents finds the blueprint agents the factory deploys: the ones it
// names, and every other one when it has the wildcard (or no agents: at
// all).
func checkAgents(dp *Factory, d *home.Factory, f *blueprint.Blueprint) []*blueprint.Agent {
	if !slices.ContainsFunc(dp.Runtimes, agentsRuntime) {
		return nil
	}
	var agents []*blueprint.Agent
	for _, name := range d.Agents {
		a := f.Agents[name]
		if a == nil {
			dp.Diags.Errorf("factory.unknown-agent", d.File, "agent %q isn't in the blueprint", name).
				Hintf("the blueprint has: %v", blueprint.SortedKeys(f.Agents))
			continue
		}
		if len(a.Problems) == 0 {
			agents = append(agents, a)
		}
	}
	if d.AllAgents {
		for _, name := range blueprint.SortedKeys(f.Agents) {
			if a := f.Agents[name]; !slices.Contains(d.Agents, name) && len(a.Problems) == 0 {
				agents = append(agents, a)
			}
		}
	}
	if len(agents) == 0 && len(f.Agents) == 0 {
		for _, rt := range dp.Runtimes {
			if agentsRuntime(rt) {
				dp.Diags.Errorf("factory.idle-runtime", d.File, "runtimes: lists %s, which deploys agents, but the blueprint has none", rt.Name).
					Hintf("add an Agent SDK project to the blueprint's agents/ folder, or remove %s", rt.Name)
			}
		}
	}
	dp.Agents = agents
	return agents
}

// planAgents works out the deployment each agent should have on each
// runtime that deploys agents, and what stands in its way.
func planAgents(dp *Factory, d *home.Factory, agents []*blueprint.Agent) []*Resource {
	if len(agents) == 0 {
		return nil
	}
	var eventRepos []string
	for _, repo := range d.SpecificTargets() {
		r, err := EventRepo(repo)
		if err != nil {
			dp.Diags.Errorf("agent.target", d.File, "%v", err).
				Hintf("deploy agents from a factory whose targets are all on GitHub, GitLab or Bitbucket")
			continue
		}
		eventRepos = append(eventRepos, r)
	}
	eventRepos = provider.SortedEventRepos(eventRepos)

	var out []*Resource
	for _, rt := range dp.Runtimes {
		if !agentsRuntime(rt) {
			continue
		}
		for _, a := range agents {
			slug := stub.AgentSlug(d.Tag(), a.Ref.Name)
			if len(slug) > stub.AgentSlugMaxLen {
				dp.Diags.Errorf("agent.slug", a.Where(), "agent %s would be deployed as %s, which is longer than the %d characters Cursor allows", a.Ref.Name, slug, stub.AgentSlugMaxLen).
					Hintf("give the factory a shorter alias:, or the agent a shorter name")
				continue
			}
			repo, err := AgentRepoURL(a.Source.Git)
			if err != nil {
				dp.Diags.Errorf("agent.source", a.Where(), "Cursor builds agents from git, but %v", err).
					Hintf("use the blueprint (or import) from an https:// git URL that Cursor can clone, like github.com/acme/blueprint")
				continue
			}
			deploy := &AgentDeploy{Agent: a, Slug: slug, Redeploy: true, Shape: home.AgentShape{
				Git: repo, Commit: a.Source.Commit, Path: a.Source.Path, Hash: a.Hash, EventRepos: eventRepos,
			}}
			id := stub.AgentIdentity(d.Tag(), a.Ref.Name)
			out = append(out, &Resource{
				Key: home.Key{Runtime: rt.Name, ID: id}, Factory: d.Name, Runtime: rt.Name, Endpoint: d.Endpoint(),
				Agent: deploy, Fingerprint: AgentFingerprint(deploy.Shape), Mode: rt.Mode, Provider: rt.Provider,
			})
			rt.Agents = append(rt.Agents, deploy)
		}
	}
	return out
}

// EventRepo turns a target into a repo Cursor's agents can watch: owner/name
// for GitHub, and the full name for GitLab and Bitbucket.
func EventRepo(target string) (string, error) {
	host, path, _ := strings.Cut(target, "/")
	switch host {
	case "github.com":
		return path, nil
	case "gitlab.com", "bitbucket.org":
		return target, nil
	}
	return "", fmt.Errorf("target %s is on %s, and Cursor's agents only watch repos on github.com, gitlab.com and bitbucket.org", target, host)
}

// AgentRepoURL is the https URL Cursor builds an agent from, for a git:
// value of a blueprint or import.
func AgentRepoURL(git string) (string, error) {
	url := repourl.GitURL(git)
	if !strings.HasPrefix(url, "https://") {
		canonical, err := repourl.Normalize(git)
		if err != nil {
			return "", fmt.Errorf("its repo %q isn't one Cursor can reach (%v)", git, err)
		}
		url = repourl.CloneURL(canonical)
	}
	return strings.TrimSuffix(url, ".git"), nil
}

// AgentFingerprint fingerprints what a deployment should be: what Cursor
// builds and the repos it watches. The commit isn't in it, so a merge that
// doesn't touch the agent's files doesn't redeploy it.
func AgentFingerprint(s home.AgentShape) string {
	data, _ := json.Marshal([]any{"agent/1", s.Git, s.Path, s.Hash, provider.SortedEventRepos(s.EventRepos)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// settleAgent decides, for an agent that's already deployed, whether an
// update needs a new build or only changes the repos it watches. An agent
// whose files didn't change keeps the commit it was built from.
func settleAgent(want *AgentDeploy, cur home.Shape) {
	have := cur.Agent
	if have == nil || have.Git != want.Shape.Git || have.Path != want.Shape.Path || have.Hash != want.Shape.Hash || have.Commit == "" {
		want.Redeploy = true
		return
	}
	want.Redeploy = false
	want.Shape.Commit = have.Commit
	want.Shape.Secrets, want.Shape.UnsetSecrets = have.Secrets, have.UnsetSecrets
}

// agentReasons explains how a deployment differs from the wanted one.
func agentReasons(cur home.Shape, want *AgentDeploy) []string {
	have := cur.Agent
	if have == nil {
		return []string{"Fabrica can't tell which version of the agent is deployed"}
	}
	var reasons []string
	switch {
	case have.Git != want.Shape.Git || have.Path != want.Shape.Path:
		reasons = append(reasons, fmt.Sprintf("it moved from %s to %s", agentSource(have.Git, have.Path), agentSource(want.Shape.Git, want.Shape.Path)))
	case have.Hash != want.Shape.Hash:
		reasons = append(reasons, "its files changed in the blueprint (redeploys it from "+shortCommit(want.Shape.Commit)+")")
	}
	if added, removed := diffNames(have.EventRepos, want.Shape.EventRepos); len(added)+len(removed) > 0 {
		var parts []string
		if len(added) > 0 {
			parts = append(parts, "adds "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			parts = append(parts, "drops "+strings.Join(removed, ", "))
		}
		reasons = append(reasons, "the repos it watches changed ("+strings.Join(parts, "; ")+")")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "its deployment changed")
	}
	return reasons
}

func agentSource(git, path string) string {
	if path == "" {
		return git
	}
	return git + " (" + path + ")"
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// whyAgentGone explains why an agent's deployment is no longer wanted.
func (e *Env) whyAgentGone(dp *Factory, r home.Resource) string {
	d := dp.Spec
	switch {
	case d == nil:
		return "its factory file was deleted"
	case e.Destroy:
		return "the factory is being destroyed"
	case !d.HasRuntime(r.Runtime):
		return fmt.Sprintf("runtime %s was removed from the factory", r.Runtime)
	case !d.HasAgent(r.Agent):
		return fmt.Sprintf("agent %s was removed from the factory", r.Agent)
	case dp.Blueprint != nil && dp.Blueprint.Agents[r.Agent] == nil:
		return fmt.Sprintf("agent %s was removed from the blueprint", r.Agent)
	case !strings.HasPrefix(r.ID, stub.IdentityPrefix(d.Tag())):
		return "it's left over from the factory's earlier tag (before a new alias: or server:), and Fabrica deploys the agent under its new name"
	}
	return "it's no longer deployed"
}

// AgentSlugOf is the slug Fabrica deploys an agent identity under.
func AgentSlugOf(id string) string {
	prefix, _, _, _ := stub.SplitIdentity(id)
	agent, _ := stub.AgentOf(id)
	return stub.AgentSlug(strings.TrimSuffix(prefix, "#"), agent)
}

// warnAgentDeletes makes every change that deletes an agent's deployment
// hard to miss: the deployment's hosted data goes with it, for good.
func warnAgentDeletes(dp *Factory) {
	for _, c := range dp.Changes {
		if c.Action != Delete || c.Agent == "" {
			continue
		}
		slug := AgentSlugOf(c.Key.ID)
		if c.Current != nil && c.Current.RemoteID != "" {
			slug = c.Current.RemoteID
		}
		dp.Diags.Warnf("agent.data-loss", c.Key.ID, "deleting agent %s's deployment %s on %s CAN'T BE UNDONE: it also deletes the deployment's hosted sessions, KV store (ctx.host.kv), files (ctx.host.files) and eval runs", c.Agent, slug, c.Key.Runtime).
			Hintf("copy out anything you want to keep before you apply; if you didn't mean to remove the agent, put it back in the factory (or the blueprint) instead")
	}
}

// noteUnsetSecrets reminds you of the secrets an agent declares that the
// last apply couldn't set.
func noteUnsetSecrets(dp *Factory) {
	if dp.State == nil || dp.Spec == nil || dp.Destroy {
		return
	}
	for _, r := range dp.State.Resources {
		if r.Agent == "" || r.Shape.Agent == nil || len(r.Shape.Agent.UnsetSecrets) == 0 {
			continue
		}
		slug := r.RemoteID
		if slug == "" {
			slug = AgentSlugOf(r.ID)
		}
		dp.Notes = append(dp.Notes, fmt.Sprintf("agent %s needs secrets that weren't set when it was last deployed (%s): set each one with `agent-sdk secret set %s <NAME>`, or put them in apply's environment for the agent's next deploy",
			r.Agent, strings.Join(r.Shape.Agent.UnsetSecrets, ", "), slug))
	}
}
