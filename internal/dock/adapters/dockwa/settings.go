package dockwa

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

// DefaultEmoji is the reaction of a dock that never chose one (as GitHub
// and Slack docks react).
const DefaultEmoji = "👀"

// maxEmoji bounds a reaction in bytes (the longest emoji sequences, a
// family or a flag with tags, are about 30).
const maxEmoji = 64

// Settings are what a WhatsApp dock does to a message once an agent has
// it (migration 00047). Both are off until the owner turns them on.
type Settings struct {
	// React: react to the message with Emoji.
	React bool   `db:"react" json:"react"`
	Emoji string `db:"emoji" json:"emoji"`
	// MarkRead: send the read receipt (blue ticks). When the account's
	// read receipts are off WhatsApp keeps it private.
	MarkRead bool `db:"mark_read" json:"mark_read"`
}

// DefaultSettings are a dock's until it saves its own.
func DefaultSettings() Settings { return Settings{Emoji: DefaultEmoji} }

// SettingsPatch changes some settings (nil: unchanged).
type SettingsPatch struct {
	React    *bool   `json:"react,omitempty"`
	Emoji    *string `json:"emoji,omitempty"`
	MarkRead *bool   `json:"mark_read,omitempty"`
}

func (p SettingsPatch) Validate() error {
	if p.React == nil && p.Emoji == nil && p.MarkRead == nil {
		return errx.Validation("nothing to update")
	}
	if p.Emoji != nil && !ValidEmoji(*p.Emoji) {
		return errx.InvalidField("emoji", "emoji must be one emoji")
	}
	return nil
}

// Apply returns s with the patch's settings set.
func (p SettingsPatch) Apply(s Settings) Settings {
	if p.React != nil {
		s.React = *p.React
	}
	if p.Emoji != nil {
		s.Emoji = *p.Emoji
	}
	if p.MarkRead != nil {
		s.MarkRead = *p.MarkRead
	}
	return s
}

// marks: the gateway reacts to or reads a message (Receive's answer) only
// when it went to an agent just now: a new ship, queued for one. Held
// messages (customs, a group message without a mention, no agent called)
// and redeliveries are left alone, and so is the account's own chat.
func marks(st ship.Ship, created bool, kind conversation.Kind) bool {
	return created && st.Status == ship.StatusQueued && kind != conversation.KindSelf
}

// SettingsStore keeps the settings in whatsapp_docks (no secret in them,
// so nothing is sealed). A dock without a row has DefaultSettings.
type SettingsStore struct{ db *sqlx.DB }

func NewSettingsStore(db *sqlx.DB) *SettingsStore { return &SettingsStore{db: db} }

// Get answers a dock's settings (DefaultSettings when it saved none).
func (s *SettingsStore) Get(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out, `SELECT react, emoji, mark_read FROM whatsapp_docks
		WHERE organization_id = $1 AND dock_id = $2`, org, dock)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	return out, pgerr.Map(err, "read whatsapp dock settings")
}

// Update applies a patch to a dock's settings and answers them. The dock
// must be the organization's (the foreign key; the caller checked its
// channel).
func (s *SettingsStore) Update(ctx context.Context, org identity.OrganizationID, dock identity.DockID, p SettingsPatch) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out, `INSERT INTO whatsapp_docks AS w (dock_id, organization_id, react, emoji, mark_read)
		VALUES ($2, $1, COALESCE($3, false), COALESCE($4, $6), COALESCE($5, false))
		ON CONFLICT (dock_id) DO UPDATE SET react = COALESCE($3, w.react), emoji = COALESCE($4, w.emoji),
			mark_read = COALESCE($5, w.mark_read), updated_at = now()
		WHERE w.organization_id = $1
		RETURNING react, emoji, mark_read`, org, dock, p.React, p.Emoji, p.MarkRead, DefaultEmoji)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, errx.NotFound("dock not found")
	}
	return out, pgerr.MapWith(err, "save whatsapp dock settings", pgerr.Messages{NotFound: "dock not found", ForeignKey: "dock not found"})
}

// ValidEmoji: e is one emoji, as WhatsApp takes for a reaction: a
// pictograph with its variation selector and skin tone, a sequence of
// them joined by ZWJ (👩‍💻), a flag (two regional indicators, or a black
// flag with tags) or a keycap (1️⃣). Text, spaces and two emoji side by side
// are refused.
func ValidEmoji(e string) bool {
	if e == "" || len(e) > maxEmoji || !utf8.ValidString(e) {
		return false
	}
	runes := []rune(e)
	// A keycap: 0-9, # or * then (FE0F) 20E3.
	if k := runes[0]; (k >= '0' && k <= '9') || k == '#' || k == '*' {
		rest := string(runes[1:])
		return rest == "\u20e3" || rest == "\ufe0f\u20e3"
	}
	// Two regional indicators are a flag.
	if len(runes) == 2 && regional(runes[0]) && regional(runes[1]) {
		return true
	}
	// Otherwise ZWJ-joined parts, each one pictograph plus modifiers.
	base := false
	for i, r := range runes {
		switch {
		case r == 0x200d: // ZWJ: the next part needs its own pictograph
			if !base || i == len(runes)-1 {
				return false
			}
			base = false
		case r == 0xfe0f || r == 0xfe0e || (r >= 0x1f3fb && r <= 0x1f3ff) || (r >= 0xe0020 && r <= 0xe007f):
			// variation selectors, skin tones, tag characters: after a pictograph
			if !base {
				return false
			}
		case pictograph(r):
			if base {
				return false // two emoji side by side
			}
			base = true
		default:
			return false
		}
	}
	return base
}

func regional(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }

// pictograph: the code points emoji are drawn from (the emoji planes, the
// Miscellaneous Symbols and Dingbats, and the few other older symbols with
// an emoji presentation: arrows, clocks, squares, stars).
func pictograph(r rune) bool {
	switch {
	case r >= 0x1f000 && r <= 0x1faff && !regional(r) && !(r >= 0x1f3fb && r <= 0x1f3ff):
		return true
	case r >= 0x2600 && r <= 0x27bf,
		r >= 0x2194 && r <= 0x2199, r >= 0x23e9 && r <= 0x23f3, r >= 0x23f8 && r <= 0x23fa,
		r >= 0x25fb && r <= 0x25fe, r >= 0x2b05 && r <= 0x2b07:
		return true
	}
	switch r {
	case 0x00a9, 0x00ae, 0x203c, 0x2049, 0x2122, 0x2139, 0x21a9, 0x21aa, 0x231a, 0x231b, 0x2328, 0x23cf,
		0x24c2, 0x25aa, 0x25ab, 0x25b6, 0x25c0, 0x2934, 0x2935, 0x2b1b, 0x2b1c, 0x2b50, 0x2b55,
		0x3030, 0x303d, 0x3297, 0x3299:
		return true
	}
	return false
}
