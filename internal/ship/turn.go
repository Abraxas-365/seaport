package ship

import (
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// EventTurn: a delivery ended and the summary of what its agent did was
// stored. Data: Turn.
const EventTurn = "ship.turn"

// TurnOutcome is how a delivery ended.
type TurnOutcome string

const (
	// TurnAcked: the agent finished (the delivery was acked).
	TurnAcked TurnOutcome = "acked"
	// TurnReleased: the turn failed and its ships went back to the queue.
	TurnReleased TurnOutcome = "released"
	// TurnDropped: the connection closed mid-turn.
	TurnDropped TurnOutcome = "dropped"
)

// Turn is what an agent did on one delivery: the tools it called (title,
// kind, status and timing; never their input or output, nor its thinking).
// The crew hub builds it from the connector's activity frames and stores
// it when the delivery ends, so a conversation shows how each answer was
// made after the live activity is gone.
type Turn struct {
	Delivery     identity.DeliveryID     `db:"id" json:"delivery_id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Conversation identity.ConversationID `db:"conversation_id" json:"conversation_id"`
	Agent        identity.AgentID        `db:"agent_id" json:"agent_id"`
	AgentName    string                  `db:"agent_name" json:"agent_name"`
	Outcome      TurnOutcome             `db:"outcome" json:"outcome"`
	StartedAt    time.Time               `db:"started_at" json:"started_at"`
	EndedAt      time.Time               `db:"ended_at" json:"ended_at"`
	Tools        []TurnTool              `db:"-" json:"tools"`
	// Omitted counts the tools past MaxTurnTools (not kept).
	Omitted int `db:"omitted" json:"omitted,omitempty"`
}

// TurnTool is one tool call of a turn. Status is the last one seen
// (crew.Tool*: a call still pending when the turn ended stays so).
type TurnTool struct {
	Title     string     `json:"title"`
	Kind      string     `json:"kind,omitempty"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Turn limits: tools kept per turn, a title's length.
const (
	MaxTurnTools = 200
	MaxToolTitle = 300
	MaxToolKind  = 32
)

var turnOutcomes = map[TurnOutcome]bool{TurnAcked: true, TurnReleased: true, TurnDropped: true}

var turnStatuses = map[string]bool{"pending": true, "in_progress": true, "completed": true, "failed": true}

func (t Turn) Validate() error {
	switch {
	case t.Delivery.IsZero():
		return errx.InvalidField("delivery_id", "delivery_id is required")
	case t.Conversation.IsZero():
		return errx.InvalidField("conversation_id", "conversation_id is required")
	case t.Agent.IsZero():
		return errx.InvalidField("agent_id", "agent_id is required")
	case !turnOutcomes[t.Outcome]:
		return errx.InvalidField("outcome", "unknown outcome "+strconv.Quote(string(t.Outcome)))
	case t.StartedAt.IsZero() || t.EndedAt.Before(t.StartedAt):
		return errx.InvalidField("ended_at", "a turn ends after it starts")
	case len(t.Tools) == 0:
		return errx.InvalidField("tools", "a turn without tools is not stored")
	case len(t.Tools) > MaxTurnTools:
		return errx.InvalidField("tools", "at most "+strconv.Itoa(MaxTurnTools)+" tools per turn")
	case t.Omitted < 0:
		return errx.InvalidField("omitted", "omitted can't be negative")
	}
	if err := text.Required("agent_name", t.AgentName, text.MaxName); err != nil {
		return err
	}
	for _, tool := range t.Tools {
		if err := text.Required("title", tool.Title, MaxToolTitle); err != nil {
			return err
		}
		if err := text.Optional("kind", tool.Kind, MaxToolKind); err != nil {
			return err
		}
		if !turnStatuses[tool.Status] {
			return errx.InvalidField("status", "unknown tool status "+strconv.Quote(tool.Status))
		}
		if tool.StartedAt.IsZero() || (tool.EndedAt != nil && tool.EndedAt.Before(tool.StartedAt)) {
			return errx.InvalidField("tools", "a tool ends after it starts")
		}
	}
	return nil
}

// ToolTitle makes a tool call's title storable: no control characters,
// credential-looking strings redacted (text.Redact), at most
// MaxToolTitle runes.
func ToolTitle(s string) string {
	s = text.Redact(text.Clean(text.StripControl(s)))
	if utf8.RuneCountInString(s) > MaxToolTitle {
		r := []rune(s)
		s = string(r[:MaxToolTitle-1]) + "…"
	}
	return s
}
