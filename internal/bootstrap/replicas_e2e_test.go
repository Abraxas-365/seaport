package bootstrap_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/coder/websocket"
)

// Two port processes on one database act as one port: a ship arriving at
// one wakes an agent connected to the other, the crew status and the
// connection quota span both, a revoke on one closes connections on the
// other, a stopping process hands its deliveries back at once, and the
// connections of a process that stops touching them are reaped.
func TestReplicasShareTheCrew(t *testing.T) {
	limit := func(c *config.Server) { c.MaxCrewConnections = 2 }
	a := startPortWith(t, limit, bootstrap.Options{})
	b := joinPort(t, limit, bootstrap.Options{})

	d := a.apiDock("t")
	issued := a.issueAs(adminToken, a.agent(testAgent).ID, "replicas")
	a.letIn(d)
	first := a.inject(d, "555", "r1", "hola")

	// The agent on b gets what arrived at a.
	onB := b.dialCrewAs(issued.Value, crew.Frame{Type: crew.FrameHello, Agent: "on-b", Concurrency: 1})
	f := onB.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != first.ID {
		t.Fatalf("delivery on b: %+v", f)
	}
	waitCrew(t, a, func(st crew.Status) bool {
		return len(st.Agents) == 1 && st.Agents[0].Name == "on-b" && len(st.Agents[0].Deliveries) == 1
	})
	onB.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	second := a.inject(d, "555", "r2", "sigues?")
	if f := onB.read(); f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != second.ID {
		t.Fatalf("second delivery on b: %+v", f)
	} else {
		onB.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}

	third := a.inject(d, "555", "r3", "otra")
	f = onB.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != third.ID {
		t.Fatalf("third delivery: %+v", f)
	}

	// The quota counts both processes.
	onA := a.dialCrewAs(issued.Value, crew.Frame{Type: crew.FrameHello, Agent: "on-a", Concurrency: 1})
	if code := dialStatus(b, issued.Value); code != http.StatusUnprocessableEntity {
		t.Fatalf("third connection across replicas: %d", code)
	}

	// A stopping process hands its delivery back at once (no lease wait).
	onA.quiet(200 * time.Millisecond) // one in flight per conversation
	b.stop()
	if f := onA.read(); f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != third.ID {
		t.Fatalf("redelivery after b stopped: %+v", f)
	} else {
		onA.send(crew.Frame{Type: crew.FrameAck, DeliveryID: &f.Delivery.ID})
	}
	waitCrew(t, a, func(st crew.Status) bool { return len(st.Agents) == 1 && st.Agents[0].Name == "on-a" })

	// A revoke on c closes the connection on a.
	c := joinPort(t, limit, bootstrap.Options{})
	if code := c.admin(http.MethodDelete, "/api/v1/crew/tokens/"+issued.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	closed(t, onA)
	waitCrew(t, c, func(st crew.Status) bool { return len(st.Agents) == 0 })
}

// A connection whose process stops touching it (it died or stalled) is
// reaped by another process: its delivery goes to another agent at once
// and its own process, if alive, closes it.
func TestStaleConnectionIsReaped(t *testing.T) {
	a := startPort(t)
	b := joinPort(t, func(*config.Server) {}, bootstrap.Options{})
	d := a.apiDock("t")
	b.token = a.crewToken() // one token, both processes
	a.letIn(d)
	s := a.inject(d, "666", "x1", "hola")

	stalled := b.dialCrew(1)
	f := stalled.read()
	if f.Type != crew.FrameDelivery {
		t.Fatalf("delivery: %+v", f)
	}
	var st crew.Status
	waitCrew(t, a, func(got crew.Status) bool {
		st = got
		return len(got.Agents) == 1 && len(got.Agents[0].Deliveries) == 1
	})
	other := a.dialCrew(1)
	other.quiet(200 * time.Millisecond)

	// b "stalls": its row ages past the stale period.
	db, err := bootstrap.OpenDB(context.Background(), testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE crew_connections SET seen_at = now() - interval '1 hour', process = 'gone' WHERE session = $1`,
		st.Agents[0].Session); err != nil {
		t.Fatal(err)
	}
	if f := other.read(); f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ID != s.ID || f.Delivery.ID == st.Agents[0].Deliveries[0].ID {
		t.Fatalf("redelivery after reap: %+v", f)
	}
	closed(t, stalled) // b's touch no longer finds it
}

func waitCrew(t *testing.T, p *port, ok func(crew.Status) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var st crew.Status
	for time.Now().Before(deadline) {
		st = crew.Status{}
		if code := p.admin(http.MethodGet, "/api/v1/crew", nil, &st); code == http.StatusOK && ok(st) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("crew status: %+v", st)
}

func dialStatus(p *port, token string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.srv.URL, "http")+"/crew/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err == nil {
		ws.CloseNow()
		return http.StatusSwitchingProtocols
	}
	if resp == nil {
		p.t.Fatal(err)
	}
	return resp.StatusCode
}

// closed waits for the server to close c.
func closed(t *testing.T, c *crewConn) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("connection still open")
		}
	}
}
