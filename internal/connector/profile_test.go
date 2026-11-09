package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"
)

// The system prompt goes into session setup requests only, in append mode.
func TestSystemPromptIsAddedToSessionSetup(t *testing.T) {
	for _, method := range []string{"session/new", "session/load", "session/resume"} {
		line := []byte(`{"jsonrpc":"2.0","id":3,"method":"` + method + `","params":{"cwd":"/w","mcpServers":[]}}` + "\n")
		out, ok := withSystemPrompt(line, "be brief")
		if !ok || !bytes.HasSuffix(out, []byte("\n")) {
			t.Fatalf("%s: not rewritten: %s", method, out)
		}
		var msg struct {
			ID     int `json:"id"`
			Params struct {
				Cwd          string `json:"cwd"`
				SystemPrompt struct {
					Mode    string `json:"mode"`
					Content []struct{ Type, Text string }
				} `json:"systemPrompt"`
			} `json:"params"`
		}
		if err := json.Unmarshal(out, &msg); err != nil {
			t.Fatal(err)
		}
		sp := msg.Params.SystemPrompt
		if msg.ID != 3 || msg.Params.Cwd != "/w" || sp.Mode != "append" || len(sp.Content) != 1 || sp.Content[0].Text != "be brief" {
			t.Fatalf("%s: %s", method, out)
		}
	}
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","id":4,"result":{}}`,
		`not json`,
	} {
		if _, ok := withSystemPrompt([]byte(line), "x"); ok {
			t.Fatalf("rewrote %s", line)
		}
	}
}

// The injector rewrites only when the agent advertised the capability.
func TestInjectorFollowsTheCapability(t *testing.T) {
	init := `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"systemPrompt":{}}}}}` + "\n"
	for _, tc := range []struct {
		name, init string
		want       bool
	}{
		{"advertised", init, true},
		{"not advertised", `{"jsonrpc":"2.0","id":0,"result":{"agentCapabilities":{"sessionCapabilities":{}}}}` + "\n", false},
	} {
		w := &wire{prompt: "be brief"}
		// split across reads, as a pipe may deliver it
		r := &sniffer{r: io.MultiReader(strings.NewReader(tc.init[:20]), strings.NewReader(tc.init[20:])), wire: w}
		if b, _ := io.ReadAll(r); string(b) != tc.init {
			t.Fatalf("%s: sniffer changed the stream", tc.name)
		}
		var out bytes.Buffer
		line := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/w"}}` + "\n"
		if n, err := (injector{w: &out, wire: w}).Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("write = %d, %v", n, err)
		}
		if got := strings.Contains(out.String(), "systemPrompt"); got != tc.want {
			t.Fatalf("%s: systemPrompt sent = %v: %s", tc.name, got, out.String())
		}
	}
}

func TestPickOption(t *testing.T) {
	model := acp.SessionConfigOptionCategoryModel
	ungrouped := acp.SessionConfigSelectOptionsUngrouped{{Name: "Opus", Value: "opus"}, {Name: "Sonnet 5", Value: "sonnet"}}
	grouped := acp.SessionConfigSelectOptionsGrouped{{Group: "g", Name: "G", Options: []acp.SessionConfigSelectOption{{Name: "High", Value: "high"}}}}
	options := []acp.SessionConfigOption{
		{Select: &acp.SessionConfigOptionSelect{Id: "m", Category: &model, CurrentValue: "opus", Options: acp.SessionConfigSelectOptions{Ungrouped: &ungrouped}}},
		{Select: &acp.SessionConfigOptionSelect{Id: "reasoning_effort", CurrentValue: "low", Options: acp.SessionConfigSelectOptions{Grouped: &grouped}}},
	}
	if o, v, ok := pickOption(options, model, []string{"model"}, "sonnet 5"); !ok || o.Id != "m" || v != "sonnet" {
		t.Fatalf("model by name: %v %v", v, ok)
	}
	if o, v, ok := pickOption(options, acp.SessionConfigOptionCategoryThoughtLevel, []string{"reasoning_effort"}, "HIGH"); !ok || o.Id != "reasoning_effort" || v != "high" {
		t.Fatalf("effort by id: %v %v", v, ok)
	}
	if _, _, ok := pickOption(options, model, nil, "gpt"); ok {
		t.Fatal("found a model the agent doesn't have")
	}
}

func TestFetchKit(t *testing.T) {
	var gotAuth, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAgent = r.Header.Get("Authorization"), r.URL.Query().Get("agent")
		switch r.URL.Path {
		case "/crew/profile":
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("Authorization") == "Bearer bad" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"message":"this crew token belongs to agent pm"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(agent.Kit{Name: "pm", Version: 2, Revision: "abc", Env: map[string]string{"K": "v"}})
		default: // an older port: the web app answers
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	kit, err := fetchKit(ctx, Config{Server: srv.URL, Token: "sp_crew_x", As: "pm"})
	if err != nil || kit == nil || kit.Version != 2 || kit.Env["K"] != "v" || gotAuth != "Bearer sp_crew_x" || gotAgent != "pm" {
		t.Fatalf("kit = %+v, err = %v, auth %q agent %q", kit, err, gotAuth, gotAgent)
	}
	if _, err := fetchKit(ctx, Config{Server: srv.URL, Token: "bad"}); !errx.IsType(err, errx.TypeAuthorization) || !strings.Contains(err.Error(), "belongs to agent pm") {
		t.Fatalf("refused: err = %v", err)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer old.Close()
	if kit, err := fetchKit(ctx, Config{Server: old.URL}); kit != nil || err != nil {
		t.Fatalf("older port: kit = %+v, err = %v", kit, err)
	}
}

// A profile signal makes an idle connector fetch the kit and, when it
// changed, drain and end the connection to restart its harness; an
// unchanged kit keeps it working.
func TestChangedProfileRestartsWhenIdle(t *testing.T) {
	var revision atomic.Value
	revision.Store("r1")
	frames := make(chan crew.Frame, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/crew/profile" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(agent.Kit{Name: "pm", Revision: revision.Load().(string)})
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ctx := r.Context()
		read := func() (crew.Frame, error) {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return crew.Frame{}, err
			}
			var f crew.Frame
			err = json.Unmarshal(raw, &f)
			return f, err
		}
		write := func(f crew.Frame) {
			raw, _ := json.Marshal(f)
			_ = ws.Write(ctx, websocket.MessageText, raw)
		}
		hello, err := read()
		if err != nil {
			return
		}
		frames <- hello
		write(crew.Frame{Type: crew.FrameWelcome, MCPURL: "http://x/mcp"})
		write(crew.Frame{Type: crew.FrameProfile}) // unchanged: nothing happens
		time.Sleep(200 * time.Millisecond)
		revision.Store("r2")
		write(crew.Frame{Type: crew.FrameProfile})
		for {
			f, err := read()
			if err != nil {
				return
			}
			frames <- f
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := &Agent{kit: &agent.Kit{Name: "pm", Version: 3, Revision: "r1"}, wire: &wire{}}
	err := session(ctx, Config{Server: srv.URL, Token: "sp_crew_x"}, a)
	if !errors.Is(err, errRestart) {
		t.Fatalf("err = %v, want errRestart", err)
	}
	var got []string
	for len(got) < 2 {
		select {
		case f := <-frames:
			got = append(got, string(f.Type))
			if f.Type == crew.FrameHello && f.ProfileVersion != 3 {
				t.Fatalf("hello profile_version = %d, want 3", f.ProfileVersion)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frames = %v, want hello,drain", got)
		}
	}
	if strings.Join(got, ",") != "hello,drain" {
		t.Fatalf("frames = %v, want hello,drain", got)
	}
}
