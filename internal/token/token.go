// Package token makes and checks the secret tokens that let cloud runtimes
// talk to a Fabrica server.
//
// Each factory gets its own token, computed as HMAC(server secret,
// factory name). The server can check a token without storing any, and
// `fabrica apply` (holding the same secret, for example as a CI secret)
// computes the same token to put into each runtime's connector. To rotate
// every token at once, change the server secret and re-apply.
//
// A server also has an admin token, computed the same way from the name
// "_admin", which no factory can have. It unlocks the read side of run
// history (the admin API and the dashboard), and nothing else.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvVar is the environment variable holding the server secret, both for
// `fabrica mcp --http` and for `fabrica apply`.
const EnvVar = "FABRICA_SERVER_SECRET"

// AdminEnvVar is the environment variable holding an admin token, for
// people who have it but not the server secret.
const AdminEnvVar = "FABRICA_ADMIN_TOKEN"

// prefix makes Fabrica tokens easy to recognize, for people and for secret
// scanners alike.
const prefix = "fab_"

// adminPrefix and adminName make the admin token. Factory names can't
// contain an underscore, so no factory is called _admin, and the prefix
// keeps the admin token apart from every factory token anyway.
const (
	adminPrefix = "fab_admin_"
	adminName   = "_admin"
)

// minSecretLen is the shortest server secret we accept. Anything shorter
// is too easy to guess.
const minSecretLen = 32

// SecretFromEnv reads the server secret from FABRICA_SERVER_SECRET.
func SecretFromEnv() (string, error) {
	s := strings.TrimSpace(os.Getenv(EnvVar))
	if s == "" {
		return "", errors.New(EnvVar + " isn't set; set it to the same long random secret your Fabrica server uses (for example from `openssl rand -hex 32`)")
	}
	if err := Check(s); err != nil {
		return "", fmt.Errorf("%s %w", EnvVar, err)
	}
	return s, nil
}

// Check says whether s is long enough to be a server secret.
func Check(s string) error {
	if len(s) < minSecretLen {
		return errors.New("is too short; use at least 32 characters of random text (for example from `openssl rand -hex 32`)")
	}
	return nil
}

// NewSecret makes a new server secret: 32 random bytes, as 64 hex
// characters, just like `openssl rand -hex 32`.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// For computes the token of one factory.
func For(secret, factory string) string {
	return prefix + mac(secret, factory)
}

// mac is HMAC-SHA256 of a name under the secret, as base64url.
func mac(secret, name string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(name))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Valid reports whether tok is the right token for the factory. It
// compares in constant time, so timing can't leak the token.
func Valid(secret, factory, tok string) bool {
	want := For(secret, factory)
	return hmac.Equal([]byte(want), []byte(tok))
}

// Admin is the admin token of a server: it unlocks the admin API and the
// dashboard, and nothing else.
func Admin(secret string) string {
	return adminPrefix + mac(secret, adminName)
}

// ValidAdmin checks a token against the admin token, in constant time.
func ValidAdmin(secret, tok string) bool {
	return hmac.Equal([]byte(Admin(secret)), []byte(tok))
}

// Hash is a short, one-way fingerprint of a token. State files record it,
// so `fabrica plan` can tell when the secret was rotated without the token
// itself ever being stored.
func Hash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}
