import * as React from "react"
import {
  ArrowRightLeft,
  Brain,
  ChevronDown,
  FileText,
  Globe,
  MessageSquareText,
  Pencil,
  Search,
  TerminalSquare,
  Trash2,
  Wrench,
  type LucideIcon,
} from "lucide-react"

import type { Activity, ActivityStep } from "@/lib/activity"
import type { ToolStatus } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Spinner } from "@/components/ui/spinner"
import { StatusDot } from "./status"
import { toneClasses, toolLabel, toolTone } from "./tone"

/** ACP tool kinds → icon. */
export const kindIcon: Record<string, LucideIcon> = {
  read: FileText,
  edit: Pencil,
  delete: Trash2,
  move: ArrowRightLeft,
  search: Search,
  execute: TerminalSquare,
  think: Brain,
  fetch: Globe,
}

/** Steps shown before "ver todo". */
const recent = 6

function useNow() {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  return now
}

export function duration(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
}

/**
 * What an agent is doing on a conversation right now: its thinking, its
 * text and the tools it calls (title and status), as it happens. Live
 * only: nothing of it is stored, so it shows what arrived since the page
 * opened.
 */
export function AgentActivity({ activity, className }: { activity: Activity; className?: string }) {
  const [open, setOpen] = React.useState(true)
  const [all, setAll] = React.useState(false)
  const tools = activity.steps.filter((s) => s.kind === "tool")
  const ticking = useNow()
  // An ended turn stops its clocks (its summary replaces it shortly).
  const now = activity.endedAt ?? ticking
  const shown = all ? activity.steps : activity.steps.slice(-recent)
  const hidden = activity.steps.length - shown.length
  const current = activity.steps[activity.steps.length - 1]

  return (
    <div
      data-slot="agent-activity"
      className={cn(
        "ml-auto w-full max-w-md overflow-hidden rounded-2xl rounded-tr-md bg-background/80 text-xs ring-1 ring-primary/15",
        className,
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/50"
      >
        <StatusDot tone={activity.endedAt ? "neutral" : "info"} pulse={!activity.endedAt} />
        <span className="min-w-0 flex-1 truncate">
          <span className="font-medium">@{activity.agentName}</span>{" "}
          <span className="text-muted-foreground">
            {activity.endedAt ? "trabajó" : open || !current ? "trabajando" : summary(current)}
            {tools.length > 0 && ` · ${tools.length} ${tools.length === 1 ? "herramienta" : "herramientas"}`}
          </span>
        </span>
        <span className="font-mono text-muted-foreground tabular-nums">{duration(now - activity.startedAt)}</span>
        <ChevronDown className={cn("size-3.5 text-muted-foreground transition-transform", !open && "-rotate-90")} />
      </button>
      {open && (
        <ol className="space-y-1.5 border-t px-3 py-2">
          {hidden > 0 && (
            <li>
              <button type="button" className="text-muted-foreground underline-offset-2 hover:underline" onClick={() => setAll(true)}>
                Ver {hidden} {hidden === 1 ? "paso anterior" : "pasos anteriores"}
              </button>
            </li>
          )}
          {shown.map((s, i) => (
            <Step key={s.kind === "tool" ? s.id : `${s.kind}-${s.at}-${i}`} step={s} now={now} />
          ))}
        </ol>
      )}
    </div>
  )
}

function summary(s: ActivityStep) {
  if (s.kind === "tool") return s.title
  return s.kind === "thought" ? "pensando…" : "escribiendo…"
}

function Step({ step, now }: { step: ActivityStep; now: number }) {
  if (step.kind === "tool") {
    return (
      <ToolRow
        title={step.title}
        kind={step.toolKind}
        status={step.status}
        ms={(step.endedAt ?? now) - step.at}
        live
      />
    )
  }
  return <TextStep thought={step.kind === "thought"} text={step.text} />
}

/** One tool call: kind icon, title, duration and status (a spinner while
 * it runs, when live). */
export function ToolRow({
  title,
  kind,
  status,
  ms,
  live = false,
}: {
  title: string
  kind?: string
  status: ToolStatus
  ms?: number
  live?: boolean
}) {
  const Icon = kindIcon[kind ?? ""] ?? Wrench
  const busy = live && (status === "in_progress" || status === "pending")
  return (
    <li className="flex items-center gap-2" title={toolLabel[status]}>
      <Icon className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate font-mono">{title}</span>
      {ms !== undefined && <span className="font-mono text-muted-foreground tabular-nums">{duration(ms)}</span>}
      {busy ? (
        <Spinner className={cn("size-3.5", toneClasses[toolTone[status]].text)} aria-label={toolLabel[status]} />
      ) : (
        <StatusDot tone={toolTone[status]} aria-label={toolLabel[status]} className="mx-0.75" />
      )}
    </li>
  )
}

/** A thought (muted, italic) or the agent's own text, clamped. */
function TextStep({ thought, text }: { thought: boolean; text: string }) {
  const [full, setFull] = React.useState(false)
  const Icon = thought ? Brain : MessageSquareText
  const long = text.length > 280 || text.split("\n").length > 4
  return (
    <li className="flex gap-2">
      <Icon className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <p
          className={cn(
            "break-words whitespace-pre-wrap",
            thought ? "text-muted-foreground italic" : "text-foreground",
            !full && "line-clamp-4",
          )}
        >
          {text.trim()}
        </p>
        {long && (
          <button
            type="button"
            className="text-muted-foreground underline-offset-2 hover:underline"
            onClick={() => setFull((f) => !f)}
          >
            {full ? "menos" : "más"}
          </button>
        )}
      </div>
    </li>
  )
}
