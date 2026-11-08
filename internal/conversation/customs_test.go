package conversation_test

import (
	"testing"

	"github.com/Abraxas-365/puerto/internal/conversation"
)

func TestNormalizePattern(t *testing.T) {
	for in, want := range map[string]string{
		"51 999-123-436":         "+51999123436",
		"+51*":                   "+51*",
		"51*":                    "+51*",
		"*436":                   "*436",
		"2314@LID":               "2314@lid",
		"*@example.com":          "*@example.com",
		"  @Juan ":               "@juan",
		"120363@g.us":            "120363@g.us",
		"51999123436@s.whatsapp": "51999123436@s.whatsapp",
	} {
		if got := conversation.NormalizePattern(in); got != want {
			t.Errorf("NormalizePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, handle string
		want            bool
	}{
		{"+51999123436", "+51999123436", true},
		{"+51999123436", "+519991234360", false},
		{"+51*", "+51999123436", true},
		{"+51*", "+34600000000", false},
		{"*436", "+51999123436", true},
		{"*@example.com", "Ana@Example.com", true},
		{"*@example.com", "ana@example.com.evil.com", false},
		{"+51*436", "+51999123436", true},
		{"+51*436", "+51436", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
	}
	for _, c := range cases {
		if got := conversation.Match(c.pattern, c.handle); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.handle, got, c.want)
		}
	}
}

func rule(effect conversation.Effect, pattern string) conversation.Rule {
	return conversation.Rule{Effect: effect, Pattern: conversation.NormalizePattern(pattern)}
}

func TestDecide(t *testing.T) {
	ask := conversation.Customs{Policy: conversation.Policy{Default: conversation.DefaultAsk}, Rules: []conversation.Rule{
		rule(conversation.EffectAllow, "+51*"),
		rule(conversation.EffectDeny, "51999888777"),
	}}
	peru := []string{"2314@lid", "+51999123436"}
	spam := []string{"+51999888777"}
	spain := []string{"+34600000000"}
	cases := []struct {
		name    string
		customs conversation.Customs
		kind    conversation.Kind
		access  conversation.Access
		handles []string
		want    conversation.Status
	}{
		{"allow rule", ask, conversation.KindDirect, conversation.AccessAuto, peru, conversation.StatusAllowed},
		{"deny wins over allow", ask, conversation.KindDirect, conversation.AccessAuto, spam, conversation.StatusDenied},
		{"no rule: ask", ask, conversation.KindDirect, conversation.AccessAuto, spain, conversation.StatusPending},
		{"override allow beats deny rule", ask, conversation.KindDirect, conversation.AccessAllow, spam, conversation.StatusAllowed},
		{"override deny beats allow rule", ask, conversation.KindDirect, conversation.AccessDeny, peru, conversation.StatusDenied},
		{"self always allowed", ask, conversation.KindSelf, conversation.AccessAuto, spam, conversation.StatusAllowed},
		{"default allow", conversation.Customs{Policy: conversation.Policy{Default: conversation.DefaultAllow}},
			conversation.KindDirect, conversation.AccessAuto, spain, conversation.StatusAllowed},
		{"default deny", conversation.Customs{Policy: conversation.Policy{Default: conversation.DefaultDeny}},
			conversation.KindGroup, conversation.AccessAuto, spain, conversation.StatusDenied},
	}
	for _, c := range cases {
		if got := c.customs.Decide(c.kind, c.access, c.handles); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestAdmit(t *testing.T) {
	customs := conversation.Customs{
		Policy: conversation.Policy{Default: conversation.DefaultAsk, GroupMode: conversation.GroupMentions},
		Rules:  []conversation.Rule{rule(conversation.EffectDeny, "+51999888777")},
	}
	group := conversation.Conversation{Kind: conversation.KindGroup, Access: conversation.AccessAllow, Status: conversation.StatusAllowed}
	member := []string{"+51999123436"}
	troll := []string{"+51999888777"}

	if got := customs.Admit(group, member, false); got != conversation.HoldNotMentioned {
		t.Errorf("unmentioned group message: %q", got)
	}
	if got := customs.Admit(group, member, true); got != conversation.HoldNone {
		t.Errorf("mentioned group message: %q", got)
	}
	all := group
	all.GroupMode = conversation.GroupAll
	if got := customs.Admit(all, member, false); got != conversation.HoldNone {
		t.Errorf("group in all mode: %q", got)
	}
	auto := group
	auto.Access = conversation.AccessAuto
	if got := customs.Admit(auto, troll, true); got != conversation.HoldSenderDenied {
		t.Errorf("denied member: %q", got)
	}
	// The owner allowed this chat explicitly: a sender rule doesn't mute it.
	direct := conversation.Conversation{Kind: conversation.KindDirect, Access: conversation.AccessAllow, Status: conversation.StatusAllowed}
	if got := customs.Admit(direct, troll, false); got != conversation.HoldNone {
		t.Errorf("explicitly allowed direct chat: %q", got)
	}
	self := conversation.Conversation{Kind: conversation.KindSelf, Status: conversation.StatusAllowed}
	if got := customs.Admit(self, troll, false); got != conversation.HoldNone {
		t.Errorf("self chat: %q", got)
	}
}
