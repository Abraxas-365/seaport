package connector

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/coder/websocket"
)

func TestProxyBypass(t *testing.T) {
	if got := proxyBypass("localhost,127.0.0.1,::1,port.example.com"); got != "<-loopback>;port.example.com" {
		t.Fatalf("bypass %q", got)
	}
	if got := proxyBypass(""); got != "" {
		t.Fatalf("empty bypass %q", got)
	}
}

// The browser MCP may read and write where the agent works: chrome-devtools-mcp
// allows only its temp directory without --workspace, so take_screenshot's
// filePath was refused in the slot's workspace, home and /tmp. Roots are
// absolute, cleaned and each listed once.
func TestBrowserMCPWorkspaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("slot paths are Unix paths")
	}
	roots := browserRoots("/workspace", "/home/agent", "/home/agent/.tmp/")
	if strings.Join(roots, ",") != "/workspace,/home/agent,/home/agent/.tmp,/tmp" {
		t.Fatalf("roots %v", roots)
	}
	if got := browserRoots("", "relative", "/tmp"); strings.Join(got, ",") != "/tmp" {
		t.Fatalf("roots without cwd and home %v", got)
	}
	args := strings.Join(browserMCPArgs("http://127.0.0.1:9222", roots), " ")
	want := "--browserUrl http://127.0.0.1:9222 --no-usage-statistics --no-performance-crux" +
		" --workspace /workspace --workspace /home/agent --workspace /home/agent/.tmp --workspace /tmp"
	if args != want {
		t.Fatalf("args\n got %s\nwant %s", args, want)
	}
}

// TestPipeCDP: lines in go to the socket, messages out come as lines.
func TestPipeCDP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			// echo with an id, like CDP answers
			if err := c.Write(ctx, websocket.MessageText, append([]byte(`{"echo":`), append(msg, '}')...)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	in, inw := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- pipeCDP(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), in, out) }()
	_, _ = io.WriteString(inw, `{"id":1,"method":"Target.getTargets"}`+"\n")
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), `{"echo":{"id":1`) {
		if time.Now().After(deadline) {
			t.Fatalf("no echo: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	inw.Close() // stdin closed: the relay ends
	if err := <-done; err != nil {
		t.Fatalf("relay: %v", err)
	}
	if !strings.HasPrefix(out.String(), hosting.BrowserHello+"\n") {
		t.Fatalf("the hello must come first: %q", out.String())
	}
	if !strings.HasSuffix(out.String(), "}\n") {
		t.Fatalf("messages must end in a newline: %q", out.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
