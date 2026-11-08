// Package seal encrypts and authenticates secrets at rest (AES-256-GCM with
// a key derived from SEAPORT_SECRET_KEY by HKDF-SHA256). Each use derives
// its own key (info), and every value binds a purpose as associated data,
// so a value sealed for one use or row opens for no other.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// Sealer seals values under one derived key.
type Sealer struct{ aead cipher.AEAD }

// New derives the key named info (a new info re-keys) from secret.
func New(secret, info string) (Sealer, error) {
	if err := config.CheckSecretKey(secret); err != nil {
		return Sealer{}, err
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, info, 32)
	if err != nil {
		return Sealer{}, errx.Wrap(err, "hkdf", errx.TypeInternal)
	}
	return WithKey(key)
}

// WithKey seals with a 32-byte key derived by the caller (a format that
// predates New must keep opening what it sealed).
func WithKey(key []byte) (Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return Sealer{}, errx.Wrap(err, "aes", errx.TypeInternal)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Sealer{}, errx.Wrap(err, "gcm", errx.TypeInternal)
	}
	return Sealer{aead: aead}, nil
}

// Seal returns nonce || ciphertext.
func (s Sealer) Seal(purpose string, plain []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, plain, []byte(purpose))
}

// Open reverses Seal; tampered, foreign-key or other-purpose values fail.
func (s Sealer) Open(purpose string, sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errx.Unauthorized("invalid sealed value")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(purpose))
	if err != nil {
		return nil, errx.Unauthorized("invalid sealed value")
	}
	return plain, nil
}

// SealString is Seal as base64url (cookies, text columns).
func (s Sealer) SealString(purpose string, plain []byte) string {
	return base64.RawURLEncoding.EncodeToString(s.Seal(purpose, plain))
}

// OpenString reverses SealString.
func (s Sealer) OpenString(purpose, sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return nil, errx.Unauthorized("invalid sealed value")
	}
	return s.Open(purpose, raw)
}
