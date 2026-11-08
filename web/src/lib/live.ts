// Live updates: one EventSource on /api/v1/events patches the React Query
// cache, so screens never poll. On "reset" (server restart or a gap older
// than the replay window) everything is refetched.

import { useEffect, useState } from "react"
import type { InfiniteData, QueryClient } from "@tanstack/react-query"

import { keys } from "./query"
import type {
  Conversation,
  CrewStatus,
  Dock,
  EventMap,
  EventType,
  InboxItem,
  Paginated,
  Ship,
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
    if (ev.conversation_id) qc.invalidateQueries({ queryKey: keys.ships(ev.conversation_id) })
    else qc.invalidateQueries({ queryKey: ["ships"] })
    qc.invalidateQueries({ queryKey: keys.inboxAll })
  },
  "crew.changed": (qc, crew: CrewStatus) => {
    qc.setQueryData(keys.crew, crew)
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
    src.addEventListener("reset", () => qc.invalidateQueries())
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
          console.warn("puerto event", type, err)
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
