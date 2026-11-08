package conversation

import (
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Customs decides which chats and messages may wake the crew. Each dock
// (a WhatsApp number...) has a Policy and allow/deny Rules matched against
// handles: the normalized identities a channel adapter reports for a chat
// and its senders ("+51999123436", "2314…@lid", "1203…@g.us", "@user",
// "ana@example.com"). The engine knows nothing about channels.

// DefaultAccess is what a dock does with chats no rule or override covers.
type DefaultAccess string

const (
	// DefaultAsk holds new chats until the owner allows them.
	DefaultAsk   DefaultAccess = "ask"
	DefaultAllow DefaultAccess = "allow"
	DefaultDeny  DefaultAccess = "deny"
)

func (d DefaultAccess) Valid() bool { return d == DefaultAsk || d == DefaultAllow || d == DefaultDeny }

// GroupMode says which messages of an allowed group wake the crew.
type GroupMode string

const (
	// GroupMentions: only messages that mention the account or reply to it.
	GroupMentions GroupMode = "mentions"
	// GroupAll: every message.
	GroupAll GroupMode = "all"
	// GroupInherit (conversation override only): use the dock's mode.
	GroupInherit GroupMode = ""
)

func (m GroupMode) Valid() bool { return m == GroupMentions || m == GroupAll }

// Access is the owner's manual decision on one conversation.
type Access string

const (
	// AccessAuto follows the dock's rules and default.
	AccessAuto  Access = "auto"
	AccessAllow Access = "allow"
	AccessDeny  Access = "deny"
)

func (a Access) Valid() bool { return a == AccessAuto || a == AccessAllow || a == AccessDeny }

// Status is the customs outcome of a conversation (stored, recomputed when
// a message arrives and whenever rules, the policy or the override change).
type Status string

const (
	StatusAllowed Status = "allowed"
	// StatusPending: held until the owner decides (dock default ask).
	StatusPending Status = "pending"
	StatusDenied  Status = "denied"
)

func (s Status) Valid() bool { return s == StatusAllowed || s == StatusPending || s == StatusDenied }

// Effect of a rule.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

func (e Effect) Valid() bool { return e == EffectAllow || e == EffectDeny }

// Policy is a dock's customs configuration.
type Policy struct {
	Dock      identity.DockID `db:"dock_id" json:"dock_id"`
	Default   DefaultAccess   `db:"default_access" json:"default"`
	GroupMode GroupMode       `db:"group_mode" json:"group_mode"`
}

// DefaultPolicy applies to docks never configured: ask, groups by mention.
func DefaultPolicy(d identity.DockID) Policy {
	return Policy{Dock: d, Default: DefaultAsk, GroupMode: GroupMentions}
}

// Rule allows or denies the chats and senders whose handles match Pattern.
type Rule struct {
	ID        identity.RuleID `db:"id" json:"id"`
	Dock      identity.DockID `db:"dock_id" json:"dock_id"`
	Effect    Effect          `db:"effect" json:"effect"`
	Pattern   string          `db:"pattern" json:"pattern"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// Customs is a dock's whole configuration.
type Customs struct {
	Policy Policy `json:"policy"`
	Rules  []Rule `json:"rules"`
}

// MaxPattern bounds a rule pattern; MaxRules the rules of one dock.
const (
	MaxPattern = 256
	MaxRules   = 1000
)

// SetPolicy changes a dock's policy; nil fields keep their value.
type SetPolicy struct {
	Default   *DefaultAccess
	GroupMode *GroupMode
}

func (p SetPolicy) Validate() error {
	if p.Default == nil && p.GroupMode == nil {
		return errx.Validation("nothing to update")
	}
	if p.Default != nil && !p.Default.Valid() {
		return errx.InvalidField("default", "default must be ask, allow or deny")
	}
	if p.GroupMode != nil && !p.GroupMode.Valid() {
		return errx.InvalidField("group_mode", "group_mode must be mentions or all")
	}
	return nil
}

// AddRule adds an allow/deny rule to a dock.
type AddRule struct {
	Dock    identity.DockID
	Effect  Effect
	Pattern string
}

func (r AddRule) Validate() error {
	if r.Dock.IsZero() {
		return errx.InvalidField("dock_id", "dock_id is required")
	}
	if !r.Effect.Valid() {
		return errx.InvalidField("effect", "effect must be allow or deny")
	}
	if err := text.Required("pattern", r.Pattern, MaxPattern); err != nil {
		return err
	}
	if NormalizePattern(r.Pattern) == "*" {
		return errx.InvalidField("pattern", `use the dock default instead of a "*" rule`)
	}
	return nil
}

// NormalizePattern lowercases a pattern and writes phone numbers the way
// adapters report them: "51 999-123-436" and "51999*" become "+51999123436"
// and "+51999*".
func NormalizePattern(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	digits := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(p)
	if isPhonePattern(digits) {
		if !strings.HasPrefix(digits, "+") && !strings.HasPrefix(digits, "*") {
			digits = "+" + digits
		}
		return digits
	}
	return p
}

func isPhonePattern(s string) bool {
	s = strings.TrimPrefix(s, "+")
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '*':
		default:
			return false
		}
	}
	return hasDigit
}

// Match reports whether a normalized pattern matches a handle: "*" stands
// for any run of characters, everything else is literal, case-insensitive.
func Match(pattern, handle string) bool {
	handle = strings.ToLower(handle)
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == handle
	}
	if !strings.HasPrefix(handle, parts[0]) {
		return false
	}
	handle = handle[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(handle, mid)
		if i < 0 {
			return false
		}
		handle = handle[i+len(mid):]
	}
	return strings.HasSuffix(handle, last)
}

func matches(rules []Rule, effect Effect, handles []string) bool {
	for _, r := range rules {
		if r.Effect != effect {
			continue
		}
		for _, h := range handles {
			if Match(r.Pattern, h) {
				return true
			}
		}
	}
	return false
}

// Decide is the chat-level customs decision for a conversation whose chat
// is known by handles. Order: the owner's override, the owner's own chat,
// deny rules, allow rules, the dock default.
func (c Customs) Decide(kind Kind, access Access, handles []string) Status {
	switch {
	case access == AccessAllow:
		return StatusAllowed
	case access == AccessDeny:
		return StatusDenied
	case kind == KindSelf:
		return StatusAllowed
	case matches(c.Rules, EffectDeny, handles):
		return StatusDenied
	case matches(c.Rules, EffectAllow, handles):
		return StatusAllowed
	}
	switch c.Policy.Default {
	case DefaultAllow:
		return StatusAllowed
	case DefaultDeny:
		return StatusDenied
	}
	return StatusPending
}

// Hold reasons of a message in an allowed conversation.
const (
	HoldNone         = ""
	HoldSenderDenied = "sender denied by a rule"
	HoldNotMentioned = "group message without a mention"
)

// Admit is the message-level decision in an allowed conversation: a sender
// denied by a rule is held, and so is a group message that doesn't address
// the account when the group (or its dock) is in mentions mode. It returns
// the hold reason, HoldNone to deliver.
func (c Customs) Admit(conv Conversation, senders []string, addressed bool) string {
	if conv.Kind == KindSelf {
		return HoldNone
	}
	if conv.Access != AccessAllow && matches(c.Rules, EffectDeny, senders) {
		return HoldSenderDenied
	}
	if conv.Kind == KindGroup && !addressed && c.GroupModeOf(conv) == GroupMentions {
		return HoldNotMentioned
	}
	return HoldNone
}

// GroupModeOf is the conversation's override or the dock's mode.
func (c Customs) GroupModeOf(conv Conversation) GroupMode {
	if conv.GroupMode != GroupInherit {
		return conv.GroupMode
	}
	if c.Policy.GroupMode == GroupInherit {
		return GroupMentions
	}
	return c.Policy.GroupMode
}

// MaxHandles bounds the handles one arrival reports (chat or sender).
const MaxHandles = 16

func validateHandles(field string, handles []string) error {
	if len(handles) > MaxHandles {
		return errx.InvalidField(field, field+" has too many entries")
	}
	for _, h := range handles {
		if err := text.Required(field, h, MaxExternalID); err != nil {
			return err
		}
	}
	return nil
}

// CleanHandles lowercases, trims and dedupes handles.
func CleanHandles(handles []string) []string {
	out := make([]string, 0, len(handles))
	seen := map[string]bool{}
	for _, h := range handles {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
