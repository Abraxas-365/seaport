package dockwa

import "testing"

// A destination's target is a phone (+digits) or a chat JID (a group's).
func TestChatJID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"+34600000000", "34600000000@s.whatsapp.net"},
		{" +34600000001 ", "34600000001@s.whatsapp.net"},
		{"120363000000000000@g.us", "120363000000000000@g.us"},
		{"34600000002@s.whatsapp.net", "34600000002@s.whatsapp.net"},
	} {
		got, err := chatJID(tc.in)
		if err != nil || got.String() != tc.want {
			t.Errorf("chatJID(%q) = %v, %v; want %s", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "+", "+34 600", "ops", "@g.us", "+34600000000@g.us"} {
		if _, err := chatJID(bad); err == nil {
			t.Errorf("chatJID(%q) accepted", bad)
		}
	}
}
