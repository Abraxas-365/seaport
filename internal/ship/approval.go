package ship

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// EventApproval: an approval was asked, answered, cancelled or expired.
// Data: Approval.
const EventApproval = "ship.approval"

// ApprovalStatus is where an approval is.
//
//	pending → answered | expired | cancelled
type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalAnswered  ApprovalStatus = "answered"
	ApprovalExpired   ApprovalStatus = "expired"
	ApprovalCancelled ApprovalStatus = "cancelled"
)

func (s ApprovalStatus) Valid() bool {
	switch s {
	case ApprovalPending, ApprovalAnswered, ApprovalExpired, ApprovalCancelled:
		return true
	}
	return false
}

// Approval limits.
const (
	// ApprovalTTL is how long a question waits for its answer by default
	// (config SEAPORT_APPROVAL_TTL).
	ApprovalTTL = 24 * time.Hour
	// MaxApprovalText bounds the question.
	MaxApprovalText = 4000
	// MinApprovalOptions and MaxApprovalOptions bound the choices; each is
	// at most MaxApprovalOption characters.
	MinApprovalOptions = 2
	MaxApprovalOptions = 6
	MaxApprovalOption  = 64
	// MaxApprovalWait is the longest an ask may block waiting for the
	// answer: under the server's 5 minute request deadline (httpx.Deadline;
	// /mcp and /crew/approvals aren't exempt streams), which cuts a longer
	// wait anyway. Past it the agent asks again with approval(id, wait).
	MaxApprovalWait = 290 * time.Second
	// ApprovalPoll is how often a blocked ask looks for the answer.
	ApprovalPoll = 2 * time.Second
)

// ApprovalTrailer ends the card's text: it tells people how to answer from
// any channel.
const ApprovalTrailer = "Responde con el número o la opción."

// An approval that ended wakes its agent with a system message; one whose
// wake failed is retried by the sweeper once it is ApprovalWakeRetry old,
// until it is ApprovalWakeWindow old.
const (
	ApprovalWakeRetry  = 10 * time.Second
	ApprovalWakeWindow = 24 * time.Hour
)

// System senders: messages Seaport itself puts in a conversation. They
// never answer approvals and customs rules don't see people in them.
const (
	SystemSenderPrefix = "system:"
	SystemApproval     = "system:approval"
)

// Approval is a question an agent asked people (see AskApproval).
type Approval struct {
	ID           identity.ApprovalID     `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Conversation identity.ConversationID `db:"conversation_id" json:"conversation_id"`
	Agent        identity.AgentID        `db:"agent_id" json:"agent_id"`
	// Delivery is the one the agent was working on when it asked (nil when
	// it was none that Seaport knows of).
	Delivery *identity.DeliveryID `db:"delivery_id" json:"delivery_id,omitempty"`
	// Ship is the outbound ship that carries the card.
	Ship    identity.ShipID `db:"ship_id" json:"ship_id"`
	Text    string          `db:"text" json:"text"`
	Options []string        `db:"-" json:"options"`
	Status  ApprovalStatus  `db:"status" json:"status"`
	// Answer is one of Options, once answered.
	Answer *string `db:"answer" json:"answer,omitempty"`
	// AnsweredBy is the web user (subject) or the channel sender (its
	// handle, then its display name in parentheses).
	AnsweredBy string `db:"answered_by" json:"answered_by,omitempty"`
	// AnsweredShip is the inbound ship that answered from the channel.
	AnsweredShip *identity.ShipID `db:"answered_ship_id" json:"answered_ship_id,omitempty"`
	ExpiresAt    time.Time        `db:"expires_at" json:"expires_at"`
	CreatedAt    time.Time        `db:"created_at" json:"created_at"`
	AnsweredAt   *time.Time       `db:"answered_at" json:"answered_at,omitempty"`
	// EndedAt is when it stopped being pending (answered, cancelled or
	// expired); WokenAt when its agent was told, or needed no telling
	// (nil: not yet, the sweeper retries).
	EndedAt *time.Time `db:"ended_at" json:"-"`
	WokenAt *time.Time `db:"woken_at" json:"-"`
}

// Option resolves an option by its text (case-insensitive) to the stored
// spelling.
func (a Approval) Option(option string) (string, bool) {
	option = strings.TrimSpace(option)
	for _, o := range a.Options {
		if strings.EqualFold(o, option) {
			return o, true
		}
	}
	return "", false
}

// Match reads a person's reply in the conversation as one of the options
// (MatchOption).
func (a Approval) Match(reply string) (string, bool) { return MatchOption(a.Options, reply) }

// Card is the message people see.
func (a Approval) Card() string { return ApprovalCard(a.Text, a.Options) }

// ApprovalCard renders the question with its numbered options.
func ApprovalCard(question string, options []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\n")
	for i, o := range options {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(o)
		b.WriteString("\n")
	}
	b.WriteString(ApprovalTrailer)
	return b.String()
}

// replyPrefixes may precede an option's number ("opción 2").
var replyPrefixes = []string{"opción", "opcion", "option", "número", "numero", "number", "nº", "n°", "no.", "n."}

// MatchOption reads reply as one option: its text (case-insensitive, no
// accent folding), or its 1-based number ("2", "2.", "opción 2"). A
// reply that carries more than that ("2 pero espera") is no answer: for
// an irreversible action a miss is better than a wrong guess.
func MatchOption(options []string, reply string) (string, bool) {
	r := strings.TrimSpace(reply)
	// A mention first ("@bot 2"): groups only hear messages that call us.
	for strings.HasPrefix(r, "@") {
		_, rest, _ := strings.Cut(r, " ")
		r = strings.TrimSpace(rest)
	}
	if r == "" || utf8.RuneCountInString(r) > 2*MaxApprovalOption {
		return "", false
	}
	for _, o := range options {
		if strings.EqualFold(o, r) {
			return o, true
		}
	}
	t := strings.TrimFunc(r, func(c rune) bool {
		return unicode.IsSpace(c) || strings.ContainsRune(".!¡?¿,;:()#*\"'", c)
	})
	for _, o := range options {
		if strings.EqualFold(o, t) {
			return o, true
		}
	}
	lower := strings.ToLower(t)
	for _, p := range replyPrefixes {
		if rest, ok := strings.CutPrefix(lower, p); ok {
			lower = strings.TrimLeft(rest, " .:")
			break
		}
	}
	if lower == "" || len(lower) > 2 {
		return "", false
	}
	n, err := strconv.Atoi(lower)
	if err != nil || n < 1 || n > len(options) || strings.TrimLeft(lower, "0123456789") != "" {
		return "", false
	}
	return options[n-1], true
}

// ValidateOptions checks the choices of a question: 2 to 6, each short,
// unique (ignoring case) and not starting with a digit (replies "1".."6"
// choose by number).
func ValidateOptions(options []string) error {
	if len(options) < MinApprovalOptions || len(options) > MaxApprovalOptions {
		return errx.InvalidField("options", "options must be between "+strconv.Itoa(MinApprovalOptions)+" and "+strconv.Itoa(MaxApprovalOptions))
	}
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		if err := text.Required("options", o, MaxApprovalOption); err != nil {
			return err
		}
		o = strings.TrimSpace(o)
		if strings.ContainsAny(o, "\n\r") {
			return errx.InvalidField("options", "an option is one line")
		}
		if c, _ := utf8.DecodeRuneInString(o); unicode.IsDigit(c) {
			return errx.InvalidField("options", "an option can't start with a digit (people answer by number): "+strconv.Quote(o))
		}
		key := strings.ToLower(o)
		if seen[key] {
			return errx.InvalidField("options", "options must be different: "+strconv.Quote(o))
		}
		seen[key] = true
	}
	return nil
}

// AskApproval is an agent's question to the people of a conversation.
type AskApproval struct {
	Conversation identity.ConversationID
	// Agent asks (required: approvals are an agent's).
	Agent   identity.AgentID
	Text    string
	Options []string
	// Wait, when positive, makes the call block until the answer comes,
	// the question ends or Wait passes (cut short by the request's own
	// deadline). Zero returns at once.
	Wait time.Duration
}

func (a AskApproval) Validate() error {
	if a.Conversation.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	if a.Agent.IsZero() {
		return errx.InvalidField("agent_id", "approvals are asked by an agent (use its crew token)")
	}
	if err := text.Required("text", a.Text, MaxApprovalText); err != nil {
		return err
	}
	if a.Wait < 0 || a.Wait > MaxApprovalWait {
		return errx.InvalidField("wait_seconds", "wait_seconds must be between 0 and "+strconv.Itoa(int(MaxApprovalWait/time.Second)))
	}
	return ValidateOptions(a.Options)
}

// Answer is a person's choice from the web or the API.
type Answer struct {
	Option string
	// By names who answered (a web user).
	By string
}

// Answering is the approval an inbound message answers from the channel
// (Answerer): the message goes to Agent, whichever way it was routed, and
// the repository records the answer in the message's own transaction, so
// no delivery shows the message without it.
type Answering struct {
	Approval identity.ApprovalID
	Agent    identity.AgentID
	Option   string
	By       string
}

// Answerer decides, in CreateInbound's transaction, whether a person's
// message answers one of the conversation's pending approvals (oldest
// first, locked), given the dock's team and the agent routing picked
// (zero when nobody takes it).
type Answerer func(pending []Approval, team []agent.Member, routed identity.AgentID) (Answering, bool)

func (a Answer) Validate() error {
	if err := text.Required("option", a.Option, MaxApprovalOption); err != nil {
		return err
	}
	return text.Required("by", a.By, MaxSenderID)
}

// ApprovalFilter narrows a list of approvals.
type ApprovalFilter struct {
	// Status keeps one status (empty: every status).
	Status ApprovalStatus
}

func (f ApprovalFilter) Validate() error {
	if f.Status != "" && !f.Status.Valid() {
		return errx.InvalidField("status", "status must be pending, answered, expired or cancelled")
	}
	return nil
}
