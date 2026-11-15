// The wire fixtures exported by Go (internal/bootstrap/wire_fixtures_test.go):
// one sample of every live event payload and of the main API responses,
// marshalled with the real JSON tags. Regenerate with
// `SEAPORT_UPDATE_FIXTURES=1 go test ./internal/bootstrap -run TestWireFixtures`.

import raw from "@/lib/__fixtures__/wire.json"
import type { EventMap, EventType, HomeRules } from "@/lib/types"

export const wire = raw

/** A fresh copy of the sample payload of an event type (tests may mutate it). */
export function eventData<K extends EventType>(type: K): EventMap[K] {
  return structuredClone((raw.events as Record<string, unknown>)[type]) as EventMap[K]
}

/** A fresh copy of a named API response sample, typed by the caller. */
export function apiData<T>(name: keyof typeof raw.api): T {
  return structuredClone(raw.api[name]) as unknown as T
}

export function homeRules(harness: "rness" | "claude-code"): HomeRules {
  return structuredClone(raw.home_rules[harness]) as HomeRules
}
