// Package identity holds the typed generic IDs of every entity.
package identity

import (
	"database/sql/driver"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/google/uuid"
)

// ID is a validated UUID identity. The zero value is invalid. The phantom
// type parameter makes ID[dockTag] and ID[shipTag] different types at no
// runtime cost.
type ID[T any] struct{ v uuid.UUID }

func ParseID[T any](raw string) (ID[T], error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return ID[T]{}, errx.InvalidField(tagName[T](), tagName[T]()+" must be a valid UUID")
	}
	return ID[T]{v: parsed}, nil
}

// MustParseID is like ParseID but panics. Tests and static init only.
func MustParseID[T any](raw string) ID[T] {
	id, err := ParseID[T](raw)
	if err != nil {
		panic(err)
	}
	return id
}

func NewID[T any]() ID[T] { return ID[T]{v: uuid.New()} }

func (id ID[T]) String() string  { return id.v.String() }
func (id ID[T]) IsZero() bool    { return id.v == uuid.Nil }
func (id ID[T]) UUID() uuid.UUID { return id.v }

// MarshalText implements encoding.TextMarshaler (used by encoding/json).
func (id ID[T]) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return []byte(""), nil
	}
	return []byte(id.v.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler (used by encoding/json).
func (id *ID[T]) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		id.v = uuid.Nil
		return nil
	}
	parsed, err := uuid.Parse(string(b))
	if err != nil {
		return errx.InvalidField(tagName[T](), tagName[T]()+" must be a valid UUID")
	}
	id.v = parsed
	return nil
}

// Value implements database/sql/driver.Valuer.
func (id ID[T]) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil
	}
	return id.v.String(), nil
}

// Scan implements database/sql.Scanner.
func (id *ID[T]) Scan(src any) error {
	if src == nil {
		id.v = uuid.Nil
		return nil
	}
	var (
		parsed uuid.UUID
		err    error
	)
	switch v := src.(type) {
	case string:
		parsed, err = uuid.Parse(v)
	case []byte:
		if len(v) == 16 {
			parsed, err = uuid.FromBytes(v)
		} else {
			parsed, err = uuid.ParseBytes(v)
		}
	case [16]byte:
		parsed = uuid.UUID(v)
	default:
		return errx.Internal("cannot scan into " + tagName[T]())
	}
	if err != nil {
		return errx.Internal(tagName[T]() + " in storage is not a valid UUID")
	}
	id.v = parsed
	return nil
}

type (
	organizationTag struct{}
	dockTag         struct{}
	conversationTag struct{}
	shipTag         struct{}
	deliveryTag     struct{}
	ruleTag         struct{}
)

func tagName[T any]() string {
	var zero T
	switch any(zero).(type) {
	case organizationTag:
		return "organization_id"
	case dockTag:
		return "dock_id"
	case conversationTag:
		return "conversation_id"
	case shipTag:
		return "ship_id"
	case deliveryTag:
		return "delivery_id"
	case ruleTag:
		return "rule_id"
	default:
		return "id"
	}
}

// OrganizationID identifies a tenant: an IAMKit organization in iam mode,
// config.DefaultOrganization in the single-organization modes. Seaport
// stores only the id (IAMKit owns organizations).
type OrganizationID = ID[organizationTag]

func ParseOrganizationID(raw string) (OrganizationID, error) {
	return ParseID[organizationTag](raw)
}
func MustParseOrganizationID(raw string) OrganizationID { return MustParseID[organizationTag](raw) }

// DockID identifies a linked channel account (a WhatsApp number...).
type DockID = ID[dockTag]

func NewDockID() DockID                      { return NewID[dockTag]() }
func ParseDockID(raw string) (DockID, error) { return ParseID[dockTag](raw) }
func MustParseDockID(raw string) DockID      { return MustParseID[dockTag](raw) }

// ConversationID identifies a chat on a dock.
type ConversationID = ID[conversationTag]

func NewConversationID() ConversationID { return NewID[conversationTag]() }
func ParseConversationID(raw string) (ConversationID, error) {
	return ParseID[conversationTag](raw)
}
func MustParseConversationID(raw string) ConversationID { return MustParseID[conversationTag](raw) }

// ShipID identifies one message, inbound or outbound.
type ShipID = ID[shipTag]

func NewShipID() ShipID                      { return NewID[shipTag]() }
func ParseShipID(raw string) (ShipID, error) { return ParseID[shipTag](raw) }
func MustParseShipID(raw string) ShipID      { return MustParseID[shipTag](raw) }

// DeliveryID identifies one claim of queued ships handed to an agent.
type DeliveryID = ID[deliveryTag]

func NewDeliveryID() DeliveryID                      { return NewID[deliveryTag]() }
func ParseDeliveryID(raw string) (DeliveryID, error) { return ParseID[deliveryTag](raw) }
func MustParseDeliveryID(raw string) DeliveryID      { return MustParseID[deliveryTag](raw) }

// RuleID identifies a customs rule of a dock.
type RuleID = ID[ruleTag]

func NewRuleID() RuleID                      { return NewID[ruleTag]() }
func ParseRuleID(raw string) (RuleID, error) { return ParseID[ruleTag](raw) }
