// Package agent is the organization's library of agents. An agent is the
// identity crew tokens connect as; it works only through the docks it is
// assigned to (its team on each dock). Route decides, for each inbound
// message, which agent of the dock's team it is for.
package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

const (
	// MaxAgents bounds an organization's agents.
	MaxAgents = 100
	// MaxDescription bounds an agent's description.
	MaxDescription = 1000
)

// Hold reasons of a message no agent takes (the ship is held, like a
// customs hold: readable in the history). Assigning an agent to the dock
// routes again the recent ones the chat hasn't moved past (Releaser).
const (
	HoldNoAgent   = "no agent is assigned to this dock"
	HoldNotCalled = "no agent was called (mention one with @name)"
	// HoldAgentGone: it was waiting for an agent taken off the dock or
	// deleted.
	HoldAgentGone = "its agent left the dock"
)

// RoutingHolds are the hold reasons a change of the dock's team can lift.
var RoutingHolds = []string{HoldNoAgent, HoldNotCalled, HoldAgentGone}

// nameRule is an agent name: also its @mention handle.
var nameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Agent is one agent of the library.
type Agent struct {
	ID           identity.AgentID        `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	// Name is unique in the organization and is how people call it
	// (@name) on a dock with several agents.
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	CreatedBy   string    `db:"created_by" json:"created_by"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
	// ProfileVersion is the profile it runs (0: none saved yet).
	ProfileVersion int `db:"profile_version" json:"profile_version"`
	// Docks are where it works; none: it is inactive (nothing reaches it).
	Docks []Assignment `db:"-" json:"docks"`
	// Takeovers are the sites where the agent asked a person to take over
	// its browser (MCP tool ask_takeover), newest first.
	Takeovers Takeovers `db:"-" json:"takeovers"`
}

// Takeovers is the list, newest first.
type Takeovers []Takeover

// Takeover is one site where a person must take over the agent's browser
// from the web's viewer: a sign-in, a captcha, a 2FA code, anything the
// agent must not or cannot do itself.
type Takeover struct {
	// Host is the site (a DNS name, lowercase).
	Host string `json:"host"`
	// Kind says what the person must do (TakeoverKinds).
	Kind TakeoverKind `json:"kind"`
	// Note is what the agent was trying to do (optional, one line; required
	// for KindOther).
	Note string `json:"note"`
	// By says who asked: the agent (ask_takeover) or the connector's
	// detector (TakeoverBys; "" in old rows reads as the agent).
	By TakeoverBy `json:"by,omitempty"`
	// Path is the page's URL path when the detector asked ("" otherwise;
	// never the query or the page's content).
	Path string `json:"path,omitempty"`
	// Conversation is the one the agent was working, when it said so.
	Conversation *identity.ConversationID `json:"conversation_id,omitempty"`
	At           time.Time                `json:"at"`
	// NotifiedAt is when a person was told (nil: nobody was, or the
	// profile's browser_assist is off).
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
	// NotifiedIn is the conversation the notice went to: the call's, or the
	// one opened at the profile's destination. The agent is woken there
	// when the person is done.
	NotifiedIn *identity.ConversationID `json:"notified_in,omitempty"`
}

// Find returns the host's takeover (every listed one is open: ending one
// removes it).
func (l Takeovers) Find(host string) (Takeover, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, t := range l {
		if t.Host == host {
			return t, true
		}
	}
	return Takeover{}, false
}

// TakeoverKind is what the person must do in the browser.
type TakeoverKind string

const (
	KindLogin   TakeoverKind = "login"
	KindCaptcha TakeoverKind = "captcha"
	Kind2FA     TakeoverKind = "2fa"
	KindOther   TakeoverKind = "other"
)

// TakeoverKinds are the valid kinds.
var TakeoverKinds = []TakeoverKind{KindLogin, KindCaptcha, Kind2FA, KindOther}

// TakeoverBy is who asked for the takeover.
type TakeoverBy string

const (
	// TakeoverByAgent: the agent called ask_takeover.
	TakeoverByAgent TakeoverBy = "agent"
	// TakeoverByDetector: the connector's page detector (the profile's
	// browser_detect) saw a page only a person can pass.
	TakeoverByDetector TakeoverBy = "detector"
)

// TakeoverBys are the valid askers.
var TakeoverBys = []TakeoverBy{TakeoverByAgent, TakeoverByDetector}

// Asker is who asked, the agent for rows from before By existed.
func (t Takeover) Asker() TakeoverBy {
	if t.By == "" {
		return TakeoverByAgent
	}
	return t.By
}

// MaxTakeovers bounds an agent's open takeovers (the oldest go).
const MaxTakeovers = 8

// DetectEvery is how often the detector asks a takeover for the same host
// again while the page still needs a person (a page reloads, a sign-in
// has several steps).
const DetectEvery = 10 * time.Minute

// MaxTakeoverPath bounds Takeover.Path.
const MaxTakeoverPath = 200

// TakeoverTTL is how long a takeover waits for a person: older ones are
// swept (Commands.SweepTakeovers) and the agent is told nobody came.
const TakeoverTTL = time.Hour

// TakeoverEnd is why a takeover ended: a person said so (Listo), or
// nobody came within TakeoverTTL.
type TakeoverEnd string

const (
	TakeoverDone    TakeoverEnd = "done"
	TakeoverExpired TakeoverEnd = "expired"
)

// AgentRef names an agent of an organization (the sweeper's unit of work).
type AgentRef struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Agent        identity.AgentID        `db:"id"`
}

// Expired splits the takeovers into the live ones and those asked before
// now - TakeoverTTL.
func (l Takeovers) Expired(now time.Time) (live, expired Takeovers) {
	live = Takeovers{}
	for _, t := range l {
		if now.Sub(t.At) >= TakeoverTTL {
			expired = append(expired, t)
		} else {
			live = append(live, t)
		}
	}
	return live, expired
}

// AskTakeover is what ask_takeover (and the detector) asks for.
type AskTakeover struct {
	Host string
	// Kind defaults to KindLogin.
	Kind TakeoverKind
	Note string
	// By defaults to TakeoverByAgent.
	By TakeoverBy
	// Path is the page's URL path (the detector's; "" from the agent). No
	// query: it may carry tokens.
	Path string
	// Conversation is optional: the one the agent is working.
	Conversation *identity.ConversationID
	// Wait blocks the call until a person ends the takeover, at most
	// MaxTakeoverWait and short of the request's deadline (0: return at
	// once).
	Wait time.Duration
}

// MaxTakeoverWait bounds AskTakeover.Wait (the MCP request's deadline is
// 300 s; the margin is for the answer).
const MaxTakeoverWait = 290 * time.Second

func (r AskTakeover) Validate() error {
	if err := ValidHost(r.Host); err != nil {
		return err
	}
	if r.Wait < 0 || r.Wait > MaxTakeoverWait {
		return errx.InvalidField("wait_seconds", "wait_seconds must be between 0 and 290")
	}
	kind := r.kind()
	if !slices.Contains(TakeoverKinds, kind) {
		return errx.InvalidField("kind", "kind must be one of login, captcha, 2fa, other")
	}
	if kind == KindOther && strings.TrimSpace(r.Note) == "" {
		return errx.InvalidField("note", "say what the person must do when kind is other")
	}
	if !slices.Contains(TakeoverBys, r.by()) {
		return errx.InvalidField("by", "by must be agent or detector")
	}
	if r.Path != "" && (len(r.Path) > MaxTakeoverPath || !strings.HasPrefix(r.Path, "/") || strings.ContainsAny(r.Path, "?#") || text.HasForbiddenControl(r.Path) || strings.ContainsFunc(r.Path, unicode.IsSpace)) {
		return errx.InvalidField("path", "path must be a URL path (/a/b) of at most "+strconv.Itoa(MaxTakeoverPath)+" characters, without query or fragment")
	}
	return text.Optional("note", r.Note, maxTakeoverNote)
}

func (r AskTakeover) kind() TakeoverKind {
	if r.Kind == "" {
		return KindLogin
	}
	return r.Kind
}

func (r AskTakeover) by() TakeoverBy {
	if r.By == "" {
		return TakeoverByAgent
	}
	return r.By
}

// ValidHost checks a takeover's host: a site's DNS name.
func ValidHost(host string) error {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || len(h) > 253 || !hostRule.MatchString(h) {
		return errx.InvalidField("host", "host must be a site's DNS name (app.example.com)")
	}
	return nil
}

// EndTakeover is what Listo (and the DELETE) asks: the host whose takeover
// a person finished, and who says so (for the audit trail, EventTakeover).
type EndTakeover struct {
	Host string
	// By is the person's name or subject (optional: the API's caller).
	By string
}

func (r EndTakeover) Validate() error {
	if err := ValidHost(r.Host); err != nil {
		return err
	}
	return text.Optional("by", r.By, text.MaxName)
}

// Add puts the takeover in list (replacing the host's older one, whose
// notice is kept: a person was told once; at most MaxTakeovers, newest
// first).
func (r AskTakeover) Add(list Takeovers, now time.Time) Takeovers {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	out := make(Takeovers, 0, len(list)+1)
	t := Takeover{Host: host, Kind: r.kind(), Note: text.Clean(r.Note), By: r.by(), Path: r.Path, Conversation: r.Conversation, At: now}
	for _, l := range list {
		if l.Host == host {
			t.NotifiedAt, t.NotifiedIn = l.NotifiedAt, l.NotifiedIn
		}
	}
	out = append(out, t)
	for _, l := range list {
		if l.Host != host && len(out) < MaxTakeovers {
			out = append(out, l)
		}
	}
	return out
}

// Notified marks the host's takeover as told to a person in conv.
func (l Takeovers) Notified(host string, conv identity.ConversationID, now time.Time) Takeovers {
	out := slices.Clone(l)
	for i := range out {
		if out[i].Host == host {
			out[i].NotifiedAt, out[i].NotifiedIn = &now, &conv
		}
	}
	return out
}

// Without removes the host's takeover.
func (l Takeovers) Without(host string) Takeovers {
	out := Takeovers{}
	for _, t := range l {
		if t.Host != host {
			out = append(out, t)
		}
	}
	return out
}

// hostRule is a takeover's host: labels of letters, digits and -,
// at least two of them.
var hostRule = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

const maxTakeoverNote = 200

// Assignment is an agent on a dock.
type Assignment struct {
	Dock identity.DockID `db:"dock_id" json:"dock_id"`
	// Default: it takes the dock's messages that call nobody.
	Default bool `db:"is_default" json:"default"`
}

// Member is one agent of a dock's team.
type Member struct {
	Agent   identity.AgentID `db:"agent_id" json:"agent_id"`
	Name    string           `db:"name" json:"name"`
	Default bool             `db:"is_default" json:"default"`
}

// ValidName checks an agent name.
func ValidName(name string) error {
	if !nameRule.MatchString(name) {
		return errx.InvalidField("name", "name must be 1-32 lowercase letters, digits, - or _, starting with a letter or digit")
	}
	return nil
}

// Create is the input of Commands.Create.
type Create struct {
	Name        string
	Description string
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (c Create) Validate() error {
	if err := ValidName(c.Name); err != nil {
		return err
	}
	if err := text.Optional("description", c.Description, MaxDescription); err != nil {
		return err
	}
	return text.Optional("created_by", c.CreatedBy, text.MaxName)
}

// Clone is the input of Commands.Clone: a new agent that starts as a copy
// of another's current config.
type Clone struct {
	// Name is the new agent's (same rules as Create).
	Name string
	// Description of the new agent; nil: the source's.
	Description *string
	// Secrets copies the source's secrets too (resealed for the clone,
	// never read back). Off, the clone starts with none: a config that
	// names some (an MCP header's ${NAME}, a model key, a repo credential)
	// fails where it is used until they are set.
	Secrets bool
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (c Clone) Validate() error {
	if err := ValidName(c.Name); err != nil {
		return err
	}
	if c.Description != nil {
		if err := text.Optional("description", *c.Description, MaxDescription); err != nil {
			return err
		}
	}
	return text.Optional("created_by", c.CreatedBy, text.MaxName)
}

// Update changes what is set.
type Update struct {
	Name        *string
	Description *string
}

func (u Update) Validate() error {
	if u.Name != nil {
		if err := ValidName(*u.Name); err != nil {
			return err
		}
	}
	if u.Description != nil {
		return text.Optional("description", *u.Description, MaxDescription)
	}
	return nil
}

// Assign puts an agent on a dock (or changes whether it is the default;
// making it the default demotes the previous one).
type Assign struct {
	Dock    identity.DockID
	Agent   identity.AgentID
	Default bool
}

func (a Assign) Validate() error {
	if a.Dock.IsZero() {
		return errx.InvalidField("dock_id", "dock_id is required")
	}
	if a.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	return nil
}

// Route picks the agent of the dock's team an inbound message is for, or
// says why nobody takes it:
//
//  1. no team: held (HoldNoAgent);
//  2. the first @name of a member in the text;
//  3. previous, the agent the conversation's last routed message went to,
//     while it is still on the team (a chat goes on with whoever it was
//     talking to without calling it again);
//  4. the only member;
//  5. the default;
//  6. else held (HoldNotCalled).
func Route(team []Member, msg string, previous identity.AgentID) (identity.AgentID, string) {
	if len(team) == 0 {
		return identity.AgentID{}, HoldNoAgent
	}
	if m, ok := Mentioned(team, msg); ok {
		return m.Agent, ""
	}
	if !previous.IsZero() {
		for _, m := range team {
			if m.Agent == previous {
				return previous, ""
			}
		}
	}
	if len(team) == 1 {
		return team[0].Agent, ""
	}
	for _, m := range team {
		if m.Default {
			return m.Agent, ""
		}
	}
	return identity.AgentID{}, HoldNotCalled
}

// Mentioned returns the member called first in msg (@name, any case, not
// inside a longer word or handle).
func Mentioned(team []Member, msg string) (Member, bool) {
	lower := strings.ToLower(msg)
	for i := 0; i < len(lower); i++ {
		if lower[i] != '@' || (i > 0 && handleByte(lower[i-1])) {
			continue
		}
		j := i + 1
		for j < len(lower) && handleByte(lower[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		word := lower[i+1 : j]
		for _, m := range team {
			if m.Name == word {
				return m, true
			}
		}
		i = j - 1
	}
	return Member{}, false
}

// handleByte: part of an @handle ("user@pm" is an address, not a call).
func handleByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}
