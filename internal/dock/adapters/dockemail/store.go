package dockemail

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// SealInfo derives the key of mailbox passwords (migration 00032).
const SealInfo = "seaport email v1"

// Transport security of a connection: implicit TLS or STARTTLS. Plain text
// is never offered: the password would cross the network in clear.
const (
	TLS      = "tls"
	StartTLS = "starttls"
)

// Endpoint is one server of a mailbox (IMAP or SMTP).
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  string `json:"tls"`
}

// Account is a dock's mailbox: where to read, where to send, and who it is.
type Account struct {
	Dock         identity.DockID
	Organization identity.OrganizationID
	Kind         string
	Address      string
	DisplayName  string
	IMAP, SMTP   Endpoint
	Username     string
	Folder       string
	// LastUID is the highest UID of Folder taken in, under UIDValidity.
	LastUID     uint32
	UIDValidity uint32
	// Connected and LastError are the owner's last word on the connection.
	Connected bool
	LastError string
	SeenAt    *time.Time
	CreatedAt time.Time
	// Updated changes only when the account is saved again (a session
	// that sees it change reconnects with the new settings).
	Updated time.Time

	password string
}

// Store keeps the accounts (password sealed, bound to its dock and field)
// and what threading needs: threads and the message ids they answer to.
type Store struct {
	db   *sqlx.DB
	seal seal.Sealer
}

func NewStore(db *sqlx.DB, sealer seal.Sealer) *Store { return &Store{db: db, seal: sealer} }

func purpose(dock identity.DockID, field string) string {
	return "email\x00" + dock.String() + "\x00" + field
}

type accountRow struct {
	Dock         identity.DockID         `db:"dock_id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Kind         string                  `db:"kind"`
	Address      string                  `db:"address"`
	DisplayName  string                  `db:"display_name"`
	IMAPHost     string                  `db:"imap_host"`
	IMAPPort     int                     `db:"imap_port"`
	IMAPTLS      string                  `db:"imap_tls"`
	SMTPHost     string                  `db:"smtp_host"`
	SMTPPort     int                     `db:"smtp_port"`
	SMTPTLS      string                  `db:"smtp_tls"`
	Username     string                  `db:"username"`
	Password     []byte                  `db:"password"`
	Folder       string                  `db:"folder"`
	LastUID      int64                   `db:"last_uid"`
	UIDValidity  int64                   `db:"uid_validity"`
	Connected    bool                    `db:"connected"`
	LastError    string                  `db:"last_error"`
	SeenAt       *time.Time              `db:"seen_at"`
	CreatedAt    time.Time               `db:"created_at"`
	Updated      time.Time               `db:"updated_at"`
}

const accountColumns = `dock_id, organization_id, kind, address, display_name, imap_host, imap_port, imap_tls,
	smtp_host, smtp_port, smtp_tls, username, password, folder, last_uid, uid_validity, connected, last_error, seen_at, created_at, updated_at`

var accountMessages = pgerr.Messages{NotFound: "this dock has no mailbox yet"}

// Save stores the account a link checked, replacing the dock's. start is
// where reading begins when the folder (or its UIDVALIDITY) is new: the
// folder's current end, so a link brings no backlog. Linking again over the
// same folder keeps the cursor, so a new password misses no mail.
func (s *Store) Save(ctx context.Context, a Account, start Cursor) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO email_accounts (dock_id, organization_id, kind, address, display_name,
			imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, username, password, folder, last_uid, uid_validity)
		VALUES ($1, $2, 'password', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (dock_id) DO UPDATE SET address = EXCLUDED.address, display_name = EXCLUDED.display_name,
		    imap_host = EXCLUDED.imap_host, imap_port = EXCLUDED.imap_port, imap_tls = EXCLUDED.imap_tls,
		    smtp_host = EXCLUDED.smtp_host, smtp_port = EXCLUDED.smtp_port, smtp_tls = EXCLUDED.smtp_tls,
		    username = EXCLUDED.username, password = EXCLUDED.password, folder = EXCLUDED.folder,
		    last_uid = CASE WHEN email_accounts.folder = EXCLUDED.folder AND email_accounts.uid_validity = EXCLUDED.uid_validity
		                    THEN email_accounts.last_uid ELSE EXCLUDED.last_uid END,
		    uid_validity = EXCLUDED.uid_validity, connected = false, last_error = '', updated_at = now()
		WHERE email_accounts.organization_id = EXCLUDED.organization_id`,
		a.Dock, a.Organization, a.Address, a.DisplayName, a.IMAP.Host, a.IMAP.Port, a.IMAP.TLS,
		a.SMTP.Host, a.SMTP.Port, a.SMTP.TLS, a.Username, s.seal.Seal(purpose(a.Dock, "password"), []byte(a.password)),
		a.Folder, int64(start.UID), int64(start.Validity))
	return pgerr.Map(err, "store email account")
}

// Find reads and opens the dock's account (system: sessions run by dock).
func (s *Store) Find(ctx context.Context, dock identity.DockID) (Account, error) {
	var r accountRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+accountColumns+` FROM email_accounts WHERE dock_id = $1`, dock); err != nil {
		return Account{}, pgerr.MapWith(err, "find email account", accountMessages)
	}
	pw, err := s.seal.Open(purpose(dock, "password"), r.Password)
	if err != nil {
		return Account{}, errx.Internal("the mailbox password can't be opened (SEAPORT_SECRET_KEY changed?)")
	}
	return Account{Dock: r.Dock, Organization: r.Organization, Kind: r.Kind, Address: r.Address, DisplayName: r.DisplayName,
		IMAP: Endpoint{r.IMAPHost, r.IMAPPort, r.IMAPTLS}, SMTP: Endpoint{r.SMTPHost, r.SMTPPort, r.SMTPTLS},
		Username: r.Username, Folder: r.Folder, LastUID: uint32(r.LastUID), UIDValidity: uint32(r.UIDValidity),
		Connected: r.Connected, LastError: r.LastError, SeenAt: r.SeenAt, CreatedAt: r.CreatedAt, Updated: r.Updated, password: string(pw)}, nil
}

// Delete forgets the dock's account (threads stay: they are history).
func (s *Store) Delete(ctx context.Context, dock identity.DockID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM email_accounts WHERE dock_id = $1`, dock)
	return pgerr.Map(err, "delete email account")
}

// Cursor is where a session is in its folder.
type Cursor struct {
	Validity uint32
	UID      uint32
}

// SetCursor records progress: the UIDVALIDITY and the highest UID taken in.
func (s *Store) SetCursor(ctx context.Context, dock identity.DockID, c Cursor) error {
	_, err := s.db.ExecContext(ctx, `UPDATE email_accounts SET uid_validity = $2, last_uid = $3 WHERE dock_id = $1`,
		dock, int64(c.Validity), int64(c.UID))
	return pgerr.Map(err, "store email cursor")
}

const maxStateError = 300

// SetState records the connection's state for the admin API.
func (s *Store) SetState(ctx context.Context, dock identity.DockID, connected bool, problem string) error {
	if len(problem) > maxStateError {
		problem = problem[:maxStateError]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE email_accounts SET connected = $2, last_error = $3,
		seen_at = CASE WHEN $2 THEN now() ELSE seen_at END WHERE dock_id = $1`, dock, connected, strings.ToValidUTF8(problem, ""))
	return pgerr.Map(err, "store email state")
}

// CheckKey opens one account, if any: a changed SEAPORT_SECRET_KEY stops
// the start instead of failing every email dock later.
func (s *Store) CheckKey(ctx context.Context) error {
	var dock identity.DockID
	err := s.db.GetContext(ctx, &dock, `SELECT dock_id FROM email_accounts LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return pgerr.Map(err, "check email account key")
	}
	if _, err := s.Find(ctx, dock); err != nil {
		return errx.Validation("the mailbox passwords were sealed with another SEAPORT_SECRET_KEY: restore that key")
	}
	return nil
}

// Thread is what a reply needs to know about an email thread.
type Thread struct {
	Chat    string
	Subject string
	// Key is the subject without Re:/Fwd: prefixes, lowercased.
	Key          string
	Participants []string // addresses other than the mailbox's, in order seen
	LastMessage  string   // Message-ID of the newest message, no brackets
	Refs         []string // the chain of Message-IDs, oldest first
}

// MaxRefs and MaxParticipants bound what a thread keeps.
const (
	MaxRefs         = 20
	MaxParticipants = 20
)

func merge(old, add []string, max int, keepNewest bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, old...), add...) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) > max {
		if keepNewest {
			return out[len(out)-max:]
		}
		return out[:max]
	}
	return out
}

type threadRow struct {
	Chat         string `db:"chat_id"`
	Subject      string `db:"subject"`
	Key          string `db:"subject_key"`
	Participants []byte `db:"participants"`
	LastMessage  string `db:"last_message_id"`
	Refs         []byte `db:"refs"`
}

func (r threadRow) thread() Thread {
	t := Thread{Chat: r.Chat, Subject: r.Subject, Key: r.Key, LastMessage: r.LastMessage}
	_ = json.Unmarshal(r.Participants, &t.Participants) // our own JSON
	_ = json.Unmarshal(r.Refs, &t.Refs)
	return t
}

const threadColumns = `chat_id, subject, subject_key, participants, last_message_id, refs`

// Thread reads a thread of the dock by its chat id.
func (s *Store) Thread(ctx context.Context, dock identity.DockID, chat string) (Thread, error) {
	var r threadRow
	if err := s.db.GetContext(ctx, &r, `SELECT `+threadColumns+` FROM email_threads WHERE dock_id = $1 AND chat_id = $2`, dock, chat); err != nil {
		return Thread{}, pgerr.MapWith(err, "find email thread", pgerr.Messages{NotFound: "this email conversation has no known thread (was it started outside the dock?)"})
	}
	return r.thread(), nil
}

// ThreadOf finds the chat a message belongs to: the one of any of the
// Message-IDs (the message's own, what it replies to, its references).
func (s *Store) ThreadOf(ctx context.Context, dock identity.DockID, ids []string) (string, bool, error) {
	if len(ids) == 0 {
		return "", false, nil
	}
	var chat string
	err := s.db.GetContext(ctx, &chat, `SELECT chat_id FROM email_messages WHERE dock_id = $1 AND message_id = ANY($2::text[])
		ORDER BY created_at DESC LIMIT 1`, dock, ids)
	if pgerr.IsNoRows(err) {
		return "", false, nil
	}
	return chat, err == nil, pgerr.Map(err, "find email message thread")
}

// ThreadBySubject finds a recent thread with this subject that the sender
// already takes part in: a reply whose client dropped the threading headers.
func (s *Store) ThreadBySubject(ctx context.Context, dock identity.DockID, key, sender string, within time.Duration) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	var chat string
	err := s.db.GetContext(ctx, &chat, `SELECT chat_id FROM email_threads WHERE dock_id = $1 AND subject_key = $2
		AND updated_at > now() - make_interval(secs => $4) AND participants @> to_jsonb($3::text)
		ORDER BY updated_at DESC LIMIT 1`, dock, key, sender, within.Seconds())
	if pgerr.IsNoRows(err) {
		return "", false, nil
	}
	return chat, err == nil, pgerr.Map(err, "find email thread by subject")
}

// Note adds a message to a thread (creating it): its participants join,
// its Message-ID becomes the thread's newest and is remembered as the
// thread's, so what answers it finds the thread. Idempotent.
func (s *Store) Note(ctx context.Context, org identity.OrganizationID, dock identity.DockID, t Thread, messageID string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin email thread")
	}
	defer func() { _ = tx.Rollback() }()
	var cur threadRow
	err = tx.GetContext(ctx, &cur, `SELECT `+threadColumns+` FROM email_threads WHERE dock_id = $1 AND chat_id = $2 FOR UPDATE`, dock, t.Chat)
	merged := t
	switch {
	case pgerr.IsNoRows(err):
		merged.Participants = merge(nil, t.Participants, MaxParticipants, false)
		merged.Refs = merge(t.Refs, []string{messageID}, MaxRefs, true)
	case err != nil:
		return pgerr.Map(err, "read email thread")
	default:
		old := cur.thread()
		merged.Subject, merged.Key = old.Subject, old.Key
		merged.Participants = merge(old.Participants, t.Participants, MaxParticipants, false)
		merged.Refs = merge(old.Refs, append(append([]string{}, t.Refs...), messageID), MaxRefs, true)
	}
	if messageID != "" {
		merged.LastMessage = messageID
	} else if cur.LastMessage != "" && merged.LastMessage == "" {
		merged.LastMessage = cur.LastMessage
	}
	parts, _ := json.Marshal(nonNil(merged.Participants))
	refs, _ := json.Marshal(nonNil(merged.Refs))
	if _, err := tx.ExecContext(ctx, `INSERT INTO email_threads (dock_id, organization_id, chat_id, subject, subject_key, participants, last_message_id, refs)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (dock_id, chat_id) DO UPDATE SET participants = EXCLUDED.participants, last_message_id = EXCLUDED.last_message_id,
		    refs = EXCLUDED.refs, updated_at = now()`,
		dock, org, merged.Chat, merged.Subject, merged.Key, parts, merged.LastMessage, refs); err != nil {
		return pgerr.Map(err, "store email thread")
	}
	if messageID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO email_messages (dock_id, organization_id, message_id, chat_id) VALUES ($1, $2, $3, $4)
			ON CONFLICT (dock_id, message_id) DO NOTHING`, dock, org, messageID, merged.Chat); err != nil {
			return pgerr.Map(err, "store email message")
		}
	}
	return pgerr.Map(tx.Commit(), "commit email thread")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
