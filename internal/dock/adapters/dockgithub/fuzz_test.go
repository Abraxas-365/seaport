package dockgithub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// FuzzGitHubSignature: with a fixed webhook secret, only the exact
// "sha256=<hex HMAC of the body>" passes; the right one always does.
func FuzzGitHubSignature(f *testing.F) {
	secret := []byte("fuzz-webhook-secret")
	sign := func(body []byte) string {
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	body := []byte(`{"action":"created"}`)
	f.Add(body, sign(body))
	f.Add(body, strings.ToUpper(sign(body)))
	f.Add(body, "sha256=")
	f.Add(body, "sha1="+strings.Repeat("0", 40))
	f.Add([]byte{}, sign(nil))
	f.Add(body, " "+sign(body))
	f.Add(body, "sha256="+strings.Repeat("0", 64))
	f.Fuzz(func(t *testing.T, body []byte, header string) {
		want := sign(body)
		if !validSignature(secret, body, want) {
			t.Fatal("the right signature was refused")
		}
		if validSignature(secret, body, header) && !strings.EqualFold(header, want) {
			t.Fatalf("accepted %q, the signature is %q", header, want)
		}
		if validSignature(nil, body, header) && !strings.HasPrefix(header, "sha256=") {
			t.Fatalf("accepted %q with no secret", header)
		}
	})
}
