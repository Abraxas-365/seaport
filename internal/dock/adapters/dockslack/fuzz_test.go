package dockslack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FuzzSlackSignature: with a fixed signing secret, only the exact
// "v0=<hex HMAC of v0:ts:body>" inside the time window passes; any other
// body, timestamp or header (including mutations of a good one) never does.
func FuzzSlackSignature(f *testing.F) {
	now := time.Unix(1_700_000_000, 0)
	secret := []byte("fuzz-signing-secret")
	sign := func(ts string, body []byte) string {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte("v0:" + ts + ":"))
		mac.Write(body)
		return "v0=" + hex.EncodeToString(mac.Sum(nil))
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"type":"event_callback"}`)
	f.Add(body, ts, sign(ts, body))
	f.Add(body, ts, strings.ToUpper(sign(ts, body)))
	f.Add(body, "1699999000", sign("1699999000", body))
	f.Add(body, "1600000000", sign("1600000000", body))
	f.Add(body, "", "")
	f.Add([]byte(""), "+1700000000", "v0=")
	f.Add(body, ts, "v0="+strings.Repeat("0", 64))
	f.Fuzz(func(t *testing.T, body []byte, timestamp, header string) {
		got := validSignature(secret, body, timestamp, header, now)
		if !got {
			return
		}
		// Accepted: it must be the real HMAC for exactly this timestamp
		// string, and the timestamp must be inside the window.
		sec, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			t.Fatalf("accepted a timestamp %q that is not a number", timestamp)
		}
		if age := now.Sub(time.Unix(sec, 0)); age > SignatureWindow || age < -SignatureWindow {
			t.Fatalf("accepted a timestamp %v away", age)
		}
		want := sign(timestamp, body)
		if !strings.EqualFold(header, want) && header != want {
			t.Fatalf("accepted %q, the signature is %q", header, want)
		}
	})
}
