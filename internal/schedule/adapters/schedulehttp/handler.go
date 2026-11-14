// Package schedulehttp is the admin API of schedules.
package schedulehttp

import (
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/gofiber/fiber/v3"
)

type Handler struct {
	commands schedule.Commands
	queries  schedule.Queries
}

func New(c schedule.Commands, q schedule.Queries) *Handler { return &Handler{commands: c, queries: q} }

// Register mounts the routes on r (already behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/schedules", httpx.RequireFunc(permission.SchedulesRead, h.list))
	r.Post("/api/v1/schedules", httpx.RequireFunc(permission.SchedulesWrite, h.create))
	r.Post("/api/v1/schedules/preview", httpx.RequireFunc(permission.SchedulesRead, h.preview))
	r.Get("/api/v1/schedules/:id", httpx.RequireFunc(permission.SchedulesRead, h.find))
	r.Patch("/api/v1/schedules/:id", httpx.RequireFunc(permission.SchedulesWrite, h.update))
	r.Delete("/api/v1/schedules/:id", httpx.RequireFunc(permission.SchedulesWrite, h.delete))
	r.Post("/api/v1/schedules/:id/run", httpx.RequireFunc(permission.SchedulesWrite, h.run))
	r.Get("/api/v1/conversations/:id/schedules", httpx.RequireFunc(permission.SchedulesRead, h.ofConversation))
}

func (h *Handler) list(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	var f schedule.Filter
	if raw := c.Query("conversation_id"); raw != "" {
		id, err := identity.ParseConversationID(raw)
		if err != nil {
			return err
		}
		f.Conversation = &id
	}
	out, err := h.queries.List(c.Context(), org, f, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) ofConversation(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	id, err := identity.ParseConversationID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	out, err := h.queries.List(c.Context(), org, schedule.Filter{Conversation: &id}, page)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type createRequest struct {
	Name           string                  `json:"name"`
	ConversationID identity.ConversationID `json:"conversation_id"`
	AgentID        *identity.AgentID       `json:"agent_id"`
	Cron           string                  `json:"cron"`
	Timezone       string                  `json:"timezone"`
	Text           string                  `json:"text"`
	Enabled        *bool                   `json:"enabled"`
}

func (h *Handler) create(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	enabled := req.Enabled == nil || *req.Enabled
	out, err := h.commands.Create(c.Context(), org, schedule.Create{Name: req.Name, Conversation: req.ConversationID,
		Agent: req.AgentID, Cron: req.Cron, Timezone: req.Timezone, Text: req.Text, Enabled: enabled})
	if err != nil {
		return err
	}
	return httpx.Created(c, out.ID.String(), out)
}

func (h *Handler) find(c fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	out, err := h.queries.Find(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type updateRequest struct {
	Name *string `json:"name"`
	// AgentID: "" clears the agent, absent keeps it.
	AgentID  *string `json:"agent_id"`
	Cron     *string `json:"cron"`
	Timezone *string `json:"timezone"`
	Text     *string `json:"text"`
	Enabled  *bool   `json:"enabled"`
}

func (h *Handler) update(c fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	in := schedule.Update{Name: req.Name, Cron: req.Cron, Timezone: req.Timezone, Text: req.Text, Enabled: req.Enabled}
	if req.AgentID != nil {
		var agent identity.AgentID
		if *req.AgentID != "" {
			if agent, err = identity.ParseAgentID(*req.AgentID); err != nil {
				return err
			}
		}
		in.Agent = &agent
	}
	out, err := h.commands.Update(c.Context(), org, id, in)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (h *Handler) delete(c fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	if err := h.commands.Delete(c.Context(), org, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

// run posts the schedule's message now and answers the ship.
func (h *Handler) run(c fiber.Ctx) error {
	org, id, err := target(c)
	if err != nil {
		return err
	}
	out, err := h.commands.Run(c.Context(), org, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

type previewRequest struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

// preview answers the next fire times of an expression, saving nothing.
func (h *Handler) preview(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var req previewRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	times, err := h.queries.Preview(c.Context(), org, schedule.PreviewInput{Cron: req.Cron, Timezone: req.Timezone})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, struct {
		Preview []time.Time `json:"preview"`
	}{times})
}

func target(c fiber.Ctx) (identity.OrganizationID, identity.ScheduleID, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return identity.OrganizationID{}, identity.ScheduleID{}, err
	}
	id, err := identity.ParseScheduleID(httpx.Param(c, "id"))
	return org, id, err
}
