// Package credentials keeps the secrets Fabrica needs to deploy factories,
// and finds them again: the server secret of each Fabrica server, and the
// API keys of runtimes like Cursor's.
//
// Every credential has an environment variable, and the environment always
// wins: when a credential's variable is set (even to nothing), Fabrica uses
// it and looks nowhere else. That keeps CI, and anyone who already exports
// their keys, working exactly as before, and `CURSOR_API_KEY= fabrica plan`
// shows what happens without a saved key. Otherwise Fabrica looks in its
// store, where `fabrica auth login` saves credentials:
//
//   - your system's keychain, where Fabrica can use one: the macOS Keychain
//     (through /usr/bin/security), or on Linux the Secret Service (GNOME
//     Keyring or KWallet, through secret-tool);
//   - otherwise a file that only you can read, credentials.json in
//     Fabrica's config folder (~/.config/fabrica by default). Fabrica
//     refuses to read it if anyone else can.
//
// Like git, the keychain tools are run as commands rather than linked in,
// so Fabrica stays one static binary. Set FABRICA_KEYCHAIN=off to keep
// credentials in the file instead.
package credentials

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/staltz/fabrica/internal/token"
)

// KeychainEnv turns the system keychain off when set to "off", so
// credentials live in the file instead.
const KeychainEnv = "FABRICA_KEYCHAIN"

// service is what Fabrica's entries are filed under in a keychain.
const service = "fabrica"

// maxLen is the longest credential Fabrica accepts. API keys and secrets
// are far shorter, and keychain tools have line limits.
const maxLen = 1024

// Found is a credential and where it came from.
type Found struct {
	Value string
	// From says where it came from, for people: "the environment variable
	// CURSOR_API_KEY", or "your macOS Keychain". Empty when nothing was
	// found.
	From string
	// Env is true when the environment decided, even if it decided there's
	// no credential (the variable is set to nothing).
	Env bool
}

// Lookup finds a credential by its environment variables, in order. When
// any of them is set, even to nothing, the environment decides and the
// store isn't asked: the first one that's set and not blank wins.
// Otherwise it's the credential saved under the first name.
func Lookup(names ...string) (Found, error) {
	if len(names) == 0 {
		return Found{}, errors.New("credentials.Lookup needs a name")
	}
	for _, name := range names {
		if _, ok := os.LookupEnv(name); ok {
			return fromEnv(names), nil
		}
	}
	value, where, err := Get(names[0])
	if err != nil {
		return Found{}, err
	}
	return Found{Value: value, From: where}, nil
}

func fromEnv(names []string) Found {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return Found{Value: v, From: "the environment variable " + name, Env: true}
		}
	}
	return Found{Env: true, From: "the environment (" + names[0] + " is set to nothing)"}
}

// ServerKey is the key the secret of the Fabrica server at server is saved
// under, like FABRICA_SERVER_SECRET@https://fabrica.acme.dev. Each server
// has its own, so setting up one server never changes another's tokens.
func ServerKey(server string) string {
	return token.EnvVar + "@" + NormalizeServer(server)
}

// NormalizeServer is the canonical way of writing a server: URL, so
// https://Fabrica.acme.dev/ and https://fabrica.acme.dev are the same
// server.
func NormalizeServer(server string) string {
	s := strings.TrimRight(strings.TrimSpace(server), "/")
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/")
}

// ServerSecret finds the secret of the Fabrica server at server:
// FABRICA_SERVER_SECRET when it's set, which counts for every server,
// otherwise the secret saved for that server.
func ServerSecret(server string) (Found, error) {
	if _, ok := os.LookupEnv(token.EnvVar); ok {
		f := fromEnv([]string{token.EnvVar})
		if f.Value != "" {
			if err := token.Check(f.Value); err != nil {
				return Found{}, fmt.Errorf("%s %w", token.EnvVar, err)
			}
		}
		return f, nil
	}
	value, where, err := Get(ServerKey(server))
	if err != nil || value == "" {
		return Found{}, err
	}
	if err := token.Check(value); err != nil {
		return Found{}, fmt.Errorf("the server secret for %s saved in %s %w; run `fabrica auth login %s` to replace it", NormalizeServer(server), where, err, NormalizeServer(server))
	}
	return Found{Value: value, From: where}, nil
}

// Check says whether value can be a credential: one line of printable
// text, without spaces, quotes or backslashes, at most 1024 characters.
// Every API key and secret looks like that, and it keeps values safe to
// hand to keychain tools.
func Check(value string) error {
	switch {
	case value == "":
		return errors.New("it's empty")
	case len(value) > maxLen:
		return fmt.Errorf("it's %d characters long, and no API key or secret is longer than %d; check that you copied the right thing", len(value), maxLen)
	}
	for _, c := range value {
		switch {
		case c <= ' ' || c > '~':
			return errors.New("it has spaces, line breaks or other characters no API key or secret has; check that you copied the right thing")
		case c == '\'' || c == '"' || c == '\\':
			return errors.New("it has a quote or a backslash, which no API key or secret has; check that you copied the right thing")
		}
	}
	return nil
}

// Get returns the credential saved under key, and where it's saved. It
// returns "" and no error when there's none.
func Get(key string) (value, where string, err error) {
	for _, b := range backends() {
		v, ok, err := cachedGet(b, key)
		if err != nil {
			if b.id() == "keychain" {
				return "", "", fmt.Errorf("couldn't read %s from %s: %w (to skip the keychain, set %s=off)", key, b.Name(), err, KeychainEnv)
			}
			return "", "", fmt.Errorf("couldn't read %s from %s: %w", key, b.Name(), err)
		}
		if ok {
			return v, b.Name(), nil
		}
	}
	return "", "", nil
}

// Set saves a credential under key, in the best place this machine has,
// and returns where that is. Saving to a keychain also removes any copy
// left in the file.
func Set(key, value string) (where string, err error) {
	if err := Check(key); err != nil {
		return "", fmt.Errorf("can't save a credential called %q: %w", key, err)
	}
	if err := Check(value); err != nil {
		return "", fmt.Errorf("can't save %s: %w", key, err)
	}
	bs := backends()
	if len(bs) == 0 {
		return "", errors.New("there's nowhere to save credentials on this machine: no keychain Fabrica can use, and no home folder for its config")
	}
	b := bs[0]
	forget(key)
	if err := b.Set(key, value); err != nil {
		return "", fmt.Errorf("couldn't save %s in %s: %w", key, b.Name(), err)
	}
	// Read it back: keychain tools don't always say when a write failed.
	got, ok, err := b.Get(key)
	if err != nil || !ok || got != value {
		if err == nil {
			err = errors.New("it isn't there when read back")
		}
		return "", fmt.Errorf("couldn't save %s in %s: %w", key, b.Name(), err)
	}
	for _, other := range bs[1:] {
		if _, err := other.Delete(key); err != nil {
			return b.Name(), fmt.Errorf("saved %s in %s, but couldn't remove the old copy from %s: %w", key, b.Name(), other.Name(), err)
		}
	}
	return b.Name(), nil
}

// Delete forgets the credential saved under key, wherever it is, and says
// where it was.
func Delete(key string) (where []string, err error) {
	forget(key)
	for _, b := range backends() {
		removed, err := b.Delete(key)
		if err != nil {
			return where, fmt.Errorf("couldn't remove %s from %s: %w", key, b.Name(), err)
		}
		if removed {
			where = append(where, b.Name())
		}
	}
	return where, nil
}

// Where says where Set saves credentials on this machine.
func Where() string {
	bs := backends()
	if len(bs) == 0 {
		return "nowhere (no keychain, and no home folder)"
	}
	return bs[0].Name()
}

// InKeychain reports whether Set saves credentials in a system keychain,
// rather than in the file.
func InKeychain() bool {
	bs := backends()
	return len(bs) > 0 && bs[0].id() == "keychain"
}

// backend is one place credentials can live.
type backend interface {
	// Name says where, for people, like "your macOS Keychain".
	Name() string
	// id tells backends apart in the cache.
	id() string
	// Get returns the credential saved under key, and false when there's
	// none.
	Get(key string) (string, bool, error)
	Set(key, value string) error
	// Delete removes the credential saved under key, and reports whether
	// there was one.
	Delete(key string) (bool, error)
}

// backends are the places to look, best first. Set saves in the first.
// They're worked out on every call, from the environment as it is then.
func backends() []backend {
	var out []backend
	if !strings.EqualFold(strings.TrimSpace(os.Getenv(KeychainEnv)), "off") {
		if k := systemKeychain(); k != nil {
			out = append(out, k)
		}
	}
	if f, err := defaultFile(); err == nil {
		out = append(out, f)
	}
	return out
}

// The cache saves asking a keychain tool the same question over and over
// in one command: planning asks for the same key many times.
var (
	cacheMu sync.Mutex
	cache   = map[string]cachedValue{}
)

type cachedValue struct {
	value string
	ok    bool
}

func cachedGet(b backend, key string) (string, bool, error) {
	k := b.id() + "\x00" + key
	cacheMu.Lock()
	c, hit := cache[k]
	cacheMu.Unlock()
	if hit {
		return c.value, c.ok, nil
	}
	v, ok, err := b.Get(key)
	if err != nil {
		return "", false, err
	}
	cacheMu.Lock()
	cache[k] = cachedValue{v, ok}
	cacheMu.Unlock()
	return v, ok, nil
}

// forget drops a key from the cache, in every backend.
func forget(key string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for k := range cache {
		if strings.HasSuffix(k, "\x00"+key) {
			delete(cache, k)
		}
	}
}
