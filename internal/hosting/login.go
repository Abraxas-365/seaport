package hosting

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Subscription sign-in (experimental): a slot's harness signs in to the
// person's own subscription (Claude Code: their Claude account) with its
// own login, run in the slot's container. Seaport relays it: it shows the
// harness's authorize URL and hands the code the person pastes to the
// harness's stdin. The harness keeps (and refreshes) the tokens in the
// slot's state volume; Seaport stores none.

// Login states: starting (the harness's login is starting) → waiting (its
// URL is shown, a code is awaited) → verifying (the code went to the
// harness) → done | failed.
const (
	LoginStarting  = "starting"
	LoginWaiting   = "waiting"
	LoginVerifying = "verifying"
	LoginDone      = "done"
	LoginFailed    = "failed"
)

const (
	// LoginTimeout bounds one sign-in, from start to the harness's answer.
	LoginTimeout = 10 * time.Minute
	// LoginStale: a live sign-in whose relay hasn't reported for this long
	// (its process died) reads as failed.
	LoginStale = 30 * time.Second
	// LoginBeat is how often the relay reports while it waits.
	LoginBeat = 5 * time.Second
	// MaxLoginCode bounds a pasted code.
	MaxLoginCode = 2048
)

// Login is a slot's latest sign-in (never the code).
type Login struct {
	ID    string `db:"id" json:"id"`
	Slot  int    `db:"idx" json:"slot"`
	State string `db:"state" json:"state"`
	// URL is where the person signs in (once waiting).
	URL string `db:"url" json:"url"`
	// Detail says why it failed (no secrets).
	Detail    string    `db:"detail" json:"detail"`
	CreatedBy string    `db:"created_by" json:"created_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	// Run is the slot run it signs in (internal: a restart starts another).
	Run string `db:"run" json:"-"`
}

// Live: the sign-in is still going.
func (l Login) Live() bool {
	return l.State == LoginStarting || l.State == LoginWaiting || l.State == LoginVerifying
}

// LoginUpdate is a relay's report (Repository.LoginProgress).
type LoginUpdate struct {
	State, URL, Detail string
}

// LoginCode is the input of Commands.SubmitLoginCode: the code the
// harness's sign-in page showed the person.
type LoginCode struct {
	Agent identity.AgentID
	Index int
	Code  string
}

func (c LoginCode) Validate() error {
	if err := (SlotAction{Agent: c.Agent, Index: c.Index}).Validate(); err != nil {
		return err
	}
	if err := text.Required("code", c.Code, MaxLoginCode); err != nil {
		return err
	}
	if strings.IndexFunc(c.Code, unicode.IsSpace) >= 0 {
		return errx.InvalidField("code", "code must be the code as shown, without spaces")
	}
	return nil
}

// loginHosts are the hosts a harness's authorize URL may point at: the
// URL comes from inside the agent's container, so another one (a page
// asking for anything else) is never shown.
var loginHosts = []string{"claude.com", "claude.ai", "platform.claude.com", "console.anthropic.com"}

// LoginURL is the authorize URL in a line of the harness's login output
// ("" when it has none, or one pointing anywhere but loginHosts).
func LoginURL(line string) string {
	for _, f := range strings.Fields(line) {
		if !strings.HasPrefix(f, "https://") {
			continue
		}
		u, err := url.Parse(f)
		if err != nil || u.User != nil || u.Port() != "" || !strings.Contains(u.Path, "/oauth/authorize") {
			continue
		}
		for _, h := range loginHosts {
			if strings.EqualFold(u.Hostname(), h) {
				return u.String()
			}
		}
	}
	return ""
}

// wrapperLine is the CLI's own error line ("seaport: …", cmd/seaport).
func wrapperLine(l string) bool {
	name, _, ok := strings.Cut(l, ":")
	return ok && name == "seaport"
}

// LoginFailure is what to show of the harness's output when its login
// failed: its "Login failed" line, else its last line that isn't the
// URL or the wrapper's own ("seaport: …"), cleaned and bounded.
func LoginFailure(output string, exit int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	pick := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if j := strings.Index(l, "Login failed"); j >= 0 {
			pick = l[j:]
			break
		}
		if pick == "" && l != "" && LoginURL(l) == "" && !wrapperLine(l) {
			pick = l
		}
	}
	if pick == "" {
		return "the sign-in ended without an answer (exit " + strconv.Itoa(exit) + ")"
	}
	pick = text.Clean(pick)
	if len(pick) > 300 {
		pick = pick[:300] + "…"
	}
	return pick
}

// Account is how a slot's harness is signed in (from the harness itself:
// `seaport agent-account` in the container).
type Account struct {
	Slot int `json:"slot"`
	// Supported: the harness can sign in from Seaport.
	Supported bool `json:"supported"`
	LoggedIn  bool `json:"logged_in"`
	// Method: claude.ai (a subscription), oauth_token
	// (CLAUDE_CODE_OAUTH_TOKEN), api_key (the gateway's or the kit's key),
	// none.
	Method       string `json:"method"`
	Subscription string `json:"subscription,omitempty"`
}

// Commands run in a slot's container (`seaport <cmd>`, images'
// binary): the harness's sign-in and its account.
var (
	LoginCommand   = []string{"/opt/seaport/bin/seaport", "agent-login"}
	AccountCommand = []string{"/opt/seaport/bin/seaport", "agent-account"}
)

// SignsIn: slots of an environment with this spec sign in to a
// subscription (Claude Code with no model key: with one the gateway's key
// wins).
func (s Spec) SignsIn() bool {
	return s.Harness == HarnessClaudeCode && s.Models.KeyName(ProviderAnthropic) == ""
}

// subscriptionHosts are what a signed-in Claude Code reaches itself: its
// API and the token refresh.
var subscriptionHosts = []string{"api.anthropic.com", "platform.claude.com"}
