package auth

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

// sealInfo names the derived key (HKDF info): a new version re-keys.
const sealInfo = "seaport seal v1"

// Sealer encrypts and authenticates small secrets (AES-256-GCM): session
// tokens at rest, the pending sign-in cookie. purpose is bound as
// associated data, so a value sealed for one use opens for no other.
type Sealer struct{ aead cipher.AEAD }

// NewSealer derives the key from secret (SEAPORT_SECRET_KEY) with HKDF-SHA256.
func NewSealer(secret string) (Sealer, error) {
	if err := config.CheckSecretKey(secret); err != nil {
		return Sealer{}, err
	}
	key, err := hkdf.Key(sha256.New, []byte(secret), nil, sealInfo, 32)
	if err != nil {
		return Sealer{}, errx.Wrap(err, "hkdf", errx.TypeInternal)
	}
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
