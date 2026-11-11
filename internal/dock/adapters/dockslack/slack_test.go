package dockslack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func sign(secret, body, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSignatureAndWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ts := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	body := `{"type":"event_callback"}`
	secret := "signing-secret-test"
	for name, c := range map[string]struct {
		ts, sig string
		want    bool
	}{
		"good":         {ts(0), sign(secret, body, ts(0)), true},
		"edge":         {ts(-4 * time.Minute), sign(secret, body, ts(-4*time.Minute)), true},
		"old":          {ts(-6 * time.Minute), sign(secret, body, ts(-6*time.Minute)), false},
		"future":       {ts(6 * time.Minute), sign(secret, body, ts(6*time.Minute)), false},
		"wrong secret": {ts(0), sign("other", body, ts(0)), false},
		"other body":   {ts(0), sign(secret, body+" ", ts(0)), false},
		"no prefix":    {ts(0), strings.TrimPrefix(sign(secret, body, ts(0)), "v0="), false},
		"not hex":      {ts(0), "v0=zz", false},
		"no timestamp": {"", sign(secret, body, ""), false},
	} {
		if got := validSignature([]byte(secret), []byte(body), c.ts, c.sig, now); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func names(kind byte, id, label string) string {
	if kind == 'C' {
		if label != "" {
			return "#" + label
		}
		return "#" + id
	}
	if label != "" {
		return "@" + label
	}
	return "@name-of-" + id
}

func TestToMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                          "plain text",
		"see <https://example.com|the doc>":   "see [the doc](https://example.com)",
		"<https://example.com/a?b=1&amp;c=2>": "https://example.com/a?b=1&c=2",
		"hi <@U123>":                          "hi @name-of-U123",
		"hi <@U123|ana>":                      "hi @ana",
		"in <#C1|general>":                    "in #general",
		"*bold* and ~gone~":                   "**bold** and ~~gone~~",
		"*a* *b*":                             "**a** **b**",
		"2 * 3 * 4":                           "2 * 3 * 4",
		"a &lt;b&gt; &amp; c":                 "a <b> & c",
		"`*not bold* <@U1>`":                  "`*not bold* <@U1>`",
		"```\n*x* &lt;\n```":                  "```\n*x* <\n```",
		"• one\n• two":                        "- one\n- two",
		"<!here> look":                        "@here look",
		"<mailto:a@example.com|mail me>":      "[mail me](mailto:a@example.com)",
		"<https://example.com/a_(b)|x>":       "[x](https://example.com/a_%28b%29)",
	} {
		if got := ToMarkdown(in, names); got != want {
			t.Errorf("ToMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplit(t *testing.T) {
	if got := Split("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short = %q", got)
	}
	long := strings.Repeat("word ", 5000) // 25 000 runes
	parts := Split(long, MaxMarkdown)
	if len(parts) < 3 {
		t.Fatalf("%d parts", len(parts))
	}
	var joined []string
	for _, p := range parts {
		if n := utf8.RuneCountInString(p); n > MaxMarkdown {
			t.Fatalf("part of %d runes", n)
		}
		joined = append(joined, strings.Fields(p)...)
	}
	if len(joined) != 5000 {
		t.Fatalf("lost words: %d", len(joined))
	}
	// A fence a cut falls inside is closed and reopened.
	code := "```\n" + strings.Repeat("line of code\n", 40) + "```"
	for _, p := range Split(code, 200) {
		if strings.Count(p, "```")%2 != 0 {
			t.Fatalf("unbalanced fence in %q", p)
		}
	}
	// Multibyte text is cut by runes, never inside one.
	for _, p := range Split(strings.Repeat("ñandú ", 100), 50) {
		if !utf8.ValidString(p) || utf8.RuneCountInString(p) > 50 {
			t.Fatalf("bad part %q", p)
		}
	}
}

type fakeDir struct{}

func (fakeDir) User(id string) userInfo {
	return userInfo{Name: "Ana " + id, Email: "ana@example.com"}
}
func (fakeDir) Channel(id string) string { return "#general" }

func lookup(known ...string) lookups {
	return lookups{
		dir: fakeDir{},
		known: func(channel, ts string) (bool, error) {
			for _, k := range known {
				if k == channel+"/"+ts {
					return true, nil
				}
			}
			return false, nil
		},
		fetch: func(string) func(context.Context) ([]byte, error) { return nil },
	}
}

func TestRouting(t *testing.T) {
	d := identity.NewDockID()
	app := App{Dock: d, BotUserID: "U0BOT", BotID: "B0BOT"}
	team := []agent.Member{{Name: "reviewer"}}
	ev := func(e event) envelope {
		if e.User == "" {
			e.User = "U0ANA"
		}
		if e.TS == "" {
			e.TS = "1700000000.000100"
		}
		if e.Channel == "" {
			e.Channel = "C0GEN"
		}
		if e.Type == "" {
			e.Type = "message"
		}
		if e.ChannelType == "" {
			e.ChannelType = "channel"
		}
		return envelope{Type: "event_callback", Event: e}
	}
	type want struct {
		ok     bool
		chat   string
		thread string
		text   string
	}
	for name, c := range map[string]struct {
		env   envelope
		known []string
		want  want
	}{
		"app_mention": {ev(event{Type: "app_mention", Text: "<@U0BOT> please review"}), nil,
			want{true, "C0GEN/1700000000.000100", "1700000000.000100", "please review"}},
		"mention inside a message event": {ev(event{Text: "hey <@U0BOT>, look at this"}), nil,
			want{true, "C0GEN/1700000000.000100", "1700000000.000100", "hey look at this"}},
		"agent handle": {ev(event{Text: "@reviewer look"}), nil, want{true, "C0GEN/1700000000.000100", "1700000000.000100", "@reviewer look"}},
		"mention in a thread joins the thread": {ev(event{Type: "app_mention", Text: "<@U0BOT> hi", ThreadTS: "1699999999.000001"}), nil,
			want{true, "C0GEN/1699999999.000001", "1699999999.000001", "hi"}},
		"follow-up in a known thread": {ev(event{Text: "and this?", ThreadTS: "1699999999.000001"}), []string{"C0GEN/1699999999.000001"},
			want{true, "C0GEN/1699999999.000001", "1699999999.000001", "and this?"}},
		"unknown thread":         {ev(event{Text: "and this?", ThreadTS: "1699999999.000001"}), nil, want{}},
		"unrelated message":      {ev(event{Text: "lunch?"}), nil, want{}},
		"same ts, other channel": {ev(event{Text: "x", Channel: "C0OTHER", ThreadTS: "1699999999.000001"}), []string{"C0GEN/1699999999.000001"}, want{}},
		"dm":                     {ev(event{Text: "hello", Channel: "D0DM", ChannelType: "im"}), nil, want{true, "D0DM", "", "hello"}},
		"own bot message":        {ev(event{Text: "<@U0BOT> loop", BotID: "B0BOT", User: "U0BOT"}), nil, want{}},
		"own user":               {ev(event{Type: "app_mention", Text: "<@U0BOT> loop", User: "U0BOT"}), nil, want{}},
		"other bot":              {ev(event{Text: "@reviewer", BotID: "B0OTHER"}), nil, want{}},
		"edited":                 {ev(event{Text: "@reviewer", Subtype: "message_changed"}), nil, want{}},
		"joined":                 {ev(event{Text: "<@U1> joined", Subtype: "channel_join"}), nil, want{}},
		"empty mention":          {ev(event{Type: "app_mention", Text: "<@U0BOT>"}), nil, want{true, "C0GEN/1700000000.000100", "1700000000.000100", "(mentioned you)"}},
		"other event":            {ev(event{Type: "reaction_added", Text: "x"}), nil, want{}},
		"mrkdwn":                 {ev(event{Type: "app_mention", Text: "<@U0BOT> see <https://example.com|docs> *now*"}), nil, want{true, "C0GEN/1700000000.000100", "1700000000.000100", "see [docs](https://example.com) **now**"}},
	} {
		at, ok, err := parse(c.env, d, app, team, lookup(c.known...))
		if err != nil || ok != c.want.ok {
			t.Errorf("%s: ok=%v err=%v", name, ok, err)
			continue
		}
		if !ok {
			continue
		}
		if at.in.ChatID != c.want.chat || at.thread != c.want.thread || at.in.Text != c.want.text || !at.in.Addressed {
			t.Errorf("%s: chat=%q thread=%q text=%q addressed=%v", name, at.in.ChatID, at.thread, at.in.Text, at.in.Addressed)
		}
	}

	// A DM is direct, titled by the person; a channel thread is a group
	// titled by the channel; ids and handles follow the plan.
	at, _, _ := parse(ev(event{Text: "hi", Channel: "D0DM", ChannelType: "im"}), d, app, team, lookup())
	if at.in.ChatKind != conversation.KindDirect || at.in.ChatTitle != "Ana U0ANA" || at.in.ExternalID != "msg-D0DM-1700000000.000100" {
		t.Errorf("dm: %+v", at.in)
	}
	if strings.Join(at.in.SenderHandles, ",") != "U0ANA,ana@example.com" {
		t.Errorf("sender handles = %v", at.in.SenderHandles)
	}
	at, _, _ = parse(ev(event{Type: "app_mention", Text: "<@U0BOT> hi"}), d, app, team, lookup())
	if at.in.ChatKind != conversation.KindGroup || at.in.ChatTitle != "#general" || strings.Join(at.in.ChatHandles, ",") != "C0GEN,#general" {
		t.Errorf("thread: %+v", at.in)
	}
}

func TestImageFiles(t *testing.T) {
	d := identity.NewDockID()
	app := App{Dock: d, BotUserID: "U0BOT"}
	env := envelope{Type: "event_callback", Event: event{Type: "app_mention", Subtype: "file_share", User: "U0ANA", Channel: "C0GEN",
		ChannelType: "channel", TS: "1700000000.000100", Text: "<@U0BOT> look",
		Files: []file{{Name: "a.png", Mimetype: "image/png", URL: "https://files.example.com/a.png"},
			{Name: "r.pdf", Mimetype: "application/pdf", URL: "https://files.example.com/r.pdf"}}}}
	fetched := ""
	lk := lookup()
	lk.fetch = func(u string) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) { fetched = u; return []byte("x"), nil }
	}
	at, ok, err := parse(env, d, app, nil, lk)
	if err != nil || !ok || len(at.in.Media) != 1 || at.in.Media[0].Name != "a.png" || !strings.Contains(at.in.Text, "[file: r.pdf]") {
		t.Fatalf("files: %v %v %+v", ok, err, at.in)
	}
	if _, err := at.in.Media[0].Fetch(context.Background()); err != nil || fetched != "https://files.example.com/a.png" {
		t.Fatalf("fetch: %v %q", err, fetched)
	}
}

func TestManifest(t *testing.T) {
	g := &Gateway{cfg: Config{PublicURL: "https://port.example.com"}}
	d := identity.NewDockID()
	var m struct {
		Display struct {
			Name string `json:"name"`
		} `json:"display_information"`
		Features struct {
			Bot struct {
				Name string `json:"display_name"`
			} `json:"bot_user"`
		} `json:"features"`
		OAuth struct {
			Redirects []string `json:"redirect_urls"`
			Scopes    struct {
				Bot []string `json:"bot"`
			} `json:"scopes"`
		} `json:"oauth_config"`
		Settings struct {
			Events struct {
				URL    string   `json:"request_url"`
				Events []string `json:"bot_events"`
			} `json:"event_subscriptions"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(g.manifest(d, "Seaport Support"), &m); err != nil {
		t.Fatal(err)
	}
	if m.Display.Name != "Seaport Support" || m.Features.Bot.Name != "seaport-support" ||
		m.Settings.Events.URL != "https://port.example.com/slack/events/"+d.String() ||
		len(m.OAuth.Redirects) != 1 || m.OAuth.Redirects[0] != "https://port.example.com/slack/oauth/callback" {
		t.Fatalf("manifest = %+v", m)
	}
	if strings.Join(m.OAuth.Scopes.Bot, " ") != "app_mentions:read chat:write channels:history groups:history im:history im:read im:write reactions:write users:read users:read.email files:read files:write" {
		t.Fatalf("scopes = %v", m.OAuth.Scopes.Bot)
	}
	if strings.Join(m.Settings.Events.Events, " ") != "app_mention message.channels message.groups message.im" {
		t.Fatalf("events = %v", m.Settings.Events.Events)
	}
	// Long names are cut to Slack's limit.
	if err := json.Unmarshal(g.manifest(d, strings.Repeat("x", 80)), &m); err != nil || len(m.Display.Name) != maxAppName {
		t.Fatalf("long name: %v %d", err, len(m.Display.Name))
	}
}

func TestSplitChat(t *testing.T) {
	for in, want := range map[string][3]string{
		"C0GEN/1700000000.000100": {"C0GEN", "1700000000.000100", "ok"},
		"D0DM":                    {"D0DM", "", "ok"},
		"C0GEN/notats":            {"", "", ""},
		"c0gen/1700000000.0001":   {"", "", ""},
		"":                        {"", "", ""},
	} {
		ch, ts, ok := splitChat(in)
		if (want[2] == "ok") != ok || ch != want[0] || ts != want[1] {
			t.Errorf("splitChat(%q) = %q %q %v", in, ch, ts, ok)
		}
	}
}
