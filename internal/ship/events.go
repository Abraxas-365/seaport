package ship

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the ship domain publishes (see internal/event). Data is the
// payload type named in each comment.
const (
	// EventCreated: a ship was stored — inbound (queued or held) or an
	// outbound reply (pending). Data: Ship.
	EventCreated = "ship.created"
	// EventUpdated: an outbound ship's outcome (sent | failed). Data: Ship.
	EventUpdated = "ship.updated"
	// EventDelivery: a delivery opened, grew (steer), closed or was
	// released. Data: DeliveryEvent.
	EventDelivery = "delivery.changed"
)

// DeliveryState is what happened to a delivery.
type DeliveryState string

const (
	DeliveryClaimed  DeliveryState = "claimed"
	DeliveryJoined   DeliveryState = "joined"
	DeliveryAcked    DeliveryState = "acked"
	DeliveryReleased DeliveryState = "released"
	// DeliveryExpired: leases ended (Delivery/Conversation are empty; Count
	// says how many ships went back to the queue).
	DeliveryExpired DeliveryState = "expired"
)

// DeliveryEvent is the payload of EventDelivery.
type DeliveryEvent struct {
	State        DeliveryState            `json:"state"`
	Delivery     *identity.DeliveryID     `json:"delivery_id,omitempty"`
	Conversation *identity.ConversationID `json:"conversation_id,omitempty"`
	// Ships: claimed/joined ships, or the handled ones of an ack.
	Ships []identity.ShipID `json:"ship_ids,omitempty"`
	// Count: ships requeued (acked, released, expired).
	Count int    `json:"count,omitempty"`
	Owner string `json:"owner,omitempty"`
}
