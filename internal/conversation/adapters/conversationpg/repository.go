// Package conversationpg is the Postgres repository of conversations and
// their customs.
package conversationpg

import (
	"context"
	"strconv"
	"strings"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/pgerr"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/jmoiron/sqlx"
)

const columns = `id, dock_id, channel, external_id, kind, title, access, group_mode, status, last_activity_at, created_at, updated_at`

var notFound = pgerr.Messages{NotFound: "conversation not found", ForeignKey: "dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ conversation.Repository = (*Repository)(nil)

func (r *Repository) Upsert(ctx context.Context, id identity.ConversationID, in conversation.Ensure, status conversation.Status) (conversation.Conversation, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conversation.Conversation{}, false, pgerr.Map(err, "begin upsert")
	}
	defer func() { _ = tx.Rollback() }()
	var row struct {
		conversation.Conversation
		Changed bool `db:"changed"`
	}
	// changed: inserted (xmax = 0) or the title changed (updated_at moved).
	err = tx.GetContext(ctx, &row, `
		INSERT INTO conversations (id, dock_id, channel, external_id, kind, title, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (dock_id, external_id) DO UPDATE
		SET title = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE conversations.title END,
		    updated_at = CASE WHEN EXCLUDED.title <> '' AND EXCLUDED.title <> conversations.title
		                      THEN now() ELSE conversations.updated_at END,
		    last_activity_at = now()
		RETURNING `+columns+`, (xmax = 0 OR updated_at = now()) AS changed`,
		id, in.Dock, in.Channel, in.ExternalID, in.Kind, in.Title, status)
	c := row.Conversation
	if err != nil {
		return c, false, pgerr.MapWith(err, "upsert conversation", notFound)
	}
	if len(in.Handles) > 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO conversation_handles (conversation_id, handle)
			SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, c.ID, in.Handles)
		if err != nil {
			return c, false, pgerr.Map(err, "add conversation handles")
		}
		if n, _ := res.RowsAffected(); n > 0 {
			row.Changed = true
		}
	}
	if err := tx.SelectContext(ctx, &c.Handles, `SELECT handle FROM conversation_handles
		WHERE conversation_id = $1 ORDER BY handle`, c.ID); err != nil {
		return c, false, pgerr.Map(err, "conversation handles")
	}
	if err := tx.Commit(); err != nil {
		return c, false, pgerr.Map(err, "commit upsert")
	}
	return c, row.Changed, nil
}

func (r *Repository) Touch(ctx context.Context, id identity.ConversationID) error {
	res, err := r.db.ExecContext(ctx, `UPDATE conversations SET last_activity_at = now() WHERE id = $1`, id)
	if err != nil {
		return pgerr.MapWith(err, "touch conversation", notFound)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "touch conversation", notFound)
	}
	return nil
}

func (r *Repository) Find(ctx context.Context, id identity.ConversationID) (conversation.Conversation, error) {
	var c conversation.Conversation
	if err := r.db.GetContext(ctx, &c, `SELECT `+columns+` FROM conversations WHERE id = $1`, id); err != nil {
		return c, pgerr.MapWith(err, "find conversation", notFound)
	}
	return c, r.withHandles(ctx, []*conversation.Conversation{&c})
}

func (r *Repository) List(ctx context.Context, f conversation.Filter, page query.Pagination) (query.Paginated[conversation.Conversation], error) {
	where, args := []string{"true"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Dock != nil {
		add("dock_id = ?", *f.Dock)
	}
	if f.Status != nil {
		add("status = ?", *f.Status)
	}
	cond := strings.Join(where, " AND ")
	out := query.Paginated[conversation.Conversation]{
		Items: []conversation.Conversation{},
		Page:  query.Page{Limit: page.Limit, Offset: page.Offset},
	}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM conversations WHERE `+cond, args...); err != nil {
		return out, pgerr.Map(err, "count conversations")
	}
	n := len(args)
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM conversations WHERE `+cond+
		` ORDER BY last_activity_at DESC, id DESC LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2),
		append(args, page.Limit, page.Offset)...)
	if err != nil {
		return out, pgerr.Map(err, "list conversations")
	}
	return out, r.withHandles(ctx, pointers(out.Items))
}

func (r *Repository) Update(ctx context.Context, id identity.ConversationID, in conversation.Update) (conversation.Conversation, error) {
	var c conversation.Conversation
	err := r.db.GetContext(ctx, &c, `
		UPDATE conversations
		SET access = COALESCE($2, access), group_mode = COALESCE($3, group_mode),
		    title = COALESCE($4, title), updated_at = now()
		WHERE id = $1
		RETURNING `+columns, id, in.Access, in.GroupMode, in.Title)
	if err != nil {
		return c, pgerr.MapWith(err, "update conversation", notFound)
	}
	return c, r.withHandles(ctx, []*conversation.Conversation{&c})
}

func (r *Repository) SetStatus(ctx context.Context, id identity.ConversationID, status conversation.Status) (conversation.Conversation, error) {
	var c conversation.Conversation
	err := r.db.GetContext(ctx, &c, `UPDATE conversations SET status = $2, updated_at = now()
		WHERE id = $1 RETURNING `+columns, id, status)
	if err != nil {
		return c, pgerr.MapWith(err, "set conversation status", notFound)
	}
	return c, r.withHandles(ctx, []*conversation.Conversation{&c})
}

func (r *Repository) ForDock(ctx context.Context, dock identity.DockID) ([]conversation.Conversation, error) {
	out := []conversation.Conversation{}
	if err := r.db.SelectContext(ctx, &out, `SELECT `+columns+` FROM conversations WHERE dock_id = $1`, dock); err != nil {
		return nil, pgerr.Map(err, "dock conversations")
	}
	return out, r.withHandles(ctx, pointers(out))
}

func (r *Repository) withHandles(ctx context.Context, convs []*conversation.Conversation) error {
	if len(convs) == 0 {
		return nil
	}
	ids := make([]string, len(convs))
	byID := make(map[identity.ConversationID]*conversation.Conversation, len(convs))
	for i, c := range convs {
		ids[i] = c.ID.String()
		c.Handles = []string{}
		byID[c.ID] = c
	}
	var rows []struct {
		Conversation identity.ConversationID `db:"conversation_id"`
		Handle       string                  `db:"handle"`
	}
	if err := r.db.SelectContext(ctx, &rows, `SELECT conversation_id, handle FROM conversation_handles
		WHERE conversation_id = ANY($1::uuid[]) ORDER BY handle`, ids); err != nil {
		return pgerr.Map(err, "conversation handles")
	}
	for _, row := range rows {
		if c := byID[row.Conversation]; c != nil {
			c.Handles = append(c.Handles, row.Handle)
		}
	}
	return nil
}

func pointers(cs []conversation.Conversation) []*conversation.Conversation {
	out := make([]*conversation.Conversation, len(cs))
	for i := range cs {
		out[i] = &cs[i]
	}
	return out
}

// ---- customs ----

// Customs is the Postgres repository of dock policies and rules.
type Customs struct{ db *sqlx.DB }

func NewCustoms(db *sqlx.DB) *Customs { return &Customs{db: db} }

var _ conversation.CustomsRepository = (*Customs)(nil)

var ruleMessages = pgerr.Messages{NotFound: "rule not found", ForeignKey: "dock not found", Unique: "that rule already exists"}

func (r *Customs) Customs(ctx context.Context, dock identity.DockID) (conversation.Policy, bool, []conversation.Rule, error) {
	var p conversation.Policy
	err := r.db.GetContext(ctx, &p, `SELECT dock_id, default_access, group_mode FROM customs_policies WHERE dock_id = $1`, dock)
	ok := err == nil
	if err != nil && !pgerr.IsNoRows(err) {
		return p, false, nil, pgerr.Map(err, "customs policy")
	}
	rules := []conversation.Rule{}
	if err := r.db.SelectContext(ctx, &rules, `SELECT id, dock_id, effect, pattern, created_at FROM customs_rules
		WHERE dock_id = $1 ORDER BY effect DESC, pattern`, dock); err != nil {
		return p, ok, nil, pgerr.Map(err, "customs rules")
	}
	return p, ok, rules, nil
}

func (r *Customs) SavePolicy(ctx context.Context, p conversation.Policy) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO customs_policies (dock_id, default_access, group_mode) VALUES ($1, $2, $3)
		ON CONFLICT (dock_id) DO UPDATE SET default_access = EXCLUDED.default_access,
		    group_mode = EXCLUDED.group_mode, updated_at = now()`, p.Dock, p.Default, p.GroupMode)
	return pgerr.MapWith(err, "save customs policy", ruleMessages)
}

func (r *Customs) AddRule(ctx context.Context, rule conversation.Rule) (conversation.Rule, error) {
	var out conversation.Rule
	err := r.db.GetContext(ctx, &out, `INSERT INTO customs_rules (id, dock_id, effect, pattern)
		VALUES ($1, $2, $3, $4) RETURNING id, dock_id, effect, pattern, created_at`,
		rule.ID, rule.Dock, rule.Effect, rule.Pattern)
	return out, pgerr.MapWith(err, "add rule", ruleMessages)
}

func (r *Customs) RemoveRule(ctx context.Context, id identity.RuleID) (conversation.Rule, error) {
	var out conversation.Rule
	err := r.db.GetContext(ctx, &out, `DELETE FROM customs_rules WHERE id = $1
		RETURNING id, dock_id, effect, pattern, created_at`, id)
	return out, pgerr.MapWith(err, "remove rule", ruleMessages)
}

func (r *Customs) CountRules(ctx context.Context, dock identity.DockID) (int, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT count(*) FROM customs_rules WHERE dock_id = $1`, dock)
	return n, pgerr.Map(err, "count rules")
}
