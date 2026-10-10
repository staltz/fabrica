package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/localtools"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/token"
)

// Options configure a run of `fabrica apply`, or of `fabrica destroy` when
// Plan.Destroy is set.
type Options struct {
	Plan *plan.Env
	// Out is where progress goes.
	Out io.Writer
	// HandoffDir is where handoff instructions are written. They contain
	// the factory's token, so they live outside the home repo.
	HandoffDir string
	// Holder names whoever is applying, for the lock entry.
	Holder string
	// Approve, when set, sees the plan once it's built, and only when it
	// has changes. If it returns an error, nothing is changed and Run
	// returns that error.
	Approve func(*plan.Plan) error
}

// Result is the outcome of an apply.
type Result struct {
	// Plan is the plan apply started from.
	Plan *plan.Plan
	// Failed lists factories (or "local tools") that couldn't be applied.
	Failed []string
	// Handoffs are the handoff files written.
	Handoffs []string
}

// ExitCode is 0 when everything was applied and 1 when something failed
// or was skipped because of errors.
func (r *Result) ExitCode() int {
	if len(r.Failed) > 0 {
		return 1
	}
	return 0
}

// Run applies a home repo: every cloud factory that has changes and no
// errors, then the local tools on this machine. With Plan.Destroy, those
// changes remove everything the factories installed.
func Run(ctx context.Context, opts Options, homeDir string) (*Result, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	res := &Result{}

	h, err := home.Open(homeDir)
	if err != nil {
		return nil, err
	}
	// Cloud factories are applied from the latest home repo, so sync
	// first. A home repo with only local factories doesn't need a remote.
	var repo *Repo
	if needsGit(h) {
		repo, err = OpenRepo(ctx, homeDir)
		if err == nil {
			err = repo.Sync(ctx)
		}
		if err != nil {
			return nil, err
		}
		if h, err = home.Open(homeDir); err != nil {
			return nil, err
		}
	}

	env := *opts.Plan
	env.Refresh = true
	p := plan.Build(ctx, &env, h)
	res.Plan = p
	if p.Diags.HasErrors() {
		fmt.Fprintf(out, "The home repo has problems, so nothing was %s:\n", pastTense(&env))
		printErrors(out, p.Diags)
		res.Failed = append(res.Failed, "home repo")
		return res, nil
	}
	if opts.Approve != nil && p.HasChanges() {
		if err := opts.Approve(p); err != nil {
			return nil, err
		}
	}

	for _, dp := range p.Factories {
		if dp.Local() {
			if dp.Diags.HasErrors() {
				fmt.Fprintf(out, "Factory %s has errors, so its local routines weren't changed:\n", dp.Name)
				printErrors(out, dp.Diags)
				res.Failed = append(res.Failed, dp.Name)
			}
			continue
		}
		if dp.Diags.HasErrors() {
			fmt.Fprintf(out, "Factory %s has errors, so it was skipped:\n", dp.Name)
			printErrors(out, dp.Diags)
			res.Failed = append(res.Failed, dp.Name)
			continue
		}
		if len(dp.Changes) == 0 {
			continue // nothing to do, and nothing to commit
		}
		files, err := applyCloud(ctx, opts, &env, repo, dp.Name)
		res.Handoffs = append(res.Handoffs, files...)
		if err != nil {
			fmt.Fprintf(out, "Factory %s: %v\n", dp.Name, err)
			res.Failed = append(res.Failed, dp.Name)
		}
	}

	if p.Local != nil {
		if err := applyLocal(out, p.Local); err != nil {
			fmt.Fprintf(out, "Local tools: %v\n", err)
			res.Failed = append(res.Failed, "local tools")
		}
	}
	if len(res.Failed) == 0 && !p.HasChanges() {
		if env.Destroy {
			fmt.Fprintln(out, "Nothing to destroy.")
		} else {
			fmt.Fprintln(out, "Everything is already in sync. Nothing to do.")
		}
	}
	return res, nil
}

// verb names what's running, for messages and commits.
func verb(env *plan.Env) string {
	if env.Destroy {
		return "destroy"
	}
	return "apply"
}

func pastTense(env *plan.Env) string {
	if env.Destroy {
		return "destroyed"
	}
	return "applied"
}

func needsGit(h *home.Home) bool {
	if len(h.StateNames) > 0 {
		return true
	}
	for _, d := range h.Factories {
		if d.Scope == provider.Cloud {
			return true
		}
	}
	return false
}

func printErrors(out io.Writer, diags diag.List) {
	for _, d := range diags.Sorted().Only(diag.Error) {
		for _, line := range strings.Split(d.String(), "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
}

// applyCloud applies one cloud factory, following the steps in the
// design: take the lock, refresh, re-plan, apply, then record the results
// and release the lock in one commit.
func applyCloud(ctx context.Context, opts Options, env *plan.Env, repo *Repo, name string) (handoffs []string, err error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	// Deleting a stub needs no token, so destroy works without the secret.
	if env.Secret == "" && !env.Destroy {
		return nil, fmt.Errorf("%s isn't set, and cloud stubs need it to compute the factory's token", token.EnvVar)
	}
	holder := opts.Holder
	if holder == "" {
		holder = Holder()
	}
	now := time.Now().UTC()
	if env.Now != nil {
		now = env.Now()
	}
	now = now.Truncate(time.Second) // tidy timestamps keep state diffs readable

	// 1. Take the lock.
	if err := repo.takeLock(ctx, name, holder, verb(env), now); err != nil {
		var locked *ErrLocked
		if errors.As(err, &locked) {
			return nil, err
		}
		return nil, fmt.Errorf("couldn't take the lock: %w", err)
	}
	fmt.Fprintf(out, "Factory %s: took the lock.\n", name)

	// 2 and 3. Refresh and re-plan against the home repo as it is now.
	locked := *env
	locked.LockHolder = holder
	locked.Refresh = true
	h, err := home.Open(repo.Dir)
	if err != nil {
		return nil, releaseAfter(ctx, repo, name, verb(env), err)
	}
	dp := locked.PlanCloud(ctx, h, name)
	if dp.Diags.HasErrors() {
		printErrors(out, dp.Diags)
		return nil, releaseAfter(ctx, repo, name, verb(env), errors.New("the re-plan inside the lock found errors, so nothing was touched"))
	}

	// 4. Apply each change, recording as we go.
	state := dp.State
	if dp.Blueprint != nil {
		state.Blueprint = &home.StateBlueprint{Git: dp.Spec.Blueprint.Git, Path: dp.Spec.Blueprint.Path, Commit: dp.Blueprint.Source.Commit}
	}
	tok := token.For(env.Secret, name)
	var failures []string
	for _, c := range dp.Changes {
		file, err := applyChange(ctx, opts, dp, state, c, tok, now)
		if file != "" {
			handoffs = append(handoffs, file)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", c.Describe(), err))
			fmt.Fprintf(out, "  ✗ %s\n    %v\n", c.Describe(), err)
			continue
		}
		verb := "done"
		if file != "" {
			verb = "handoff written to " + file
		}
		fmt.Fprintf(out, "  ✓ %s (%s)\n", c.Describe(), verb)
	}

	// 5. Record and release.
	msg := fmt.Sprintf("fabrica: %s %s (%s)", verb(env), name, plural(len(dp.Changes)-len(failures), "change"))
	if err := repo.Release(ctx, name, msg, state); err != nil {
		return handoffs, err
	}
	fmt.Fprintf(out, "Factory %s: recorded the results and released the lock.\n", name)
	if len(failures) > 0 {
		return handoffs, fmt.Errorf("%s failed:\n  %s", plural(len(failures), "change"), strings.Join(failures, "\n  "))
	}
	return handoffs, nil
}

// releaseAfter releases the lock after a problem, keeping the original
// error.
func releaseAfter(ctx context.Context, repo *Repo, name, verb string, cause error) error {
	err := repo.Update(ctx, name, "fabrica: unlock "+name+" after a failed "+verb, func(s *home.State) error {
		s.Lock = nil
		return nil
	})
	if err != nil {
		return fmt.Errorf("%v (and releasing the lock failed too: %v; run `fabrica unlock %s`)", cause, err, name)
	}
	return cause
}

// applyChange carries out one change and updates state to match.
func applyChange(ctx context.Context, opts Options, dp *plan.Factory, state *home.State, c *plan.Change, tok string, now time.Time) (string, error) {
	switch c.Action {
	case plan.Adopt:
		upsertResource(state, *c.Current)
		return "", nil
	}
	// Unverifiable resources have no remote ID, so even on an API runtime a
	// person has to delete them.
	if c.Mode == provider.Handoff || c.Action == plan.Delete && c.Current.RemoteID == "" {
		return writeHandoff(opts, dp, state, c, tok, now)
	}

	api, err := c.Provider.API()
	if err != nil {
		return "", err
	}
	switch c.Action {
	case plan.Create:
		remoteID, err := api.Create(ctx, resourceFor(c.Desired, c.Provider, tok))
		if err != nil {
			return "", err
		}
		upsertResource(state, recordFor(c.Desired, remoteID, dp, now))
	case plan.Update:
		if c.Current.RemoteID == "" {
			return "", errors.New("Fabrica doesn't know this resource's remote ID, so it can't update it through the API")
		}
		if err := api.Update(ctx, c.Current.RemoteID, resourceFor(c.Desired, c.Provider, tok)); err != nil {
			return "", err
		}
		upsertResource(state, recordFor(c.Desired, c.Current.RemoteID, dp, now))
	case plan.Delete:
		if err := api.Delete(ctx, dp.Name, c.Current.RemoteID); err != nil {
			return "", err
		}
		removeResource(state, c.Key)
	}
	dropPending(state, c.Supersedes)
	return "", nil
}

// resourceFor turns a desired stub into what a provider's API gets.
func resourceFor(r *plan.Resource, p *provider.Provider, tok string) provider.Resource {
	shape := r.Eval.Shape
	res := provider.Resource{
		Identity: r.Key.ID, Factory: r.Factory, Routine: r.Routine, Repo: r.Repo,
		Trigger: provider.ResourceTrigger{On: shape.Trigger, Cron: shape.Cron},
		Prompt:  r.Prompt, Fingerprint: r.Fingerprint,
		Fabrica: provider.ResourceFabrica{Placement: p.Matrix.TokenPlacement(), Token: tok, URL: fabricaURL(r.Endpoint, p, tok)},
	}
	for _, c := range shape.Connectors {
		rc := provider.ResourceConnector{Name: c.Name, Kind: c.Kind}
		if c.Server != nil {
			rc.URL, rc.Transport, rc.Auth, rc.Secret = c.Server.URL, c.Server.Transport, c.Server.Auth, c.Server.Secret
			rc.Command, rc.Args, rc.Env = c.Server.Command, c.Server.Args, c.Server.Env
		}
		res.Connectors = append(res.Connectors, rc)
	}
	return res
}

// fabricaURL is the URL a runtime should use for the Fabrica server: with
// the token in the path when the runtime can't send headers.
func fabricaURL(endpoint string, p *provider.Provider, tok string) string {
	if p.Matrix.TokenPlacement() == provider.TokenInURL {
		return endpoint + "/t/" + tok
	}
	return endpoint
}

func recordFor(r *plan.Resource, remoteID string, dp *plan.Factory, now time.Time) home.Resource {
	return home.Resource{
		ID: r.Key.ID, Runtime: r.Runtime, Routine: r.Routine, Repo: r.Repo, RemoteID: remoteID,
		Mode: string(r.Mode), BlueprintCommit: blueprintCommit(dp), TokenHash: r.TokenHash,
		Shape: stateShape(r.Eval.Shape), AppliedAt: now,
	}
}

func stateShape(s stub.Shape) home.Shape {
	return home.Shape{Trigger: s.Trigger, Cron: s.Cron, Connectors: s.ConnectorNames(), Memory: s.Memory, Fingerprint: s.Fingerprint()}
}

func blueprintCommit(dp *plan.Factory) string {
	if dp.Blueprint != nil {
		return dp.Blueprint.Source.Commit
	}
	return ""
}

// writeHandoff writes the instructions for a change and records it as
// pending. Handoff IDs come from what the handoff does, so issuing the same
// handoff twice gives the same ID.
func writeHandoff(opts Options, dp *plan.Factory, state *home.State, c *plan.Change, tok string, now time.Time) (string, error) {
	fingerprint, tokenHash := "", ""
	if c.Desired != nil {
		fingerprint, tokenHash = c.Desired.Fingerprint, c.Desired.TokenHash
	}
	id := HandoffID(dp.Name, c.Key, string(c.Action), fingerprint+tokenHash)
	data := provider.HandoffData{
		ID: id, Action: string(c.Action), Runtime: c.Key.Runtime, Factory: dp.Name,
		Identity: c.Key.ID, Routine: c.Routine, Repo: c.Repo,
	}
	if c.Current != nil {
		data.RemoteID = c.Current.RemoteID
	}
	switch c.Action {
	case plan.Create:
		data.ConfirmCommand = fmt.Sprintf("fabrica confirm %s --remote-id <the new ID>", id)
	default:
		data.ConfirmCommand = "fabrica confirm " + id
	}
	if r := c.Desired; r != nil {
		shape := r.Eval.Shape
		data.Trigger = provider.HandoffTrigger{On: shape.Trigger, Cron: shape.Cron, Description: stub.DescribeTrigger(blueprint.Trigger{On: shape.Trigger, Cron: shape.Cron}, r.Repo)}
		for _, conn := range shape.Connectors {
			data.Connectors = append(data.Connectors, provider.HandoffConnector{Name: conn.Name, Setup: stub.DescribeConnector(conn)})
		}
		needed := r.Eval.Routine != nil && r.Eval.Routine.Memory
		support := c.Provider.Matrix.Memory
		data.Memory = provider.HandoffMemory{Needed: needed, Support: string(support), Setup: stub.DescribeMemory(needed, support)}
		data.Fabrica = provider.HandoffFabrica{URL: fabricaURL(r.Endpoint, c.Provider, tok)}
		if c.Provider.Matrix.TokenPlacement() == provider.TokenInHeader {
			data.Fabrica.Header = "Bearer " + tok
		}
		data.Prompt, data.Fingerprint = r.Prompt, r.Fingerprint
	}
	text, err := c.Provider.RenderHandoff(data)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(opts.HandoffDir, dp.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file := filepath.Join(dir, id+".md")
	// The instructions contain the factory's token, so only you can
	// read them.
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return "", err
	}

	dropPending(state, c.Supersedes)
	pend := home.Pending{
		ID: id, Action: string(c.Action), Resource: c.Key.ID, Runtime: c.Key.Runtime,
		Routine: c.Routine, Repo: c.Repo, BlueprintCommit: blueprintCommit(dp), IssuedAt: now,
	}
	if c.Current != nil {
		pend.RemoteID = c.Current.RemoteID
	}
	if c.Desired != nil {
		shape := stateShape(c.Desired.Eval.Shape)
		pend.Shape, pend.TokenHash = &shape, tokenHash
	}
	if _, exists := state.FindPending(id); !exists {
		state.Pending = append(state.Pending, pend)
	}
	return file, nil
}

// HandoffID makes a short, stable ID for a handoff.
func HandoffID(factory string, key home.Key, action, version string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{factory, key.Runtime, key.ID, action, version}, "\x00")))
	return "h_" + hex.EncodeToString(sum[:])[:10]
}

func upsertResource(state *home.State, r home.Resource) {
	if i, ok := state.FindResource(r.Key()); ok {
		state.Resources[i] = r
		return
	}
	state.Resources = append(state.Resources, r)
}

func removeResource(state *home.State, k home.Key) {
	if i, ok := state.FindResource(k); ok {
		state.Resources = append(state.Resources[:i], state.Resources[i+1:]...)
	}
}

func dropPending(state *home.State, id string) {
	if id == "" {
		return
	}
	if i, ok := state.FindPending(id); ok {
		state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
	}
}

// applyLocal writes local tool configs.
func applyLocal(out io.Writer, lp *plan.LocalPlan) error {
	if lp.Diags.HasErrors() {
		printErrors(out, lp.Diags)
		return errors.New("the local tools have errors, so they weren't changed")
	}
	for _, rt := range lp.Runtimes {
		if len(rt.Changes) == 0 {
			continue
		}
		if err := rt.Tool.Write(rt.Desired); err != nil {
			return fmt.Errorf("%s: %w", rt.Name, err)
		}
		var files []string
		for _, f := range rt.Tool.Files() {
			files = append(files, plan.Tildify(f))
		}
		fmt.Fprintf(out, "Local %s: updated %s\n", rt.Name, strings.Join(files, ", "))
		for _, c := range rt.Changes {
			fmt.Fprintf(out, "  ✓ %s\n", c)
		}
		if rt.Name == "codex-cli" && hasHookChanges(rt.Changes) {
			fmt.Fprintln(out, "  Codex skips new or changed hooks until you trust them: open Codex and approve them in /hooks.")
		}
		if setup := localtools.TaskSetup(rt.Current.Tasks, rt.Desired.Tasks, time.Now()); len(setup) > 0 {
			fmt.Fprintln(out, "  The Claude desktop app keeps a scheduled task's schedule and folder itself, so finish in the app (Code tab → Routines):")
			for _, s := range setup {
				fmt.Fprintf(out, "    • %s\n", s)
			}
			fmt.Fprintln(out, "  Scheduled tasks only run while the app is open and the computer is awake.")
		}
	}
	return nil
}

func hasHookChanges(changes []string) bool {
	for _, c := range changes {
		if strings.Contains(c, "hook") && !strings.HasPrefix(c, "-") {
			return true
		}
	}
	return false
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
