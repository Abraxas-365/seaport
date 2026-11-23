import { AlarmClock, CalendarClock, Play, Trash2 } from "lucide-react"

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
 * (only given to those who may write). A one-shot shows its time instead of
 * a cron; one an agent set is an alarm (`setBy`: that agent's name), and
 * one that already ran is "Hecha".
 */
export function ScheduleRow({
  schedule: s,
  agentName,
  setBy,
  now,
  busy,
  onToggle,
  onRun,
  onDelete,
  className,
}: {
  schedule: Schedule
  agentName?: string
  /** the name of the agent that set it, when it is an alarm */
  setBy?: string
  /** the page's clock (useNow), for the relative times */
  now: number
  busy?: boolean
  onToggle?: (enabled: boolean) => void
  onRun?: () => void
  onDelete?: () => Promise<unknown>
  className?: string
}) {
  const oneShot = !!s.run_at
  const done = !!s.done_at
  const alarm = !!s.created_by
  return (
    <li data-slot="schedule-row" className={cn("space-y-1.5 rounded-lg border p-3", !s.enabled && "opacity-70", className)}>
      <div className="flex items-center gap-2">
        {alarm ? (
          <AlarmClock className="size-4 shrink-0 text-muted-foreground" aria-label="Alarma" />
        ) : (
          <CalendarClock className="size-4 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={s.name}>
          {s.name}
        </span>
        {onToggle && !done && (
          <Switch
            aria-label={s.enabled ? "Desactivar programación" : "Activar programación"}
            checked={s.enabled}
            disabled={busy}
            onCheckedChange={onToggle}
          />
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {oneShot ? (
          <span title={formatFull(s.run_at!)}>{alarm ? "Alarma" : "Una vez"}</span>
        ) : (
          <>
            <span title={`${s.cron} · ${s.timezone}`}>{describeCron(s.cron)}</span>
            {" · "}
            {s.timezone}
          </>
        )}
        {agentName && <> · @{agentName}</>}
        {alarm && setBy && <> · puesta por @{setBy}</>}
      </p>
      <p className="line-clamp-2 text-xs break-words text-foreground/80">{s.text}</p>
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
          {done ? (
            <span title={formatFull(s.done_at!)}>Hecha {formatRelative(s.done_at!, now)}</span>
          ) : s.enabled ? (
            <span title={formatFull(s.next_at)}>{oneShot ? "Suena" : "Próxima"} {formatRelative(s.next_at, now)}</span>
          ) : (
            "Desactivada"
          )}
          {!done && s.last_at && (
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
