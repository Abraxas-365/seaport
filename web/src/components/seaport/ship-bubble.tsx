import { AlertCircle, Bot, Check, CheckCheck, Clock, Hand, Hourglass } from "lucide-react"

import { formatFull, formatTime } from "@/lib/format"
import type { Ship } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { shipLabel } from "./tone"

/** Delivery state of a ship, as a tiny icon (WhatsApp-like ticks). */
export function ShipStatusIcon({ ship, className }: { ship: Ship; className?: string }) {
  const cls = cn("size-3.5 shrink-0", className)
  switch (ship.status) {
    case "held":
      return <Hand className={cls} />
    case "queued":
      return <Hourglass className={cls} />
    case "claimed":
      return <Bot className={cn(cls, "motion-safe:animate-pulse")} />
    case "handled":
      return <CheckCheck className={cls} />
    case "pending":
      return <Clock className={cls} />
    case "sent":
      return <Check className={cls} />
    case "failed":
      return <AlertCircle className={cls} />
  }
}

/** Why a ship was held (internal/conversation/customs.go, internal/agent/agent.go). */
const holdLabel: Record<string, string> = {
  "sender denied by a rule": "remitente bloqueado por una regla",
  "group message without a mention": "mensaje de grupo sin mención",
  "no agent is assigned to this dock": "ningún agente atiende este muelle",
  "no agent was called (mention one with @name)": "no se llamó a ningún agente (@nombre)",
  "its agent left the dock": "su agente dejó el muelle",
}

/**
 * One message in a conversation. Inbound on the left (sender shown in
 * groups), the agent's replies on the right in the brand color. Held
 * messages are dimmed with a dashed outline: customs or routing kept them.
 * `agentName` is the agent it went to (inbound) or that sent it (outbound).
 */
export function ShipBubble({
  ship,
  agentName,
  multiAgent = false,
  showSender = false,
  grouped = false,
}: {
  ship: Ship
  agentName?: string
  /** several agents in the chat: name the agent on the bubble, not only in its tooltip */
  multiAgent?: boolean
  showSender?: boolean
  /** follows a bubble of the same side: tighter spacing, no tail */
  grouped?: boolean
}) {
  const out = ship.direction === "out"
  const held = ship.status === "held"
  const failed = ship.status === "failed"
  return (
    <div
      data-slot="ship-bubble"
      data-direction={ship.direction}
      className={cn("flex w-full", out ? "justify-end" : "justify-start", grouped ? "mt-0.5" : "mt-3")}
    >
      <div
        className={cn(
          "group/bubble relative max-w-[min(78%,36rem)] rounded-2xl px-3 py-1.5 text-sm shadow-xs",
          out
            ? "bg-bubble-out text-bubble-out-foreground"
            : "bg-bubble-in text-foreground ring-1 ring-border/70",
          !grouped && (out ? "rounded-tr-md" : "rounded-tl-md"),
          held && "bg-transparent opacity-80 shadow-none ring-1 ring-dashed ring-warning/60",
          failed && "ring-2 ring-destructive/60",
        )}
      >
        {showSender && !out && ship.sender_name && (
          <div className="mb-0.5 text-xs font-semibold text-primary">{ship.sender_name}</div>
        )}
        <p className="break-words whitespace-pre-wrap [overflow-wrap:anywhere]">{ship.text}</p>
        <Tooltip>
          <TooltipTrigger asChild>
            <div
              className={cn(
                "mt-0.5 flex items-center justify-end gap-1 text-[0.68rem] tabular-nums",
                out ? "text-bubble-out-foreground/70" : "text-muted-foreground",
              )}
            >
              {held && <span className="font-medium text-warning">retenido</span>}
              {failed && <span className="font-medium">no se envió</span>}
              {agentName && multiAgent && <span className="font-mono">@{agentName}</span>}
              <span>{formatTime(ship.sent_at)}</span>
              <ShipStatusIcon ship={ship} className="size-3" />
            </div>
          </TooltipTrigger>
          <TooltipContent side={out ? "left" : "right"}>
            {shipLabel[ship.status]} · {formatFull(ship.sent_at)}
            {held && ship.hold_reason ? ` · ${holdLabel[ship.hold_reason] ?? ship.hold_reason}` : ""}
            {agentName ? ` · ${out ? "enviado por" : "para"} @${agentName}` : ""}
            {ship.error ? ` · ${ship.error}` : ""}
          </TooltipContent>
        </Tooltip>
      </div>
    </div>
  )
}

/** A centered divider between days in a conversation. */
export function DayDivider({ label }: { label: string }) {
  return (
    <div className="sticky top-2 z-10 my-4 flex justify-center">
      <span className="rounded-full bg-background/85 px-3 py-1 text-[0.7rem] font-medium text-muted-foreground shadow-xs ring-1 ring-border backdrop-blur first-letter:uppercase">
        {label}
      </span>
    </div>
  )
}
