package hosting

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// The managed browser (plan browser-login.md). A slot whose environment
// has a browser (Spec.Browser) and whose agent's profile turns it on runs
// the engine beside the harness, with the agent's profile (cookies,
// storage) in BrowserDir of the state volume. The port's viewer reaches
// the same browser: Commands.OpenBrowser runs BrowserRelayCommand in the
// slot's container (Runtime.Exec), a CDP pipe of newline-delimited JSON,
// and the web speaks CDP over a WebSocket to it (screencast, input,
// cookies), so a person signs in to a site and the agent has the session.

// BrowserRelayCommand runs in the slot (`seaport browser-relay`).
var BrowserRelayCommand = []string{"/opt/seaport/bin/seaport", "browser-relay"}

// BrowserHello is the relay's first line once it reached the browser;
// anything else first is its failure.
const BrowserHello = `{"seaport":"ready"}`

// Browser session limits.
const (
	// BrowserIdle ends a viewer with no message either way for this long.
	BrowserIdle = 10 * time.Minute
	// BrowserMax ends a viewer however busy.
	BrowserMax = 2 * time.Hour
	// MaxBrowserMessage bounds one CDP message either way (a screencast
	// frame is a base64 JPEG; a full-page screenshot a few MB).
	MaxBrowserMessage = 32 << 20
	// ViewerBeat is how often the process relaying an open viewer marks
	// the slot viewed (agent_slots.viewer_at); the supervisor reads a mark
	// younger than ViewerStale as work (Supervised.Viewing), so a slot
	// isn't stopped or restarted under the person in its browser.
	ViewerBeat  = 20 * time.Second
	ViewerStale = 3 * ViewerBeat
)

// BrowserConn is an open viewer: CDP messages (JSON, one each) in both
// directions. Close ends the relay in the slot.
type BrowserConn interface {
	// Read is the next message from the browser (io.EOF when it ended).
	Read(ctx context.Context) ([]byte, error)
	// Write sends one message to the browser.
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// BrowserEvent is the payload of EventSlotBrowser: who opened or closed a
// slot's viewer (an audit trail; nothing of the stream is kept).
type BrowserEvent struct {
	Agent identity.AgentID `json:"agent_id"`
	Slot  int              `json:"slot"`
	// State is "opened" or "closed".
	State string `json:"state"`
	By    string `json:"by"`
}

// Browser viewer states (BrowserEvent.State).
const (
	BrowserOpened = "opened"
	BrowserClosed = "closed"
)
