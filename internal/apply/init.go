package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/stub"
)

// startInit starts the factory's init session, when apply creates (or
// recreates) the factory, after its stubs: through the chosen runtime's launcher, or
// with a handoff. It records the outcome in state.Init, which the final
// commit of the apply saves. A failed attempt is recorded too, and the
// next apply tries again with the same init ID, which keeps a launcher
// from starting the session twice. It returns the handoff file, if it
// wrote one.
func startInit(ctx context.Context, opts Options, repo *Repo, env *plan.Env, dp *plan.Factory, state *home.State, tok string, now time.Time, failedChanges int) (string, error) {
	in, c := dp.Init, dp.Init.Chosen
	if in.ID == "" {
		in.ID = plan.NewInitID()
	}
	rec := &home.Init{
		ID: in.ID, Routine: in.Routine.Ref.Name, Runtime: c.Runtime, Repo: in.Repo,
		Mode: string(c.Mode), BlueprintCommit: blueprintCommit(dp), At: now,
	}
	state.Init = rec
	fail := func(err error) (string, error) {
		rec.Status, rec.Error = home.InitFailed, err.Error()
		return "", err
	}
	if failedChanges > 0 {
		return fail(fmt.Errorf("didn't start it, because %s of this apply failed; the next apply tries again", plural(failedChanges, "other change")))
	}
	session := sessionFor(dp.Name, in, c, tok, filepath.Join(opts.SessionDir, dp.Name, in.ID))
	if c.Mode == provider.Handoff {
		file, err := writeLaunchHandoff(opts, dp.Name, in, c, session)
		if err != nil {
			return fail(err)
		}
		rec.Status = home.InitWaiting
		return file, nil
	}

	// Say it's starting before it does, so an apply that stops halfway
	// leaves a trace for the next one.
	rec.Status = home.InitStarting
	msg := fmt.Sprintf("fabrica: %s %s (starting init %s on %s)", verb(env), dp.Name, rec.Routine, rec.Runtime)
	if err := repo.Checkpoint(ctx, dp.Name, msg, state); err != nil {
		return fail(fmt.Errorf("didn't start it, because recording that it was about to start failed: %w", err))
	}
	wait := opts.WaitForServer
	if wait == nil {
		wait = waitForServer
	}
	if err := wait(ctx, session.Fabrica); err != nil {
		return fail(err)
	}
	launcher, err := c.Provider.Launcher()
	if err != nil {
		return fail(err)
	}
	launched, err := launcher.Launch(ctx, session)
	if err != nil {
		return fail(err)
	}
	rec.Status, rec.Session, rec.URL = home.InitStarted, launched.SessionID, launched.URL
	if launched.Log != "" {
		fmt.Fprintf(outOf(opts), "    The session runs in the background on this machine; it writes to %s\n", plan.Tildify(launched.Log))
	}
	return "", nil
}

// sessionFor describes the init session to a launcher.
func sessionFor(factory string, in *plan.Init, c *plan.InitCandidate, tok, dir string) provider.Session {
	shape := c.Eval.Shape
	return provider.Session{
		ID: in.ID, Identity: in.Identity, Factory: factory, Routine: in.Routine.Ref.Name,
		Repo: in.Repo, Repos: in.Repos, Connectors: connectorsFor(shape), Model: modelFor(shape),
		Fabrica: provider.ResourceFabrica{Placement: c.Provider.Matrix.TokenPlacement(), Token: tok, URL: fabricaURL(in.Endpoint, c.Provider, tok)},
		Prompt:  in.Prompt, Fingerprint: in.Fingerprint, Dir: dir,
	}
}

// writeLaunchHandoff writes how a person starts the init session. Like
// any handoff, it holds the factory's token, so only you can read it.
func writeLaunchHandoff(opts Options, factory string, in *plan.Init, c *plan.InitCandidate, s provider.Session) (string, error) {
	data := provider.LaunchData{
		ID: in.ID, Runtime: c.Runtime, Factory: factory, Routine: s.Routine, Identity: in.Identity,
		Repo: in.Repo, Repos: in.Repos, Prompt: s.Prompt, Fingerprint: s.Fingerprint,
		Model:          provider.HandoffModel{Name: c.Eval.Shape.Model, Effort: c.Eval.Shape.Effort, Setup: stub.DescribeModel(c.Eval.Shape)},
		Fabrica:        provider.HandoffFabrica{URL: s.Fabrica.URL},
		ConfirmCommand: fmt.Sprintf("fabrica confirm %s --remote-id <the session's ID or URL>", in.ID),
	}
	if s.Fabrica.Placement == provider.TokenInHeader {
		data.Fabrica.Header = "Bearer " + s.Fabrica.Token
	}
	for _, conn := range c.Eval.Shape.Connectors {
		data.Connectors = append(data.Connectors, provider.HandoffConnector{Name: conn.Name, Setup: stub.DescribeConnector(conn)})
	}
	text, err := c.Provider.RenderLaunch(data)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(opts.HandoffDir, factory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file := filepath.Join(dir, in.ID+".md")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return "", err
	}
	return file, nil
}

// How long apply waits for the Fabrica server to serve a new factory
// before starting its init session: the server reads the home repo about
// once a minute, and a session that starts before then would be turned
// away.
var (
	serverWait     = 3 * time.Minute
	serverWaitPoll = 5 * time.Second
)

// waitForServer waits until the factory's Fabrica server serves it and
// accepts its token.
func waitForServer(ctx context.Context, f provider.ResourceFabrica) error {
	ctx, cancel := context.WithTimeout(ctx, serverWait)
	defer cancel()
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var last string
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
		if err != nil {
			return err
		}
		if f.Placement == provider.TokenInHeader {
			req.Header.Set("Authorization", "Bearer "+f.Token)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("User-Agent", "fabrica")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusNotFound:
				last = "it doesn't serve the factory yet (it reads the home repo about once a minute)"
			case http.StatusUnauthorized:
				return errors.New("didn't start it: the Fabrica server refuses the factory's token, so it was made with another secret than the server's; run `fabrica auth` to check")
			default:
				return nil
			}
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("didn't start it: the Fabrica server isn't ready for the factory after %s (%s); the next apply tries again", serverWait, last)
			}
			return ctx.Err()
		case <-time.After(serverWaitPoll):
		}
	}
}

// describeInit is a line about the init, for apply's progress.
func describeInit(in *plan.Init) string {
	return fmt.Sprintf("init %s @ %s on %s (%s)", in.Routine.Ref.Name, in.Repo, in.Chosen.Runtime, in.Chosen.Mode)
}

func outOf(opts Options) io.Writer {
	if opts.Out == nil {
		return io.Discard
	}
	return opts.Out
}

// confirmInit closes an init's handoff: someone started the session.
func confirmInit(s *home.State, id, remoteID string, absent bool, now time.Time) error {
	st := s.Init
	switch {
	case absent:
		return fmt.Errorf("--absent is for an API create whose result was never recorded; %s is an init handoff, so start the session and confirm it", id)
	case st.Status == home.InitStarted:
		return fmt.Errorf("init %s was already started", id)
	case st.Status != home.InitWaiting:
		return fmt.Errorf("init %s isn't waiting for a handoff (it's %s); `fabrica apply` starts it", id, st.Status)
	}
	st.Status, st.At = home.InitStarted, now.UTC().Truncate(time.Second)
	if strings.HasPrefix(remoteID, "https://") || strings.HasPrefix(remoteID, "http://") {
		st.URL = remoteID
	} else {
		st.Session = remoteID
	}
	return nil
}
