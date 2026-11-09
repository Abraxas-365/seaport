// Wire types of the Seaport admin API (mirror internal/*/ Go JSON tags).

import type { Permission } from "./permissions.gen"

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
  /** Inbound: the agent it is (or was) for; outbound: the agent that sent it (none: a person). */
  agent_id?: ID
  /** Why a held ship was held (customs or routing). */
  hold_reason?: string
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

/** One live connection (`seaport connect`) working as a library agent. */
export interface Agent {
  session: string
  agent_id: ID
  agent_name: string
  /** The connector's label. */
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

/** An agent on a dock. */
export interface Assignment {
  dock_id: ID
  /** It takes the dock's messages that call nobody. */
  default: boolean
}

/** An agent of the organization's library (GET /api/v1/agents). */
export interface LibraryAgent {
  id: ID
  /** Unique in the organization; how people call it (@name). */
  name: string
  description: string
  created_by: string
  created_at: string
  updated_at: string
  /** Where it works; none: inactive. */
  docks: Assignment[]
}

/** One agent of a dock's team (GET /api/v1/docks/{id}/agents). */
export interface TeamMember {
  agent_id: ID
  name: string
  default: boolean
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

/** GET /api/v1/config: how this port authenticates (SEAPORT_AUTH). */
export interface PortConfig {
  auth: "none" | "token" | "iam"
  version: string
}

/** GET /api/v1/session: who this browser is, in every mode. */
export interface Session {
  authenticated: boolean
  expires_at?: string
  subject?: string
  name?: string
  email?: string
  organization_id?: string
  organization_name?: string
  /** What the user may do (the whole catalog in none/token mode). */
  permissions?: Permission[]
}

/** A crew token (an agent credential); the value is shown only at creation. */
export interface CrewToken {
  id: ID
  /** The agent it connects as. */
  agent_id: ID
  agent_name: string
  name: string
  /** Start of the value ("sp_crew_AbCd"), to tell tokens apart. */
  hint: string
  created_by: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
}

export interface IssuedCrewToken extends CrewToken {
  value: string
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
  "agent.updated": LibraryAgent
  "agent.deleted": { id: ID }
}
export type EventType = keyof EventMap
