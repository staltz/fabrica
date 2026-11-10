package apply

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/token"
)

// Webhook is what `fabrica webhook` records: the webhook a runtime made
// for one of a factory's stubs.
type Webhook struct {
	Factory string
	Runtime string
	// Identity is the stub's identity, like 4ckr#tidy@github.com/acme/app.
	Identity string
	URL      string
	// Token is what the runtime checks on each call, in the clear. It's
	// sealed before it's written.
	Token string
	// Secret is the factory's server secret, which seals the token.
	Secret string
}

// SetWebhook records the webhook a runtime made for a stub that fires on
// one (a routine with a pace trigger), so the Fabrica server can call it
// on the factory's heartbeat. No runtime's API hands a webhook's URL and
// token over, so a person copies them from the runtime.
//
// The token is sealed with the server secret before it goes into the
// factory's state file. Like confirm, it commits and pushes the state file,
// and refuses while an apply holds the factory's lock (that apply would
// write over it when it's done).
func SetWebhook(ctx context.Context, homeDir string, w Webhook, now time.Time) (*home.Resource, error) {
	if err := checkWebhookURL(w.URL); err != nil {
		return nil, err
	}
	w.Token = strings.TrimSpace(w.Token)
	if w.Token == "" {
		return nil, errors.New("the webhook's token is empty; paste the token (or API key) the runtime shows for the webhook")
	}
	if w.Secret == "" {
		return nil, fmt.Errorf("Fabrica needs the server secret of factory %s to seal the webhook's token; set %s, or save it with `fabrica auth login <the factory's server>`", w.Factory, token.EnvVar)
	}
	sealed, err := token.Seal(w.Secret, w.Factory, w.Identity, w.Token)
	if err != nil {
		return nil, err
	}
	hook := &home.Webhook{URL: w.URL, Token: sealed, TokenHash: token.Hash(token.For(w.Secret, w.Factory)), SetAt: now.UTC().Truncate(time.Second)}

	r, err := OpenRepo(ctx, homeDir)
	if err != nil {
		return nil, err
	}
	var recorded home.Resource
	key := home.Key{Runtime: w.Runtime, ID: w.Identity}
	err = r.Update(ctx, w.Factory, "fabrica: webhook for "+w.Identity, func(s *home.State) error {
		if s.Lock != nil {
			return &ErrLocked{Factory: w.Factory, Lock: *s.Lock}
		}
		i, ok := s.FindResource(key)
		if !ok {
			if p, pending := pendingFor(s, key); pending {
				return fmt.Errorf("stub %s on %s isn't installed yet: handoff %s is still pending; follow it and run `fabrica confirm %s` first", w.Identity, w.Runtime, p.ID, p.ID)
			}
			return fmt.Errorf("factory %s has no stub %s on %s; `fabrica plan` lists the stubs that need a webhook, with the command for each", w.Factory, w.Identity, w.Runtime)
		}
		if !s.Resources[i].HasWebhookTrigger() {
			return fmt.Errorf("stub %s on %s doesn't fire on a webhook; only the stubs of routines with a pace trigger do", w.Identity, w.Runtime)
		}
		s.Resources[i].Webhook = hook
		recorded = s.Resources[i]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &recorded, nil
}

// pendingFor finds a pending handoff for a resource.
func pendingFor(s *home.State, key home.Key) (home.Pending, bool) {
	for _, p := range s.Pending {
		if p.Key() == key {
			return p, true
		}
	}
	return home.Pending{}, false
}

// checkWebhookURL makes sure a webhook URL is one Fabrica can POST to.
func checkWebhookURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case raw == "" || strings.Contains(raw, "<"):
		return errors.New("--url is missing; pass the webhook's URL, as the runtime shows it")
	case err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http":
		return fmt.Errorf("%q isn't a webhook URL; pass the full URL the runtime shows, starting with https://", raw)
	case u.User != nil:
		return errors.New("the webhook URL has a user name or password in it; pass the URL without it, and the token on its own when asked")
	}
	return nil
}
