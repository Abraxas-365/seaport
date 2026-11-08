// Package crew is the wire protocol between the port and the agents that
// work it (connectors). Connectors dial out to /crew/ws, so agents behind
// NAT need no public address.
//
//	server → client: welcome, delivery, steer
//	client → server: hello, ack, release, reply
//
// Steering: a connector whose agent can take input mid-turn says
// hello.steer. Ships that arrive for a conversation while its delivery is
// in flight then join that delivery and come as a steer frame (same
// delivery id, only the new ships) instead of waiting for the next
// delivery. The ack names the ships the agent actually saw; joined ships it
// could not take (the turn had ended) go back to the queue.
package crew

import (
	"encoding/json"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// FrameType names a frame.
type FrameType string

const (
	FrameHello    FrameType = "hello"
	FrameWelcome  FrameType = "welcome"
	FrameDelivery FrameType = "delivery"
	FrameSteer    FrameType = "steer"
	FrameAck      FrameType = "ack"
	FrameRelease  FrameType = "release"
	FrameReply    FrameType = "reply"
	FrameError    FrameType = "error"
)

const (
	// DefaultConcurrency is how many conversations a connector works at
	// once when its hello does not say.
	DefaultConcurrency = 4
	// MaxConcurrency bounds hello.concurrency.
	MaxConcurrency = 32
	// MaxFrame bounds one WebSocket frame.
	MaxFrame = 4 << 20
	// PingInterval is how often both sides ping to detect dead links.
	PingInterval = 30 * time.Second
)

// Frame is the one envelope on the wire; Type says which fields are set.
type Frame struct {
	Type FrameType `json:"type"`

	// hello
	Agent       string `json:"agent,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Steer       bool   `json:"steer,omitempty"`

	// welcome
	MCPURL string `json:"mcp_url,omitempty"`

	// delivery, steer
	Delivery *ship.Delivery `json:"delivery,omitempty"`

	// ack, release
	DeliveryID *identity.DeliveryID `json:"delivery_id,omitempty"`
	// ack: the ships the agent handled
	ShipIDs []identity.ShipID `json:"ship_ids,omitempty"`

	// reply (connectors without MCP send the agent's answer this way)
	ConversationID *identity.ConversationID `json:"conversation_id,omitempty"`
	Text           string                   `json:"text,omitempty"`

	// error
	Message string `json:"message,omitempty"`
}

// Hello is the first frame a connector sends.
type Hello struct {
	Agent       string
	Concurrency int
	// Steer: the agent takes new ships mid-turn (steer frames).
	Steer bool
}

func (h Hello) Validate() error {
	if h.Concurrency < 0 || h.Concurrency > MaxConcurrency {
		return errx.InvalidField("concurrency", "concurrency must be between 1 and 32")
	}
	return text.Optional("agent", h.Agent, text.MaxName)
}

// EffectiveConcurrency applies the default.
func (h Hello) EffectiveConcurrency() int {
	if h.Concurrency == 0 {
		return DefaultConcurrency
	}
	return h.Concurrency
}

func Decode(raw []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, errx.Validation("frame is not valid JSON")
	}
	return f, nil
}
