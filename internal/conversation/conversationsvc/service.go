// Package conversationsvc implements the conversation ports: chats and the
// customs that decide which of them, and which messages, wake the crew.
package conversationsvc

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo    conversation.Repository
	customs conversation.CustomsRepository
	events  event.Publisher

	// locks serializes customs decisions per dock (in every process).
	locks conversation.DockLocker
	// gate delivers what waited while a chat was pending (Bind; nil: none).
	gate conversation.Gate
}

func New(repo conversation.Repository, customs conversation.CustomsRepository, locks conversation.DockLocker, events event.Publisher) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, customs: customs, locks: locks, events: events}
}

// Bind wires the gate (the ship service, built after this one).
func (s *Service) Bind(gate conversation.Gate) { s.gate = gate }

var (
	_ conversation.Commands        = (*Service)(nil)
	_ conversation.Queries         = (*Service)(nil)
	_ conversation.CustomsCommands = (*Service)(nil)
	_ conversation.CustomsQueries  = (*Service)(nil)
)

func (s *Service) lock(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (func(), error) {
	unlock, err := s.locks.Lock(ctx, org, dock)
	if err != nil {
		return nil, errx.Wrap(err, "lock dock customs", errx.TypeInternal)
	}
	return unlock, nil
}

func (s *Service) Arrive(ctx context.Context, org identity.OrganizationID, input conversation.Arrive) (conversation.Arrival, error) {
	if err := input.Validate(); err != nil {
		return conversation.Arrival{}, err
	}
	chat := input.Chat
	chat.ExternalID = text.Clean(chat.ExternalID)
	chat.Title = text.Clean(chat.Title)
	chat.Handles = conversation.CleanHandles(append(chat.Handles, chat.ExternalID))
	senders := conversation.CleanHandles(input.Senders)

	unlock, err := s.lock(ctx, org, chat.Dock)
	if err != nil {
		return conversation.Arrival{}, err
	}
	defer unlock()
	customs, err := s.load(ctx, org, chat.Dock)
	if err != nil {
		return conversation.Arrival{}, err
	}
	initial := customs.Decide(chat.Kind, conversation.AccessAuto, chat.Handles)
	conv, changed, err := s.repo.Upsert(ctx, org, identity.NewConversationID(), chat, initial)
	if err != nil {
		return conversation.Arrival{}, errx.Wrap(err, "ensure conversation", errx.TypeInternal)
	}
	// An existing chat may have learned handles or be under newer rules.
	before := conv.Status
	if conv, err = s.redecide(ctx, org, customs, conv); err != nil {
		return conversation.Arrival{}, err
	}
	if changed && conv.Status == before {
		s.events.Publish(org, conversation.EventUpdated, conv) // redecide published otherwise
	}
	out := conversation.Arrival{Conversation: conv}
	if !conv.Allowed() {
		out.Hold = conversation.HoldDenied
		if conv.Status == conversation.StatusPending {
			out.Hold = conversation.HoldPending
		}
		return out, nil
	}
	out.Hold = customs.Admit(conv, senders, input.Addressed)
	return out, nil
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.ConversationID, input conversation.Update) (conversation.Conversation, error) {
	if err := input.Validate(); err != nil {
		return conversation.Conversation{}, err
	}
	if input.Title != nil {
		t := text.Clean(*input.Title)
		input.Title = &t
	}
	current, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if input.GroupMode != nil && *input.GroupMode != conversation.GroupInherit && current.Kind != conversation.KindGroup {
		return conversation.Conversation{}, errx.InvalidField("group_mode", "group_mode only applies to groups")
	}
	unlock, err := s.lock(ctx, org, current.Dock)
	if err != nil {
		return conversation.Conversation{}, err
	}
	defer unlock()
	c, err := s.repo.Update(ctx, org, id, input)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "update conversation", errx.TypeInternal)
	}
	customs, err := s.load(ctx, org, c.Dock)
	if err != nil {
		return conversation.Conversation{}, err
	}
	before := c.Status
	if c, err = s.redecide(ctx, org, customs, c); err != nil {
		return conversation.Conversation{}, err
	}
	if c.Status == before {
		s.events.Publish(org, conversation.EventUpdated, c)
	}
	return c, nil
}

func (s *Service) Touch(ctx context.Context, org identity.OrganizationID, id identity.ConversationID) error {
	if id.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	return errx.Wrap(s.repo.Touch(ctx, org, id), "touch conversation", errx.TypeInternal)
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.ConversationID) (conversation.Conversation, error) {
	c, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	return c, nil
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, filter conversation.Filter, page query.Pagination) (query.Paginated[conversation.Conversation], error) {
	if filter.Status != nil && !filter.Status.Valid() {
		return query.Paginated[conversation.Conversation]{}, errx.InvalidField("status", "status must be allowed, pending or denied")
	}
	out, err := s.repo.List(ctx, org, filter, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list conversations", errx.TypeInternal)
	}
	return out, nil
}

// ---- customs ----

func (s *Service) Customs(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (conversation.Customs, error) {
	if dock.IsZero() {
		return conversation.Customs{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	return s.load(ctx, org, dock)
}

func (s *Service) SetPolicy(ctx context.Context, org identity.OrganizationID, dock identity.DockID, input conversation.SetPolicy) (conversation.Customs, error) {
	if err := input.Validate(); err != nil {
		return conversation.Customs{}, err
	}
	if dock.IsZero() {
		return conversation.Customs{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	unlock, err := s.lock(ctx, org, dock)
	if err != nil {
		return conversation.Customs{}, err
	}
	defer unlock()
	customs, err := s.load(ctx, org, dock)
	if err != nil {
		return conversation.Customs{}, err
	}
	if input.Default != nil {
		customs.Policy.Default = *input.Default
	}
	if input.GroupMode != nil {
		customs.Policy.GroupMode = *input.GroupMode
	}
	if err := s.customs.SavePolicy(ctx, org, customs.Policy); err != nil {
		return conversation.Customs{}, errx.Wrap(err, "save customs policy", errx.TypeInternal)
	}
	return customs, s.customsChanged(ctx, org, customs)
}

func (s *Service) AddRule(ctx context.Context, org identity.OrganizationID, input conversation.AddRule) (conversation.Rule, error) {
	if err := input.Validate(); err != nil {
		return conversation.Rule{}, err
	}
	unlock, err := s.lock(ctx, org, input.Dock)
	if err != nil {
		return conversation.Rule{}, err
	}
	defer unlock()
	if _, err := s.load(ctx, org, input.Dock); err != nil {
		return conversation.Rule{}, err
	}
	n, err := s.customs.CountRules(ctx, org, input.Dock)
	if err != nil {
		return conversation.Rule{}, errx.Wrap(err, "count rules", errx.TypeInternal)
	}
	if n >= conversation.MaxRules {
		return conversation.Rule{}, errx.Business("a dock holds at most 1000 rules")
	}
	rule, err := s.customs.AddRule(ctx, org, conversation.Rule{ID: identity.NewRuleID(), Dock: input.Dock,
		Effect: input.Effect, Pattern: conversation.NormalizePattern(input.Pattern)})
	if err != nil {
		return conversation.Rule{}, errx.Wrap(err, "add rule", errx.TypeInternal)
	}
	customs, err := s.load(ctx, org, input.Dock)
	if err != nil {
		return conversation.Rule{}, err
	}
	return rule, s.customsChanged(ctx, org, customs)
}

func (s *Service) RemoveRule(ctx context.Context, org identity.OrganizationID, id identity.RuleID) error {
	if id.IsZero() {
		return errx.InvalidField("rule_id", "rule_id is required")
	}
	rule, err := s.customs.RemoveRule(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "remove rule", errx.TypeInternal)
	}
	unlock, err := s.lock(ctx, org, rule.Dock)
	if err != nil {
		return err
	}
	defer unlock()
	customs, err := s.load(ctx, org, rule.Dock)
	if err != nil {
		return err
	}
	return s.customsChanged(ctx, org, customs)
}

func (s *Service) load(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (conversation.Customs, error) {
	policy, ok, rules, err := s.customs.Customs(ctx, org, dock)
	if err != nil {
		return conversation.Customs{}, errx.Wrap(err, "load customs", errx.TypeInternal)
	}
	if !ok {
		policy = conversation.DefaultPolicy(dock)
	}
	if rules == nil {
		rules = []conversation.Rule{}
	}
	return conversation.Customs{Policy: policy, Rules: rules}, nil
}

// redecide stores a changed customs status. Callers hold the dock lock.
func (s *Service) redecide(ctx context.Context, org identity.OrganizationID, customs conversation.Customs, c conversation.Conversation) (conversation.Conversation, error) {
	status := customs.Decide(c.Kind, c.Access, c.Handles)
	if status == c.Status {
		return c, nil
	}
	updated, err := s.repo.SetStatus(ctx, org, c.ID, status)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "set conversation status", errx.TypeInternal)
	}
	slog.Info("customs status changed", "conversation", c.ID, "title", c.Title,
		"from", c.Status, "to", status, "handles", strings.Join(c.Handles, ","))
	s.events.Publish(org, conversation.EventUpdated, updated)
	if updated.Allowed() && s.gate != nil {
		// The status is stored: a failed release leaves those messages
		// held (readable with history), it doesn't undo the decision.
		if _, err := s.gate.ReleaseHeld(ctx, org, updated.ID); err != nil {
			slog.Error("deliver what waited at customs", "conversation", updated.ID, "err", err)
		}
	}
	return updated, nil
}

// customsChanged announces a dock's new customs and re-decides its chats.
func (s *Service) customsChanged(ctx context.Context, org identity.OrganizationID, customs conversation.Customs) error {
	s.events.Publish(org, conversation.EventCustoms, conversation.CustomsEvent{Customs: customs})
	return s.redecideDock(ctx, org, customs)
}

func (s *Service) redecideDock(ctx context.Context, org identity.OrganizationID, customs conversation.Customs) error {
	convs, err := s.repo.ForDock(ctx, org, customs.Policy.Dock)
	if err != nil {
		return errx.Wrap(err, "list dock conversations", errx.TypeInternal)
	}
	for _, c := range convs {
		if _, err := s.redecide(ctx, org, customs, c); err != nil {
			return err
		}
	}
	return nil
}
