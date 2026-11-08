package auth

import (
	"crypto/sha256"

	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/seal"
)

// Sealer encrypts and authenticates small secrets: session tokens at rest,
// the pending sign-in cookie. purpose is bound as associated data, so a
// value sealed for one use opens for no other.
type Sealer struct{ s seal.Sealer }

// NewSealer derives the key from secret (SEAPORT_SECRET_KEY). The
// derivation predates internal/seal's HKDF and is kept as is, so existing
// browser sessions and sign-in cookies keep opening; it differs from every
// HKDF-derived key, so the WhatsApp store never shares this key.
func NewSealer(secret string) (Sealer, error) {
	if err := config.CheckSecretKey(secret); err != nil {
		return Sealer{}, err
	}
	key := sha256.Sum256([]byte("seaport-seal:" + secret))
	s, err := seal.WithKey(key[:])
	return Sealer{s: s}, err
}

// Seal returns base64url(nonce || ciphertext).
func (s Sealer) Seal(purpose string, plain []byte) string { return s.s.SealString(purpose, plain) }

// Open reverses Seal; tampered, foreign or other-purpose values fail.
func (s Sealer) Open(purpose, sealed string) ([]byte, error) { return s.s.OpenString(purpose, sealed) }
