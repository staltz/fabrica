// Package heartbeat is the local heartbeat: what `fabrica heartbeat` does
// for the local factories in your personal home repo.
//
// A routine with a pace trigger, like `{ on: pace, every: 4 }`, runs every
// 4 paces of its factory. In a cloud factory, the remote Fabrica server
// keeps that time and calls the stubs' webhooks. A local factory has no
// server running all the time, so a job that `fabrica apply` installs (a
// launchd agent, or a systemd user timer) runs `fabrica heartbeat` every
// minute instead. Each run is a tick: for every local factory with a pace,
// it works out the beat it's in (counted from the Unix epoch, like the
// server does), and on a beat that's new, it starts a session for each
// routine that runs on it, on each runtime and repo the factory runs it on.
//
// Nothing is installed in the local tools for these routines. A session
// is a headless run of the tool (`claude -p` for claude-code-local) in a
// fresh clone of the repo, connected to your local `fabrica mcp`, which
// serves the routine like it serves a hook's: it matches the factory for
// the clone's remote and checks the stub.
//
// Every start is claimed in runs.db first (the pace_fires table), so a
// routine starts at most once per beat on each runtime and repo, however
// many ticks run. A computer that was asleep when a beat started starts it
// at the first tick after it wakes, as long as the beat hasn't ended; a
// beat that passed entirely while it slept is skipped.
package heartbeat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/pace"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/stub"
)

// Options configure the local heartbeat.
type Options struct {
	// HomeDir is your personal home repo.
	HomeDir string
	// Binary is the fabrica command a session runs its Fabrica MCP server
	// with: `<Binary> mcp --runtime <runtime> --home <HomeDir>`.
	Binary string
	Git    *gitx.Cache
	Loader *blueprint.Loader
	// Runs is your local run history, runs.db in your home repo.
	Runs *runs.DB
	// CacheDir is Fabrica's cache. Sessions get folders in
	// sessions/<factory>/, and the heartbeat remembers the last beat it
	// handled for each factory in heartbeat/.
	CacheDir string
	Logger   *slog.Logger
	// Launcher returns what starts sessions on a runtime. Nil means the
	// runtime's own launcher.
	Launcher func(*provider.Provider) (provider.Launcher, error)
}

// sessionMaxIdle is how long a session's folder (a clone of the repo and
// its log) is kept after the session last wrote to its log.
const sessionMaxIdle = 24 * time.Hour

// Started is one session a tick started (or tried to).
type Started struct {
	Factory, Routine, Runtime, Repo string
	BeatAt                          time.Time
	// Log is the session's output, when it started.
	Log string
	Err error
}

// Tick fires what the beat now falls in runs, in every local factory with
// a pace, unless it fired already. It returns the sessions it started, or
// tried to.
func Tick(ctx context.Context, o Options, now time.Time) ([]Started, error) {
	log := o.logger()
	h, err := home.Open(o.HomeDir)
	if err != nil {
		return nil, err
	}
	var out []Started
	for _, name := range h.Names() {
		d := h.Factories[name]
		if d.Scope != provider.Local || d.Pace == 0 || len(d.SpecificTargets()) == 0 {
			continue
		}
		beat := d.Pace.BeatAt(now)
		if o.handled(name, d.Pace, beat) {
			continue
		}
		started, err := o.beat(ctx, h, d, beat, now)
		out = append(out, started...)
		if err != nil {
			log.Error("heartbeat: skipped a beat of a factory; trying again at the next tick", "factory", name, "beat", beat.At.Format(time.RFC3339), "error", err)
			continue
		}
		o.markHandled(name, d.Pace, beat)
	}
	o.prune()
	return out, nil
}

// beat starts the sessions one factory's beat runs.
func (o Options) beat(ctx context.Context, h *home.Home, d *home.Factory, beat pace.Beat, now time.Time) ([]Started, error) {
	resolved, err := o.Git.Remote(repourl.GitURL(d.Blueprint.Git), gitx.Full).Resolve(ctx, d.Blueprint.Ref)
	if err != nil {
		return nil, fmt.Errorf("couldn't read the blueprint: %w", err)
	}
	f, diags := o.Loader.LoadGit(ctx, d.Blueprint.Git, resolved.Commit, d.Blueprint.Path)
	if errs := diags.Only(diag.Error); f == nil || len(errs) > 0 {
		return nil, fmt.Errorf("the blueprint %s has problems; `fabrica plan` says which", d.Blueprint)
	}
	var out []Started
	for _, rname := range sortedRoutines(f) {
		r := f.Routines[rname]
		if !beat.Fires(r.Every()) {
			continue
		}
		for _, rt := range d.Runtimes {
			if !d.Runs(rname, rt) {
				continue
			}
			out = append(out, o.fireRoutine(ctx, h, d, f, r, rt, beat, now)...)
		}
	}
	return out, nil
}

// fireRoutine starts a routine's sessions on one runtime: one per repo.
func (o Options) fireRoutine(ctx context.Context, h *home.Home, d *home.Factory, f *blueprint.Blueprint, r *blueprint.Routine, rt string, beat pace.Beat, now time.Time) []Started {
	log := o.logger().With("factory", d.Name, "routine", r.Ref.Name, "runtime", rt)
	p, ok := provider.Builtin(rt)
	if !ok {
		return nil
	}
	ev := stub.Evaluate(f, r, stub.Target{Factory: d.Name, Runtime: p, Pace: d.Pace})
	var st *stub.Stub
	for _, s := range ev.Stubs {
		if s.Shape.HasTrigger(blueprint.PaceTrigger) {
			st = s
		}
	}
	if !ev.OK() || st == nil {
		log.Warn("heartbeat: the routine can't run on its pace here; `fabrica plan` says why")
		return nil
	}
	fp := st.Shape.Fingerprint()
	var out []Started
	for _, repo := range d.SpecificTargets() {
		if m, _ := h.MatchLocal(repo, rt); m != d {
			continue // another local factory serves this repo here
		}
		id := stub.Identity(d.Tag(), st.Name(r.Ref.Name), repo)
		fire := runs.Fire{Factory: d.Name, Runtime: rt, Stub: id, Routine: r.Ref.Name, Repo: repo, BeatAt: beat.At, FiredAt: now}
		claimed, err := o.Runs.ClaimFire(ctx, fire)
		if err != nil {
			log.Error("heartbeat: couldn't record a start, so it's skipped", "repo", repo, "error", err)
			continue
		}
		if !claimed {
			continue // started on this beat already
		}
		s := Started{Factory: d.Name, Routine: r.Ref.Name, Runtime: rt, Repo: repo, BeatAt: beat.At}
		s.Log, s.Err = o.launch(ctx, p, d, r, st, id, repo, fp, beat)
		if s.Err != nil {
			log.Warn("heartbeat: couldn't start the session", "repo", repo, "error", s.Err)
		} else {
			log.Info("heartbeat: started a session", "repo", repo, "log", s.Log)
		}
		if err := o.Runs.FinishFire(context.WithoutCancel(ctx), fire, s.Err); err != nil {
			log.Error("heartbeat: couldn't record how a start went", "error", err)
		}
		out = append(out, s)
	}
	return out
}

// launch starts one session: a headless run of the tool in a fresh clone
// of the repo, served by your local `fabrica mcp`.
func (o Options) launch(ctx context.Context, p *provider.Provider, d *home.Factory, r *blueprint.Routine, st *stub.Stub, identity, repo, fp string, beat pace.Beat) (string, error) {
	newLauncher := o.Launcher
	if newLauncher == nil {
		newLauncher = (*provider.Provider).Launcher
	}
	l, err := newLauncher(p)
	if err != nil {
		return "", err
	}
	id := SessionID(d.Name, p.Name(), identity, beat)
	rt := p.Name()
	prompt := stub.LocalPrompt(r.Ref.Name, d.Name, rt, repo, fp) + " " + BeatText(d, r, beat)
	launched, err := l.Launch(ctx, provider.Session{
		ID: id, Identity: identity, Factory: d.Name, Routine: r.Ref.Name, Repo: repo, Repos: []string{repo},
		Connectors: st.Shape.ResourceConnectors(), Prompt: prompt, Fingerprint: fp,
		Dir:            filepath.Join(o.CacheDir, "sessions", d.Name, id),
		Title:          fmt.Sprintf("Fabrica routine %s of factory %s", r.Ref.Name, d.Name),
		FabricaCommand: []string{o.Binary, "mcp", "--runtime", rt, "--home", o.HomeDir},
	})
	return launched.Log, err
}

// SessionID names the session a routine's stub runs on one beat, like
// p_3f2a1c9e04: the same on every try, so a launcher never starts it twice.
func SessionID(factory, runtime, identity string, beat pace.Beat) string {
	sum := sha256.Sum256([]byte(factory + "\x00" + runtime + "\x00" + identity + "\x00" + beat.At.Format(time.RFC3339)))
	return "p_" + hex.EncodeToString(sum[:5])
}

// BeatText tells the session why it started.
func BeatText(d *home.Factory, r *blueprint.Routine, beat pace.Beat) string {
	return fmt.Sprintf("Fabrica started this run on the heartbeat of factory %s: the beat at %s (routine %s runs %s, every %s at the factory's pace of %s).",
		d.Name, beat.At.Format(time.RFC3339), r.Ref.Name, blueprint.DescribeEvery(r.Every()), pace.Minutes(d.Pace.Times(r.Every())), d.Pace)
}

// handled reports whether a tick already went through this beat of the
// factory, at this pace. It only saves work: claims in runs.db are what
// keep a routine from starting twice.
func (o Options) handled(factory string, p pace.Pace, beat pace.Beat) bool {
	data, err := os.ReadFile(o.markerPath(factory))
	return err == nil && strings.TrimSpace(string(data)) == marker(p, beat)
}

func (o Options) markHandled(factory string, p pace.Pace, beat pace.Beat) {
	path := o.markerPath(factory)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte(marker(p, beat)+"\n"), 0o600)
	}
}

func (o Options) markerPath(factory string) string {
	return filepath.Join(o.CacheDir, "heartbeat", factory)
}

func marker(p pace.Pace, beat pace.Beat) string {
	return p.String() + " " + strconv.FormatInt(beat.N, 10)
}

// prune removes the folders of sessions the heartbeat started that haven't
// written to their log for a day: they're done, and each holds a clone.
func (o Options) prune() {
	root := filepath.Join(o.CacheDir, "sessions")
	factories, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, fdir := range factories {
		sessions, err := os.ReadDir(filepath.Join(root, fdir.Name()))
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if !strings.HasPrefix(s.Name(), "p_") {
				continue // init sessions are apply's
			}
			dir := filepath.Join(root, fdir.Name(), s.Name())
			info, err := os.Stat(filepath.Join(dir, "session.log"))
			if errors.Is(err, fs.ErrNotExist) {
				info, err = os.Stat(dir)
			}
			if err == nil && time.Since(info.ModTime()) > sessionMaxIdle {
				if err := os.RemoveAll(dir); err != nil {
					o.logger().Warn("heartbeat: couldn't remove an old session's folder", "dir", dir, "error", err)
				}
			}
		}
	}
}

// Watch ticks at the start of every minute until ctx is done, for running
// the heartbeat in the foreground instead of from a job. Beats always
// start on a whole minute, so that's as often as anything can be due.
func Watch(ctx context.Context, o Options, now func() time.Time) error {
	for {
		if _, err := Tick(ctx, o, now()); err != nil {
			o.logger().Error("heartbeat: tick failed", "error", err)
		}
		t := now()
		wait := t.Truncate(time.Minute).Add(time.Minute).Sub(t) + time.Second
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (o Options) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
}

func sortedRoutines(f *blueprint.Blueprint) []string {
	names := make([]string, 0, len(f.Routines))
	for name := range f.Routines {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
