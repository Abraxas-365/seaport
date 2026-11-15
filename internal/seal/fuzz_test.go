package seal_test

import (
	"bytes"
	"testing"

	"github.com/Abraxas-365/seaport/internal/seal"
)

// FuzzSealOpen: opening arbitrary bytes (a tampered column, a forged
// cookie) never panics, and never yields plaintext unless the bytes are a
// value this sealer made for this very purpose.
func FuzzSealOpen(f *testing.F) {
	s, err := seal.New(key, "fuzz v1")
	if err != nil {
		f.Fatal(err)
	}
	real := s.Seal("row", []byte("a secret"))
	f.Add("row", real)
	f.Add("other", real)
	f.Add("row", real[:len(real)-1])
	f.Add("row", real[:12])
	f.Add("row", []byte{})
	f.Add("", []byte(nil))
	f.Add("row", bytes.Repeat([]byte{0}, 64))
	flipped := bytes.Clone(real)
	flipped[len(flipped)/2] ^= 0x80
	f.Add("row", flipped)
	f.Fuzz(func(t *testing.T, purpose string, sealed []byte) {
		plain, err := s.Open(purpose, sealed)
		if err != nil {
			if plain != nil {
				t.Fatalf("plaintext returned with an error: %q", plain)
			}
			_, _ = s.OpenString(purpose, string(sealed))
			return
		}
		// Authenticated: only a value sealed under this key and purpose
		// opens, and sealing the plaintext again opens too (round trip).
		if purpose != "row" || !bytes.Equal(plain, []byte("a secret")) {
			t.Fatalf("garbage opened: purpose %q -> %q", purpose, plain)
		}
		if again, err := s.Open(purpose, s.Seal(purpose, plain)); err != nil || !bytes.Equal(again, plain) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

// FuzzSealRoundTrip: anything sealed opens back, only under its purpose,
// and the sealed value never contains the plaintext it protects.
func FuzzSealRoundTrip(f *testing.F) {
	s, _ := seal.New(key, "fuzz v1")
	f.Add("row 1", []byte("hello world, a secret"))
	f.Add("", []byte{})
	f.Add("p", bytes.Repeat([]byte("x"), 1000))
	f.Fuzz(func(t *testing.T, purpose string, plain []byte) {
		sealed := s.Seal(purpose, plain)
		got, err := s.Open(purpose, sealed)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip: %q %v", got, err)
		}
		if len(plain) >= 8 && bytes.Contains(sealed, plain) {
			t.Fatal("sealed value holds the plaintext")
		}
		if _, err := s.Open(purpose+"x", sealed); err == nil {
			t.Fatal("opened under another purpose")
		}
		str := s.SealString(purpose, plain)
		if got, err := s.OpenString(purpose, str); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("string round trip: %v", err)
		}
	})
}
