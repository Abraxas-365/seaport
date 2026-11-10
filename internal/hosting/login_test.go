package hosting

import (
	"slices"
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Only an https authorize URL of a sign-in host is shown: the URL comes
// from inside the agent's container.
func TestLoginURL(t *testing.T) {
	good := "https://claude.com/cai/oauth/authorize?code=true&client_id=x&state=s"
	for line, want := range map[string]string{
		"If the browser didn't open, visit: " + good: good,
		good: good,
		"visit: https://platform.claude.com/oauth/authorize?x=1": "https://platform.claude.com/oauth/authorize?x=1",
		"visit: http://claude.com/cai/oauth/authorize?x=1":       "",
		"visit: https://evil.example/cai/oauth/authorize?x=1":    "",
		"visit: https://claude.com.evil.example/oauth/authorize": "",
		"visit: https://user@claude.com/oauth/authorize":         "",
		"visit: https://claude.com:8443/oauth/authorize":         "",
		"visit: https://claude.com/settings":                     "",
		"Opening browser to sign in…":                            "",
	} {
		if got := LoginURL(line); got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
}

func TestLoginFailure(t *testing.T) {
	out := "Opening browser…\nvisit: https://claude.com/cai/oauth/authorize?x=1\nPaste code here > Login failed: Request failed with status code 400\n"
	if got := LoginFailure(out, 1); got != "Login failed: Request failed with status code 400" {
		t.Fatalf("failure: %q", got)
	}
	if got := LoginFailure(out+"seaport: sign in: exit status 1\n", 1); got != "Login failed: Request failed with status code 400" {
		t.Fatalf("failure before the wrapper's line: %q", got)
	}
	if got := LoginFailure("visit: https://claude.com/cai/oauth/authorize?x=1\nseaport: sign in\n", 2); got != "the sign-in ended without an answer (exit 2)" {
		t.Fatalf("no answer: %q", got)
	}
}

// A subscription slot (Claude Code, no model key) reaches its provider
// itself; one with a key goes through the gateway.
func TestSubscriptionEgress(t *testing.T) {
	sub := Spec{Harness: HarnessClaudeCode, Network: Network{Egress: []string{"pypi.org"}}}
	if !sub.SignsIn() || !slices.Equal(sub.EgressHosts(), []string{"pypi.org", "api.anthropic.com", "platform.claude.com"}) {
		t.Fatalf("subscription egress: %v", sub.EgressHosts())
	}
	keyed := sub
	keyed.Models = Models{Anthropic: "${ANTHROPIC_KEY}"}
	if keyed.SignsIn() || !slices.Equal(keyed.EgressHosts(), []string{"pypi.org"}) {
		t.Fatalf("keyed egress: %v", keyed.EgressHosts())
	}
	if (Spec{Harness: HarnessRness}).SignsIn() {
		t.Fatal("rness signs in")
	}
	if err := (LoginCode{Agent: identity.NewAgentID(), Index: 1, Code: "abc#def"}).Validate(); err != nil {
		t.Fatalf("code: %v", err)
	}
	if err := (LoginCode{Agent: identity.NewAgentID(), Index: 1, Code: "a b"}).Validate(); err == nil {
		t.Fatal("a code with a space")
	}
}
