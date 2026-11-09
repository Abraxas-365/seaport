// Package agent is the organization's library of agents. An agent is the
// identity crew tokens connect as; it works only through the docks it is
// assigned to (its team on each dock). Route decides, for each inbound
// message, which agent of the dock's team it is for.
package agent

import (
	"regexp"
	"strings"
	"time"

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
// customs hold: readable in the history, never delivered later).
const (
	HoldNoAgent   = "no agent is assigned to this dock"
	HoldNotCalled = "no agent was called (mention one with @name)"
	// HoldAgentGone: it was waiting for an agent taken off the dock or
	// deleted.
	HoldAgentGone = "its agent left the dock"
)

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
}

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
