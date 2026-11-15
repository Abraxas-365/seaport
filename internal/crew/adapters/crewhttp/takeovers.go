package crewhttp

import (
	"net/http"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Takeovers serves POST /crew/takeovers (behind crew auth): the MCP
// ask_takeover over plain HTTP, for the connector's page detector (the
// profile's browser_detect), which has no MCP client. Agent tokens only:
// a takeover is the agent's.
type Takeovers struct {
	Agents interface {
		agent.Commands
		agent.Queries
	}
	Conversations conversation.Queries
}

// Ask is POST /crew/takeovers.
func (h Takeovers) Ask(c fiber.Ctx) error {
	p, err := agentOf(c)
	if err != nil {
		return err
	}
	var in crew.TakeoverRequest
	if err := httpx.DecodeJSON(c, &in); err != nil {
		return err
	}
	ask := in.Ask()
	if ask.Conversation != nil {
		if err := h.visible(c, p.Organization, p.Agent, *ask.Conversation); err != nil {
			return err
		}
	}
	a, err := h.Agents.RequestTakeover(c.Context(), p.Organization, p.Agent, ask)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusCreated, crew.AnswerTakeover(a, ask))
}

// visible refuses a conversation of a dock the agent isn't on (not found,
// like the MCP tools).
func (h Takeovers) visible(c fiber.Ctx, org identity.OrganizationID, id identity.AgentID, conv identity.ConversationID) error {
	cv, err := h.Conversations.Find(c.Context(), org, conv)
	if err != nil {
		return err
	}
	a, err := h.Agents.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	for _, d := range a.Docks {
		if d.Dock == cv.Dock {
			return nil
		}
	}
	return errx.NotFound("conversation not found")
}
