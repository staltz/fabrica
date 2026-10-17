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
	"sort"
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
	// SessionDir is where init sessions started on this machine keep their
	// checkout and log, one folder per factory and init.
	SessionDir string
	// WaitForServer waits until the factory's Fabrica server is ready for
	// an init session, before apply starts one. Nil means asking the
	// server over HTTP.
	WaitForServer func(context.Context, provider.ResourceFabrica) error
}

// Result is the outcome of an apply.
type Result struct {
	// Plan is the plan apply started from.
	Plan *plan.Plan
	// Failed lists factories (or "local tools") that couldn't be applied.
	Failed []string
	// Handoffs are the handoff files written.
	Handoffs []string
	// Setups are files saying what to set up by hand for resources an API
	// made, because the API can't set it up itself.
	Setups []string
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
		if !dp.HasWork() {
			continue // nothing to do, and nothing to commit
		}
		server := ""
		if dp.Spec != nil {
			server = dp.Spec.Server
		}
		files, setups, err := applyCloud(ctx, opts, &env, repo, dp.Name, server)
		res.Handoffs = append(res.Handoffs, files...)
		res.Setups = append(res.Setups, setups...)
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
func applyCloud(ctx context.Context, opts Options, env *plan.Env, repo *Repo, name, server string) (handoffs, setups []string, err error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	// Deleting a stub needs no token, so destroy works without the secret,
	// and so does applying a factory whose file was deleted.
	secret := ""
	if !env.Destroy && server != "" {
		if secret, err = env.SecretFor(server); err != nil {
			return nil, nil, err
		}
		if secret == "" {
			return nil, nil, fmt.Errorf("there's no server secret for %s, and cloud stubs need it to compute the factory's token; run `fabrica auth login` to set it up (or set %s)", server, token.EnvVar)
		}
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
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("couldn't take the lock: %w", err)
	}
	fmt.Fprintf(out, "Factory %s: took the lock.\n", name)

	// 2 and 3. Refresh and re-plan against the home repo as it is now.
	locked := *env
	locked.LockHolder = holder
	locked.Refresh = true
	h, err := home.Open(repo.Dir)
	if err != nil {
		return nil, nil, releaseAfter(ctx, repo, name, verb(env), err)
	}
	dp := locked.PlanCloud(ctx, h, name)
	if dp.Diags.HasErrors() {
		printErrors(out, dp.Diags)
		return nil, nil, releaseAfter(ctx, repo, name, verb(env), errors.New("the re-plan inside the lock found errors, so nothing was touched"))
	}

	// 4. Apply each change, recording as we go.
	state := dp.State
	if dp.Blueprint != nil {
		state.Blueprint = &home.StateBlueprint{Git: dp.Spec.Blueprint.Git, Path: dp.Spec.Blueprint.Path, Commit: dp.Blueprint.Source.Commit}
	}
	tok := ""
	if secret != "" {
		tok = token.For(secret, name)
	}
	apis := apiClients{}
	touched := map[*provider.Provider]bool{} // runtimes with creates or updates through their API
	var failures []string
	for _, c := range dp.Changes {
		// A runtime that can't list its resources can't find a create it
		// doesn't have an ID for. Record the attempt before calling the
		// API, so a lost answer or a crash isn't retried into a duplicate.
		// A failed record means the create never started.
		guardID := ""
		if getOnlyCreate(c) {
			id, err := beginUncertainCreate(ctx, repo, env, dp, state, c, now)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", c.Describe(), err))
				fmt.Fprintf(out, "  ✗ %s\n    %v\n", c.Describe(), err)
				continue
			}
			guardID = id
		}
		if c.Mode == provider.API && c.Desired != nil {
			touched[c.Provider] = true
		}
		file, err := applyChange(ctx, opts, dp, state, c, tok, now, apis)
		if file != "" {
			handoffs = append(handoffs, file)
		}
		if err != nil {
			if guardID != "" && provider.IsRejectedCreate(err) {
				dropUncertain(state, c.Key)
			} else if guardID != "" {
				err = fmt.Errorf("%w. This may have created it anyway, and %s can't list its resources, so apply will not try again: %s", err, c.Key.Runtime, plan.UncertainCreateHint(c.Key.ID, guardID))
			}
			failures = append(failures, fmt.Sprintf("%s: %v", c.Describe(), err))
			fmt.Fprintf(out, "  ✗ %s\n    %v\n", c.Describe(), err)
			continue
		}
		done := "done"
		if file != "" {
			done = "handoff written to " + file
		}
		fmt.Fprintf(out, "  ✓ %s (%s)\n", c.Describe(), done)
		// The ID is known now, so the attempt record can go, and state
		// should say so before the next change. If this checkpoint fails,
		// the final record below still has the ID.
		if guardID != "" {
			dropUncertain(state, c.Key)
			msg := fmt.Sprintf("fabrica: %s %s (created %s @ %s on %s)", verb(env), name, c.Routine, c.Repo, c.Key.Runtime)
			if err := repo.Checkpoint(ctx, name, msg, state); err != nil {
				fmt.Fprintf(out, "    (couldn't record it in the home repo right away: %v)\n", err)
			}
		}
	}
	setups = writeSetups(out, opts, dp, tok, touched, apis)

	// The factory's init session, when this apply creates it. It comes
	// last, once its stubs are in.
	done := len(dp.Changes) - len(failures)
	if dp.Init.Start() {
		file, err := startInit(ctx, opts, repo, env, dp, state, tok, now, len(failures))
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", describeInit(dp.Init), err))
			fmt.Fprintf(out, "  ✗ %s\n    %v\n", describeInit(dp.Init), err)
		case file != "":
			handoffs = append(handoffs, file)
			fmt.Fprintf(out, "  ✓ %s (handoff written to %s)\n", describeInit(dp.Init), file)
		default:
			fmt.Fprintf(out, "  ✓ %s (started%s)\n", describeInit(dp.Init), startedWhere(state.Init))
		}
		if err == nil {
			done++
		}
	}

	// A destroy ends the factory's life, so the apply that recreates it
	// runs its init again. One that failed partway hasn't ended it.
	switch {
	case env.Destroy && len(failures) == 0:
		state.Init, state.DestroyedAt = nil, &now
	case !env.Destroy:
		state.DestroyedAt = nil
	}

	// 5. Record and release.
	msg := fmt.Sprintf("fabrica: %s %s (%s)", verb(env), name, plural(done, "change"))
	if err := repo.Release(ctx, name, msg, state); err != nil {
		return handoffs, setups, err
	}
	fmt.Fprintf(out, "Factory %s: recorded the results and released the lock.\n", name)
	if len(failures) > 0 {
		return handoffs, setups, fmt.Errorf("%s failed:\n  %s", plural(len(failures), "change"), strings.Join(failures, "\n  "))
	}
	return handoffs, setups, nil
}

func startedWhere(st *home.Init) string {
	switch {
	case st.URL != "":
		return ": " + st.URL
	case st.Session != "":
		return ", session " + st.Session
	}
	return ""
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

// apiClients holds one API client per runtime during an apply, so a
// client that has to log in does it once.
type apiClients map[*provider.Provider]provider.RuntimeAPI

func (a apiClients) get(p *provider.Provider) (provider.RuntimeAPI, error) {
	if api, ok := a[p]; ok {
		return api, nil
	}
	api, err := p.API()
	if err != nil {
		return nil, err
	}
	a[p] = api
	return api, nil
}

// writeSetups writes down, for each runtime whose API needs it, what to set
// up by hand for the factory's resources there: all of them, not only the
// ones this apply touched, so the file is always complete. It's written
// even when the API calls failed, since a missing setup can be why. The
// files hold the factory's token, so they're private, like handoffs.
func writeSetups(out io.Writer, opts Options, dp *plan.Factory, tok string, touched map[*provider.Provider]bool, apis apiClients) []string {
	var files []string
	for p := range touched {
		ms, ok := apis[p].(provider.ManualSetup)
		if !ok {
			continue
		}
		var resources []provider.Resource
		for _, r := range dp.Desired {
			if r.Provider == p {
				resources = append(resources, resourceFor(r, p, tok))
			}
		}
		text := ms.Setup(dp.Name, resources)
		if text == "" {
			continue
		}
		dir := filepath.Join(opts.HandoffDir, dp.Name)
		file := filepath.Join(dir, p.Name()+"-setup.md")
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			err = os.WriteFile(file, []byte(text), 0o600)
		}
		if err != nil {
			fmt.Fprintf(out, "Factory %s: couldn't write what to set up by hand for %s: %v\n", dp.Name, p.Name(), err)
			continue
		}
		fmt.Fprintf(out, "Factory %s: %s's API can't set up everything its resources rely on; see %s\n", dp.Name, p.Name(), file)
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

// getOnlyCreate reports whether this change creates a resource on a
// runtime that can only find resources by an ID state already has.
func getOnlyCreate(c *plan.Change) bool {
	return c.Action == plan.Create && c.Mode == provider.API && c.Provider != nil && !c.Provider.Matrix.FindByIdentity
}

// beginUncertainCreate records, and commits, that a create is about to
// happen whose result may be lost. The returned ID is what `fabrica
// confirm` later resolves.
func beginUncertainCreate(ctx context.Context, repo *Repo, env *plan.Env, dp *plan.Factory, state *home.State, c *plan.Change, now time.Time) (string, error) {
	shape := stateShape(c.Desired.Eval.Shape)
	id := HandoffID(dp.Name, c.Key, string(plan.Create), c.Desired.Fingerprint+c.Desired.TokenHash)
	// One pending entry per resource. An older handoff for it is replaced:
	// this create is what has to be resolved now.
	dropPendingForKey(state, c.Key)
	state.Pending = append(state.Pending, home.Pending{
		ID: id, Action: string(plan.Create), Resource: c.Key.ID, Runtime: c.Key.Runtime,
		Routine: c.Routine, Repo: c.Repo, Uncertain: true,
		Shape: &shape, TokenHash: c.Desired.TokenHash, BlueprintCommit: blueprintCommit(dp), IssuedAt: now,
	})
	msg := fmt.Sprintf("fabrica: %s %s (creating %s @ %s on %s)", verb(env), dp.Name, c.Routine, c.Repo, c.Key.Runtime)
	if err := repo.Checkpoint(ctx, dp.Name, msg, state); err != nil {
		dropUncertain(state, c.Key)
		return "", fmt.Errorf("didn't create it, because recording that the create was about to happen failed: %w", err)
	}
	return id, nil
}

func dropUncertain(state *home.State, key home.Key) {
	for i := 0; i < len(state.Pending); {
		p := state.Pending[i]
		if p.Uncertain && p.Key() == key {
			state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			continue
		}
		i++
	}
}

func dropPendingForKey(state *home.State, key home.Key) {
	for i := 0; i < len(state.Pending); {
		if state.Pending[i].Key() == key {
			state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			continue
		}
		i++
	}
}

// applyChange carries out one change and updates state to match.
func applyChange(ctx context.Context, opts Options, dp *plan.Factory, state *home.State, c *plan.Change, tok string, now time.Time, apis apiClients) (string, error) {
	switch c.Action {
	case plan.Adopt:
		upsertResource(state, *c.Current)
		return "", nil
	}
	// Unverifiable resources have no remote ID, so even on an API runtime a
	// person has to update or delete them.
	if c.Mode == provider.Handoff || c.Action != plan.Create && c.Current.RemoteID == "" {
		return writeHandoff(opts, dp, state, c, tok, now)
	}

	api, err := apis.get(c.Provider)
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
		Memory:  shape.Memory, SideEffects: shape.SideEffects, Prompt: r.Prompt, Fingerprint: r.Fingerprint,
		Fabrica: provider.ResourceFabrica{Placement: p.Matrix.TokenPlacement(), Token: tok, URL: fabricaURL(r.Endpoint, p, tok)},
	}
	res.Model, res.Connectors = modelFor(shape), connectorsFor(shape)
	return res
}

// modelFor is the model a shape picked, for a provider; nil means the
// runtime's default.
func modelFor(shape stub.Shape) *provider.ResourceModel {
	if shape.Model == "" {
		return nil
	}
	return &provider.ResourceModel{Name: shape.Model, Effort: shape.Effort, Fast: shape.Fast}
}

// connectorsFor is a shape's connectors, for a provider.
func connectorsFor(shape stub.Shape) []provider.ResourceConnector {
	var out []provider.ResourceConnector
	for _, c := range shape.Connectors {
		rc := provider.ResourceConnector{Name: c.Name, Kind: c.Kind}
		if c.Server != nil {
			rc.URL, rc.Transport, rc.Auth, rc.Secret = c.Server.URL, c.Server.Transport, c.Server.Auth, c.Server.Secret
			rc.Command, rc.Args, rc.Env = c.Server.Command, c.Server.Args, c.Server.Env
		}
		out = append(out, rc)
	}
	return out
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
	return home.Shape{Trigger: s.Trigger, Cron: s.Cron, Connectors: s.ConnectorNames(), Memory: s.Memory, SideEffects: s.SideEffects, Model: s.Model, Effort: s.Effort, Fast: s.Fast, Fingerprint: s.Fingerprint()}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
		data.RemoteID, data.FormerIdentity = c.Current.RemoteID, c.Current.FormerID
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
		for _, name := range sortedKeys(shape.SideEffects) {
			setting := shape.SideEffects[name]
			data.SideEffects = append(data.SideEffects, provider.HandoffSideEffect{Name: name, Setting: setting, Setup: stub.DescribeSideEffect(name, setting)})
		}
		data.Model = provider.HandoffModel{Name: shape.Model, Effort: shape.Effort, Fast: shape.Fast, Setup: stub.DescribeModel(shape)}
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
			fmt.Fprintln(out, "  The Claude desktop app keeps a scheduled task's schedule, folder and auto-fix setting itself, so finish in the app (Code tab → Routines):")
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
