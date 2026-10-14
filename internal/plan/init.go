package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// A blueprint's init routine runs once per cloud factory: apply starts a
// session for it the first time it creates the factory. The routine lists
// the runtimes it would like, in order (or else the factory's own), and
// the first one Fabrica can start a session on by itself wins. When none
// can, apply writes a handoff for the first one a person can start it on.

// Init is what apply would do about a factory's init session.
type Init struct {
	Routine *blueprint.Routine
	// Candidates are the runtimes the init would like, in order, with what
	// each one can do.
	Candidates []*InitCandidate
	// Chosen is the runtime the session starts on, and how. Nil when
	// there's nothing to start.
	Chosen *InitCandidate
	// ID is the init's ID, once an attempt to start it recorded one. The
	// first attempt gets its ID from apply (see NewInitID).
	ID string
	// Identity, Repo, Repos, Prompt and Fingerprint describe the session
	// to start.
	Identity    string
	Repo        string
	Repos       []string
	Endpoint    string
	Prompt      string
	Fingerprint string
	// Reason says why apply starts it now.
	Reason string
	// Waiting is the init's handoff, while it waits for `fabrica confirm`.
	Waiting *home.Init
}

// Start reports whether apply should start the session.
func (i *Init) Start() bool { return i != nil && i.Chosen != nil }

// InitCandidate is one runtime an init would like to run on.
type InitCandidate struct {
	Runtime  string
	Provider *provider.Provider
	Eval     *stub.Evaluation
	// Mode is how Fabrica would start the session there: api (by itself)
	// or handoff. Empty when it can't at all.
	Mode provider.ApplyMode
	// Why says why Fabrica can't start a session there by itself (and,
	// when Mode is empty, not by handoff either).
	Why string
}

// planInit works out whether the factory's init session should start, and
// where.
func (e *Env) planInit(dp *Factory, d *home.Factory, f *blueprint.Blueprint) {
	r := f.Init()
	if r == nil {
		return
	}
	if d.HasRoutine(r.Ref.Name) {
		dp.Diags.Errorf("factory.init-routine", d.File, "routine %s is the blueprint's init routine: it runs by itself, once, when the factory is first applied, so it doesn't go under routines:", r.Ref.Name).
			Hintf("remove it from routines:")
		return
	}
	st := dp.State.Init
	switch {
	case d.NoInit:
		dp.Notes = append(dp.Notes, fmt.Sprintf("init routine %s never runs here: the factory says init: false", r.Ref.Name))
		return
	case st == nil && dp.State.Born():
		dp.Notes = append(dp.Notes, fmt.Sprintf("init routine %s doesn't run: it only runs the first time a factory is applied, and this one was applied before", r.Ref.Name))
		return
	case st != nil && st.Status == home.InitStarted:
		dp.Notes = append(dp.Notes, "init "+describeInitRecord(st))
		return
	case st != nil && st.Status == home.InitWaiting:
		dp.Init = &Init{Routine: r, Waiting: st}
		return
	}

	in := &Init{Routine: r, Endpoint: d.Endpoint(), Identity: stub.InitIdentity(d.Endpoint(), r.Ref.Name), Repos: d.SpecificTargets()}
	in.Repo = in.Repos[0]
	switch {
	case st == nil:
		in.Reason = "the factory is being applied for the first time"
	case st.Status == home.InitStarting:
		in.Reason = "an earlier apply stopped while it was starting it"
		in.ID = st.ID
	default:
		in.Reason = "the last attempt failed: " + st.Error
		in.ID = st.ID
	}
	dp.Init = in
	checkInitParams(dp, d, r)

	runtimes := r.Runtimes
	if len(runtimes) == 0 {
		runtimes = d.Runtimes
	}
	for _, rt := range runtimes {
		in.Candidates = append(in.Candidates, e.initCandidate(rt, d, f, r))
	}
	// The first runtime Fabrica can start it on by itself wins; failing
	// that, the first one a person can.
	for _, mode := range []provider.ApplyMode{provider.API, provider.Handoff} {
		for _, c := range in.Candidates {
			if c.Mode == mode && in.Chosen == nil {
				in.Chosen = c
			}
		}
	}
	if in.Chosen == nil {
		var why []string
		for _, c := range in.Candidates {
			why = append(why, c.Runtime+": "+c.Why)
		}
		dp.Diags.Errorf("init.no-runtime", r.Where(), "init routine %s has to run once now, but there's no runtime to start it on (%s)", r.Ref.Name, strings.Join(why, "; ")).
			Hintf("set up one of them (like `fabrica auth login`), add a runtime it can start on to the routine's runtimes:, or set init: false in %s if this factory doesn't need it", d.File)
		return
	}
	in.Fingerprint = in.Chosen.Eval.Shape.Fingerprint()
	in.Prompt = stub.InitPrompt(in.Identity, d.Name, r.Ref.Name, in.Chosen.Runtime, in.Repo, in.Fingerprint, in.Repos)
}

// initCandidate checks one runtime an init would like to run on.
func (e *Env) initCandidate(rt string, d *home.Factory, f *blueprint.Blueprint, r *blueprint.Routine) *InitCandidate {
	c := &InitCandidate{Runtime: rt}
	p, err := resolveProvider(rt, f)
	if err != nil {
		c.Why = err.Error()
		return c
	}
	c.Provider = p
	c.Eval = stub.Evaluate(f, r, stub.Target{Factory: d.Name, Runtime: p, Endpoint: d.Endpoint()})
	if errs := c.Eval.Diags.Only(diag.Error); len(errs) > 0 {
		var msgs []string
		for _, d := range errs {
			msgs = append(msgs, d.Message)
		}
		c.Why = "it can't run the routine: " + strings.Join(msgs, "; ")
		return c
	}
	if p.IsLocal() && e.SkipLocal {
		c.Why = "it's a local runtime, and there's nothing local in CI"
		return c
	}
	why := p.LaunchUnavailable()
	if why == nil {
		c.Mode = provider.API
		return c
	}
	c.Why = why.Error()
	if p.CanHandOffLaunch() {
		c.Mode = provider.Handoff
	}
	return c
}

// checkInitParams checks that the factory sets every param the init
// needs, for the repo it starts in. (Whether params it sets are declared
// is checked with the other routines'.)
func checkInitParams(dp *Factory, d *home.Factory, r *blueprint.Routine) {
	repo := d.SpecificTargets()[0]
	merged := d.ParamsFor(repo)
	for _, prm := range r.Params {
		if _, ok := merged[prm.Name]; prm.Required && !ok {
			dp.Diags.Errorf("factory.missing-param", d.File, "init routine %s needs param %q, but it isn't set for %s", r.Ref.Name, prm.Name, repo).
				Hintf("set it under params: (for every repo) or in the repo's own params:")
		}
	}
}

// NewInitID makes the ID of a factory's init, on the first attempt to
// start it. The time goes in, so a factory that's deleted and made again
// under the same name gets a new init; later attempts reuse the ID state
// recorded.
func NewInitID(factory, identity string, now time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{factory, identity, now.UTC().Format(time.RFC3339Nano)}, "\x00")))
	return "i_" + hex.EncodeToString(sum[:])[:10]
}

// describeInitRecord says what happened to an init, like "onboard
// started on cursor-cloud on 2026-10-10 (https://…)".
func describeInitRecord(st *home.Init) string {
	s := fmt.Sprintf("%s started on %s on %s", st.Routine, st.Runtime, st.At.Format("2006-01-02"))
	switch {
	case st.URL != "":
		s += " (" + st.URL + ")"
	case st.Session != "":
		s += " (session " + st.Session + ")"
	}
	return s
}

// Describe is a one-line summary of what apply does about the init.
func (i *Init) Describe() string {
	c := i.Chosen
	how := "Fabrica starts it"
	if c.Mode == provider.Handoff {
		how = "a handoff says how to start it"
	}
	return fmt.Sprintf("! init   %s @ %s on %s (%s; %s): %s", i.Routine.Ref.Name, i.Repo, c.Runtime, c.Mode, how, i.Reason)
}
