package connector

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// webauthnPage asks for a security key as soon as it loads (as Google's
// 2-Step Verification does) and records a click on its other button in
// the title.
const webauthnPage = `<!doctype html><title>t</title>
<button id="other" style="position:absolute;left:50px;top:150px;width:200px;height:50px">other</button>
<script>
const log = (s) => { document.title += " " + s }
document.getElementById("other").onclick = () => log("clicked")
const r = (n) => crypto.getRandomValues(new Uint8Array(n))
log("asked")
navigator.credentials.get({ publicKey: { challenge: r(32), allowCredentials: [{ type: "public-key", id: r(32), transports: ["usb"] }], timeout: 60000 } })
  .then(() => log("got"), (e) => log("refused:" + e.name))
</script>`

// TestGuardWebAuthnRealChromium runs the real engine (an environment image
// with the browser, SEAPORT_TEST_CHROMIUM=1; Go CI has no Chromium): a tab
// that asks for a security key while it loads must still take a click.
// Without the guard Chromium's native dialog takes the page's input.
func TestGuardWebAuthnRealChromium(t *testing.T) {
	if os.Getenv("SEAPORT_TEST_CHROMIUM") == "" {
		t.Skip("needs Chromium: SEAPORT_TEST_CHROMIUM=1 in an image with the browser")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(webauthnPage))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	// localhost: a secure context whose host is a valid relying party.
	page := "http://localhost:" + strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:") + "/"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := StartBrowser(ctx, "chromium", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	time.Sleep(500 * time.Millisecond) // the guard attaches

	wsURL, err := browserSocket(ctx, b.URL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dialCDP(ctx, wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	call := func(session, method string, params any) json.RawMessage {
		t.Helper()
		raw, err := c.call(ctx, session, method, params)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return raw
	}
	var target struct {
		TargetID string `json:"targetId"`
	}
	_ = json.Unmarshal(call("", "Target.createTarget", map[string]any{"url": page}), &target)
	var att struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(call("", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}), &att)
	title := func() string {
		var r struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(call(att.SessionID, "Runtime.evaluate", map[string]any{"expression": "document.title", "returnByValue": true}), &r)
		return r.Result.Value
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(title(), "asked") {
		if time.Now().After(deadline) {
			t.Fatalf("the page never asked for a key: %q", title())
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(time.Second) // a native dialog would be up by now
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		call(att.SessionID, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": 150, "y": 175, "button": "left", "clickCount": 1})
	}
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(title(), "clicked") {
		if time.Now().After(deadline) {
			t.Fatalf("a security-key request blocked the page: %q", title())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
