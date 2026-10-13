// Package token makes and checks the secret tokens that let cloud runtimes
// talk to a Fabrica server.
//
// Each factory gets its own token, computed as HMAC(server secret,
// factory name). The server can check a token without storing any, and
// `fabrica apply` (holding the same secret, for example as a CI secret)
// computes the same token to put into each runtime's connector. To rotate
// every token at once, change the server secret and re-apply.
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

// prefix makes Fabrica tokens easy to recognize, for people and for secret
// scanners alike.
const prefix = "fab_"

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
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(factory))
	return prefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Valid reports whether tok is the right token for the factory. It
// compares in constant time, so timing can't leak the token.
func Valid(secret, factory, tok string) bool {
	want := For(secret, factory)
	return hmac.Equal([]byte(want), []byte(tok))
}

// Hash is a short, one-way fingerprint of a token. State files record it,
// so `fabrica plan` can tell when the secret was rotated without the token
// itself ever being stored.
func Hash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])[:12]
}
