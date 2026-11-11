package dockslack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
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

// SignatureWindow is how far a request's timestamp may be from now (Slack
// recommends five minutes: older ones are replays).
const SignatureWindow = 5 * time.Minute

// validSignature checks X-Slack-Signature: "v0=" + hex HMAC-SHA256 of
// "v0:<timestamp>:<body>" with the app's signing secret, and a timestamp
// (X-Slack-Request-Timestamp) within SignatureWindow of now.
func validSignature(secret, body []byte, timestamp, header string, now time.Time) bool {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(sec, 0))
	if age > SignatureWindow || age < -SignatureWindow {
		return false
	}
	got, ok := strings.CutPrefix(header, "v0=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// envelope is what the Events API posts.
type envelope struct {
	Type      string `json:"type"` // url_verification | event_callback
	Challenge string `json:"challenge"`
	TeamID    string `json:"team_id"`
	AppID     string `json:"api_app_id"`
	Event     event  `json:"event"`
}

// event is an event's payload (the fields Seaport reads).
type event struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"` // channel | group | im | mpim
	Hidden      bool   `json:"hidden"`
	Files       []file `json:"files"`
}

type file struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Mimetype string `json:"mimetype"`
	URL      string `json:"url_private"`
	Mode     string `json:"mode"` // tombstone: deleted
}

// directory names the people and channels of a workspace.
type directory interface {
	User(id string) userInfo
	Channel(id string) string
}

// arrival is one event turned into a message, plus how to mark it seen.
type arrival struct {
	in ship.Inbound
	// channel and ts are the message (the 👀 goes there).
	channel, ts string
	// thread is the thread root Seaport now answers in ("": none, a DM).
	thread string
}

// allowed subtypes of a message: a plain one, one with files, a thread
// reply also sent to the channel. Edits, deletions, joins... carry no
// new message for the crew.
func plain(subtype string) bool {
	return subtype == "" || subtype == "file_share" || subtype == "thread_broadcast"
}

// Lookups is what parse asks of the world besides the payload.
type lookups struct {
	dir directory
	// known: Seaport answers in this thread.
	known func(channel, ts string) (bool, error)
	// fetch downloads a private file (with the bot token).
	fetch func(url string) func(context.Context) ([]byte, error)
}

// parse turns an event into an arrival, or ok=false for what carries no
// message for the crew: other events, bots' messages (our own bot's
// included, so a reply never loops), empty texts, and messages that call
// neither the app nor an agent of the dock's team outside a thread
// Seaport answers in. DMs are always for the crew.
func parse(env envelope, d identity.DockID, a App, team []agent.Member, lk lookups) (arrival, bool, error) {
	ev := env.Event
	if env.Type != "event_callback" || ev.Channel == "" || ev.TS == "" || ev.Hidden {
		return arrival{}, false, nil
	}
	mention := ev.Type == "app_mention"
	if ev.Type != "message" && !mention {
		return arrival{}, false, nil
	}
	if !plain(ev.Subtype) || ev.BotID != "" || ev.User == "" || ev.User == a.BotUserID {
		return arrival{}, false, nil
	}
	if ev.Type == "message" && ev.ChannelType == "" {
		return arrival{}, false, nil
	}
	direct := ev.ChannelType == "im"
	raw := ev.Text
	botMentioned := false
	if a.BotUserID != "" {
		own := ownMention(a.BotUserID)
		if own.MatchString(raw) {
			botMentioned = true
			raw = strings.TrimSpace(own.ReplaceAllString(raw, ""))
		}
	}
	_, agentCalled := agent.Mentioned(team, angle.ReplaceAllString(raw, " "))
	called := botMentioned || agentCalled

	root := ev.ThreadTS
	if root == "" {
		root = ev.TS
	}
	followUp := false
	if !direct && !mention && !called && ev.ThreadTS != "" {
		known, err := lk.known(ev.Channel, ev.ThreadTS)
		if err != nil {
			return arrival{}, false, err
		}
		followUp = known
	}
	if !direct && !mention && !called && !followUp {
		return arrival{}, false, nil
	}

	body := ToMarkdown(raw, func(kind byte, id, label string) string {
		if kind == 'U' {
			if label != "" {
				return "@" + label
			}
			return "@" + lk.dir.User(id).Name
		}
		if label != "" {
			return "#" + label
		}
		return lk.dir.Channel(id)
	})
	var media []ship.Media
	for _, f := range ev.Files {
		if f.Mode == "tombstone" || f.URL == "" {
			continue
		}
		name := firstOf(f.Name, f.Title, "file")
		if strings.HasPrefix(f.Mimetype, "image/") && len(media) < ship.MaxAttachments {
			media = append(media, ship.Media{Kind: ship.AttachmentImage, Name: name, Fetch: lk.fetch(f.URL)})
			continue
		}
		if body != "" {
			body += "\n"
		}
		body += "[file: " + name + "]"
	}
	if strings.TrimSpace(body) == "" && len(media) == 0 {
		if !botMentioned {
			return arrival{}, false, nil
		}
		body = "(mentioned you)"
	}

	sender := lk.dir.User(ev.User)
	senders := []string{ev.User}
	if sender.Email != "" {
		senders = append(senders, sender.Email)
	}
	in := ship.Inbound{
		Dock: d, Channel: dock.ChannelSlack,
		ExternalID: "msg-" + ev.Channel + "-" + ev.TS,
		SenderID:   ev.User, SenderName: sender.Name,
		Text: clip(body, text.MaxMessage), SentAt: sentAt(ev.TS),
		SenderHandles: senders, Addressed: true, Media: media,
	}
	out := arrival{channel: ev.Channel, ts: ev.TS}
	if direct {
		in.ChatID, in.ChatKind = ev.Channel, conversation.KindDirect
		in.ChatTitle = clip(sender.Name, text.MaxName)
		in.ChatHandles = []string{ev.Channel}
	} else {
		name := lk.dir.Channel(ev.Channel)
		in.ChatID, in.ChatKind = ev.Channel+"/"+root, conversation.KindGroup
		in.ChatTitle = clip(name, text.MaxName)
		in.ChatHandles = []string{ev.Channel}
		if strings.HasPrefix(name, "#") && name != "#"+ev.Channel {
			in.ChatHandles = append(in.ChatHandles, name)
		}
		out.thread = root
	}
	out.in = in
	return out, true, nil
}

// ownMention matches the bot's own mention (and the punctuation after it).
func ownMention(botUser string) *regexp.Regexp {
	return regexp.MustCompile(`<@` + regexp.QuoteMeta(botUser) + `(\|[^>]*)?>[ \t]*[,:]?[ \t]*`)
}

// sentAt reads a Slack timestamp ("1700000000.000200").
func sentAt(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Now()
	}
	var ns int64
	if frac != "" {
		if len(frac) > 9 {
			frac = frac[:9]
		}
		frac += strings.Repeat("0", 9-len(frac))
		ns, _ = strconv.ParseInt(frac, 10, 64)
	}
	return time.Unix(s, ns)
}

// clip cuts s to at most max runes.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// splitChat parses a conversation id: "<channel>/<thread ts>" (a thread)
// or "<channel>" (a DM).
func splitChat(chat string) (channel, thread string, ok bool) {
	channel, thread, _ = strings.Cut(chat, "/")
	if !validID(channel) || (thread != "" && !validTS(thread)) {
		return "", "", false
	}
	return channel, thread, true
}

// validID: a Slack channel id (C…, G…, D…).
func validID(s string) bool {
	if len(s) < 2 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// validTS: a message timestamp, "1700000000.000200".
func validTS(s string) bool {
	sec, frac, ok := strings.Cut(s, ".")
	if !ok || sec == "" || frac == "" || len(s) > 32 {
		return false
	}
	for _, p := range []string{sec, frac} {
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	return true
}
