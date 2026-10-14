package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/stub"
	"github.com/staltz/fabrica/internal/token"
)

func gitURL(spec string) string { return repourl.GitURL(spec) }

// PlanCloud plans one cloud factory, including one whose file was
// deleted (all its resources become deletes), or one being destroyed (the
// same, though its file is still there). Apply calls it again once it holds
// the factory's lock.
func (e *Env) PlanCloud(ctx context.Context, h *home.Home, name string) *Factory {
	d := h.Factories[name]
	dp := &Factory{Name: name, Spec: d, Destroy: e.Destroy}
	state, err := home.ReadState(h.Dir, name)
	if err != nil {
		dp.Diags.Errorf("state.invalid", home.StatePath(name), "%v", err)
		return dp
	}
	dp.State = state
	if state.Lock != nil && (e.LockHolder == "" || state.Lock.Holder != e.LockHolder) {
		dp.Diags.Errorf("factory.locked", home.StatePath(name), "factory %s is locked by %q since %s", name, state.Lock.Holder, state.Lock.Since.Format(time.RFC3339)).
			Hintf("another apply is running; wait for it. If it crashed, run `fabrica unlock %s` (the next apply adopts whatever it created; a create whose ID was never recorded is reported instead, and not retried)", name)
	}
	seen := map[home.Key]bool{}
	for _, r := range state.Resources {
		if seen[r.Key()] {
			dp.Diags.Errorf("state.duplicate", home.StatePath(name), "state lists %s on %s twice", r.ID, r.Runtime).
				Hintf("only Fabrica writes state files; restore this one from git history")
		}
		seen[r.Key()] = true
	}

	if d != nil && !e.Destroy {
		e.planDesired(ctx, dp, d)
	}
	providers := e.providersForCleanup(ctx, dp)
	current := e.refresh(ctx, dp, providers)
	e.diff(dp, current, providers)
	return dp
}

// planDesired works out every stub the factory should have, and every
// error that stands in the way.
func (e *Env) planDesired(ctx context.Context, dp *Factory, d *home.Factory) {
	e.loadBlueprint(ctx, dp, d.Blueprint)
	f := dp.Blueprint
	if f == nil {
		return
	}
	for _, rt := range d.Runtimes {
		p, err := resolveProvider(rt, f)
		if err != nil {
			dp.Diags.Errorf("factory.unknown-runtime", d.File, "%v", err)
			continue
		}
		if p.Matrix.Scope != d.Scope {
			dp.Diags.Errorf("factory.runtime-scope", d.File, "runtime %s is a %s runtime, but this is a %s factory", rt, p.Matrix.Scope, d.Scope)
			continue
		}
		mode, err := p.Mode()
		if err != nil {
			dp.Diags.Errorf("provider.no-mode", d.File, "%v", err)
			continue
		}
		if why := p.APIUnavailable(); why != nil && mode != provider.API {
			dp.Notes = append(dp.Notes, fmt.Sprintf("%s uses %s mode because %v; run `fabrica auth login` to set it up, and Fabrica manages %s through its API instead", rt, mode, why, rt))
		}
		dp.Runtimes = append(dp.Runtimes, &Runtime{Name: rt, Provider: p, Mode: mode, Evaluations: map[string]*stub.Evaluation{}})
	}
	routines := checkRoutinesAndParams(dp, d, f)
	for _, rt := range dp.Runtimes {
		for _, r := range routines {
			if !d.Runs(r.Ref.Name, rt.Name) {
				continue
			}
			ev := stub.Evaluate(f, r, stub.Target{Factory: d.Name, Runtime: rt.Provider, Endpoint: d.Endpoint()})
			rt.Evaluations[r.Ref.Name] = ev
			dp.Diags = append(dp.Diags, ev.Diags...)
		}
	}
	e.checkProductRepos(ctx, dp, d, f)

	if dp.Diags.HasErrors() {
		return // no point working out stubs that can't be installed
	}
	tokenHash := ""
	if secret, err := e.SecretFor(d.Server); err != nil {
		dp.Notes = append(dp.Notes, fmt.Sprintf("couldn't read the server secret, so rotated tokens go unnoticed: %v", err))
	} else if secret != "" {
		tokenHash = token.Hash(token.For(secret, d.Name))
	}
	for _, rt := range dp.Runtimes {
		for _, r := range routines {
			ev := rt.Evaluations[r.Ref.Name]
			if ev == nil {
				continue // the routine doesn't run on this runtime
			}
			fp := ev.Shape.Fingerprint()
			for _, repo := range d.SpecificTargets() {
				id := stub.Identity(d.Endpoint(), r.Ref.Name, repo)
				dp.Desired = append(dp.Desired, &Resource{
					Key: home.Key{Runtime: rt.Name, ID: id}, Factory: d.Name, Runtime: rt.Name,
					Routine: r.Ref.Name, Repo: repo, Endpoint: d.Endpoint(), Eval: ev, Fingerprint: fp,
					Prompt: stub.Prompt(id, r.Ref.Name, rt.Name, repo, fp), TokenHash: tokenHash,
					Mode: rt.Mode, Provider: rt.Provider,
				})
			}
		}
	}
}

// checkProductRepos reads each deployed repo's default branch and checks
// it can be served: remote runs can't inspect the workspace, so this is
// where a missing Fabrica line or a skill clash gets caught.
func (e *Env) checkProductRepos(ctx context.Context, dp *Factory, d *home.Factory, f *blueprint.Blueprint) {
	for _, repo := range d.SpecificTargets() {
		ws, err := product.InspectRemote(ctx, e.Git, repo)
		if err != nil {
			dp.Diags.Errorf("product.unreadable", repo, "couldn't read the repo's default branch: %v", err)
			continue
		}
		for _, diagnostic := range product.Check(ws, f) {
			if diagnostic.Severity == diag.Note {
				continue
			}
			dp.Diags.Add(diagnostic)
		}
	}
}

// providersForCleanup finds the provider of every runtime that has
// something in state, including runtimes the factory no longer uses.
func (e *Env) providersForCleanup(ctx context.Context, dp *Factory) map[string]*provider.Provider {
	out := map[string]*provider.Provider{}
	for _, rt := range dp.Runtimes {
		out[rt.Name] = rt.Provider
	}
	if dp.State == nil {
		return out
	}
	needed := map[string]bool{}
	for _, r := range dp.State.Resources {
		needed[r.Runtime] = true
	}
	for _, p := range dp.State.Pending {
		needed[p.Runtime] = true
	}
	var oldBlueprint *blueprint.Blueprint
	for rt := range needed {
		if out[rt] != nil {
			continue
		}
		if p, ok := provider.Builtin(rt); ok {
			out[rt] = p
			continue
		}
		// A folder provider the factory no longer uses: load it from
		// the blueprint the last apply used.
		if oldBlueprint == nil && dp.State.Blueprint != nil {
			sf := dp.State.Blueprint
			oldBlueprint, _ = e.Loader.LoadGit(ctx, sf.Git, sf.Commit, sf.Path)
		}
		if p, err := resolveProvider(rt, oldBlueprint); err == nil {
			out[rt] = p
		} else {
			dp.Diags.Errorf("cleanup.unknown-runtime", home.StatePath(dp.Name), "can't clean up resources on runtime %s: %v", rt, err)
		}
	}
	return out
}

// refresh returns what's installed. It starts from state and, for
// runtimes whose API can find resources, asks the runtime: by listing
// what it has, or else by looking up each remote ID that state records.
func (e *Env) refresh(ctx context.Context, dp *Factory, providers map[string]*provider.Provider) map[home.Key]home.Resource {
	current := map[home.Key]home.Resource{}
	if dp.State != nil {
		for _, r := range dp.State.Resources {
			current[r.Key()] = r
		}
	}
	if !e.Refresh || dp.Diags.HasErrors() {
		return current
	}
	for _, rt := range sortedProviderNames(providers) {
		p := providers[rt]
		if mode, _ := p.Mode(); mode != provider.API || p.IsLocal() {
			continue
		}
		api, err := p.API()
		if err != nil {
			continue
		}
		switch api := api.(type) {
		case provider.Lister:
			e.refreshByListing(ctx, dp, rt, p, api, current)
		case provider.Getter:
			e.refreshByRemoteID(ctx, dp, rt, api, current)
		}
	}
	return current
}

// refreshByListing asks a runtime for everything under the factory's
// endpoints. Anything it has that state doesn't is adopted.
func (e *Env) refreshByListing(ctx context.Context, dp *Factory, rt string, p *provider.Provider, api provider.Lister, current map[home.Key]home.Resource) {
	var found []provider.RemoteResource
	for _, endpoint := range endpointsToList(dp) {
		list, err := api.List(ctx, dp.Name, endpoint)
		if err != nil {
			dp.Diags.Errorf("refresh.failed", rt, "couldn't list resources: %v", err)
			return
		}
		found = append(found, list...)
	}
	byID := map[string][]provider.RemoteResource{}
	for _, r := range found {
		byID[r.Identity] = append(byID[r.Identity], r)
	}
	for _, id := range sortedRemoteIDs(byID) {
		list := byID[id]
		if len(list) > 1 {
			var ids []string
			for _, r := range list {
				ids = append(ids, r.RemoteID)
			}
			dp.Diags.Errorf("refresh.duplicate", rt, "two or more resources share the identity %s: %s", id, strings.Join(ids, ", ")).
				Hintf("Fabrica won't pick one to delete; remove the extra ones by hand, then plan again")
			continue
		}
		key := home.Key{Runtime: rt, ID: id}
		r, inState := current[key]
		if !inState {
			r = home.Resource{ID: id, Runtime: rt, Mode: string(provider.API)}
			r.Routine, r.Repo = splitIdentity(id)
			dp.Changes = append(dp.Changes, &Change{Action: Adopt, Key: key, Routine: r.Routine, Repo: r.Repo, Mode: provider.API, Provider: p,
				Current: &home.Resource{ID: id, Runtime: rt, RemoteID: list[0].RemoteID, Routine: r.Routine, Repo: r.Repo, Mode: string(provider.API), Shape: home.Shape{Fingerprint: list[0].Fingerprint}},
				Reasons: []string{"it exists in the runtime but isn't in state (maybe an earlier apply crashed before recording it)"}})
		}
		r.RemoteID = list[0].RemoteID
		if list[0].Fingerprint != "" {
			r.Shape.Fingerprint = list[0].Fingerprint
		}
		current[key] = r
	}
	// Resources state remembers but the runtime no longer has.
	for key, r := range current {
		if key.Runtime == rt && r.RemoteID != "" && len(byID[key.ID]) == 0 {
			delete(current, key)
			dp.Notes = append(dp.Notes, fmt.Sprintf("%s on %s is in state but gone from the runtime (deleted by hand?)", key.ID, rt))
		}
	}
}

// refreshByRemoteID looks up, one by one, the resources state records on
// a runtime that can't list them. It can't find a resource that state
// never got an ID for, which is why apply records a create before calling
// the API and refuses to retry one whose ID it never learned.
func (e *Env) refreshByRemoteID(ctx context.Context, dp *Factory, rt string, api provider.Getter, current map[home.Key]home.Resource) {
	for _, key := range sortedKeysOf(current) {
		r := current[key]
		if key.Runtime != rt || r.RemoteID == "" {
			continue // unverifiable resources have nothing to look up
		}
		remote, err := api.Get(ctx, r.RemoteID)
		if err != nil {
			dp.Diags.Errorf("refresh.failed", rt, "couldn't look up %s (%s): %v", key.ID, r.RemoteID, err)
			return
		}
		if remote == nil {
			delete(current, key)
			note := fmt.Sprintf("%s on %s is in state but gone from the runtime (deleted by hand?)", key.ID, rt)
			if r.Mode == string(provider.Handoff) {
				// Fabrica can't search for it, so say what a mistyped ID
				// at `fabrica confirm` would mean.
				note += fmt.Sprintf("; it was set up by hand, so if %s isn't its real ID, it's still there, and apply's new one would run next to it: delete the old one in the runtime", r.RemoteID)
			}
			dp.Notes = append(dp.Notes, note)
			continue
		}
		// The installed stub is whatever its prompt says, and a stub that
		// lost its name or fingerprint needs putting back.
		r.Shape.Fingerprint = remote.Fingerprint
		if remote.Identity != key.ID {
			r.Shape.Fingerprint = ""
			dp.Notes = append(dp.Notes, fmt.Sprintf("%s on %s (%s) is named %q in the runtime; apply names it back", key.ID, rt, r.RemoteID, remote.Identity))
		}
		current[key] = r
	}
}

// endpointsToList is every endpoint this factory may own resources
// under: its current one, plus any older ones still recorded in state (from
// before a server: change, or for a deleted factory).
func endpointsToList(dp *Factory) []string {
	seen := map[string]bool{}
	var out []string
	add := func(endpoint string) {
		// Compare without the scheme: identities don't carry one.
		if id := stub.EndpointID(endpoint); endpoint != "" && !seen[id] {
			seen[id] = true
			out = append(out, endpoint)
		}
	}
	if dp.Spec != nil {
		add(dp.Spec.Endpoint())
	}
	if dp.State != nil {
		for _, r := range dp.State.Resources {
			if prefix, _, ok := strings.Cut(r.ID, "#"); ok {
				add("https://" + prefix)
			}
		}
	}
	return out
}

// diff compares what's wanted with what's there.
func (e *Env) diff(dp *Factory, current map[home.Key]home.Resource, providers map[string]*provider.Provider) {
	pending := map[home.Key]home.Pending{}
	// uncertain are API creates whose result was never learned. They must
	// not become another create: the resource may already exist, and a
	// runtime that can't list resources would never notice the duplicate.
	uncertain := map[home.Key]bool{}
	if dp.State != nil {
		for _, p := range dp.State.Pending {
			if !p.Uncertain {
				pending[p.Key()] = p
				continue
			}
			if uncertain[p.Key()] {
				continue
			}
			uncertain[p.Key()] = true
			dp.Diags.Errorf("refresh.uncertain-create", p.Resource, "a create on %s may have happened, but Fabrica never learned its ID, and %s can't list its resources, so apply will not create another", p.Runtime, p.Runtime).
				Hintf("%s", UncertainCreateHint(p.Resource, p.ID))
		}
	}
	wanted := map[home.Key]bool{}
	for _, want := range dp.Desired {
		wanted[want.Key] = true
		if uncertain[want.Key] {
			continue
		}
		pend, hasPend := pending[want.Key]
		cur, hasCur := current[want.Key]
		if hasPend && pend.Action != string(Delete) && pend.Shape != nil && pend.Shape.Fingerprint == want.Fingerprint && tokenMatches(pend.TokenHash, want.TokenHash) {
			dp.Pending = append(dp.Pending, pend)
			continue
		}
		c := &Change{Key: want.Key, Routine: want.Routine, Repo: want.Repo, Mode: want.Mode, Provider: want.Provider, Desired: want}
		if hasPend {
			c.Supersedes = pend.ID
		}
		if hasCur {
			reasons := shapeReasons(cur.Shape, want)
			if cur.RenamedFrom != "" {
				// Its fingerprint was dropped, so say why instead.
				reasons = []string{fmt.Sprintf("its prompt still names runtime %s, which is now called %s", cur.RenamedFrom, cur.Runtime)}
			}
			if !tokenMatches(cur.TokenHash, want.TokenHash) {
				reasons = append(reasons, "the factory's token changed (the server secret was rotated)")
			}
			if len(reasons) == 0 && !hasPend {
				continue // in sync
			}
			if len(reasons) == 0 {
				reasons = []string{"a pending handoff was made for another version of this stub"}
			}
			curCopy := cur
			c.Action, c.Current, c.Reasons = Update, &curCopy, reasons
		} else {
			c.Action, c.Reasons = Create, []string{"it isn't installed yet"}
		}
		dp.Changes = append(dp.Changes, c)
	}

	for _, key := range sortedKeysOf(current) {
		if wanted[key] {
			continue
		}
		cur := current[key]
		if pend, ok := pending[key]; ok && pend.Action == string(Delete) {
			dp.Pending = append(dp.Pending, pend)
			continue
		}
		p := providers[key.Runtime]
		mode := provider.Handoff
		if p != nil {
			mode, _ = p.Mode()
		}
		c := &Change{Action: Delete, Key: key, Routine: cur.Routine, Repo: cur.Repo, Mode: mode, Provider: p, Current: &cur,
			Reasons: []string{e.whyGone(dp, cur)}}
		if pend, ok := pending[key]; ok {
			c.Supersedes = pend.ID
		}
		if p == nil {
			continue // the error was reported in providersForCleanup
		}
		dp.Changes = append(dp.Changes, c)
	}

	// A create handoff that's still pending for something no longer
	// wanted: we can't know whether someone already did it, so ask them to
	// delete it if they did. The delete handoff that replaces it waits here
	// too, since state never recorded the resource.
	for _, key := range sortedPendingKeys(pending) {
		pend := pending[key]
		if _, ok := current[key]; ok || wanted[key] {
			continue
		}
		if pend.Action == string(Delete) {
			dp.Pending = append(dp.Pending, pend)
			continue
		}
		if pend.Action != string(Create) {
			continue
		}
		p := providers[key.Runtime]
		if p == nil {
			continue
		}
		mode, _ := p.Mode()
		dp.Changes = append(dp.Changes, &Change{Action: Delete, Key: key, Routine: pend.Routine, Repo: pend.Repo, Mode: mode, Provider: p,
			Current:    &home.Resource{ID: pend.Resource, Runtime: pend.Runtime, Routine: pend.Routine, Repo: pend.Repo, Mode: string(mode)},
			Reasons:    []string{"a create handoff for it was never confirmed, and it's no longer deployed; if someone created it anyway, it needs to go"},
			Supersedes: pend.ID})
	}
	sort.SliceStable(dp.Changes, func(i, j int) bool {
		a, b := dp.Changes[i], dp.Changes[j]
		if a.Key.ID != b.Key.ID {
			return a.Key.ID < b.Key.ID
		}
		return a.Key.Runtime < b.Key.Runtime
	})
}

// shapeReasons explains how an installed stub differs from the wanted one.
func shapeReasons(cur home.Shape, want *Resource) []string {
	if cur.Fingerprint == want.Fingerprint {
		return nil
	}
	shape := want.Eval.Shape
	var reasons []string
	if cur.Trigger != "" && (cur.Trigger != shape.Trigger || cur.Cron != shape.Cron) {
		reasons = append(reasons, fmt.Sprintf("the trigger changed from %s to %s", describeTrigger(cur.Trigger, cur.Cron), describeTrigger(shape.Trigger, shape.Cron)))
	}
	if cur.Trigger != "" {
		if added, removed := diffNames(cur.Connectors, shape.ConnectorNames()); len(added)+len(removed) > 0 {
			var parts []string
			if len(added) > 0 {
				parts = append(parts, "adds "+strings.Join(added, ", "))
			}
			if len(removed) > 0 {
				parts = append(parts, "drops "+strings.Join(removed, ", "))
			}
			reasons = append(reasons, "the connectors changed ("+strings.Join(parts, "; ")+")")
		}
		if cur.Memory != shape.Memory {
			reasons = append(reasons, fmt.Sprintf("the runtime's memory should be %s", describeMemory(shape.Memory)))
		}
		for _, name := range sortedSettings(shape.SideEffects) {
			if want := shape.SideEffects[name]; cur.SideEffects[name] != want {
				reasons = append(reasons, fmt.Sprintf("the runtime's own %s should be %s", name, want))
			}
		}
		if cur.Model != shape.Model || cur.Effort != shape.Effort || cur.Fast != shape.Fast {
			reasons = append(reasons, "the model should be "+describeModel(shape.Model, shape.Effort, shape.Fast))
		}
	}
	if len(reasons) == 0 {
		if cur.Fingerprint == "" {
			reasons = append(reasons, "Fabrica can't tell which version of the stub is installed")
		} else {
			reasons = append(reasons, fmt.Sprintf("its shape changed (a connector's settings or the server address; stub %s → %s)", cur.Fingerprint, want.Fingerprint))
		}
	}
	return reasons
}

func sortedSettings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func describeMemory(setting string) string {
	if setting == "" {
		return "left to Fabrica"
	}
	return setting
}

func describeModel(model, effort, fast string) string {
	if model == "" {
		return "the runtime's default"
	}
	var settings []string
	if effort != "" {
		settings = append(settings, effort)
	}
	switch fast {
	case stub.FastOn:
		settings = append(settings, "fast")
	case stub.FastOff:
		settings = append(settings, "not fast")
	}
	if len(settings) == 0 {
		return model
	}
	return model + " (" + strings.Join(settings, ", ") + ")"
}

func describeTrigger(on, cron string) string {
	if cron != "" {
		return on + " (" + cron + ")"
	}
	return on
}

// whyGone explains why an installed stub is no longer deployed.
func (e *Env) whyGone(dp *Factory, r home.Resource) string {
	d := dp.Spec
	switch {
	case d == nil:
		return "its factory file was deleted"
	case e.Destroy:
		return "the factory is being destroyed"
	case !d.HasRuntime(r.Runtime):
		return fmt.Sprintf("runtime %s was removed from the factory", r.Runtime)
	case !d.HasRoutine(r.Routine):
		return fmt.Sprintf("routine %s was removed from the factory", r.Routine)
	case !d.Runs(r.Routine, r.Runtime):
		return fmt.Sprintf("routine %s no longer runs on runtime %s", r.Routine, r.Runtime)
	case !d.Covers(r.Repo):
		return fmt.Sprintf("repo %s was removed from the factory", r.Repo)
	case d.Endpoint() != "" && !strings.HasPrefix(r.ID, stub.EndpointID(d.Endpoint())+"#"):
		return "the factory's server address changed, so this stub belongs to the old one"
	}
	return "it's no longer deployed"
}

// UncertainCreateHint is how to resolve a create whose ID was never
// recorded. Apply uses the same wording when the create fails uncertainly.
func UncertainCreateHint(identity, id string) string {
	return fmt.Sprintf("look for a resource named %s. If it's there, run `fabrica confirm %s --remote-id <its ID>`. If it isn't, run `fabrica confirm %s --absent`", identity, id, id)
}

func tokenMatches(have, want string) bool {
	// When we don't know the wanted token (no secret), we can't tell.
	return want == "" || have == want
}

func diffNames(before, after []string) (added, removed []string) {
	b := map[string]bool{}
	for _, n := range before {
		b[n] = true
	}
	a := map[string]bool{}
	for _, n := range after {
		a[n] = true
		if !b[n] {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !a[n] {
			removed = append(removed, n)
		}
	}
	return added, removed
}

// splitIdentity pulls the routine and repo out of an identity like
// host/factory#routine@repo.
func splitIdentity(id string) (routine, repo string) {
	_, rest, _ := strings.Cut(id, "#")
	routine, repo, _ = strings.Cut(rest, "@")
	return routine, repo
}

func sortedKeysOf(m map[home.Key]home.Resource) []home.Key {
	keys := make([]home.Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ID != keys[j].ID {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].Runtime < keys[j].Runtime
	})
	return keys
}

func sortedPendingKeys(m map[home.Key]home.Pending) []home.Key {
	keys := make([]home.Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ID != keys[j].ID {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].Runtime < keys[j].Runtime
	})
	return keys
}

func sortedProviderNames(m map[string]*provider.Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRemoteIDs(m map[string][]provider.RemoteResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
