package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/coder/websocket"
)

// A refused crew token ends the connector instead of retrying forever: a
// revoked or expired token never works again.
func TestRefusedTokenStopsReconnecting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	calls := 0
	// session fails at the dial, before it touches the agent (nil here).
	err := keepConnected(ctx, nil, time.Millisecond, func(ctx context.Context) error {
		calls++
		return session(ctx, Config{Server: srv.URL, Token: "sp_crew_revoked"}, nil)
	})
	if !errx.IsType(err, errx.TypeAuthorization) || !strings.Contains(err.Error(), "rejected the crew token") {
		t.Fatalf("err = %v, want the rejected-token error", err)
	}
	if calls != 1 {
		t.Fatalf("dialed %d times, want 1", calls)
	}
}

// A hello the port refuses (an agent the token isn't for) ends it too.
func TestRefusedHelloStopsReconnecting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = ws.Read(r.Context())
		_ = ws.Close(websocket.StatusPolicyViolation, "this crew token belongs to agent pm, not reviewer")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := 0
	err := keepConnected(ctx, nil, time.Millisecond, func(ctx context.Context) error {
		calls++
		return session(ctx, Config{Server: srv.URL, Token: "sp_crew_x", As: "reviewer"}, &Agent{})
	})
	if !errx.IsType(err, errx.TypeAuthorization) || !strings.Contains(err.Error(), "belongs to agent pm") || calls != 1 {
		t.Fatalf("err = %v after %d calls", err, calls)
	}
}

// Other failures (port down, restarts) are retried until ctx ends.
func TestLostConnectionsAreRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := keepConnected(ctx, nil, time.Millisecond, func(context.Context) error {
		calls++
		if calls == 4 {
			cancel()
		}
		return errx.Wrap(errors.New("connection refused"), "dial port", errx.TypeExternal)
	})
	if err != nil || calls != 4 {
		t.Fatalf("err = %v after %d calls, want nil after 4", err, calls)
	}
}

// An agent that exited ends the connector with an error.
func TestAgentExitStopsReconnecting(t *testing.T) {
	done := make(chan struct{})
	close(done)
	err := keepConnected(context.Background(), done, time.Millisecond, func(context.Context) error {
		return errors.New("lost")
	})
	if err == nil || !strings.Contains(err.Error(), "agent process exited") {
		t.Fatalf("err = %v", err)
	}
}
