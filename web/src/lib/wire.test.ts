// Compile-time contract between the wire fixtures exported by Go and the
// TypeScript wire types (types.ts). `tsc -b` (pnpm build) fails here when
//  - a field Go sends has no counterpart in the type (`Extra`), or
//  - a field the type requires is missing, or has another type (`Fixture
//    extends Widen<Type>`; string literal unions are widened because JSON
//    imports only know `string`).
// The runtime test below only keeps vitest from reporting an empty file.

import { describe, expect, it } from "vitest"

import wire from "./__fixtures__/wire.json"
import type {
  Approval,
  Build,
  Conversation,
  CrewStatus,
  CrewToken,
  Customs,
  Dock,
  Environment,
  EventMap,
  Hosted,
  InboxItem,
  LibraryAgent,
  Paginated,
  Schedule,
  Ship,
  Turn,
  Vault,
  VaultAccess,
  VaultDetail,
  VaultEntry,
  VaultEntrySummary,
  VaultEntryVersion,
  VaultHit,
} from "./types"

type Events = typeof wire.events
type Api = typeof wire.api

/** String literal unions → string, deeply (what a JSON module can know). */
type Widen<T> = T extends string
  ? string
  : T extends readonly (infer U)[]
    ? Widen<U>[]
    : T extends object
      ? { [K in keyof T]: Widen<T[K]> }
      : T

/** Paths of fields the fixture has and the type doesn't ("" paths: shape mismatch). */
type Extra<F, T, P extends string = ""> = F extends readonly (infer FU)[]
  ? NonNullable<T> extends readonly (infer TU)[]
    ? Extra<FU, TU, `${P}[]`>
    : P
  : F extends object
    ? NonNullable<T> extends object
      ? { [K in keyof F & string]: K extends keyof NonNullable<T> ? Extra<F[K], NonNullable<T>[K], `${P}.${K}`> : `${P}.${K}` }[keyof F & string]
      : P
    : never

/** The check: fixture F is a T, and sends nothing T doesn't know. */
type Matches<F, T> = [Extra<F, T>] extends [never] ? (F extends Widen<T> ? true : "type mismatch") : Extra<F, T>

/** Compile error unless every value of `R` is `true`. */
type AllTrue<R extends Record<string, true>> = R

// Every event type the web knows, against its payload sample.
export type EventsMatch = AllTrue<{ [K in keyof EventMap]: Matches<Events[K & keyof Events], EventMap[K]> }>

// The API responses the web reads.
export type ApiMatches = AllTrue<{
  dock: Matches<Api["dock"], Dock>
  dock_linking: Matches<Api["dock_linking"], Dock>
  dock_trigger: Matches<Api["dock_trigger"], Dock>
  docks: Matches<Api["docks"], Paginated<Dock>>
  conversation: Matches<Api["conversation"], Conversation>
  conversation_dm: Matches<Api["conversation_dm"], Conversation>
  inbox_item: Matches<Api["inbox_item"], InboxItem>
  ship_in: Matches<Api["ship_in"], Ship>
  ship_held: Matches<Api["ship_held"], Ship>
  ship_out: Matches<Api["ship_out"], Ship>
  ships: Matches<Api["ships"], Paginated<Ship>>
  approval: Matches<Api["approval"], Approval>
  approvals: Matches<Api["approvals"], Paginated<Approval>>
  turns: Matches<Api["turns"], Paginated<Turn>>
  customs: Matches<Api["customs"], Customs>
  agent: Matches<Api["agent"], LibraryAgent>
  agents: Matches<Api["agents"], Paginated<LibraryAgent>>
  crew_status: Matches<Api["crew_status"], CrewStatus>
  crew_token: Matches<Api["crew_token"], CrewToken>
  schedule: Matches<Api["schedule"], Schedule>
  schedules: Matches<Api["schedules"], Paginated<Schedule>>
  environment: Matches<Api["environment"], Environment>
  environments: Matches<Api["environments"], Paginated<Environment>>
  build: Matches<Api["build"], Build>
  hosted: Matches<Api["hosted"], Hosted>
  vault: Matches<Api["vault"], Vault>
  vaults: Matches<Api["vaults"], Paginated<Vault>>
  vault_detail: Matches<Api["vault_detail"], VaultDetail>
  vault_access: Matches<Api["vault_access"], VaultAccess[]>
  vault_entry: Matches<Api["vault_entry"], VaultEntry>
  vault_entries: Matches<Api["vault_entries"], Paginated<VaultEntrySummary>>
  vault_versions: Matches<Api["vault_versions"], { versions: VaultEntryVersion[] }>
  vault_hits: Matches<Api["vault_hits"], { hits: VaultHit[] }>
}>

describe("wire fixtures vs TypeScript types", () => {
  it("are checked by the compiler (tsc -b)", () => {
    expect(Object.keys(wire.events).length).toBeGreaterThan(0)
  })
})
