// Live updates: one EventSource on /api/v1/events patches the React Query
// cache, so screens never poll. On "reset" (server restart or a gap older
// than the replay window) everything is refetched.

import { useEffect, useState } from "react"
import type { InfiniteData, QueryClient } from "@tanstack/react-query"

import { applyActivity, clearActivity, deliveryChanged, turnStored } from "./activity"
import { keys } from "./query"
import type {
  Build,
  Conversation,
  CrewStatus,
  Dock,
  EventMap,
  EventType,
  InboxItem,
  Paginated,
  Ship,
  Turn,
} from "./types"

export type LiveState = "connecting" | "live" | "offline"

type Listener = (state: LiveState) => void

/** History is an infinite query of pages, newest first. */
function upsertShip(qc: QueryClient, ship: Ship) {
  qc.setQueryData<InfiniteData<Paginated<Ship>>>(keys.ships(ship.conversation_id), (old) => {
    if (!old) return old
    for (let p = 0; p < old.pages.length; p++) {
      const i = old.pages[p].items.findIndex((s) => s.id === ship.id)
      if (i >= 0) {
        const pages = old.pages.slice()
        const items = pages[p].items.slice()
        items[i] = ship
        pages[p] = { ...pages[p], items }
        return { ...old, pages }
      }
    }
    const [first, ...rest] = old.pages
    if (!first) return old
    return {
      ...old,
      pages: [{ items: [ship, ...first.items], page: { ...first.page, total: first.page.total + 1 } }, ...rest],
    }
  })
}

function patchPage(old: Paginated<InboxItem>, conv: Conversation) {
  const i = old.items.findIndex((c) => c.id === conv.id)
  if (i < 0) return old
  const items = old.items.slice()
  items[i] = { ...items[i], ...conv }
  return { ...old, items }
}

/** Inbox caches are infinite (lists) or single pages (counters). */
function patchInboxConversation(qc: QueryClient, conv: Conversation) {
  qc.setQueriesData<InfiniteData<Paginated<InboxItem>> | Paginated<InboxItem>>(
    { queryKey: keys.inboxAll },
    (old) => {
      if (!old) return old
      if ("pages" in old) return { ...old, pages: old.pages.map((p) => patchPage(p, conv)) }
      return patchPage(old, conv)
    },
  )
}

function patchDock(qc: QueryClient, dock: Dock) {
  if (dock.status === "linking") {
    // Events never carry the pairing QR (they are stored): keep the one we
    // have and fetch the new one.
    const old = qc.getQueryData<Dock>(keys.dock(dock.id))
    dock = { ...dock, qr_code: old?.qr_code }
    void qc.invalidateQueries({ queryKey: keys.dock(dock.id), exact: true })
  }
  // A GitHub dock's state follows its installations: refetch them.
  if (dock.channel === "github") void qc.invalidateQueries({ queryKey: keys.github(dock.id) })
  if (dock.channel === "jira") void qc.invalidateQueries({ queryKey: keys.jira(dock.id) })
  qc.setQueryData(keys.dock(dock.id), dock)
  qc.setQueryData<Paginated<Dock>>(keys.docks, (old) => {
    if (!old) return old
    const i = old.items.findIndex((d) => d.id === dock.id)
    const items = old.items.slice()
    if (i >= 0) items[i] = dock
    else items.push(dock)
    return { ...old, items }
  })
}

const handlers: { [K in EventType]: (qc: QueryClient, data: EventMap[K]) => void } = {
  "dock.updated": patchDock,
  "dock.deleted": (qc, { id }) => {
    qc.setQueryData<Paginated<Dock>>(keys.docks, (old) =>
      old ? { ...old, items: old.items.filter((d) => d.id !== id) } : old,
    )
    qc.removeQueries({ queryKey: keys.dock(id) })
    qc.removeQueries({ queryKey: keys.customs(id) })
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "conversation.updated": (qc, conv) => {
    qc.setQueryData(keys.conversation(conv.id), conv)
    patchInboxConversation(qc, conv)
    // Order, status filters and new chats: let the list refetch.
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "customs.updated": (qc, { customs }) => {
    qc.setQueryData(keys.customs(customs.policy.dock_id), customs)
  },
  "ship.created": (qc, ship) => {
    upsertShip(qc, ship)
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "ship.updated": (qc, ship) => {
    upsertShip(qc, ship)
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "delivery.changed": (qc, ev) => {
    deliveryChanged(ev)
    if (ev.conversation_id) qc.invalidateQueries({ queryKey: keys.ships(ev.conversation_id) })
    else qc.invalidateQueries({ queryKey: ["ships"] })
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "ship.turn": (qc, turn) => {
    qc.setQueryData<Paginated<Turn>>(keys.turns(turn.conversation_id), (old) =>
      old && !old.items.some((t) => t.delivery_id === turn.delivery_id)
        ? { items: [turn, ...old.items], page: { ...old.page, total: old.page.total + 1 } }
        : old,
    )
    turnStored(turn)
  },
  "crew.changed": (qc, crew: CrewStatus) => {
    qc.setQueryData(keys.crew, crew)
  },
  // Live only, outside the query cache (src/lib/activity.ts).
  "crew.activity": (_qc, ev) => applyActivity(ev),
  // A default demotes another agent without its own event: refetch.
  // Unassigning or deleting an agent holds what waited for it (no ship
  // events): refetch histories and the inbox too.
  "agent.updated": (qc) => {
    qc.invalidateQueries({ queryKey: keys.agents })
    qc.invalidateQueries({ queryKey: keys.profileAll }) // a saved profile or secret
    qc.invalidateQueries({ queryKey: ["secrets"] })
    qc.invalidateQueries({ queryKey: keys.teamAll })
    qc.invalidateQueries({ queryKey: keys.crewTokens })
    qc.invalidateQueries({ queryKey: ["ships"] })
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "agent.deleted": (qc) => {
    qc.invalidateQueries({ queryKey: keys.agents })
    qc.invalidateQueries({ queryKey: keys.teamAll })
    qc.invalidateQueries({ queryKey: keys.crewTokens })
    qc.invalidateQueries({ queryKey: ["ships"] })
    qc.invalidateQueries({ queryKey: keys.inboxAll })
    qc.invalidateQueries({ queryKey: keys.hosting }) // its hosting went with it
    qc.invalidateQueries({ queryKey: keys.environments })
  },
  "environment.updated": (qc, env) => {
    qc.setQueryData(keys.environment(env.id), env)
    qc.invalidateQueries({ queryKey: keys.environments, exact: true })
    qc.invalidateQueries({ queryKey: keys.environmentVersions(env.id) })
  },
  "environment.deleted": (qc, { id }) => {
    qc.invalidateQueries({ queryKey: keys.environments, exact: true })
    qc.removeQueries({ queryKey: ["environments", id] })
    qc.removeQueries({ queryKey: ["build", id] })
  },
  // Events carry no log: a running build's query refetches for it.
  "environment.build": (qc, b) => {
    qc.setQueryData<Build | null>(keys.build(b.environment_id, b.version), (old) => ({ ...b, log: old?.log }))
    qc.invalidateQueries({ queryKey: keys.build(b.environment_id, b.version) })
  },
  // Unhosting sends the agent with no environment and no slots.
  "slot.updated": (qc, h) => {
    qc.setQueryData(keys.hosted(h.agent_id), h.environment_id ? h : null)
    qc.invalidateQueries({ queryKey: keys.hosting, exact: true })
    qc.invalidateQueries({ queryKey: keys.environments, exact: true })
  },
}

/** One shared stream for the whole app. */
class LiveStream {
  private source: EventSource | null = null
  private state: LiveState = "connecting"
  private listeners = new Set<Listener>()

  start(qc: QueryClient) {
    if (this.source) return
    const src = new EventSource("/api/v1/events", { withCredentials: true })
    this.source = src
    this.set("connecting")
    src.addEventListener("ready", () => this.set("live"))
    src.addEventListener("reset", () => {
      clearActivity()
      qc.invalidateQueries()
    })
    src.onerror = () => {
      // EventSource reconnects by itself (retry: 3000) and resends
      // Last-Event-ID; a closed source (e.g. 401) does not.
      this.set(src.readyState === EventSource.CLOSED ? "offline" : "connecting")
    }
    for (const type of Object.keys(handlers) as EventType[]) {
      src.addEventListener(type, (e) => {
        try {
          const data = JSON.parse((e as MessageEvent).data)
          ;(handlers[type] as (qc: QueryClient, d: unknown) => void)(qc, data)
        } catch (err) {
          console.warn("seaport event", type, err)
        }
      })
    }
  }

  stop() {
    this.source?.close()
    this.source = null
    this.set("offline")
  }

  private set(s: LiveState) {
    this.state = s
    this.listeners.forEach((l) => l(s))
  }

  subscribe(l: Listener) {
    this.listeners.add(l)
    l(this.state)
    return () => {
      this.listeners.delete(l)
    }
  }
}

export const live = new LiveStream()

export function useLiveEvents(qc: QueryClient, enabled: boolean) {
  useEffect(() => {
    if (!enabled) return
    live.start(qc)
    return () => live.stop()
  }, [qc, enabled])
}

export function useLiveState() {
  const [state, setState] = useState<LiveState>("connecting")
  useEffect(() => live.subscribe(setState), [])
  return state
}
