package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/staltz/fabrica/internal/credentials"
)

// An init routine doesn't get a stub that waits for a trigger. It gets one
// session, started once, when `fabrica apply` creates (or recreates) its
// factory. A provider's launch_modes say how Fabrica can start that
// session on its runtime: by itself (api), through the runtime's API or a
// local tool's command line, or by writing instructions for a person to
// start it (handoff).

// Session is one init session to start.
type Session struct {
	// ID identifies this factory's init, like "i_3f2a1c9e04". It's the same
	// every time apply tries to start it, and a launcher must never start
	// a second session for an ID it already started one for: apply tries
	// again after a failure, and the first try may have got further than it
	// knew.
	ID string `json:"id"`
	// Identity is the init's Fabrica identity, like
	// "fabrica.acme.dev/acme-app#onboard".
	Identity string `json:"identity"`
	Factory  string `json:"factory"`
	Routine  string `json:"routine"`
	// Repo is the repo the session starts in: the factory's first target.
	Repo string `json:"repo"`
	// Repos are all of the factory's targets, Repo first.
	Repos      []string            `json:"repos"`
	Connectors []ResourceConnector `json:"connectors"`
	Fabrica    ResourceFabrica     `json:"fabrica"`
	// Model is the model to run the session on, on runtimes whose matrix
	// lists models; nil means the runtime's default.
	Model       *ResourceModel `json:"model,omitempty"`
	Prompt      string         `json:"prompt"`
	Fingerprint string         `json:"fingerprint"`
	// Dir is a folder on this machine that Fabrica keeps for this session,
	// readable only by you. Local launchers check the repo out and keep
	// their log there. Folder providers don't get it.
	Dir string `json:"-"`
}

// Launched is what a launcher reports about the session it started.
type Launched struct {
	// SessionID is the runtime's own ID for the session.
	SessionID string `json:"session_id"`
	// URL is where to watch the session, when the runtime has one.
	URL string `json:"url,omitempty"`
	// Log is the file a local session writes to.
	Log string `json:"-"`
}

// Launcher starts init sessions on a runtime.
type Launcher interface {
	// Launch starts the session and says how to find it. When a session
	// with the same Session.ID was already started, it reports that one
	// instead of starting another.
	Launch(ctx context.Context, s Session) (Launched, error)
}

// builtinLaunchers are the built-in runtimes Fabrica can start init
// sessions on by itself. Each constructor fails when it can't, like when
// a credential isn't set or a command isn't installed.
var builtinLaunchers = map[string]func() (Launcher, error){
	"cursor-cloud":      newCursorLauncher,
	"claude-code-local": newClaudeLauncher,
}

// LaunchUnavailable says why Fabrica can't start an init session on this
// runtime by itself right now. It's nil when it can.
func (p *Provider) LaunchUnavailable() error {
	if !p.Matrix.HasLaunchMode(API) {
		return errors.New("it has no API for starting sessions")
	}
	if p.Matrix.Scope == Cloud {
		for _, c := range p.Matrix.Credentials {
			found, err := credentials.Lookup(c.Names()...)
			if err != nil {
				return err
			}
			if found.Value == "" {
				return fmt.Errorf("%s isn't set", c.Env)
			}
		}
	}
	switch {
	case p.APIPath != "":
		return nil
	case p.builtinLauncher != nil:
		_, err := p.builtinLauncher()
		return err
	}
	return fmt.Errorf("runtime %s has no launcher", p.Name())
}

// Launcher returns what starts init sessions on this runtime by itself.
func (p *Provider) Launcher() (Launcher, error) {
	switch {
	case p.APIPath != "":
		env, err := p.credentialEnv()
		if err != nil {
			return nil, err
		}
		return &APIClient{Path: p.APIPath, Env: env}, nil
	case p.builtinLauncher != nil:
		return p.builtinLauncher()
	}
	return nil, fmt.Errorf("runtime %s has no launcher", p.Name())
}

// CanHandOffLaunch reports whether Fabrica can write instructions for a
// person to start an init session here.
func (p *Provider) CanHandOffLaunch() bool { return p.launchHandoff != nil }

// Launch starts an init session through a folder provider's api program.
func (c *APIClient) Launch(ctx context.Context, s Session) (Launched, error) {
	resp, err := c.call(ctx, request{Op: "launch", Factory: s.Factory, Session: &s})
	if err != nil {
		return Launched{}, err
	}
	if resp.SessionID == "" {
		return Launched{}, fmt.Errorf("%s launch didn't return a session_id", c.Path)
	}
	return Launched{SessionID: resp.SessionID, URL: resp.URL}, nil
}

// LaunchData is what a launch.md template gets to work with: everything a
// person needs to start an init session by hand.
type LaunchData struct {
	// ID is the init's ID, used with `fabrica confirm`.
	ID       string
	Runtime  string
	Factory  string
	Routine  string
	Identity string
	// Repo is the repo to start the session in, and Repos are all of the
	// factory's targets (Repo first).
	Repo        string
	Repos       []string
	Connectors  []HandoffConnector
	Model       HandoffModel
	Fabrica     HandoffFabrica
	Prompt      string
	Fingerprint string
	// ConfirmCommand is the exact `fabrica confirm …` line to run once the
	// session is started.
	ConfirmCommand string
}

// RenderLaunch fills in the provider's launch.md template.
func (p *Provider) RenderLaunch(data LaunchData) (string, error) {
	if p.launchHandoff == nil {
		return "", fmt.Errorf("provider %s has no launch.md template", p.Name())
	}
	var buf bytes.Buffer
	if err := p.launchHandoff.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("provider %s: filling in launch.md: %w", p.Name(), err)
	}
	return strings.TrimSpace(buf.String()) + "\n", nil
}

func parseLaunchHandoff(name, text string) (*template.Template, error) {
	return template.New(name + "/launch.md").Funcs(handoffFuncs).Option("missingkey=error").Parse(text)
}

// SessionUUID turns an init's ID into a UUID, for runtimes that let the
// caller pick a session's ID: the same init always gets the same UUID, so
// a retried launch finds the session the first try started.
func SessionUUID(id string) string {
	sum := sha256.Sum256([]byte("fabrica-init\x00" + id))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50 // version 5 (name-based, SHA)
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
