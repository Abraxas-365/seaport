package crew

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// EventActivity: what an agent is doing on a delivery right now (its
// thinking, its text, the tools it calls). Live only: flashed, never
// stored, so a client sees it only while connected (event.Flasher).
// Data: Activity.
const EventActivity = "crew.activity"

// Activity is a batch of what the agent did on a delivery since the
// connector's previous activity frame. The connector sends them while the
// turn runs; the port checks them and flashes them to the web as is.
type Activity struct {
	// Delivery and Conversation are filled by the port (the delivery must
	// be open on the sending connection; the frame's conversation is
	// ignored).
	Delivery     identity.DeliveryID     `json:"delivery_id"`
	Conversation identity.ConversationID `json:"conversation_id"`
	AgentID      identity.AgentID        `json:"agent_id"`
	AgentName    string                  `json:"agent_name"`
	At           time.Time               `json:"at"`
	Items        []ActivityItem          `json:"items"`
}

// Activity item kinds.
const (
	// ActivityThought: a chunk of the agent's reasoning (append to the
	// previous thought chunks of the turn).
	ActivityThought = "thought"
	// ActivityMessage: a chunk of the agent's own text (append likewise).
	ActivityMessage = "message"
	// ActivityTool: a tool call started or changed (by Tool id).
	ActivityTool = "tool"
)

// Tool call statuses (ACP's).
const (
	ToolPending    = "pending"
	ToolInProgress = "in_progress"
	ToolCompleted  = "completed"
	ToolFailed     = "failed"
)

// ActivityItem is one step. Tool items carry the call's title, kind and
// status, never its input or output (they may hold file contents or
// secrets the agent printed).
type ActivityItem struct {
	Kind string `json:"kind"`
	// Text is a thought or message chunk.
	Text string `json:"text,omitempty"`
	// Tool identifies a call within the turn; Title, ToolKind (ACP: read,
	// edit, execute, search, fetch, think, other…) and Status may change in
	// later items of the same Tool.
	Tool     string `json:"tool,omitempty"`
	Title    string `json:"title,omitempty"`
	ToolKind string `json:"tool_kind,omitempty"`
	Status   string `json:"status,omitempty"`
}

// Activity limits: a frame's items, and their encoded size (JSON), which
// keeps a flashed activity under event.MaxFlash with its envelope.
const (
	MaxActivityItems = 32
	MaxActivityBytes = 6000
	MaxToolID        = 128
)

var toolStatuses = map[string]bool{"": true, ToolPending: true, ToolInProgress: true, ToolCompleted: true, ToolFailed: true}

// Validate checks what a connector sent.
func (a Activity) Validate() error {
	if len(a.Items) == 0 {
		return errx.InvalidField("items", "items is required")
	}
	if len(a.Items) > MaxActivityItems {
		return errx.InvalidField("items", "at most "+strconv.Itoa(MaxActivityItems)+" items per frame")
	}
	for _, it := range a.Items {
		switch it.Kind {
		case ActivityThought, ActivityMessage:
			if it.Text == "" {
				return errx.InvalidField("items", "thought and message items need text")
			}
		case ActivityTool:
			if it.Tool == "" || len(it.Tool) > MaxToolID {
				return errx.InvalidField("items", "tool items need a tool id of at most "+strconv.Itoa(MaxToolID)+" bytes")
			}
			if !toolStatuses[it.Status] {
				return errx.InvalidField("items", "unknown tool status "+strconv.Quote(it.Status))
			}
			if err := text.Optional("tool_kind", it.ToolKind, 32); err != nil {
				return err
			}
		default:
			return errx.InvalidField("items", "unknown item kind "+strconv.Quote(it.Kind))
		}
		for _, s := range []string{it.Text, it.Title, it.Tool} {
			if !utf8.ValidString(s) || text.HasForbiddenControl(s) {
				return errx.InvalidField("items", "item text must be UTF-8 without control characters")
			}
		}
	}
	if ActivitySize(a.Items) > MaxActivityBytes {
		return errx.InvalidField("items", "items encode to at most "+strconv.Itoa(MaxActivityBytes)+" bytes per frame")
	}
	return nil
}

// ActivitySize is the encoded size of items, as the port flashes them
// (HTML characters unescaped).
func ActivitySize(items []ActivityItem) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(items); err != nil {
		return MaxActivityBytes + 1
	}
	return buf.Len() - 1
}
