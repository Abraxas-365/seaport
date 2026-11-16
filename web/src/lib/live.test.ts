// Every handler of live.ts applied to a seeded QueryClient with the payload
// Go publishes (src/test/wire.ts: fixtures exported by the Go tests), so a
// change of shape in an event breaks here, not in the browser.

import { renderHook, act } from "@testing-library/react"
import type { QueryClient } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { clearActivity, useActivity } from "./activity"
import { handlers, live } from "./live"
import { keys } from "./query"
import type {
  Approval,
  Build,
  Conversation,
  Customs,
  Dock,
  Environment,
  EventMap,
  EventType,
  Hosted,
  InboxItem,
  Paginated,
  Ship,
  Turn,
  Vault,
} from "./types"
import { apiData, eventData, wire } from "@/test/wire"
import { infinite, invalidated, newClient, page } from "@/test/query"
import type { InfiniteData } from "@tanstack/react-query"

let qc: QueryClient

beforeEach(() => {
  qc = newClient()
})
afterEach(() => {
  clearActivity()
  vi.useRealTimers()
})

/** Applies an event as the stream does: the JSON text of the fixture, parsed. */
function apply<K extends EventType>(type: K, data: EventMap[K] = eventData(type)) {
  const parsed = JSON.parse(JSON.stringify(data)) as EventMap[K]
  ;(handlers[type] as (qc: QueryClient, d: EventMap[K]) => void)(qc, parsed)
}

/** Seeds a query so invalidation can be observed on it. */
function seed(key: readonly unknown[], data: unknown = {}) {
  qc.setQueryData(key, data)
}

/** Event types the web ignores on purpose (also allow-listed, with the reason, in TestEveryEventTypeHasALiveHandler). */
const ignored = ["agent.takeover", "slot.browser"]

describe("the fixtures", () => {
  it("have a payload for every handler, and the only payloads without one are the ignored events", () => {
    const withPayload = Object.keys(wire.events)
    expect(withPayload.filter((t) => !(t in handlers)).sort()).toEqual(ignored)
    expect(Object.keys(handlers).filter((t) => !withPayload.includes(t))).toEqual([])
  })
})

describe("dock events", () => {
  it("dock.updated replaces the dock in its detail and in the list, or adds it", () => {
    const dock = eventData("dock.updated")
    const other = { ...dock, id: "other" }
    seed(keys.docks, page<Dock>([{ ...dock, name: "old" }, other]))
    seed(keys.dock(dock.id), { ...dock, name: "old" })
    apply("dock.updated", { ...dock, name: "new" })
    expect(qc.getQueryData<Dock>(keys.dock(dock.id))?.name).toBe("new")
    const list = qc.getQueryData<Paginated<Dock>>(keys.docks)!
    expect(list.items.map((d) => d.name)).toEqual(["new", other.name])

    apply("dock.updated", { ...dock, id: "fresh" })
    expect(qc.getQueryData<Paginated<Dock>>(keys.docks)!.items).toHaveLength(3)
  })

  it("dock.updated keeps the pairing QR of a linking dock and refetches it", () => {
    const linking = apiData<Dock>("dock_linking")
    seed(keys.dock(linking.id), { ...linking, qr_code: "old-qr" })
    const invalidate = vi.spyOn(qc, "invalidateQueries")
    apply("dock.updated", { ...linking, qr_code: undefined })
    expect(qc.getQueryData<Dock>(keys.dock(linking.id))?.qr_code).toBe("old-qr")
    expect(invalidate).toHaveBeenCalledWith({ queryKey: keys.dock(linking.id), exact: true })
  })

  it.each([
    ["github", keys.github],
    ["jira", keys.jira],
    ["email", keys.email],
    ["slack", keys.slack],
    ["webhook", keys.webhook],
  ] as const)("dock.updated of a %s dock refetches its channel state", (channel, key) => {
    const dock = { ...eventData("dock.updated"), channel }
    const invalidate = vi.spyOn(qc, "invalidateQueries")
    apply("dock.updated", dock)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: key(dock.id) })
  })

  it("dock.deleted drops the dock everywhere and refetches the inbox", () => {
    const { id } = eventData("dock.deleted")
    seed(keys.docks, page<Dock>([{ ...eventData("dock.updated"), id }, { ...eventData("dock.updated"), id: "keep" }]))
    seed(keys.dock(id))
    seed(keys.customs(id))
    seed(keys.inbox({}))
    apply("dock.deleted")
    expect(qc.getQueryData<Paginated<Dock>>(keys.docks)!.items.map((d) => d.id)).toEqual(["keep"])
    expect(qc.getQueryData(keys.dock(id))).toBeUndefined()
    expect(qc.getQueryData(keys.customs(id))).toBeUndefined()
    expect(invalidated(qc, keys.inbox({}))).toBe(true)
  })
})

describe("conversation events", () => {
  it("conversation.updated patches the detail and the inbox rows, and refetches the lists", () => {
    const conv = eventData("conversation.updated")
    const row: InboxItem = { ...conv, title: "old", last_ship: null, held: 2, queued: 0, in_flight: false }
    seed(keys.conversation(conv.id), { ...conv, title: "old" })
    seed(keys.inbox({ a: 1 }), infinite<InboxItem>([row]))
    seed(keys.inbox({ b: 1 }), page<InboxItem>([row]))
    apply("conversation.updated", { ...conv, title: "renamed" })
    expect(qc.getQueryData<Conversation>(keys.conversation(conv.id))?.title).toBe("renamed")
    const list = qc.getQueryData<InfiniteData<Paginated<InboxItem>>>(keys.inbox({ a: 1 }))!
    expect(list.pages[0].items[0]).toMatchObject({ title: "renamed", held: 2 }) // counters survive the patch
    expect(qc.getQueryData<Paginated<InboxItem>>(keys.inbox({ b: 1 }))!.items[0].title).toBe("renamed")
    expect(invalidated(qc, keys.inbox({ a: 1 }))).toBe(true)
  })

  it("customs.updated is the flat customs of the dock, stored under its dock", () => {
    const customs = eventData("customs.updated")
    expect(customs.policy.dock_id).toBeTruthy() // the shape Go publishes: policy + rules
    seed(keys.customs(customs.policy.dock_id), { policy: { ...customs.policy, default: "deny" }, rules: [] })
    apply("customs.updated")
    expect(qc.getQueryData<Customs>(keys.customs(customs.policy.dock_id))).toEqual(customs)
  })
})

describe("ship events", () => {
  for (const type of ["ship.created", "ship.updated"] as const) {
    it(`${type} adds the ship to the history or replaces it, and refetches inbox and threads`, () => {
      const ship = eventData(type)
      const older: Ship = { ...ship, id: "older" }
      seed(keys.ships(ship.conversation_id), infinite<Ship>([older]))
      seed(keys.ships("any", true))
      seed(keys.inbox({}))
      apply(type)
      let pages = qc.getQueryData<InfiniteData<Paginated<Ship>>>(keys.ships(ship.conversation_id))!.pages
      expect(pages[0].items.map((s) => s.id)).toEqual([ship.id, "older"])
      expect(pages[0].page.total).toBe(2)
      expect(invalidated(qc, keys.inbox({}))).toBe(true)
      expect(invalidated(qc, keys.ships("any", true))).toBe(true)

      apply(type, { ...ship, text: "edited" })
      pages = qc.getQueryData<InfiniteData<Paginated<Ship>>>(keys.ships(ship.conversation_id))!.pages
      expect(pages[0].items).toHaveLength(2)
      expect(pages[0].items[0].text).toBe("edited")
    })
  }

  it("ship.created leaves a history that was never loaded alone", () => {
    apply("ship.created")
    expect(qc.getQueryData(keys.ships(eventData("ship.created").conversation_id))).toBeUndefined()
  })

  it("delivery.changed refetches the histories of its conversation and the inbox", () => {
    const ev = eventData("delivery.changed")
    seed(keys.ships(ev.conversation_id!))
    seed(keys.ships("unrelated"))
    seed(keys.inbox({}))
    apply("delivery.changed")
    expect(invalidated(qc, keys.ships(ev.conversation_id!))).toBe(true)
    expect(invalidated(qc, keys.ships("unrelated"))).toBe(false)
    expect(invalidated(qc, keys.inbox({}))).toBe(true)
  })

  it("delivery.changed without a conversation (expired leases) refetches every history", () => {
    seed(keys.ships("a"))
    seed(keys.ships("b"))
    apply("delivery.changed", { state: "expired", count: 2 })
    expect(invalidated(qc, keys.ships("a")) && invalidated(qc, keys.ships("b"))).toBe(true)
  })

  it("ship.turn prepends the turn once", () => {
    const turn = eventData("ship.turn")
    const key = keys.turns(turn.conversation_id)
    seed(key, page<Turn>([]))
    apply("ship.turn")
    apply("ship.turn")
    const turns = qc.getQueryData<Paginated<Turn>>(key)!
    expect(turns.items).toHaveLength(1)
    expect(turns.page.total).toBe(1)
    expect(turns.items[0].tools[0].title).toBe(turn.tools[0].title)
  })

  it("ship.turn refetches turns still loading instead of patching them", () => {
    const turn = eventData("ship.turn")
    const key = keys.turns(turn.conversation_id)
    void qc.prefetchQuery({ queryKey: key, queryFn: () => new Promise<Paginated<Turn>>(() => {}) })
    const invalidate = vi.spyOn(qc, "invalidateQueries")
    apply("ship.turn")
    expect(invalidate).toHaveBeenCalledWith({ queryKey: key })
  })

  it("ship.approval adds an approval, then updates it in place", () => {
    const a = eventData("ship.approval")
    const key = keys.approvals(a.conversation_id)
    seed(key, page<Approval>([]))
    apply("ship.approval", { ...a, status: "pending", answer: undefined })
    apply("ship.approval", { ...a, status: "answered" })
    const list = qc.getQueryData<Paginated<Approval>>(key)!
    expect(list.items).toHaveLength(1)
    expect(list.items[0]).toMatchObject({ id: a.id, status: "answered", answer: a.answer })
    expect(list.page.total).toBe(1)
  })
})

describe("crew events", () => {
  it("crew.changed replaces the crew status", () => {
    apply("crew.changed")
    const crew = qc.getQueryData<typeof wire.api.crew_status>(keys.crew)!
    expect(crew.agents[0]).toMatchObject({ agent_name: "support-bot", deliveries: [{ ships: 1 }] })
  })

  it("crew.activity builds live activity outside the cache, and the delivery ending closes it", () => {
    vi.useFakeTimers()
    const ev = eventData("crew.activity")
    const { result } = renderHook(() => useActivity(ev.conversation_id))
    expect(result.current).toBeUndefined()
    act(() => apply("crew.activity"))
    expect(result.current?.agentName).toBe(ev.agent_name)
    expect(result.current?.steps.map((s) => s.kind)).toEqual(["thought", "tool", "message"])
    expect(result.current?.endedAt).toBeUndefined()

    act(() => apply("delivery.changed"))
    expect(result.current?.endedAt).toBeDefined()
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBeUndefined() // grace over, no summary came

    act(() => apply("crew.activity"))
    expect(result.current).toBeDefined()
    act(() => apply("ship.turn")) // its stored summary replaces it
    expect(result.current).toBeUndefined()
  })
})

describe("agent events", () => {
  const touched = () => [
    keys.agents,
    keys.profile("a"),
    keys.secrets("a"),
    keys.team("d"),
    keys.crewTokens,
    keys.ships("c"),
    keys.inbox({}),
  ]

  it("agent.updated refetches the library, profile, secrets, teams, tokens, histories and inbox", () => {
    for (const k of touched()) seed(k)
    apply("agent.updated")
    for (const k of touched()) expect(invalidated(qc, k), JSON.stringify(k)).toBe(true)
  })

  it("agent.deleted refetches those and the hosting that went with it", () => {
    for (const k of [...touched().filter((k) => k[0] !== "profile" && k[0] !== "secrets"), keys.hosting, keys.environments])
      seed(k)
    apply("agent.deleted")
    for (const k of [keys.agents, keys.team("d"), keys.crewTokens, keys.ships("c"), keys.inbox({}), keys.hosting, keys.environments])
      expect(invalidated(qc, k), JSON.stringify(k)).toBe(true)
  })
})

describe("schedule events", () => {
  for (const type of ["schedule.created", "schedule.updated", "schedule.deleted", "schedule.fired"] as const) {
    it(`${type} refetches the schedules of its conversation only`, () => {
      const s = eventData(type)
      seed(keys.schedules(s.conversation_id))
      seed(keys.schedules("other"))
      apply(type)
      expect(invalidated(qc, keys.schedules(s.conversation_id))).toBe(true)
      expect(invalidated(qc, keys.schedules("other"))).toBe(false)
    })
  }
})

describe("vault events", () => {
  it("vault.created refetches the list only", () => {
    seed(keys.vaults, page<Vault>([]))
    apply("vault.created")
    expect(invalidated(qc, keys.vaults)).toBe(true)
  })

  it("vault.updated refetches the vault, the list and every agent's and dock's grants", () => {
    const v = eventData("vault.updated")
    seed(keys.vault(v.id), v)
    seed(keys.vaults, page<Vault>([v]))
    seed(keys.agentVaults("a"))
    seed(keys.dockVaults("d"))
    apply("vault.updated")
    expect(invalidated(qc, keys.vault(v.id))).toBe(true)
    expect(invalidated(qc, keys.vaults)).toBe(true)
    expect(invalidated(qc, keys.agentVaults("a"))).toBe(true)
    expect(invalidated(qc, keys.dockVaults("d"))).toBe(true)
  })

  it("vault.deleted drops the vault and its entries, and refetches the list and the grants", () => {
    const v = eventData("vault.deleted")
    seed(keys.vault(v.id), v)
    seed(keys.vaultEntries(v.id))
    seed(keys.vaults, page<Vault>([v]))
    seed(keys.agentVaults("a"))
    apply("vault.deleted")
    expect(qc.getQueryData(keys.vault(v.id))).toBeUndefined()
    expect(qc.getQueryData(keys.vaultEntries(v.id))).toBeUndefined()
    expect(invalidated(qc, keys.vaults)).toBe(true)
    expect(invalidated(qc, keys.agentVaults("a"))).toBe(true)
  })

  it("vault.entry carries no body: the entries, the entry, its versions and the vault's count refetch", () => {
    const ev = eventData("vault.entry")
    expect("body" in ev).toBe(false)
    seed(keys.vaultEntries(ev.vault_id))
    seed(keys.vaultEntry(ev.vault_id, ev.entry_id))
    seed(keys.vaultVersions(ev.vault_id, ev.entry_id))
    seed(keys.vault(ev.vault_id))
    seed(keys.vaults, page<Vault>([]))
    seed(keys.vault("other"))
    apply("vault.entry")
    expect(invalidated(qc, keys.vaultEntries(ev.vault_id))).toBe(true)
    expect(invalidated(qc, keys.vaultEntry(ev.vault_id, ev.entry_id))).toBe(true)
    expect(invalidated(qc, keys.vaultVersions(ev.vault_id, ev.entry_id))).toBe(true)
    expect(invalidated(qc, keys.vault(ev.vault_id))).toBe(true)
    expect(invalidated(qc, keys.vaults)).toBe(true)
    expect(invalidated(qc, keys.vault("other"))).toBe(false)
  })
})

describe("hosting events", () => {
  it("environment.updated stores the environment and refetches the list, versions and secrets", () => {
    const env = eventData("environment.updated")
    seed(keys.environments, page<Environment>([]))
    seed(keys.environment(env.id), { ...env, version: 1 })
    seed(keys.environmentVersions(env.id))
    seed(keys.environmentSecrets(env.id))
    apply("environment.updated")
    expect(qc.getQueryData<Environment>(keys.environment(env.id))?.version).toBe(env.version)
    expect(invalidated(qc, keys.environments)).toBe(true)
    expect(invalidated(qc, keys.environmentVersions(env.id))).toBe(true)
    expect(invalidated(qc, keys.environmentSecrets(env.id))).toBe(true)
  })

  it("environment.deleted drops its queries and builds, and refetches the list", () => {
    const { id } = eventData("environment.deleted")
    seed(keys.environments, page<Environment>([]))
    seed(keys.environment(id))
    seed(keys.build(id, 2))
    apply("environment.deleted")
    expect(invalidated(qc, keys.environments)).toBe(true)
    expect(qc.getQueryData(keys.environment(id))).toBeUndefined()
    expect(qc.getQueryData(keys.build(id, 2))).toBeUndefined()
  })

  it("environment.build keeps the log the event doesn't carry", () => {
    const b = eventData("environment.build")
    expect(b.log).toBeUndefined()
    seed(keys.build(b.environment_id, b.version), { ...b, state: "building", log: "step 1\nstep 2" })
    apply("environment.build")
    expect(qc.getQueryData<Build>(keys.build(b.environment_id, b.version))).toMatchObject({ state: "ready", log: "step 1\nstep 2" })
    expect(invalidated(qc, keys.build(b.environment_id, b.version))).toBe(true)
  })

  it("slot.updated stores the hosted agent, or null once unhosted", () => {
    const h = eventData("slot.updated")
    seed(keys.hosting)
    seed(keys.environments, page<Environment>([]))
    apply("slot.updated")
    expect(qc.getQueryData<Hosted>(keys.hosted(h.agent_id))?.slots[0].state).toBe("running")
    expect(invalidated(qc, keys.hosting)).toBe(true)
    expect(invalidated(qc, keys.environments)).toBe(true)

    // Go sends no environment_id (a zero id marshals as "") and no slots.
    apply("slot.updated", { ...h, environment_id: "", slots: [] })
    expect(qc.getQueryData(keys.hosted(h.agent_id))).toBeNull()
  })
})

describe("the stream", () => {
  class FakeSource {
    static last: FakeSource | undefined
    static readonly CLOSED = 2
    readyState = 1
    onerror: (() => void) | null = null
    private listeners = new Map<string, (e: MessageEvent) => void>()
    constructor() {
      FakeSource.last = this
    }
    addEventListener(type: string, l: (e: MessageEvent) => void) {
      this.listeners.set(type, l)
    }
    emit(type: string, data = "{}") {
      this.listeners.get(type)?.(new MessageEvent(type, { data }))
    }
    close() {}
  }
  afterEach(() => {
    live.stop()
    vi.unstubAllGlobals()
  })

  it("refetches what is shown on its first ready only (events before it subscribed are not replayed)", () => {
    vi.stubGlobal("EventSource", FakeSource)
    const invalidate = vi.spyOn(qc, "invalidateQueries")
    live.start(qc)
    FakeSource.last!.emit("ready")
    expect(invalidate).toHaveBeenCalledWith({ type: "active" })
    invalidate.mockClear()
    FakeSource.last!.emit("ready") // a reconnect: Last-Event-ID replays the gap
    expect(invalidate).not.toHaveBeenCalled()
  })
})
