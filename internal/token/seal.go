package token

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// sealPrefix marks a sealed value, and its format's version.
const sealPrefix = "sealed:v1:"

// Seal encrypts a secret a runtime gave a factory's stub, like the token of
// the webhook that fires it, so it can be kept in the factory's state file
// in the home repo. Only the server secret opens it: the Fabrica server,
// which calls the webhook, and whoever runs `fabrica apply` with it.
//
// The key is HMAC(server secret, "seal:" + factory), and the stub's
// identity is authenticated along with it, so a sealed value only opens for
// the factory and stub it was sealed for. Rotating the server secret makes
// every sealed value unreadable, like it makes every token change.
func Seal(secret, factory, identity, plaintext string) (string, error) {
	gcm, err := sealer(secret, factory)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(identity))
	return sealPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts what Seal sealed, for the same factory and stub.
func Open(secret, factory, identity, sealed string) (string, error) {
	data, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return "", errors.New("it isn't a value Fabrica sealed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		return "", errors.New("it isn't a value Fabrica sealed")
	}
	gcm, err := sealer(secret, factory)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("it isn't a value Fabrica sealed")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(identity))
	if err != nil {
		return "", errors.New("it was sealed with another server secret, or for another stub")
	}
	return string(plain), nil
}

func sealer(secret, factory string) (cipher.AEAD, error) {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("seal:" + factory))
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
