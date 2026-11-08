// Package conversationsvc implements the conversation ports: chats and the
// customs that decide which of them, and which messages, wake the crew.
package conversationsvc

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/text"
)

type Service struct {
	repo    conversation.Repository
	customs conversation.CustomsRepository
	events  event.Publisher

	// locks serializes customs decisions per dock, so a status computed
	// from old rules can't land after a rule change re-decided the dock.
	mu    sync.Mutex
	locks map[identity.DockID]*sync.Mutex
}

func New(repo conversation.Repository, customs conversation.CustomsRepository, events event.Publisher) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, customs: customs, events: events, locks: map[identity.DockID]*sync.Mutex{}}
}

var (
	_ conversation.Commands        = (*Service)(nil)
	_ conversation.Queries         = (*Service)(nil)
	_ conversation.CustomsCommands = (*Service)(nil)
	_ conversation.CustomsQueries  = (*Service)(nil)
)

func (s *Service) lock(dock identity.DockID) func() {
	s.mu.Lock()
	l, ok := s.locks[dock]
	if !ok {
		l = &sync.Mutex{}
		s.locks[dock] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (s *Service) Arrive(ctx context.Context, input conversation.Arrive) (conversation.Arrival, error) {
	if err := input.Validate(); err != nil {
		return conversation.Arrival{}, err
	}
	chat := input.Chat
	chat.ExternalID = text.Clean(chat.ExternalID)
	chat.Title = text.Clean(chat.Title)
	chat.Handles = conversation.CleanHandles(append(chat.Handles, chat.ExternalID))
	senders := conversation.CleanHandles(input.Senders)

	defer s.lock(chat.Dock)()
	customs, err := s.load(ctx, chat.Dock)
	if err != nil {
		return conversation.Arrival{}, err
	}
	initial := customs.Decide(chat.Kind, conversation.AccessAuto, chat.Handles)
	conv, changed, err := s.repo.Upsert(ctx, identity.NewConversationID(), chat, initial)
	if err != nil {
		return conversation.Arrival{}, errx.Wrap(err, "ensure conversation", errx.TypeInternal)
	}
	// An existing chat may have learned handles or be under newer rules.
	before := conv.Status
	if conv, err = s.redecide(ctx, customs, conv); err != nil {
		return conversation.Arrival{}, err
	}
	if changed && conv.Status == before {
		s.events.Publish(conversation.EventUpdated, conv) // redecide published otherwise
	}
	out := conversation.Arrival{Conversation: conv}
	if !conv.Allowed() {
		out.Hold = "conversation " + string(conv.Status)
		return out, nil
	}
	out.Hold = customs.Admit(conv, senders, input.Addressed)
	return out, nil
}

func (s *Service) Update(ctx context.Context, id identity.ConversationID, input conversation.Update) (conversation.Conversation, error) {
	if err := input.Validate(); err != nil {
		return conversation.Conversation{}, err
	}
	if input.Title != nil {
		t := text.Clean(*input.Title)
		input.Title = &t
	}
	current, err := s.repo.Find(ctx, id)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if input.GroupMode != nil && *input.GroupMode != conversation.GroupInherit && current.Kind != conversation.KindGroup {
		return conversation.Conversation{}, errx.InvalidField("group_mode", "group_mode only applies to groups")
	}
	defer s.lock(current.Dock)()
	c, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "update conversation", errx.TypeInternal)
	}
	customs, err := s.load(ctx, c.Dock)
	if err != nil {
		return conversation.Conversation{}, err
	}
	before := c.Status
	if c, err = s.redecide(ctx, customs, c); err != nil {
		return conversation.Conversation{}, err
	}
	if c.Status == before {
		s.events.Publish(conversation.EventUpdated, c)
	}
	return c, nil
}

func (s *Service) Touch(ctx context.Context, id identity.ConversationID) error {
	if id.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	return errx.Wrap(s.repo.Touch(ctx, id), "touch conversation", errx.TypeInternal)
}

func (s *Service) Find(ctx context.Context, id identity.ConversationID) (conversation.Conversation, error) {
	c, err := s.repo.Find(ctx, id)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	return c, nil
}

func (s *Service) List(ctx context.Context, filter conversation.Filter, page query.Pagination) (query.Paginated[conversation.Conversation], error) {
	if filter.Status != nil && !filter.Status.Valid() {
		return query.Paginated[conversation.Conversation]{}, errx.InvalidField("status", "status must be allowed, pending or denied")
	}
	out, err := s.repo.List(ctx, filter, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list conversations", errx.TypeInternal)
	}
	return out, nil
}

// ---- customs ----

func (s *Service) Customs(ctx context.Context, dock identity.DockID) (conversation.Customs, error) {
	if dock.IsZero() {
		return conversation.Customs{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	return s.load(ctx, dock)
}

func (s *Service) SetPolicy(ctx context.Context, dock identity.DockID, input conversation.SetPolicy) (conversation.Customs, error) {
	if err := input.Validate(); err != nil {
		return conversation.Customs{}, err
	}
	if dock.IsZero() {
		return conversation.Customs{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	defer s.lock(dock)()
	customs, err := s.load(ctx, dock)
	if err != nil {
		return conversation.Customs{}, err
	}
	if input.Default != nil {
		customs.Policy.Default = *input.Default
	}
	if input.GroupMode != nil {
		customs.Policy.GroupMode = *input.GroupMode
	}
	if err := s.customs.SavePolicy(ctx, customs.Policy); err != nil {
		return conversation.Customs{}, errx.Wrap(err, "save customs policy", errx.TypeInternal)
	}
	return customs, s.customsChanged(ctx, customs)
}

func (s *Service) AddRule(ctx context.Context, input conversation.AddRule) (conversation.Rule, error) {
	if err := input.Validate(); err != nil {
		return conversation.Rule{}, err
	}
	defer s.lock(input.Dock)()
	n, err := s.customs.CountRules(ctx, input.Dock)
	if err != nil {
		return conversation.Rule{}, errx.Wrap(err, "count rules", errx.TypeInternal)
	}
	if n >= conversation.MaxRules {
		return conversation.Rule{}, errx.Business("a dock holds at most 1000 rules")
	}
	rule, err := s.customs.AddRule(ctx, conversation.Rule{ID: identity.NewRuleID(), Dock: input.Dock,
		Effect: input.Effect, Pattern: conversation.NormalizePattern(input.Pattern)})
	if err != nil {
		return conversation.Rule{}, errx.Wrap(err, "add rule", errx.TypeInternal)
	}
	customs, err := s.load(ctx, input.Dock)
	if err != nil {
		return conversation.Rule{}, err
	}
	return rule, s.customsChanged(ctx, customs)
}

func (s *Service) RemoveRule(ctx context.Context, id identity.RuleID) error {
	if id.IsZero() {
		return errx.InvalidField("rule_id", "rule_id is required")
	}
	rule, err := s.customs.RemoveRule(ctx, id)
	if err != nil {
		return errx.Wrap(err, "remove rule", errx.TypeInternal)
	}
	defer s.lock(rule.Dock)()
	customs, err := s.load(ctx, rule.Dock)
	if err != nil {
		return err
	}
	return s.customsChanged(ctx, customs)
}

func (s *Service) load(ctx context.Context, dock identity.DockID) (conversation.Customs, error) {
	policy, ok, rules, err := s.customs.Customs(ctx, dock)
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
func (s *Service) redecide(ctx context.Context, customs conversation.Customs, c conversation.Conversation) (conversation.Conversation, error) {
	status := customs.Decide(c.Kind, c.Access, c.Handles)
	if status == c.Status {
		return c, nil
	}
	updated, err := s.repo.SetStatus(ctx, c.ID, status)
	if err != nil {
		return conversation.Conversation{}, errx.Wrap(err, "set conversation status", errx.TypeInternal)
	}
	slog.Info("customs status changed", "conversation", c.ID, "title", c.Title,
		"from", c.Status, "to", status, "handles", strings.Join(c.Handles, ","))
	s.events.Publish(conversation.EventUpdated, updated)
	return updated, nil
}

// customsChanged announces a dock's new customs and re-decides its chats.
func (s *Service) customsChanged(ctx context.Context, customs conversation.Customs) error {
	s.events.Publish(conversation.EventCustoms, conversation.CustomsEvent{Customs: customs})
	return s.redecideDock(ctx, customs)
}

func (s *Service) redecideDock(ctx context.Context, customs conversation.Customs) error {
	convs, err := s.repo.ForDock(ctx, customs.Policy.Dock)
	if err != nil {
		return errx.Wrap(err, "list dock conversations", errx.TypeInternal)
	}
	for _, c := range convs {
		if _, err := s.redecide(ctx, customs, c); err != nil {
			return err
		}
	}
	return nil
}
