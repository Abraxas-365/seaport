package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Outbound limits hold under concurrent replies and answer 429 with
// Retry-After; failed and successful sends both count.
func TestOutboundRateLimits(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) {
		c.SendPerMinute, c.SendPerHour, c.SendPerChatMinute = 5, 100, 3
	}, bootstrap.Options{})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "api", "name": "limits"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	conv := func(chat string) string {
		s := p.inject(d, chat, "m-"+chat, "hola")
		if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+s.Conversation.String(),
			map[string]string{"access": "allow"}, nil); code != http.StatusOK {
			t.Fatalf("allow: %d", code)
		}
		return s.Conversation.String()
	}
	a, b, c := conv("111"), conv("222"), conv("333")

	// 10 concurrent replies to one chat: exactly 3 pass.
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code := p.admin(http.MethodPost, "/api/v1/conversations/"+a+"/ships", map[string]string{"text": fmt.Sprint("r", i)}, nil)
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[http.StatusCreated] != 3 || codes[http.StatusTooManyRequests] != 7 {
		t.Fatalf("per-chat limit under concurrency: %v", codes)
	}

	// The dock's minute limit (5) spans chats: 2 more pass, then 429.
	for i, conv := range []string{b, b} {
		if code := p.admin(http.MethodPost, "/api/v1/conversations/"+conv+"/ships", map[string]string{"text": fmt.Sprint("b", i)}, nil); code != http.StatusCreated {
			t.Fatalf("reply %d under dock limit: %d", i, code)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/api/v1/conversations/"+c+"/ships",
		jsonBody(map[string]string{"text": "over"}))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("dock limit: %d Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}

	// Refused replies leave no ship behind.
	var hist struct {
		Items []ship.Ship `json:"items"`
	}
	p.admin(http.MethodGet, "/api/v1/conversations/"+a+"/ships?limit=100", nil, &hist)
	out := 0
	for _, s := range hist.Items {
		if s.Direction == ship.DirectionOut {
			out++
		}
	}
	if out != 3 {
		t.Fatalf("conversation a has %d outbound ships, want 3", out)
	}
}

func jsonBody(v any) *bytes.Reader {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw)
}
