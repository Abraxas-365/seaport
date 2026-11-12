// Package eventsse serves the event bus to browsers as Server-Sent Events.
package eventsse

import (
	"bufio"
	"fmt"
	"time"

	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/gofiber/fiber/v3"
)

// heartbeat keeps proxies from closing an idle stream.
var heartbeat = 25 * time.Second

// maxStream is the longest a stream runs before the client must
// reconnect (and authenticate again).
var maxStream = 10 * time.Minute

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
//
// The stream is written by c.SendStreamWriter: fasthttp runs its closure
// while the handler returns and Fiber recycles c, so the closure touches
// nothing of c (everything it needs is read first).
func Handler(bus *event.Bus, done <-chan struct{}, perms map[string]string) fiber.Handler {
	return func(c fiber.Ctx) error {
		last := c.Get("Last-Event-ID")
		if last == "" {
			last = c.Query("last_event_id")
		}
		p, err := httpx.Caller(c)
		if err != nil {
			return err
		}
		// Subscribe before the first byte: a failure is still an ordinary
		// error response. The context only bounds the replay read.
		sub, replay, reset, err := bus.Subscribe(c.Context(), p.Organization, last)
		if err != nil {
			return err
		}
		c.Set("Content-Type", "text/event-stream")
		c.Set("X-Accel-Buffering", "no")
		// Closed by app.Shutdown (fasthttp's server done channel) and by
		// nothing else: a dead client shows as a failed write.
		shutdown := c.RequestCtx().Done()
		boot := bus.Boot()
		return c.SendStreamWriter(func(w *bufio.Writer) {
			defer sub.Close()
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
				return err == nil && w.Flush() == nil
			}
			fmt.Fprintf(w, "retry: 3000\nevent: ready\ndata: {\"boot\":%q}\n\n", boot)
			if reset {
				fmt.Fprint(w, "event: reset\ndata: {}\n\n")
			}
			if w.Flush() != nil {
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
			for {
				select {
				case <-expiry.C:
					return
				case <-done:
					return
				case <-shutdown:
					return
				case e, ok := <-sub.C:
					if !ok {
						return // too slow: the client reconnects and replays
					}
					if !write(e) {
						return
					}
				case <-tick.C:
					if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || w.Flush() != nil {
						return
					}
				}
			}
		})
	}
}
