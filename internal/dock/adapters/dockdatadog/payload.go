package dockdatadog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Payload is the body Datadog's webhook integration sends when its payload
// is the one the guide gives (Datadog's template variables, one per
// field). Unknown fields are ignored, numbers may come quoted or bare.
//
//	{"id": "$ID", "alert_id": "$ALERT_ID", "transition": "$ALERT_TRANSITION",
//	 "title": "$ALERT_TITLE", "status": "$ALERT_STATUS", "scope": "$ALERT_SCOPE",
//	 "tags": "$TAGS", "message": "$EVENT_MSG", "link": "$LINK",
//	 "priority": "$PRIORITY", "date": "$DATE", "org": "$ORG_NAME"}
type Payload struct {
	// ID is the event's id (ship id event-<id>; a retry is a duplicate).
	ID flexString `json:"id"`
	// AlertID is the monitor's id: with Scope it names the conversation.
	AlertID    flexString `json:"alert_id"`
	Transition string     `json:"transition"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	Scope      string     `json:"scope"`
	// Tags are comma separated (Datadog's $TAGS).
	Tags     string `json:"tags"`
	Message  string `json:"message"`
	Link     string `json:"link"`
	Priority string `json:"priority"`
	Date     string `json:"date"`
	Org      string `json:"org"`
}

// flexString reads a JSON string or number as a string.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(string(b))
	return nil
}

// MaxBody bounds a Datadog body: the default JSON limit, so the route
// needs no body tier of its own.
const MaxBody = 1 << 20

// MaxTags bounds the tags kept as handles (with the monitor and scope,
// within conversation.MaxHandles).
const MaxTags = 12

// maxText bounds the rendered message (Datadog messages are short; a
// runaway one is cut, never refused).
const maxText = 16 * 1024

// Parse reads a body. An unreadable body or one without a monitor is
// refused.
func Parse(body []byte) (Payload, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return Payload{}, errx.UnsupportedMediaType("the body must be a JSON object (the payload of Datadog's webhook integration)")
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Payload{}, errx.UnsupportedMediaType("the body must be JSON")
	}
	p.AlertID = flexString(strings.TrimSpace(string(p.AlertID)))
	p.ID = flexString(strings.TrimSpace(string(p.ID)))
	if p.AlertID == "" || strings.HasPrefix(string(p.AlertID), "$") {
		return Payload{}, errx.InvalidField("alert_id", "alert_id must be the monitor's id ($ALERT_ID)")
	}
	if err := text.Required("alert_id", string(p.AlertID), 64); err != nil {
		return Payload{}, err
	}
	return p, nil
}

// Kind is what happened to the monitor.
type Kind string

const (
	KindTriggered Kind = "Triggered"
	KindRecovered Kind = "Recovered"
	KindRenotify  Kind = "Re-notify"
	KindWarn      Kind = "Warn"
	KindNoData    Kind = "No data"
	KindOther     Kind = "Alert"
)

// KindOf reads Datadog's $ALERT_TRANSITION.
func KindOf(transition string) Kind {
	switch strings.ToLower(strings.TrimSpace(transition)) {
	case "triggered":
		return KindTriggered
	case "recovered":
		return KindRecovered
	case "re-notify", "renotify", "re_notify":
		return KindRenotify
	case "warn":
		return KindWarn
	case "no data":
		return KindNoData
	}
	return KindOther
}

// Message is an alert as the message the ship domain takes.
type Message struct {
	ChatID     string
	ChatTitle  string
	ExternalID string
	Text       string
	Handles    []string
	Kind       Kind
	Monitor    string
	Scope      string
}

// Render turns a payload into the message of its conversation.
func Render(p Payload, now time.Time) Message {
	monitor := string(p.AlertID)
	scope := strings.TrimSpace(p.Scope)
	if scope == "" || strings.HasPrefix(scope, "$") {
		scope = "*"
	}
	kind := KindOf(p.Transition)
	title := strings.TrimSpace(p.Title)
	if title == "" || strings.HasPrefix(title, "$") {
		title = "Monitor " + monitor
	}
	tags := splitTags(p.Tags)

	m := Message{
		ChatID:  "monitor/" + monitor + "/" + scope,
		Kind:    kind,
		Monitor: monitor,
		Scope:   scope,
	}
	m.ChatTitle = truncate(title, text.MaxName)
	if scope != "*" {
		m.ChatTitle = truncate(title+" · "+scope, text.MaxName)
	}
	id := string(p.ID)
	if id == "" || strings.HasPrefix(id, "$") {
		// No id: the body's own fields and the UTC minute (a retry within
		// it is a duplicate, a re-notify later is not).
		id = fmt.Sprintf("%s-%s-%s-%s", monitor, scope, strings.ToLower(string(kind)), now.UTC().Format("200601021504"))
	}
	m.ExternalID = truncate("event-"+id, conversation.MaxExternalID)

	handles := []string{m.ChatID, "monitor:" + monitor}
	if scope != "*" {
		handles = append(handles, "scope:"+scope)
	}
	for i, t := range tags {
		if i >= MaxTags {
			break
		}
		handles = append(handles, "tag:"+t)
	}
	m.Handles = conversation.CleanHandles(handles)

	var b strings.Builder
	fmt.Fprintf(&b, "**%s** · %s", kind, title)
	if s := strings.TrimSpace(p.Status); s != "" && !strings.HasPrefix(s, "$") && !strings.EqualFold(s, title) {
		b.WriteString("\n" + s)
	}
	if scope != "*" {
		b.WriteString("\nScope: " + scope)
	}
	if pr := strings.TrimSpace(p.Priority); pr != "" && !strings.HasPrefix(pr, "$") {
		b.WriteString("\nPriority: " + pr)
	}
	if len(tags) > 0 {
		b.WriteString("\nTags: " + strings.Join(tags, ", "))
	}
	if msg := strings.TrimSpace(p.Message); msg != "" && !strings.HasPrefix(msg, "$") {
		b.WriteString("\n\n" + msg)
	}
	if link := strings.TrimSpace(p.Link); strings.HasPrefix(link, "https://") {
		b.WriteString("\n\n" + link)
	}
	m.Text = truncate(b.String(), maxText)
	return m
}

// splitTags reads Datadog's comma-separated $TAGS, trimmed and deduped,
// in order.
func splitTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "$") {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] || utf8.RuneCountInString(t) > 200 {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// String names the kind for logs.
func (k Kind) String() string { return string(k) }

// shortID names a dock in a rule: the first group of its id.
func shortID(id fmt.Stringer) string {
	s := id.String()
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	return s
}
