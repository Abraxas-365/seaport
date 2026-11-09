package agent_test

import (
	"testing"

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
