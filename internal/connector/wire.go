package connector

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// The ACP Go SDK (v0.13.5) predates the client-provided system prompt
// (ACP RFD, stable since 2026-11-06): session/new, session/load and
// session/resume take systemPrompt {mode, content}, and agents that take
// it advertise agentCapabilities.sessionCapabilities.systemPrompt. Until
// the SDK has the field, the connector reads the capability off the
// initialize response and adds the field to those requests on the wire.
// The SDK writes one JSON-RPC message per Write and reads newline-framed
// messages, so both work line by line.

// sessionSetup are the requests that take systemPrompt.
var sessionSetup = map[string]bool{"session/new": true, "session/load": true, "session/resume": true}

// wire holds what the injector needs; set by the agent.
type wire struct {
	mu sync.Mutex
	// supported: the agent advertised sessionCapabilities.systemPrompt.
	supported bool
	known     bool // the initialize response was seen
	prompt    string
}

func (w *wire) systemPrompt() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prompt, w.supported && w.prompt != ""
}

// Supported reports whether the agent takes systemPrompt (false until
// initialize answered).
func (w *wire) Supported() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.supported
}

// injector adds systemPrompt to session setup requests written to the agent.
type injector struct {
	w    io.Writer
	wire *wire
}

func (in injector) Write(p []byte) (int, error) {
	out := p
	if text, ok := in.wire.systemPrompt(); ok {
		if b, changed := withSystemPrompt(p, text); changed {
			out = b
		}
	}
	if _, err := in.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// withSystemPrompt adds systemPrompt (append) to a session setup request.
func withSystemPrompt(line []byte, text string) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return nil, false
	}
	var method string
	if json.Unmarshal(msg["method"], &method) != nil || !sessionSetup[method] {
		return nil, false
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(msg["params"], &params) != nil || params == nil {
		return nil, false
	}
	sp, _ := json.Marshal(map[string]any{
		"mode":    "append",
		"content": []map[string]string{{"type": "text", "text": text}},
	})
	params["systemPrompt"] = sp
	msg["params"], _ = json.Marshal(params)
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	return append(b, '\n'), true
}

// sniffer passes the agent's output through, reading the system prompt
// capability off the first message that carries agentCapabilities (the
// initialize response); after it, bytes go straight through.
type sniffer struct {
	r    io.Reader
	wire *wire
	line []byte
	done bool
}

// maxSniff bounds the line buffered while looking (the SDK's own limit).
const maxSniff = 10 << 20

func (s *sniffer) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if s.done || n == 0 {
		return n, err
	}
	rest := p[:n]
	for len(rest) > 0 && !s.done {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			s.line = append(s.line, rest...)
			if len(s.line) > maxSniff {
				s.line = nil
			}
			break
		}
		s.line = append(s.line, rest[:i]...)
		rest = rest[i+1:]
		s.inspect(s.line)
		s.line = s.line[:0]
	}
	if s.done {
		s.line = nil
	}
	return n, err
}

func (s *sniffer) inspect(line []byte) {
	if !bytes.Contains(line, []byte(`"agentCapabilities"`)) {
		return
	}
	var msg struct {
		Result struct {
			AgentCapabilities struct {
				SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities"`
			} `json:"agentCapabilities"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	raw, ok := msg.Result.AgentCapabilities.SessionCapabilities["systemPrompt"]
	s.wire.mu.Lock()
	s.wire.supported = ok && string(raw) != "null"
	s.wire.known = true
	s.wire.mu.Unlock()
	s.done = true
}
