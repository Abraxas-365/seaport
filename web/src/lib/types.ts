// Wire types of the Puerto admin API (mirror internal/*/ Go JSON tags).

export type ID = string

export type Channel = "whatsapp" | "api"
export type DockStatus = "linking" | "linked" | "logged_out" | "failed"

export interface Dock {
  id: ID
  channel: Channel
  name: string
  address: string
  status: DockStatus
  created_at: string
  updated_at: string
  qr_code?: string
}

export type ConversationKind = "direct" | "group" | "self"
export type Access = "auto" | "allow" | "deny"
export type GroupMode = "mentions" | "all" | ""
export type CustomsStatus = "allowed" | "pending" | "denied"

export interface Conversation {
  id: ID
  dock_id: ID
  channel: Channel
  external_id: string
  kind: ConversationKind
  title: string
  access: Access
  group_mode?: GroupMode
  status: CustomsStatus
  handles: string[]
  last_activity_at: string
  created_at: string
  updated_at: string
}

export type Direction = "in" | "out"
export type ShipStatus =
  | "held"
  | "queued"
  | "claimed"
  | "handled"
  | "pending"
  | "sent"
  | "failed"

export interface Ship {
  id: ID
  conversation_id: ID
  direction: Direction
  external_id: string
  sender_id: string
  sender_name: string
  text: string
  status: ShipStatus
  delivery_id?: ID
  error?: string
  sent_at: string
  created_at: string
}

export interface InboxItem extends Conversation {
  last_ship: Ship | null
  held: number
  queued: number
  in_flight: boolean
}

export type DefaultAccess = "ask" | "allow" | "deny"
export type Effect = "allow" | "deny"

export interface Policy {
  dock_id: ID
  default: DefaultAccess
  group_mode: Exclude<GroupMode, "">
}

export interface Rule {
  id: ID
  dock_id: ID
  effect: Effect
  pattern: string
  created_at: string
}

export interface Customs {
  policy: Policy
  rules: Rule[]
}

export interface AgentDelivery {
  id: ID
  conversation_id: ID
  ships: number
  since: string
}

export interface Agent {
  session: string
  name: string
  concurrency: number
  steer: boolean
  remote: string
  connected_at: string
  deliveries: AgentDelivery[]
}

export interface CrewStatus {
  agents: Agent[]
}

export interface Page {
  total: number
  limit: number
  offset: number
}

export interface Paginated<T> {
  items: T[]
  page: Page
}

/** GET /api/v1/config: how this port authenticates (PUERTO_AUTH). */
export interface PortConfig {
  auth: "none" | "token"
  version: string
}

export interface Session {
  authenticated: boolean
  expires_at?: string
}

export interface Health {
  status: string
  version: string
}

export type DeliveryState = "claimed" | "joined" | "acked" | "released" | "expired"

export interface DeliveryEvent {
  state: DeliveryState
  delivery_id?: ID
  conversation_id?: ID
  ship_ids?: ID[]
  count?: number
  owner?: string
}

/** Server-Sent Event types of GET /api/v1/events. */
export interface EventMap {
  "dock.updated": Dock
  "dock.deleted": { id: ID }
  "conversation.updated": Conversation
  "customs.updated": { customs: Customs }
  "ship.created": Ship
  "ship.updated": Ship
  "delivery.changed": DeliveryEvent
  "crew.changed": CrewStatus
}
export type EventType = keyof EventMap
