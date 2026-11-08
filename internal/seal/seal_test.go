package seal_test

import (
	"bytes"
	"testing"

	"github.com/Abraxas-365/seaport/internal/seal"
)

const key = "Sk3Vt9xLm2Pz7Rw4Ys8Bn1Hc6Jd0Fg5Aq"

func TestSealer(t *testing.T) {
	s, err := seal.New(key, "test v1")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("a secret")
	sealed := s.Seal("row 1", plain)
	if bytes.Contains(sealed, plain) {
		t.Fatal("sealed value holds the plaintext")
	}
	if got, err := s.Open("row 1", sealed); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %q %v", got, err)
	}
	if bytes.Equal(s.Seal("row 1", plain), sealed) {
		t.Fatal("sealing twice gave the same bytes (nonce reuse)")
	}
	if _, err := s.Open("row 2", sealed); err == nil {
		t.Fatal("opened under another purpose")
	}
	other, _ := seal.New(key, "test v2")
	if _, err := other.Open("row 1", sealed); err == nil {
		t.Fatal("opened with another info (derived key)")
	}
	otherKey, _ := seal.New("Zq9Wx8Ec7Rv6Tb5Yn4Um3Ik2Ol1Pa0Sd", "test v1")
	if _, err := otherKey.Open("row 1", sealed); err == nil {
		t.Fatal("opened with another secret")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.Open("row 1", tampered); err == nil {
		t.Fatal("opened a tampered value")
	}
	if _, err := s.Open("row 1", sealed[:5]); err == nil {
		t.Fatal("opened a truncated value")
	}
	str := s.SealString("cookie", plain)
	if got, err := s.OpenString("cookie", str); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open string: %q %v", got, err)
	}
	if _, err := s.OpenString("cookie", "!!"); err == nil {
		t.Fatal("opened invalid base64")
	}
	for _, bad := range []string{"short", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := seal.New(bad, "x"); err == nil {
			t.Fatalf("accepted secret %q", bad)
		}
	}
}
