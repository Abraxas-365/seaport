// What agents are doing right now, per conversation: built from
// crew.activity events (live only: the server keeps none of it). It is not
// server state, so it lives here instead of the React Query cache; a page
// opened mid-turn shows what arrives from then on. When the delivery ends
// the activity stays (ended) until the stored turn summary (ship.turn)
// replaces it, so the panel turns into the summary without a gap.

import { useSyncExternalStore } from "react"

import type { ActivityEvent, DeliveryEvent, ID, ToolStatus, Turn } from "./types"

export type ActivityStep =
  | { kind: "thought" | "message"; text: string; at: number }
  | {
      kind: "tool"
      id: string
      title: string
      toolKind: string
      status: ToolStatus
      at: number
      /** When it completed or failed. */
      endedAt?: number
    }

export interface Activity {
  delivery: ID
  agentName: string
  steps: ActivityStep[]
  startedAt: number
  /** The delivery ended; the stored summary is on its way. */
  endedAt?: number
}

/** A turn keeps its latest steps, and the tail of long texts. */
const maxSteps = 200
const maxText = 8000
/** How long an ended turn waits for its summary (none comes for a turn
 * without tool calls). */
const endedGrace = 4000

const byConversation = new Map<ID, Activity>()
const listeners = new Set<() => void>()

function emit() {
  listeners.forEach((l) => l())
}

function tail(s: string) {
  return s.length > maxText ? "…" + s.slice(s.length - maxText) : s
}

/** Applies one crew.activity event. Steps are replaced (not mutated) so
 * React sees the change. */
export function applyActivity(ev: ActivityEvent) {
  const at = Date.parse(ev.at) || Date.now()
  let a = byConversation.get(ev.conversation_id)
  if (!a || a.delivery !== ev.delivery_id) {
    // A new turn (or one opened before this page): start over.
    a = { delivery: ev.delivery_id, agentName: ev.agent_name, steps: [], startedAt: at }
  }
  const steps = a.steps.slice()
  for (const it of ev.items) {
    if (it.kind === "tool") {
      if (!it.tool) continue
      const i = steps.findIndex((s) => s.kind === "tool" && s.id === it.tool)
      const status = it.status || undefined
      if (i >= 0) {
        const s = steps[i] as Extract<ActivityStep, { kind: "tool" }>
        const next = { ...s, title: it.title || s.title, toolKind: it.tool_kind || s.toolKind, status: status ?? s.status }
        if ((next.status === "completed" || next.status === "failed") && !s.endedAt) next.endedAt = at
        steps[i] = next
      } else {
        const st = status ?? "pending"
        steps.push({
          kind: "tool",
          id: it.tool,
          title: it.title || "Herramienta",
          toolKind: it.tool_kind ?? "",
          status: st,
          at,
          endedAt: st === "completed" || st === "failed" ? at : undefined,
        })
      }
    } else if (it.text) {
      const last = steps[steps.length - 1]
      if (last && last.kind === it.kind) steps[steps.length - 1] = { ...last, text: tail(last.text + it.text) }
      else steps.push({ kind: it.kind, text: tail(it.text), at })
    }
  }
  byConversation.set(ev.conversation_id, { ...a, steps: steps.slice(-maxSteps) })
  emit()
}

/** A delivery that ended marks its activity ended; the stored summary
 * (turnStored) or a short grace removes it. */
export function deliveryChanged(ev: DeliveryEvent) {
  if (ev.state !== "acked" && ev.state !== "released" && ev.state !== "expired") return
  for (const [conv, a] of byConversation) {
    if (a.endedAt) continue
    if ((ev.conversation_id && conv === ev.conversation_id) || (ev.delivery_id && a.delivery === ev.delivery_id)) {
      byConversation.set(conv, { ...a, endedAt: Date.now() })
      setTimeout(() => {
        if (byConversation.get(conv)?.delivery === a.delivery) {
          byConversation.delete(conv)
          emit()
        }
      }, endedGrace)
      emit()
    }
  }
}

/** The turn's summary was stored: it replaces the live activity. */
export function turnStored(turn: Turn) {
  const a = byConversation.get(turn.conversation_id)
  if (a && a.delivery === turn.delivery_id) {
    byConversation.delete(turn.conversation_id)
    emit()
  }
}

/** Forget everything (the stream reset: turns may have ended unseen). */
export function clearActivity() {
  if (byConversation.size === 0) return
  byConversation.clear()
  emit()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** The running turn's activity in a conversation, if any arrived. */
export function useActivity(conversation: ID): Activity | undefined {
  return useSyncExternalStore(subscribe, () => byConversation.get(conversation))
}
