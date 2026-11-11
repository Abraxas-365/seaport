package dockjira

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// validSignature checks Jira's X-Hub-Signature (or X-Hub-Signature-256):
// "sha256=" and the hex HMAC-SHA256 of the body with the webhook secret.
func validSignature(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// validBearer checks "Authorization: Bearer <secret>", the proof of Jira
// Automation's "Send web request" (it can't compute an HMAC), in constant
// time.
func validBearer(secret, header string) bool {
	got, ok := strings.CutPrefix(strings.TrimSpace(header), "Bearer ")
	got = strings.TrimSpace(got)
	if !ok || got == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// user is a Jira account in a payload.
type user struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	// AccountType: atlassian (people) | app (bots, Automation) | customer
	// (portal customers).
	AccountType string `json:"accountType"`
}

// event is a webhook payload (the fields Seaport reads). Admin webhooks
// carry text as wiki markup (a string) or ADF (an object): raw until read.
type event struct {
	WebhookEvent string `json:"webhookEvent"`
	Comment      *struct {
		ID      string          `json:"id"`
		Body    json.RawMessage `json:"body"`
		Author  user            `json:"author"`
		Created string          `json:"created"`
	} `json:"comment"`
	Issue *struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
			Creator     user            `json:"creator"`
			Reporter    user            `json:"reporter"`
			Created     string          `json:"created"`
		} `json:"fields"`
	} `json:"issue"`
	User user `json:"user"`
}

// arrival is one webhook turned into a message.
type arrival struct {
	in ship.Inbound
	// commentID is the Jira comment of an own message, whose stamp (the
	// Seaport message id) tells whether Seaport sent it ("" otherwise).
	commentID string
}

// parse turns a webhook into an arrival, or ok=false for events that
// carry no message for the crew: other kinds, bots (Automation, apps),
// empty texts and messages that call neither the account nor an agent of
// the team. The account's own comments are arrivals with in.Own: recorded
// as sent, waking nobody, so a reply never loops.
func parse(raw []byte, d identity.DockID, site Site, team []agent.Member) (arrival, bool) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil || e.Issue == nil || !dock.ValidIssueKey(e.Issue.Key) {
		return arrival{}, false
	}
	r := &reader{botID: site.AccountID, botName: site.DisplayName}
	var author user
	var id, body, created, commentID string
	switch {
	case e.Comment != nil && (e.WebhookEvent == "comment_created" || e.WebhookEvent == ""):
		author = e.Comment.Author
		if author.AccountID == "" {
			author = e.User
		}
		id, commentID, created = "comment-"+e.Comment.ID, e.Comment.ID, e.Comment.Created
		if e.Comment.ID == "" {
			return arrival{}, false
		}
		body = r.markdown(e.Comment.Body)
	case e.Comment == nil && e.WebhookEvent == "jira:issue_created":
		f := e.Issue.Fields
		author = f.Reporter
		if author.AccountID == "" {
			author = f.Creator
		}
		if author.AccountID == "" {
			author = e.User
		}
		id, created = "issue-"+e.Issue.ID, f.Created
		body = strings.TrimSpace(f.Summary)
		if desc := r.markdown(f.Description); desc != "" {
			body += "\n\n" + desc
		}
	default:
		return arrival{}, false
	}
	own := site.AccountID != "" && author.AccountID == site.AccountID
	if own && commentID == "" {
		return arrival{}, false // issues the account created are not messages
	}
	if !own && author.AccountType == "app" {
		return arrival{}, false // other bots: no loops
	}
	if strings.TrimSpace(body) == "" {
		return arrival{}, false
	}
	if !own && !r.mentioned && !calls(body, site.DisplayName, team) {
		return arrival{}, false
	}
	key := e.Issue.Key
	project, _, _ := strings.Cut(key, "-")
	title := key
	if s := strings.TrimSpace(e.Issue.Fields.Summary); s != "" {
		title = clip(key+": "+s, text.MaxName)
	}
	senderID := author.AccountID
	if senderID == "" {
		senderID = "unknown"
	}
	name := author.DisplayName
	if name == "" {
		name = senderID
	}
	senders := []string{senderID}
	if email := strings.ToLower(strings.TrimSpace(author.EmailAddress)); email != "" {
		senders = append(senders, email)
	}
	if t := strings.ToLower(author.AccountType); t != "" {
		senders = append(senders, "jira:"+t)
	}
	if own {
		senders = []string{senderID}
	}
	in := ship.Inbound{
		Dock: d, Channel: dock.ChannelJira,
		ChatID: key, ChatKind: conversation.KindGroup, ChatTitle: title,
		ExternalID: id, SenderID: senderID, SenderName: clip(name, text.MaxName),
		Text: clip(body, text.MaxMessage), SentAt: sentAt(created),
		ChatHandles:   []string{key, project},
		SenderHandles: senders,
		Addressed:     true,
		Own:           own,
	}
	a := arrival{in: in}
	if own {
		a.commentID = commentID
	}
	return a, true
}

// sentAt reads Jira's timestamp ("2024-05-01T10:30:00.000+0000"); now
// when it has none.
func sentAt(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339Nano, "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Now()
}

// calls: the text mentions an agent of the dock's team, or the account by
// name typed as plain text (@Seaport; a mention picked in Jira is read
// from the markup).
func calls(body, displayName string, team []agent.Member) bool {
	if _, ok := agent.Mentioned(team, body); ok {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(displayName))
	if name == "" || strings.ContainsAny(name, " \t") {
		return false
	}
	return mentions(strings.ToLower(body), name)
}

// mentions: "@name" appears in lower, not inside a longer handle or an
// address (user@name).
func mentions(lower, name string) bool {
	at := "@" + name
	for i := strings.Index(lower, at); i >= 0; {
		end := i + len(at)
		if (i == 0 || !handleChar(lower[i-1])) && (end == len(lower) || !handleChar(lower[end])) {
			return true
		}
		next := strings.Index(lower[i+1:], at)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

func handleChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// clip cuts s to at most max runes.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
