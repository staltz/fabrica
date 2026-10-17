package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"slices"
	"sync"
)

// maxCodes is how many unused codes a local dashboard keeps. Asking for one
// more drops the oldest, so a program that keeps asking can't make the list
// grow.
const maxCodes = 16

// Codes is how a local dashboard lets people in: it hands out one-time
// codes, and each one trades itself, once, for the dashboard's cookie.
//
// The cookie belongs to the dashboard and not to a code, so every browser
// that used any of its codes stays in, and lasts as long as the dashboard
// does. Its name is specific to this dashboard (cookies ignore ports, so
// two dashboards on 127.0.0.1 would otherwise log each other out) and its
// value is random and isn't a code.
//
// It's safe to use from several goroutines.
type Codes struct {
	cookieName, session string

	mu     sync.Mutex
	unused []string // oldest first, at most maxCodes
}

// NewCodes makes the codes of one local dashboard, with no code issued yet.
func NewCodes() (*Codes, error) {
	id, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	session, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	return &Codes{cookieName: "fabrica_dashboard_" + id, session: session}, nil
}

// New issues a one-time code: 128 bits of randomness. Every code it has
// issued works once, until maxCodes newer ones push it out.
func (c *Codes) New() (string, error) {
	code, err := randomHex(16)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unused = append(c.unused, code)
	if extra := len(c.unused) - maxCodes; extra > 0 {
		c.unused = slices.Delete(c.unused, 0, extra)
	}
	return code, nil
}

// use says whether the code is one that was issued and hasn't been used
// yet, and if so, uses it up.
func (c *Codes) use(code string) bool {
	if c == nil || code == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Look at every code, so how long this takes doesn't say which matched.
	found := -1
	for i, have := range c.unused {
		if subtle.ConstantTimeCompare([]byte(code), []byte(have)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return false
	}
	c.unused = slices.Delete(c.unused, found, found+1)
	return true
}

// inside says whether a cookie value is this dashboard's.
func (c *Codes) inside(value string) bool {
	return c != nil && subtle.ConstantTimeCompare([]byte(value), []byte(c.session)) == 1
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
