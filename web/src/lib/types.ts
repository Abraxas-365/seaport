// Wire types of the Seaport admin API. Nearly everything is generated from
// the Go structs into types.gen.ts (`go generate ./internal/wire`, the list is
// internal/bootstrap/wirespec); this file re-exports it and holds what has no
// Go type behind it.

export type * from "./types.gen"

import type { AgentProfile, Hosted, ID, Page, WireEvents } from "./types.gen"

/** The list envelope of the API (query.Paginated; a Go generic, so by hand). */
export interface Paginated<T> {
  items: T[]
  page: Page
}

/**
 * Event types the web has no handler for (the reason is in `unhandledEvents`
 * of web_architecture_test.go: slot.browser and agent.takeover are audit trails).
 */
export type UnhandledEvent = "slot.browser" | "agent.takeover"

/** Server-Sent Event types the web handles (GET /api/v1/events), with their payloads. */
export type EventMap = Omit<WireEvents, UnhandledEvent>
export type EventType = keyof EventMap

/** GET /api/v1/config: how this port authenticates (SEAPORT_AUTH). */
export interface PortConfig {
  auth: "none" | "token" | "iam"
  version: string
}

/**
 * Seaport role names a person or key can hold (iam mode; the roles of
 * iam/permissions.json, a list in Go, not a const block).
 */
export type MemberRole = "Admin" | "Operator" | "Viewer"

/** GET /healthz. */
export interface Health {
  status: string
  version: string
}

/** GET /api/v1/hosting: the hosted agents and whether this port runs them. */
export interface HostingStatus {
  agents: Hosted[]
  /** This port runs hosted agents (SEAPORT_RUNTIME isn't off). */
  runtime: boolean
}

/**
 * The fields the web edits of an agent's config (PUT /agents/{id}/config;
 * the rest is the server's): the profile, the harness and the machine.
 */
export type ProfileInput = Pick<
  AgentProfile,
  | "instructions"
  | "skills"
  | "mcp_servers"
  | "approve"
  | "model"
  | "effort"
  | "browser"
  | "browser_assist"
  | "browser_detect"
  | "browser_ignore_hosts"
  | "harness"
  | "machine"
>

/** The body of POST /api/v1/schedules (the handler's createRequest). */
export interface ScheduleInput {
  conversation_id: ID
  name: string
  cron: string
  timezone: string
  text: string
  agent_id?: ID
  enabled?: boolean
}
