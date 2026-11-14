import { CalendarClock, Play, Trash2 } from "lucide-react"

import { describeCron } from "@/lib/cron"
import { formatFull, formatRelative } from "@/lib/format"
import type { Schedule } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmAction } from "./confirm"

/**
 * One schedule of a conversation: name, its cron in words, when it runs
 * next (or that it is off), with the enabled switch, run-now and delete
 * (only given to those who may write).
 */
export function ScheduleRow({
  schedule: s,
  agentName,
  now,
  busy,
  onToggle,
  onRun,
  onDelete,
  className,
}: {
  schedule: Schedule
  agentName?: string
  /** the page's clock (useNow), for the relative times */
  now: number
  busy?: boolean
  onToggle?: (enabled: boolean) => void
  onRun?: () => void
  onDelete?: () => Promise<unknown>
  className?: string
}) {
  return (
    <li data-slot="schedule-row" className={cn("space-y-1.5 rounded-lg border p-3", !s.enabled && "opacity-70", className)}>
      <div className="flex items-center gap-2">
        <CalendarClock className="size-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={s.name}>
          {s.name}
        </span>
        {onToggle && (
          <Switch
            aria-label={s.enabled ? "Desactivar programación" : "Activar programación"}
            checked={s.enabled}
            disabled={busy}
            onCheckedChange={onToggle}
          />
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        <span title={`${s.cron} · ${s.timezone}`}>{describeCron(s.cron)}</span>
        {" · "}
        {s.timezone}
        {agentName && <> · @{agentName}</>}
      </p>
      <p className="line-clamp-2 text-xs break-words text-foreground/80">{s.text}</p>
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
          {s.enabled ? (
            <span title={formatFull(s.next_at)}>Próxima {formatRelative(s.next_at, now)}</span>
          ) : (
            "Desactivada"
          )}
          {s.last_at && (
            <>
              {" · "}
              <span title={formatFull(s.last_at)}>última {formatRelative(s.last_at, now)}</span>
            </>
          )}
        </span>
        {onRun && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={onRun} title="Enviar el mensaje ahora">
            <Play /> Ahora
          </Button>
        )}
        {onDelete && (
          <ConfirmAction
            destructive
            title={`¿Eliminar «${s.name}»?`}
            description={<p>Deja de enviarse. Los mensajes que ya envió se quedan en la conversación.</p>}
            actionLabel="Eliminar"
            onConfirm={onDelete}
            trigger={
              <Button size="icon-sm" variant="ghost" className="text-destructive" aria-label="Eliminar programación">
                <Trash2 />
              </Button>
            }
          />
        )}
      </div>
    </li>
  )
}
