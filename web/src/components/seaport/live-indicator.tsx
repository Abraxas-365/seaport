import { pollEvery, useLiveState, type LiveState } from "@/lib/live"
import { cn } from "@/lib/utils"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { StatusDot } from "./status"
import type { Tone } from "./tone"

const look: Record<LiveState, { tone: Tone; label: string; hint: string }> = {
  live: { tone: "success", label: "En vivo", hint: "Recibiendo cambios del puerto en tiempo real" },
  connecting: {
    tone: "warning",
    label: "Conectando…",
    hint: "Reconectando con el puerto; los datos pueden estar desactualizados",
  },
  polling: {
    tone: "warning",
    label: "Sin conexión en vivo",
    hint: `No llegan cambios en tiempo real (un proxy o túnel puede estar bloqueándolos). La página se actualiza cada ${pollEvery / 1000} s y no muestra lo que hace el agente mientras trabaja.`,
  },
  offline: { tone: "danger", label: "Sin conexión", hint: "Sin conexión con el puerto; recarga la página" },
}

/** Whether the live event stream is connected. */
export function LiveIndicator({ className, compact }: { className?: string; compact?: boolean }) {
  const state = useLiveState()
  const { tone, label, hint } = look[state]
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={cn("inline-flex items-center gap-2 text-xs text-muted-foreground", className)}>
          <StatusDot tone={tone} pulse={state === "live"} />
          {!compact && label}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{hint}</TooltipContent>
    </Tooltip>
  )
}
