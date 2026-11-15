package connector

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

// FuzzSystemPrompt: the connector rewrites the ACP lines it forwards to the
// agent. Any line (a harness or a client wrote it) either passes through
// untouched or comes back as one valid JSON line that still carries the
// original params plus the appended system prompt.
func FuzzSystemPrompt(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work","mcpServers":[]}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"s1"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{}}`,
		`{"method":"session/new","params":null}`, `{"method":"session/new","params":[]}`, `{"method":1}`, `[]`, `null`, ``, `{"method":"session/resume"}`,
		`{"method":"session/new","params":{"systemPrompt":"x"}}`, "{\"method\":\"session/new\",\"params\":{}}\n",
	} {
		f.Add([]byte(s), "be brief")
	}
	f.Fuzz(func(t *testing.T, line []byte, prompt string) {
		out, changed := withSystemPrompt(line, prompt)
		if !changed {
			return
		}
		if len(out) == 0 || out[len(out)-1] != '\n' || bytes.Count(out, []byte("\n")) != 1 {
			t.Fatalf("not exactly one line: %q", out)
		}
		var msg struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(out, &msg); err != nil || !sessionSetup[msg.Method] || msg.Params == nil {
			t.Fatalf("bad output %q: %v", out, err)
		}
		var want string // what a JSON round trip makes of the prompt (invalid UTF-8 becomes U+FFFD)
		enc, _ := json.Marshal(prompt)
		_ = json.Unmarshal(enc, &want)
		var sp struct {
			Mode    string `json:"mode"`
			Content []struct{ Type, Text string }
		}
		if err := json.Unmarshal(msg.Params["systemPrompt"], &sp); err != nil || sp.Mode != "append" || len(sp.Content) != 1 || sp.Content[0].Text != want {
			t.Fatalf("systemPrompt: %s (%v)", msg.Params["systemPrompt"], err)
		}
	})
}

// FuzzSniffer: the sniffer sits on the agent's stdout. Whatever the agent
// prints (in any chunking) passes through byte for byte and never panics.
func FuzzSniffer(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":0,"result":{"agentCapabilities":{"sessionCapabilities":{"systemPrompt":{}}}}}` + "\n",
		`{"result":{"agentCapabilities":{"sessionCapabilities":{"systemPrompt":null}}}}` + "\n" + `{"x":1}` + "\n",
		`{"result":{"agentCapabilities":` + "\n", "no newline at all", "\n\n\n", `"agentCapabilities"` + "\n", `{"result":{"agentCapabilities":[]}}` + "\n",
	} {
		f.Add([]byte(s), uint8(7))
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk uint8) {
		w := &wire{}
		s := &sniffer{r: &chunked{data: data, n: int(chunk)%32 + 1}, wire: w}
		got, err := io.ReadAll(s)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("output differs from input (%v)", err)
		}
		_ = w.Supported()
	})
}

// chunked returns data n bytes at a time.
type chunked struct {
	data []byte
	n    int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(c.n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// FuzzPortMessage: an error body from the port never panics and always
// reads as some message.
func FuzzPortMessage(f *testing.F) {
	for _, s := range []string{`{"error":{"message":"nope"}}`, `{"error":"x"}`, `{"error":{"message":""}}`, `[]`, ``, `{`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if portMessage(body, "401 Unauthorized") == "" {
			t.Fatal("empty message")
		}
	})
}
