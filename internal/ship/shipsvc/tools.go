package shipsvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Agent tools: a dock's switches (dock.Tools) of what agents may read
// besides the messages they are handed. Checked here, where every agent
// read passes (MCP tools, crew routes, the connector's deliveries), so no
// adapter can forget them.

// toolsCache keeps the switches of the docks one call already read.
type toolsCache map[identity.DockID]dock.Tools

// toolsOf reads the switches of a dock (all on without docks: tests and
// deployments that wire none).
func (s *Service) toolsOf(ctx context.Context, org identity.OrganizationID, id identity.DockID, cache toolsCache) (dock.Tools, error) {
	if s.docks == nil {
		return dock.AllTools, nil
	}
	if t, ok := cache[id]; ok {
		return t, nil
	}
	d, err := s.docks.Find(ctx, org, id)
	if err != nil {
		return dock.Tools{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if cache != nil {
		cache[id] = d.Tools
	}
	return d.Tools, nil
}

func (s *Service) AgentHistory(ctx context.Context, org identity.OrganizationID, input ship.AgentHistory) (query.Paginated[ship.Ship], error) {
	if err := input.Validate(); err != nil {
		return query.Paginated[ship.Ship]{}, err
	}
	if input.Agent == nil {
		if input.Thread {
			return s.ThreadHistory(ctx, org, input.Conversation, input.Page)
		}
		return s.History(ctx, org, input.Conversation, input.Page)
	}
	c, err := s.convs.Find(ctx, org, input.Conversation)
	if err != nil {
		return query.Paginated[ship.Ship]{}, err
	}
	if err := s.onDock(ctx, org, *input.Agent, c.Dock); err != nil {
		return query.Paginated[ship.Ship]{}, err
	}
	cache := toolsCache{}
	t, err := s.toolsOf(ctx, org, c.Dock, cache)
	if err != nil {
		return query.Paginated[ship.Ship]{}, err
	}
	if !t.History {
		return query.Paginated[ship.Ship]{}, errx.Forbidden("the owner of this conversation's dock doesn't let agents read its history; work with the messages you were handed")
	}
	doors := []conversation.Conversation{c}
	if input.Thread {
		if doors, err = s.convs.Thread(ctx, org, c.ID); err != nil {
			return query.Paginated[ship.Ship]{}, errx.Wrap(err, "find thread", errx.TypeInternal)
		}
	}
	ids := make([]identity.ConversationID, 0, len(doors))
	for _, d := range doors {
		t, err := s.toolsOf(ctx, org, d.Dock, cache)
		if err != nil {
			return query.Paginated[ship.Ship]{}, err
		}
		if t.History {
			ids = append(ids, d.ID)
		}
	}
	out, err := s.repo.HistoryOf(ctx, org, ids, input.Page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list ships", errx.TypeInternal)
	}
	if err := s.withhold(ctx, org, out.Items, cache); err != nil {
		return out, err
	}
	return out, nil
}

// withhold marks the attachments of ships whose dock keeps files from
// agents: the agent learns a file came, not what it holds.
func (s *Service) withhold(ctx context.Context, org identity.OrganizationID, ships []ship.Ship, cache toolsCache) error {
	docks := map[identity.ConversationID]identity.DockID{}
	for i := range ships {
		if len(ships[i].Attachments) == 0 {
			continue
		}
		d, ok := docks[ships[i].Conversation]
		if !ok {
			c, err := s.convs.Find(ctx, org, ships[i].Conversation)
			if err != nil {
				return errx.Wrap(err, "find ship's conversation", errx.TypeInternal)
			}
			d, docks[c.ID] = c.Dock, c.Dock
		}
		t, err := s.toolsOf(ctx, org, d, cache)
		if err != nil {
			return err
		}
		if t.Attachments {
			continue
		}
		for j := range ships[i].Attachments {
			ships[i].Attachments[j].Withheld = true
		}
	}
	return nil
}

// noHistory names the doors of a thread whose dock keeps history from
// agents.
func (s *Service) noHistory(ctx context.Context, org identity.OrganizationID, doors []conversation.Conversation, cache toolsCache) ([]identity.ConversationID, error) {
	var out []identity.ConversationID
	for _, c := range doors {
		t, err := s.toolsOf(ctx, org, c.Dock, cache)
		if err != nil {
			return nil, err
		}
		if !t.History {
			out = append(out, c.ID)
		}
	}
	return out, nil
}
