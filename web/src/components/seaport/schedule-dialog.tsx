import * as React from "react"
import { Plus } from "lucide-react"

import { cronPresets, describeCron } from "@/lib/cron"
import { useCreateSchedule, useSchedulePreview } from "@/lib/hooks"
import type { TeamMember } from "@/lib/types"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"

/** "": the dock's routing picks the agent (Select has no empty item value). */
const routing = "__routing__"

const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"

/** The next fire times of an expression in a zone, as the server computes them. */
export function SchedulePreview({ cron, timezone }: { cron: string; timezone: string }) {
  const preview = useSchedulePreview(cron, timezone)
  if (!cron.trim()) return null
  if (preview.isPending) return <p className="text-xs text-muted-foreground">Calculando…</p>
  if (preview.isError)
    return <p className="text-xs text-destructive">{preview.error instanceof Error ? preview.error.message : "Cron no válido"}</p>
  const fmt = new Intl.DateTimeFormat("es", { dateStyle: "full", timeStyle: "short", timeZone: timezone })
  return (
    <ul className="space-y-0.5 text-xs text-muted-foreground">
      {preview.data.preview.map((t) => (
        <li key={t}>{fmt.format(new Date(t))}</li>
      ))}
    </ul>
  )
}

/**
 * "Nueva programación": name, cron (four presets or custom, with the next
 * three times the server computes), time zone (the browser's), the agent
 * to call (from the dock's team) and the text.
 */
export function NewScheduleDialog({ conversationId, team }: { conversationId: string; team: TeamMember[] }) {
  const create = useCreateSchedule()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [cron, setCron] = React.useState<string>(cronPresets[1].cron)
  const [zone, setZone] = React.useState(browserZone)
  const [agent, setAgent] = React.useState(routing)
  const [text, setText] = React.useState("")
  const reset = () => {
    setName("")
    setText("")
    setAgent(routing)
    setCron(cronPresets[1].cron)
  }
  const ready = name.trim() && cron.trim() && zone.trim() && text.trim()
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) reset()
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <Plus /> Nueva
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              {
                conversation_id: conversationId,
                name: name.trim(),
                cron: cron.trim(),
                timezone: zone.trim(),
                text: text.trim(),
                agent_id: agent === routing ? undefined : agent,
              },
              { onSuccess: () => setOpen(false) },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Nueva programación</DialogTitle>
            <DialogDescription>
              Seaport escribe este mensaje en la conversación a las horas indicadas, como «Seaport · nombre». Pasa por la
              aduana y el enrutamiento como cualquier mensaje.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field>
              <FieldLabel htmlFor="schedule-name">Nombre</FieldLabel>
              <Input
                id="schedule-name"
                value={name}
                placeholder="standup"
                maxLength={100}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="schedule-cron">Cuándo</FieldLabel>
              <div className="flex flex-wrap gap-1.5">
                {cronPresets.map((p) => (
                  <Button
                    key={p.cron}
                    type="button"
                    size="xs"
                    variant={cron === p.cron ? "default" : "outline"}
                    onClick={() => setCron(p.cron)}
                  >
                    {p.label}
                  </Button>
                ))}
              </div>
              <Input
                id="schedule-cron"
                className="font-mono"
                value={cron}
                placeholder="0 9 * * 1-5"
                onChange={(e) => setCron(e.target.value)}
              />
              <FieldDescription>
                minuto hora día-del-mes mes día-de-la-semana. Como máximo una vez cada 5 minutos.{" "}
                {describeCron(cron) !== cron && <b>{describeCron(cron)}.</b>}
              </FieldDescription>
              <SchedulePreview cron={cron} timezone={zone} />
            </Field>
            <Field>
              <FieldLabel htmlFor="schedule-zone">Zona horaria</FieldLabel>
              <Input id="schedule-zone" value={zone} placeholder="America/Lima" onChange={(e) => setZone(e.target.value)} />
              <FieldDescription>Nombre IANA (America/Lima, Europe/Madrid, UTC). La hora se lee en esa zona.</FieldDescription>
            </Field>
            <Field>
              <FieldLabel>Agente</FieldLabel>
              <Select value={agent} onValueChange={setAgent}>
                <SelectTrigger aria-label="Agente">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={routing}>El que decida el muelle</SelectItem>
                  {team.map((m) => (
                    <SelectItem key={m.agent_id} value={m.agent_id} className="font-mono">
                      @{m.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FieldDescription>Si eliges uno, el mensaje lo llama por su nombre.</FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="schedule-text">Mensaje</FieldLabel>
              <Textarea
                id="schedule-text"
                rows={4}
                value={text}
                placeholder="Resume lo que pasó ayer en el repositorio y propón las prioridades de hoy."
                onChange={(e) => setText(e.target.value)}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!ready || create.isPending}>
              {create.isPending && <Spinner />} Crear
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
