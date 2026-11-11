// Package eventsse serves the event bus to browsers as Server-Sent Events.
package eventsse

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

// heartbeat keeps proxies from closing an idle stream.
const heartbeat = 25 * time.Second

// maxStream is the longest a stream runs before the client must
// reconnect (and authenticate again).
const maxStream = 10 * time.Minute

var errUnauthenticated = errx.Unauthorized("missing or invalid token")

// Handler streams events. A client reconnecting with Last-Event-ID gets
// what it missed, or a "reset" event when that is no longer known (it must
// refetch its state). Ids are the log's (every process's), so a client
// may reconnect to any replica. Every stream starts with "ready" {boot}. A stream
// only carries the events of the caller's organization (httpx principal),
// and of those only the types whose read permission (perms: event type →
// permission) the caller holds; a type missing from perms is never sent.
// A stream ends when the principal's credential expires, and after
// maxStream at most (the client reconnects, which authenticates again).
// done closes every stream (server shutdown).
func Handler(bus *event.Bus, done <-chan struct{}, perms map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("X-Accel-Buffering", "no")
		last := r.Header.Get("Last-Event-ID")
		if last == "" {
			last = r.URL.Query().Get("last_event_id")
		}
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			httpx.WriteError(w, r, errUnauthenticated)
			return
		}
		sub, replay, reset, err := bus.Subscribe(r.Context(), p.Organization, last)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		defer sub.Close()
		w.WriteHeader(http.StatusOK)

		write := func(e event.Event) bool {
			if perm, ok := perms[e.Type]; !ok || !p.Can(perm) {
				return true
			}
			var err error
			if e.ID == "" {
				// A flash: no id line, which would reset the client's
				// Last-Event-ID (flashes are never replayed).
				_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, e.Data)
			} else {
				_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.ID, e.Type, e.Data)
			}
			if err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		fmt.Fprintf(w, "retry: 3000\nevent: ready\ndata: {\"boot\":%q}\n\n", bus.Boot())
		if reset {
			fmt.Fprint(w, "event: reset\ndata: {}\n\n")
		}
		if rc.Flush() != nil {
			return
		}
		for _, e := range replay {
			if !write(e) {
				return
			}
		}
		tick := time.NewTicker(heartbeat)
		defer tick.Stop()
		// End at the credential's expiry, and at most after maxStream, so
		// revoked keys and changed permissions apply on the reconnect
		// (which resumes from Last-Event-ID: nothing is lost).
		life := maxStream
		if !p.Expires.IsZero() && time.Until(p.Expires) < life {
			life = time.Until(p.Expires)
		}
		expiry := time.NewTimer(life)
		defer expiry.Stop()
		expired := expiry.C
		for {
			select {
			case <-r.Context().Done():
				return
			case <-expired:
				return
			case <-done:
				return
			case e, ok := <-sub.C:
				if !ok {
					return // too slow: the client reconnects and replays
				}
				if !write(e) {
					return
				}
			case <-tick.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
					return
				}
			}
		}
	})
}
