package agent_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func TestRoute(t *testing.T) {
	pm, rev, ops := identity.NewAgentID(), identity.NewAgentID(), identity.NewAgentID()
	none := identity.AgentID{}
	solo := []agent.Member{{Agent: pm, Name: "pm"}}
	pair := []agent.Member{{Agent: pm, Name: "pm"}, {Agent: rev, Name: "reviewer"}}
	withDefault := []agent.Member{{Agent: pm, Name: "pm", Default: true}, {Agent: rev, Name: "reviewer"}}
	for _, c := range []struct {
		name     string
		team     []agent.Member
		msg      string
		previous identity.AgentID
		want     identity.AgentID
		hold     string
	}{
		{"no team", nil, "@pm hola", none, none, agent.HoldNoAgent},
		{"single member takes everything", solo, "hola", none, pm, ""},
		{"single member, previous gone", solo, "hola", ops, pm, ""},
		{"mention", pair, "oye @reviewer mira esto", none, rev, ""},
		{"mention any case", pair, "@Reviewer?", none, rev, ""},
		{"first mention wins", pair, "@reviewer y @pm", none, rev, ""},
		{"mention beats previous", pair, "@pm ahora tú", rev, pm, ""},
		{"mention beats default", withDefault, "@reviewer", none, rev, ""},
		{"sticky previous", pair, "y lo otro?", rev, rev, ""},
		{"previous beats default", withDefault, "sigue", rev, rev, ""},
		{"previous no longer on the team", withDefault, "sigue", ops, pm, ""},
		{"default", withDefault, "hola", none, pm, ""},
		{"nobody called", pair, "hola", none, none, agent.HoldNotCalled},
		{"unknown mention", pair, "@ops hola", none, none, agent.HoldNotCalled},
		{"an address is not a call", pair, "escribe a user@pm por favor", none, none, agent.HoldNotCalled},
		{"a longer handle is not a call", pair, "@pmx hola", none, none, agent.HoldNotCalled},
		{"punctuation ends a handle", pair, "gracias @pm.", none, pm, ""},
		{"a bare @ is nothing", pair, "@ hola @", none, none, agent.HoldNotCalled},
	} {
		got, hold := agent.Route(c.team, c.msg, c.previous)
		if got != c.want || hold != c.hold {
			t.Errorf("%s: got (%s, %q), want (%s, %q)", c.name, got, hold, c.want, c.hold)
		}
	}
}

func TestNames(t *testing.T) {
	for name, ok := range map[string]bool{
		"pm": true, "reviewer-2": true, "a_b": true, "0": true,
		"": false, "PM": false, "-pm": false, "p m": false, "pm!": false,
		"abcdefghijklmnopqrstuvwxyz0123456": false, "abcdefghijklmnopqrstuvwxyz012345": true,
	} {
		if err := agent.ValidName(name); (err == nil) != ok {
			t.Errorf("ValidName(%q) = %v", name, err)
		}
	}
}

func TestAskTakeover(t *testing.T) {
	for name, c := range map[string]struct {
		in agent.AskTakeover
		ok bool
	}{
		"login by default":   {agent.AskTakeover{Host: "App.Example.com"}, true},
		"captcha":            {agent.AskTakeover{Host: "a.example.com", Kind: agent.KindCaptcha}, true},
		"other with note":    {agent.AskTakeover{Host: "a.example.com", Kind: agent.KindOther, Note: "pay the invoice"}, true},
		"other without note": {agent.AskTakeover{Host: "a.example.com", Kind: agent.KindOther}, false},
		"unknown kind":       {agent.AskTakeover{Host: "a.example.com", Kind: "payment"}, false},
		"no host":            {agent.AskTakeover{}, false},
		"not a host":         {agent.AskTakeover{Host: "localhost"}, false},
		"long note":          {agent.AskTakeover{Host: "a.example.com", Note: string(make([]byte, 201))}, false},
		"by the detector":    {agent.AskTakeover{Host: "a.example.com", By: agent.TakeoverByDetector, Path: "/login"}, true},
		"unknown asker":      {agent.AskTakeover{Host: "a.example.com", By: "person"}, false},
		"path with a query":  {agent.AskTakeover{Host: "a.example.com", Path: "/login?token=x"}, false},
		"relative path":      {agent.AskTakeover{Host: "a.example.com", Path: "login"}, false},
		"path with a space":  {agent.AskTakeover{Host: "a.example.com", Path: "/a b"}, false},
		"long path":          {agent.AskTakeover{Host: "a.example.com", Path: "/" + strings.Repeat("a", agent.MaxTakeoverPath)}, false},
	} {
		if err := c.in.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
	now := time.Now()
	conv := identity.NewConversationID()
	var list agent.Takeovers
	for i := range agent.MaxTakeovers + 2 {
		list = agent.AskTakeover{Host: fmt.Sprintf("h%d.example.com", i), Conversation: &conv}.Add(list, now)
	}
	if len(list) != agent.MaxTakeovers || list[0].Host != fmt.Sprintf("h%d.example.com", agent.MaxTakeovers+1) || list[0].Kind != agent.KindLogin || list[0].Conversation == nil {
		t.Fatalf("add: %+v", list)
	}
	list = agent.AskTakeover{Host: "H3.Example.com", Kind: agent.Kind2FA, Note: " code "}.Add(list, now)
	if len(list) != agent.MaxTakeovers || list[0].Host != "h3.example.com" || list[0].Kind != agent.Kind2FA || list[0].Note != "code" {
		t.Fatalf("replace: %+v", list[:2])
	}
	for _, l := range list[1:] {
		if l.Host == "h3.example.com" {
			t.Fatal("the older h3 stayed")
		}
	}
	if list[0].By != agent.TakeoverByAgent || list[0].Asker() != agent.TakeoverByAgent {
		t.Fatalf("the agent asks by default: %+v", list[0])
	}
	list = agent.AskTakeover{Host: "h3.example.com", By: agent.TakeoverByDetector, Path: "/signin"}.Add(list, now)
	if list[0].By != agent.TakeoverByDetector || list[0].Path != "/signin" {
		t.Fatalf("detector: %+v", list[0])
	}
	if (agent.Takeover{}).Asker() != agent.TakeoverByAgent {
		t.Fatal("an old row (no by) reads as the agent's")
	}
}

func TestTakeoverEvent(t *testing.T) {
	for name, c := range map[string]struct {
		in agent.EndTakeover
		ok bool
	}{
		"host":      {agent.EndTakeover{Host: "App.Example.com", By: "ana"}, true},
		"no by":     {agent.EndTakeover{Host: "app.example.com"}, true},
		"bad host":  {agent.EndTakeover{Host: "not a host"}, false},
		"long name": {agent.EndTakeover{Host: "app.example.com", By: strings.Repeat("a", 300)}, false},
	} {
		if err := c.in.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
	id, conv, now := identity.NewAgentID(), identity.NewConversationID(), time.Now()
	tk := agent.Takeover{Host: "app.example.com", Kind: agent.Kind2FA, Note: "the code", Path: "/signin", Conversation: &conv, At: now}
	e := tk.Event(id, agent.TakeoverDone.State(), "ana")
	if e.Agent != id || e.Host != tk.Host || e.Kind != agent.Kind2FA || e.By != agent.TakeoverByAgent || e.Path != "/signin" ||
		e.Conversation == nil || *e.Conversation != conv || !e.AskedAt.Equal(now) || e.State != agent.TakeoverStateDone || e.EndedBy != "ana" {
		t.Fatalf("event: %+v", e)
	}
	if agent.TakeoverExpired.State() != agent.TakeoverStateExpired {
		t.Fatal("expired end")
	}
	tk.By = agent.TakeoverByDetector
	e = tk.Event(id, agent.TakeoverStateAsked, "")
	if e.By != agent.TakeoverByDetector || e.EndedBy != "" {
		t.Fatalf("detector asked: %+v", e)
	}
	// The note (what the agent was doing) stays out of the trail.
	if raw, _ := json.Marshal(e); strings.Contains(string(raw), "the code") || strings.Contains(string(raw), "ended_by") {
		t.Fatalf("payload: %s", raw)
	}
}

func TestProfileBrowserDetect(t *testing.T) {
	for name, c := range map[string]struct {
		in agent.SaveProfile
		ok bool
	}{
		"detect with browser":    {agent.SaveProfile{Browser: true, BrowserDetect: true}, true},
		"detect without browser": {agent.SaveProfile{BrowserDetect: true}, false},
		"ignore without browser": {agent.SaveProfile{BrowserIgnoreHosts: []string{"a.example.com"}}, false},
		"ignore hosts":           {agent.SaveProfile{Browser: true, BrowserIgnoreHosts: []string{"a.example.com", "B.example.com"}}, true},
		"ignore not a host":      {agent.SaveProfile{Browser: true, BrowserIgnoreHosts: []string{"https://a.example.com"}}, false},
		"ignore twice":           {agent.SaveProfile{Browser: true, BrowserIgnoreHosts: []string{"a.example.com", "A.example.com"}}, false},
		"too many ignored":       {agent.SaveProfile{Browser: true, BrowserIgnoreHosts: manyHosts(agent.MaxIgnoreHosts + 1)}, false},
		"just enough ignored":    {agent.SaveProfile{Browser: true, BrowserIgnoreHosts: manyHosts(agent.MaxIgnoreHosts)}, true},
	} {
		if err := c.in.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
	got := agent.SaveProfile{Browser: true, BrowserIgnoreHosts: []string{" A.Example.com "}}.IgnoreHosts()
	if len(got) != 1 || got[0] != "a.example.com" {
		t.Fatalf("IgnoreHosts() = %v", got)
	}
	if (agent.SaveProfile{}).IgnoreHosts() == nil {
		t.Fatal("never nil")
	}
	hosts := []string{"intranet.example.com"}
	for h, want := range map[string]bool{"intranet.example.com": true, "SSO.intranet.example.com": true, "notintranet.example.com": false, "example.com": false} {
		if agent.HostIgnored(h, hosts) != want {
			t.Errorf("HostIgnored(%s) != %v", h, want)
		}
	}
}

func manyHosts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("h%d.example.com", i)
	}
	return out
}
