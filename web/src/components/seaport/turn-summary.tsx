import * as React from "react"
import { ChevronDown } from "lucide-react"

import type { Turn } from "@/lib/types"
import { cn } from "@/lib/utils"
import { StatusDot } from "./status"
import { ToolRow, duration } from "./agent-activity"
import { turnTone } from "./tone"

const outcomeLabel: Record<Turn["outcome"], string> = {
  acked: "trabajó",
  released: "falló tras",
  dropped: "se desconectó tras",
}

/** Steps shown before "ver todo". */
const recent = 8

function ms(from: string, to?: string) {
  if (!to) return undefined
  return new Date(to).getTime() - new Date(from).getTime()
}

/**
 * What an agent did on one delivery, stored when it ended (ship.turn):
 * the tools it called with their status and duration. Folded by default,
 * under the answer it led to.
 */
export function TurnSummary({ turn, className }: { turn: Turn; className?: string }) {
  const [open, setOpen] = React.useState(false)
  const [all, setAll] = React.useState(false)
  const failed = turn.tools.filter((t) => t.status === "failed").length
  const count = turn.tools.length + (turn.omitted ?? 0)
  const shown = all ? turn.tools : turn.tools.slice(-recent)
  const hidden = turn.tools.length - shown.length
  return (
    <div
      data-slot="turn-summary"
      className={cn("ml-auto w-full max-w-md overflow-hidden rounded-xl text-xs", open && "bg-background/80 ring-1 ring-border", className)}
    >
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center justify-end gap-2 px-3 py-1.5 text-left text-muted-foreground hover:text-foreground"
      >
        <StatusDot tone={turnTone[turn.outcome]} />
        <span className="truncate">
          @{turn.agent_name} {outcomeLabel[turn.outcome]} {duration(ms(turn.started_at, turn.ended_at) ?? 0)} · {count}{" "}
          {count === 1 ? "herramienta" : "herramientas"}
          {failed > 0 && ` · ${failed} ${failed === 1 ? "falló" : "fallaron"}`}
        </span>
        <ChevronDown className={cn("size-3.5 shrink-0 transition-transform", !open && "-rotate-90")} />
      </button>
      {open && (
        <ol className="space-y-1.5 border-t px-3 py-2">
          {hidden > 0 && (
            <li>
              <button type="button" className="text-muted-foreground underline-offset-2 hover:underline" onClick={() => setAll(true)}>
                Ver {hidden} {hidden === 1 ? "herramienta anterior" : "herramientas anteriores"}
              </button>
            </li>
          )}
          {shown.map((t, i) => (
            <ToolRow key={i} title={t.title} kind={t.kind} status={t.status} ms={ms(t.started_at, t.ended_at)} />
          ))}
          {(turn.omitted ?? 0) > 0 && (
            <li className="text-muted-foreground">y {turn.omitted} más (no se guardaron)</li>
          )}
        </ol>
      )}
    </div>
  )
}
