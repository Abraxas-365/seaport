import { Star } from "lucide-react"

import { cn } from "@/lib/utils"
import { StatusDot } from "./status"

/**
 * A library agent by its handle: how people call it (@name). The dot says
 * whether a connection works as it right now; the star marks a dock's
 * default (it takes the messages that call nobody).
 */
export function AgentChip({
  name,
  online,
  isDefault,
  className,
}: {
  name: string
  /** undefined: unknown (no crew permission), no dot */
  online?: boolean
  isDefault?: boolean
  className?: string
}) {
  return (
    <span
      data-slot="agent-chip"
      className={cn(
        "inline-flex h-6 max-w-full items-center gap-1.5 rounded-md bg-muted px-2 font-mono text-xs",
        className,
      )}
      title={online === undefined ? undefined : online ? "Conectado" : "Sin conexión"}
    >
      {online !== undefined && <StatusDot tone={online ? "success" : "neutral"} className="size-1.5" />}
      <span className="truncate">@{name}</span>
      {isDefault && <Star className="size-3 shrink-0 fill-current text-primary" aria-label="por defecto" />}
    </span>
  )
}
