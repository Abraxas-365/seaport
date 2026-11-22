import * as React from "react"
import { Link } from "react-router"
import { BellRing, X } from "lucide-react"
import { toast } from "sonner"

import { useCan, useDestinations, useDocks, useUpdateHosting } from "@/lib/hooks"
import { alertWaitMinutes, defaultAlertWait, maxAlertWait } from "@/lib/machine"
import { Perm } from "@/lib/permissions.gen"
import type { Hosted, LibraryAgent } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Mono, channelLabel, channelOpens } from "@/components/seaport"

/** "10 minutos", "1 hora", "1 h 30 min". */
export function waitLabel(minutes: number): string {
  const h = Math.floor(minutes / 60)
  const m = minutes % 60
  if (!h) return `${m} ${m === 1 ? "minuto" : "minutos"}`
  if (!m) return `${h} ${h === 1 ? "hora" : "horas"}`
  return `${h} h ${m} min`
}

/**
 * Where the port tells a person that the hosted agent needs one: a failed
 * instance or image, an image building too long, conversations waiting.
 * The destination is one of a WhatsApp, Slack or API dock (they open
 * conversations); the card shows why the last notice failed, if it did.
 */
export function AlertsCard({ agent, hosted, write }: { agent: LibraryAgent; hosted: Hosted; write: boolean }) {
  const can = useCan()
  const docks = useDocks()
  const update = useUpdateHosting(agent.id)
  const alerts = hosted.alerts
  const stored = {
    dock: alerts?.dock_id ?? "",
    destination: alerts?.destination ?? "",
    wait: String(alertWaitMinutes(alerts?.wait ?? "") ?? defaultAlertWait),
  }
  const [form, setForm] = React.useState(stored)
  const [seen, setSeen] = React.useState(alerts)
  if (seen !== alerts) {
    // Someone (or a save of ours) changed them: show what is stored.
    setSeen(alerts)
    setForm(stored)
  }

  const candidates = (docks.data?.items ?? []).filter((d) => channelOpens[d.channel] && d.replies)
  const named = (id: string) => docks.data?.items.find((d) => d.id === id)
  const current = alerts && named(alerts.dock_id)
  const minutes = Number(form.wait)
  const waitOk = Number.isInteger(minutes) && minutes >= 1 && minutes <= maxAlertWait
  const changed =
    form.dock !== stored.dock || form.destination !== stored.destination || form.wait !== stored.wait
  const ready = form.dock !== "" && form.destination !== "" && waitOk

  const save = () =>
    update.mutate(
      { alerts: { dock_id: form.dock, destination: form.destination, wait: `${minutes}m` } },
      { onSuccess: () => toast.success("Avisos guardados") },
    )
  const off = () => update.mutate({ alerts: null }, { onSuccess: () => toast.success("Avisos apagados") })

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <BellRing className="size-4" /> Avisos
        </CardTitle>
        <CardDescription>
          El puerto escribe a un destino de WhatsApp, Slack o API cuando una instancia falla, la imagen no se construye
          o lleva 45 minutos construyéndose, o una conversación espera demasiado. Avisa una vez por problema y otra
          cuando se resuelve, en la misma conversación.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {alerts ? (
          <p className="text-sm" data-testid="alerts-now">
            Ahora: <Mono>{current?.name ?? alerts.dock_id}</Mono>
            {current && ` (${channelLabel[current.channel]})`} · <Mono>{alerts.destination}</Mono> · conversaciones
            esperando más de {waitLabel(alertWaitMinutes(alerts.wait) ?? defaultAlertWait)}
          </p>
        ) : (
          <p className="text-sm text-muted-foreground" data-testid="alerts-now">
            Sin avisos: nadie se entera si el agente se cae.
          </p>
        )}
        {alerts?.error && (
          <p className="rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm" role="alert">
            No se pudo enviar el último aviso: {alerts.error}. El puerto lo intenta de nuevo cada 5 minutos.
          </p>
        )}

        {write && can(Perm.DocksRead) && (
          <div className="space-y-3">
            {docks.isPending ? (
              <Skeleton className="h-9 w-56" />
            ) : candidates.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                Ningún muelle puede abrir conversaciones: los avisos necesitan uno de WhatsApp, Slack o API con
                respuestas activadas.
              </p>
            ) : (
              <div className="flex flex-wrap items-end gap-2">
                <Select
                  value={form.dock}
                  onValueChange={(dock) => setForm({ ...form, dock, destination: "" })}
                  disabled={update.isPending}
                >
                  <SelectTrigger className="w-56" aria-label="Muelle de los avisos">
                    <SelectValue placeholder="Elegir un muelle" />
                  </SelectTrigger>
                  <SelectContent>
                    {candidates.map((d) => (
                      <SelectItem key={d.id} value={d.id}>
                        {d.name} ({channelLabel[d.channel]})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {form.dock && (
                  <AlertDestinationPicker
                    dock={form.dock}
                    value={form.destination}
                    busy={update.isPending}
                    onPick={(destination) => setForm({ ...form, destination })}
                  />
                )}
              </div>
            )}
            <Field className="max-w-xs">
              <FieldLabel htmlFor="alerts-wait">Avisar si una conversación espera más de (minutos)</FieldLabel>
              <Input
                id="alerts-wait"
                type="number"
                inputMode="numeric"
                min={1}
                max={maxAlertWait}
                value={form.wait}
                onChange={(e) => setForm({ ...form, wait: e.target.value })}
                aria-invalid={!waitOk}
              />
              <FieldDescription>
                {waitOk ? waitLabel(minutes) : `Entre 1 y ${maxAlertWait} minutos (24 horas).`}
              </FieldDescription>
            </Field>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={save} disabled={!ready || !changed || update.isPending}>
                {update.isPending && <Spinner />} Guardar avisos
              </Button>
              {alerts && (
                <Button size="sm" variant="ghost" onClick={off} disabled={update.isPending}>
                  <X /> Apagar avisos
                </Button>
              )}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/** The destinations of the picked dock, or a link to make one. */
function AlertDestinationPicker({
  dock,
  value,
  busy,
  onPick,
}: {
  dock: string
  value: string
  busy: boolean
  onPick: (name: string) => void
}) {
  const dests = useDestinations(dock)
  if (dests.isLoading) return <Skeleton className="h-9 w-56" />
  const items = dests.data ?? []
  if (items.length === 0)
    return (
      <p className="text-xs text-muted-foreground">
        Ese muelle no tiene destinos:{" "}
        <Link to={`/docks/${dock}`} className="underline">
          crea uno
        </Link>{" "}
        y vuelve aquí.
      </p>
    )
  return (
    <Select value={value} onValueChange={onPick} disabled={busy}>
      <SelectTrigger className="w-56" aria-label="Destino de los avisos">
        <SelectValue placeholder="Elegir un destino" />
      </SelectTrigger>
      <SelectContent>
        {items.map((d) => (
          <SelectItem key={d.name} value={d.name}>
            {d.name}
            {d.title ? ` · ${d.title}` : ""}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
