package conversation

import (
	"context"

	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
)

type Commands interface {
	// Arrive ensures the conversation of an arriving message, re-decides its
	// customs status and says whether the message may wake the crew.
	Arrive(ctx context.Context, input Arrive) (Arrival, error)
	Update(ctx context.Context, conversation identity.ConversationID, input Update) (Conversation, error)
	// Touch records activity (a message in or out) now.
	Touch(ctx context.Context, conversation identity.ConversationID) error
}

type Queries interface {
	Find(ctx context.Context, conversation identity.ConversationID) (Conversation, error)
	List(ctx context.Context, filter Filter, page query.Pagination) (query.Paginated[Conversation], error)
}

// CustomsCommands configure a dock's customs; every change re-decides the
// status of the dock's conversations.
type CustomsCommands interface {
	SetPolicy(ctx context.Context, dock identity.DockID, input SetPolicy) (Customs, error)
	AddRule(ctx context.Context, input AddRule) (Rule, error)
	RemoveRule(ctx context.Context, rule identity.RuleID) error
}

type CustomsQueries interface {
	// Customs returns the dock's policy (DefaultPolicy when never set) and rules.
	Customs(ctx context.Context, dock identity.DockID) (Customs, error)
}

type Repository interface {
	// Upsert inserts the conversation (with status) or, on (dock, external
	// id) conflict, refreshes a non-empty title. Either way it adds the
	// handles, records activity and returns the stored row with all its
	// handles; changed is true when it was inserted or its title changed.
	Upsert(ctx context.Context, conversation identity.ConversationID, input Ensure, status Status) (c Conversation, changed bool, err error)
	Touch(ctx context.Context, conversation identity.ConversationID) error
	Find(ctx context.Context, conversation identity.ConversationID) (Conversation, error)
	List(ctx context.Context, filter Filter, page query.Pagination) (query.Paginated[Conversation], error)
	Update(ctx context.Context, conversation identity.ConversationID, input Update) (Conversation, error)
	SetStatus(ctx context.Context, conversation identity.ConversationID, status Status) (Conversation, error)
	// ForDock returns every conversation of a dock with its handles.
	ForDock(ctx context.Context, dock identity.DockID) ([]Conversation, error)
}

type CustomsRepository interface {
	// Customs returns the stored policy (ok=false when never set) and rules.
	Customs(ctx context.Context, dock identity.DockID) (policy Policy, ok bool, rules []Rule, err error)
	SavePolicy(ctx context.Context, policy Policy) error
	AddRule(ctx context.Context, rule Rule) (Rule, error)
	// RemoveRule deletes a rule and returns it (its dock).
	RemoveRule(ctx context.Context, rule identity.RuleID) (Rule, error)
	CountRules(ctx context.Context, dock identity.DockID) (int, error)
}
