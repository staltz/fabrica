package mcpserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/pace"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

// The heartbeat.
//
// A routine with a pace trigger, like `{ on: pace, every: 4 }`, runs every
// 4 paces of its factory. Runtimes don't keep that time: each stub of such
// a routine fires on a webhook (a Claude routine's API trigger, a Cursor
// automation's webhook trigger), and the remote server keeps each
// factory's heartbeat. It beats once every pace, counted from the Unix
// epoch, and on every beat it calls the webhooks of the stubs whose
// routine runs on it: those whose every: divides the beat's number.
//
// Everything it needs is read live: the pace from the factory file, every:
// from the blueprint, and the stubs and their webhooks from the factory's
// state file. So changing pace: or every: takes effect at the next refresh,
// with no apply.
//
// Each fire is claimed in the run-history database before the call, so a
// stub fires at most once per beat, even when the server restarts in the
// middle of one. A server that was down when a beat started fires it as
// soon as it's back, as long as the beat hasn't ended; a beat that passed
// entirely while it was down is skipped.

// heartbeatMaxSleep is the longest the heartbeat sleeps, so a new factory,
// a new pace or a new webhook is picked up soon after a refresh.
const heartbeatMaxSleep = time.Minute

// heartbeatParallel bounds how many webhooks are called at once.
const heartbeatParallel = 8

// RunHeartbeat keeps the heartbeat of every factory with a pace until ctx
// is done: it wakes up when the next beat of any of them starts (or after
// a minute at most) and fires what that beat runs.
func (s *Remote) RunHeartbeat(ctx context.Context) {
	for {
		now := s.now()
		s.Beat(ctx, now)
		t := time.NewTimer(s.untilNextBeat(s.now()))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// untilNextBeat is how long until the next beat of any factory starts,
// capped at heartbeatMaxSleep.
func (s *Remote) untilNextBeat(now time.Time) time.Duration {
	wait := heartbeatMaxSleep
	for _, rd := range s.current().factories {
		p := rd.factory.Pace
		if p == 0 {
			continue
		}
		next := p.Beat(p.BeatAt(now).N + 1).At
		wait = min(wait, next.Sub(now))
	}
	return max(wait, 0)
}

// Beat fires, in every factory with a pace, the stubs whose routine runs on
// the beat now falls in, unless they were fired on it already. It returns
// once every call is done.
func (s *Remote) Beat(ctx context.Context, now time.Time) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, heartbeatParallel)
	for name, rd := range s.current().factories {
		for _, f := range s.due(name, rd, now) {
			claimed, err := s.opts.Runs.ClaimFire(ctx, f.fire)
			if err != nil {
				s.opts.Logger.Error("heartbeat: couldn't record a fire, so it's skipped", "factory", name, "stub", f.fire.Stub, "error", err)
				continue
			}
			if !claimed {
				continue // fired on this beat already
			}
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer func() { <-slots; wg.Done() }()
				s.fire(ctx, f)
			}()
		}
	}
	wg.Wait()
}

// dueFire is a stub to fire on a beat.
type dueFire struct {
	fire runs.Fire
	call provider.WebhookCall
	// openErr is why the webhook's token couldn't be opened, if it
	// couldn't. The fire is still recorded, as failed, so people see it.
	openErr error
}

// due lists the stubs of one factory that fire on the beat now falls in.
func (s *Remote) due(name string, rd *remoteFactory, now time.Time) []dueFire {
	d := rd.factory
	if d.Pace == 0 || rd.refusal != "" || rd.blueprint == nil {
		return nil
	}
	beat := d.Pace.BeatAt(now)
	var out []dueFire
	for _, res := range rd.resources {
		if !res.HasWebhookTrigger() || res.Webhook == nil {
			continue
		}
		r := rd.blueprint.Routines[res.Routine]
		if r == nil || !beat.Fires(r.Every()) || !stillDeployed(d, r, res) {
			continue
		}
		f := dueFire{
			fire: runs.Fire{Factory: name, Runtime: res.Runtime, Stub: res.ID, Routine: res.Routine, Repo: res.Repo, BeatAt: beat.At, FiredAt: now},
			call: provider.WebhookCall{Runtime: res.Runtime, URL: res.Webhook.URL, Header: res.Webhook.Header, Text: beatText(d, r, beat)},
		}
		tok, err := token.Open(s.opts.Secret, name, res.ID, res.Webhook.Token)
		if err != nil {
			f.openErr = fmt.Errorf("couldn't open the webhook's token: %v; run `%s` again", err, provider.WebhookCommand(name, res.Runtime, res.ID))
		}
		f.call.Token = tok
		out = append(out, f)
	}
	return out
}

// stillDeployed reports whether the factory still runs a stub's routine on
// its runtime and repo, like the stale-stub check does. A stub it doesn't
// is left for the next apply to delete, and never fired.
func stillDeployed(d *home.Factory, r *blueprint.Routine, res home.Resource) bool {
	return d.Runs(r.Ref.Name, res.Runtime) && d.Covers(res.Repo)
}

// beatText tells the run why it started.
func beatText(d *home.Factory, r *blueprint.Routine, beat pace.Beat) string {
	return fmt.Sprintf("Fabrica started this run of routine %s on the heartbeat of factory %s: the beat at %s (it runs %s, every %s at the factory's pace of %s).",
		r.Ref.Name, d.Name, beat.At.Format(time.RFC3339), blueprint.DescribeEvery(r.Every()), pace.Minutes(d.Pace.Times(r.Every())), d.Pace)
}

// fire calls one stub's webhook and records how it went.
func (s *Remote) fire(ctx context.Context, f dueFire) {
	err := f.openErr
	if err == nil {
		err = provider.FireWebhook(ctx, s.opts.Webhooks, f.call)
	}
	log := s.opts.Logger.With("factory", f.fire.Factory, "routine", f.fire.Routine, "repo", f.fire.Repo, "runtime", f.fire.Runtime, "beat", f.fire.BeatAt.Format(time.RFC3339))
	if err != nil {
		log.Warn("heartbeat: the webhook call failed", "error", err)
	} else {
		log.Info("heartbeat: fired")
	}
	// Record it even when ctx is done, so the beat isn't left "firing".
	if rerr := s.opts.Runs.FinishFire(context.WithoutCancel(ctx), f.fire, err); rerr != nil {
		log.Error("heartbeat: couldn't record how a fire went", "error", rerr)
	}
}

func (s *Remote) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}
