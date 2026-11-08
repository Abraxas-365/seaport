import { useLiveState } from "@/lib/live"
import { cn } from "@/lib/utils"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { StatusDot } from "./status"

/** Whether the live event stream is connected. */
export function LiveIndicator({ className, compact }: { className?: string; compact?: boolean }) {
  const state = useLiveState()
  const tone = state === "live" ? "success" : state === "connecting" ? "warning" : "danger"
  const label = state === "live" ? "En vivo" : state === "connecting" ? "Conectando…" : "Sin conexión"
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={cn("inline-flex items-center gap-2 text-xs text-muted-foreground", className)}>
          <StatusDot tone={tone} pulse={state === "live"} />
          {!compact && label}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        {state === "live"
          ? "Recibiendo cambios del puerto en tiempo real"
          : "Reconectando con el puerto; los datos pueden estar desactualizados"}
      </TooltipContent>
    </Tooltip>
  )
}
