// Package crypto centralises the at-rest secret encryption used by the
// v2 codebase. Provider API keys, Telegram bot tokens, and GitHub PATs
// all encrypt through this single AES-256-GCM facade so we never roll
// our own key material twice.
//
// Key derivation: HKDF-SHA256 over the hub's `auth.jwtSecret`, using a
// per-purpose context label ("ai-vault", "github-pat", "telegram-bot")
// so a leaked AI ciphertext can't be repurposed to decrypt a Telegram
// token. The JWT secret is already required to be ≥ 32 bytes of entropy
// for the JWT itself, so reusing it as our root key is cheap and
// avoids introducing a third secret operators must manage.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Vault is a purpose-scoped encrypter. One vault per "context label" so
// callers can't accidentally use the AI key to decrypt a GitHub PAT.
type Vault struct {
	gcm cipher.AEAD
}

// NewVault derives a 256-bit key from `secret` + `label` and returns an
// AES-GCM AEAD wrapped in the Vault. `secret` is the hub's JWT secret;
// `label` is a stable, ascii context (e.g. "ai-vault").
func NewVault(secret, label string) (*Vault, error) {
	if len(secret) < 16 {
		return nil, errors.New("crypto: secret too short — set a strong jwtSecret first")
	}
	rdr := hkdf.New(sha256.New, []byte(secret), nil, []byte(label))
	key := make([]byte, 32)
	if _, err := io.ReadFull(rdr, key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{gcm: gcm}, nil
}

// Encrypt returns a base64-encoded ciphertext+nonce blob safe to drop
// into JSON or a SQL TEXT column. Format: base64(nonce || ciphertext).
func (v *Vault) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, v.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := v.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt is the inverse of Encrypt. Returns an empty string + error
// on tampering (GCM tag mismatch) or wrong-label decryption attempts —
// the AEAD authenticates the entire blob so a leaked ciphertext can't
// be silently truncated or replayed.
func (v *Vault) Decrypt(blob string) (string, error) {
	if blob == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", err
	}
	ns := v.gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("crypto: ciphertext shorter than nonce")
	}
	nonce, ciphertext := raw[:ns], raw[ns:]
	pt, err := v.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
