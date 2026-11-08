package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// MinSecretKey is the shortest SEAPORT_SECRET_KEY accepted.
const MinSecretKey = 32

// Sealer encrypts and authenticates small secrets (AES-256-GCM): session
// tokens at rest, the pending sign-in cookie. purpose is bound as
// associated data, so a value sealed for one use opens for no other.
type Sealer struct{ aead cipher.AEAD }

// NewSealer derives the key from secret (SEAPORT_SECRET_KEY).
func NewSealer(secret string) (Sealer, error) {
	if len(secret) < MinSecretKey {
		return Sealer{}, errx.Validation("SEAPORT_SECRET_KEY must be at least 32 characters")
	}
	key := sha256.Sum256([]byte("seaport-seal:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return Sealer{}, errx.Wrap(err, "aes", errx.TypeInternal)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Sealer{}, errx.Wrap(err, "gcm", errx.TypeInternal)
	}
	return Sealer{aead: aead}, nil
}

// Seal returns base64url(nonce || ciphertext).
func (s Sealer) Seal(purpose string, plain []byte) string {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(purpose)))
}

// Open reverses Seal; tampered, foreign or other-purpose values fail.
func (s Sealer) Open(purpose, sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	n := s.aead.NonceSize()
	if err != nil || len(raw) < n {
		return nil, errx.Unauthorized("invalid sealed value")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(purpose))
	if err != nil {
		return nil, errx.Unauthorized("invalid sealed value")
	}
	return plain, nil
}
