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
	// rness's manual login: its "Error: oauth: …" line, not the wrapper's.
	rness := "Open this address in any browser and sign in with your Claude account:\n\n  https://claude.com/cai/oauth/authorize?code=true&x=1\n\n" +
		"Paste the code the page shows here > Error: oauth: that code is from another sign-in: paste the one this sign-in's page shows\nseaport: sign in: exit status 1\n"
	if got := LoginFailure(rness, 1); got != "that code is from another sign-in: paste the one this sign-in's page shows" {
		t.Fatalf("rness failure: %q", got)
	}
}

// A subscription slot (Claude Code, no model key: Normalize makes it
// anthropic: subscription) reaches its provider itself; one with a key
// goes through the gateway.
func TestSubscriptionEgress(t *testing.T) {
	sub := Normalize(Spec{Harness: HarnessClaudeCode, Machine: Machine{Network: Network{Egress: []string{"pypi.org"}}}})
	if sub.Models[ProviderAnthropic] != Subscription || !sub.SignsIn() ||
		!slices.Equal(sub.EgressHosts(), []string{"pypi.org", "api.anthropic.com", "platform.claude.com"}) {
		t.Fatalf("subscription egress: %v", sub.EgressHosts())
	}
	keyed := Normalize(Spec{Harness: HarnessClaudeCode, Machine: Machine{Network: Network{Egress: []string{"pypi.org"}},
		Models: Models{"anthropic": "${ANTHROPIC_KEY}"}}})
	if keyed.SignsIn() || !slices.Equal(keyed.EgressHosts(), []string{"pypi.org"}) {
		t.Fatalf("keyed egress: %v", keyed.EgressHosts())
	}
	if Normalize(Spec{Harness: HarnessRness, Machine: Machine{Models: Models{"groq": "${GROQ_KEY}"}}}).SignsIn() {
		t.Fatal("an rness machine with a model key signs in")
	}
	rness := Normalize(Spec{Harness: HarnessRness, Machine: Machine{Network: Network{Egress: []string{"pypi.org"}}}})
	if !rness.SignsIn() || !slices.Equal(rness.EgressHosts(), []string{"pypi.org", "api.anthropic.com", "platform.claude.com"}) {
		t.Fatalf("rness subscription egress: %v", rness.EgressHosts())
	}
	// Claude Code with a groq key still signs in to Anthropic.
	mixed := Normalize(Spec{Harness: HarnessClaudeCode, Machine: Machine{Models: Models{"groq": "${GROQ_KEY}"}}})
	if !mixed.SignsIn() || !slices.Equal(mixed.Models.Providers(), []string{"groq"}) {
		t.Fatalf("mixed: %v %v", mixed.Models, mixed.Models.Providers())
	}
	// rness signs in to Anthropic and uses the other keys through the
	// gateway; only anthropic signs in.
	mixedRness := Spec{Harness: HarnessRness, Machine: Machine{Models: Models{"anthropic": Subscription, "groq": "${GROQ_KEY}"}}}
	if err := mixedRness.Validate(); err != nil {
		t.Fatalf("rness subscription with keys: %v", err)
	}
	if n := Normalize(mixedRness); !n.SignsIn() || !slices.Equal(n.Models.Providers(), []string{"groq"}) {
		t.Fatalf("rness mixed: %v %v", n.Models, n.Models.Providers())
	}
	if err := (Spec{Harness: HarnessClaudeCode, Machine: Machine{Models: Models{"openai": Subscription}}}).Validate(); err == nil {
		t.Fatal("an openai subscription accepted")
	}
	if err := (LoginCode{Agent: identity.NewAgentID(), Index: 1, Code: "abc#def"}).Validate(); err != nil {
		t.Fatalf("code: %v", err)
	}
	if err := (LoginCode{Agent: identity.NewAgentID(), Index: 1, Code: "a b"}).Validate(); err == nil {
		t.Fatal("a code with a space")
	}
}
