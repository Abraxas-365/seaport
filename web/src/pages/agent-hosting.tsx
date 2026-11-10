import * as React from "react"
import { Link } from "react-router"
import { Activity, Play, Server, ShieldAlert, Square, TerminalSquare, Unplug } from "lucide-react"

import {
  useCan,
  useEgress,
  useEnvironments,
  useHost,
  useHosted,
  useHosting,
  useNow,
  useSlotAction,
  useSlotLog,
  useUnhost,
  useUsage,
} from "@/lib/hooks"
import { formatRelative } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { Environment, Hosted, LibraryAgent, Slot } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Field, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { ConfirmAction, ErrorState, LogView, Mono, SlotRow, StatusBadge, UsageChart } from "@/components/seaport"

/** The agent's hosting: where the port runs it, its slots and their logs. */
export function HostingCard({ agent }: { agent: LibraryAgent }) {
  const can = useCan()
  const hosted = useHosted(agent.id)
  const status = useHosting()
  const envs = useEnvironments()
  const write = can(Perm.EnvironmentsWrite)
  if (!can(Perm.EnvironmentsRead)) return null
  if (hosted.isError) return <ErrorState error={hosted.error} onRetry={() => hosted.refetch()} />
  if (hosted.isPending || envs.isPending) return <Skeleton className="h-40 rounded-xl" />
  const items = envs.data?.items ?? []
  const env = hosted.data ? items.find((e) => e.id === hosted.data?.environment_id) : undefined
  const runtime = status.data?.runtime ?? true
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <Server className="size-4" /> Alojamiento
            </CardTitle>
            <CardDescription>
              {hosted.data ? (
                <>
                  El puerto lo corre en{" "}
                  {env ? (
                    <Link to={`/environments/${env.id}`} className="font-mono text-foreground underline-offset-4 hover:underline">
                      {env.name}
                    </Link>
                  ) : (
                    "su entorno"
                  )}
                  : arranca una instancia cuando le llegan mensajes y la detiene sin trabajo.
                </>
              ) : (
                <>
                  Sin alojar: trabaja donde alguien corre <Mono>seaport connect</Mono>. Alójalo en un entorno y el puerto
                  lo corre por ti.
                </>
              )}
            </CardDescription>
          </div>
          {hosted.data && write && (
            <UnhostButton agent={agent} hosted={hosted.data} />
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {!runtime && (
          <p className="rounded-lg border border-dashed px-3 py-2 text-sm text-muted-foreground">
            Este puerto no corre agentes (<Mono>SEAPORT_RUNTIME=off</Mono>): las instancias no arrancan.
          </p>
        )}
        {write && <HostForm agent={agent} hosted={hosted.data} envs={items} />}
        {hosted.data && (
          <Slots agent={agent} hosted={hosted.data} env={env} write={write} runtime={runtime} />
        )}
        {hosted.data && <Denials agent={agent} />}
        {!write && !hosted.data && items.length === 0 && (
          <p className="text-sm text-muted-foreground">Todavía no hay entornos.</p>
        )}
      </CardContent>
    </Card>
  )
}

/** Picks the environment and how many slots (hosts or moves the agent). */
function HostForm({ agent, hosted, envs }: { agent: LibraryAgent; hosted: Hosted | null; envs: Environment[] }) {
  const host = useHost(agent.id)
  const [env, setEnv] = React.useState(hosted?.environment_id ?? "")
  const [slots, setSlots] = React.useState(hosted?.slots.length ?? 1)
  const [seen, setSeen] = React.useState(hosted)
  if (seen !== hosted) {
    // Someone (or a save of ours) changed it: show what is stored.
    setSeen(hosted)
    setEnv(hosted?.environment_id ?? "")
    setSlots(hosted?.slots.length ?? 1)
  }
  if (envs.length === 0)
    return (
      <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
        Para alojarlo, primero <Link to="/environments" className="text-foreground underline underline-offset-4">crea un entorno</Link>.
      </p>
    )
  const changed = !hosted || env !== hosted.environment_id || slots !== hosted.slots.length
  const dropping = hosted?.slots.slice(slots).some((s) => s.state !== "stopped" && s.state !== "failed")
  return (
    <div className="flex flex-wrap items-end gap-3">
      <Field className="w-64">
        <FieldLabel>Entorno</FieldLabel>
        <Select value={env} onValueChange={setEnv}>
          <SelectTrigger aria-label="Entorno" className="font-mono">
            <SelectValue placeholder="Elige un entorno" />
          </SelectTrigger>
          <SelectContent>
            {envs.map((e) => (
              <SelectItem key={e.id} value={e.id} className="font-mono">
                {e.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field className="w-36">
        <FieldLabel>Instancias</FieldLabel>
        <Select value={String(slots)} onValueChange={(v) => setSlots(Number(v))}>
          <SelectTrigger aria-label="Instancias">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {Array.from({ length: 10 }, (_, i) => i + 1).map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Button
        disabled={!env || !changed || host.isPending || dropping}
        title={dropping ? "Detén primero las instancias que sobran" : undefined}
        onClick={() => host.mutate({ environment: env, slots })}
      >
        {host.isPending && <Spinner />} {hosted ? "Aplicar" : "Alojar"}
      </Button>
      {hosted && changed && (
        <Button
          variant="ghost"
          onClick={() => {
            setEnv(hosted.environment_id)
            setSlots(hosted.slots.length)
          }}
        >
          Descartar
        </Button>
      )}
    </div>
  )
}

function UnhostButton({ agent, hosted }: { agent: LibraryAgent; hosted: Hosted }) {
  const unhost = useUnhost(agent.id)
  const up = hosted.slots.some((s) => s.state !== "stopped" && s.state !== "failed")
  return (
    <ConfirmAction
      trigger={
        <Button
          variant="outline"
          size="sm"
          disabled={up}
          title={up ? "Detén sus instancias primero" : undefined}
        >
          <Unplug /> Dejar de alojar
        </Button>
      }
      title={`¿Dejar de alojar a @${agent.name}?`}
      description={
        <p>
          Se borran sus instancias y sus volúmenes de trabajo. El agente sigue en la biblioteca y puede trabajar con{" "}
          <Mono>seaport connect</Mono>.
        </p>
      }
      destructive
      actionLabel="Dejar de alojar"
      onConfirm={() => unhost.mutateAsync()}
    />
  )
}

function Slots({
  agent,
  hosted,
  env,
  write,
  runtime,
}: {
  agent: LibraryAgent
  hosted: Hosted
  env?: Environment
  write: boolean
  runtime: boolean
}) {
  const now = useNow(10_000)
  const action = useSlotAction(agent.id)
  const busy = (s: Slot) => action.isPending && action.variables?.slot === s.index
  return (
    <ul className="divide-y rounded-lg border">
      {hosted.slots.map((s) => {
        const up = s.state === "running" || s.state === "starting"
        return (
          <li key={s.index}>
            <SlotRow
              name={`${agent.name}-${s.index}`}
              slot={s}
              current={env?.version}
              now={now}
              actions={
                write && (
                  <>
                    <SlotLogDialog agent={agent} slot={s} />
                    {runtime && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={up ? `Detener ${agent.name}-${s.index}` : `Arrancar ${agent.name}-${s.index}`}
                        title={up ? "Detener" : "Arrancar"}
                        disabled={busy(s) || s.state === "stopping"}
                        onClick={() => action.mutate({ slot: s.index, action: up ? "stop" : "start" })}
                      >
                        {busy(s) ? <Spinner /> : up ? <Square /> : <Play />}
                      </Button>
                    )}
                  </>
                )
              }
            />
          </li>
        )
      })}
    </ul>
  )
}

/** The slot's recent output (harness, git, connector); may show secrets
 * the harness echoes, hence environments:write. */
function SlotLogDialog({ agent, slot }: { agent: LibraryAgent; slot: Slot }) {
  const [open, setOpen] = React.useState(false)
  const log = useSlotLog(agent.id, slot.index, open)
  const name = `${agent.name}-${slot.index}`
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={`Registro de ${name}`} title="Registro">
          <TerminalSquare />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="font-mono">{name}</DialogTitle>
          <DialogDescription>
            Las últimas líneas del contenedor; se actualiza sola. También con <Mono>seaport agents logs {agent.name}</Mono>.
          </DialogDescription>
        </DialogHeader>
        {log.isError ? (
          <ErrorState error={log.error} onRetry={() => log.refetch()} />
        ) : (
          <LogView
            text={log.data?.log}
            empty={log.isPending ? "Cargando…" : "Sin salida: la instancia no corrió o su contenedor ya no existe."}
            className="max-h-[60vh]"
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

/** Hosts the egress proxy refused for this agent's slots. */
function Denials({ agent }: { agent: LibraryAgent }) {
  const egress = useEgress(agent.id, true)
  const now = useNow()
  const denials = egress.data?.denials ?? []
  if (denials.length === 0) return null
  return (
    <div className="space-y-2">
      <h3 className="flex items-center gap-2 text-sm font-medium">
        <ShieldAlert className="size-4 text-warning" /> Salidas bloqueadas
      </h3>
      <p className="text-sm text-muted-foreground">
        Hosts que sus instancias intentaron y el proxy rechazó. Si los necesita, agrégalos a la red del entorno.
      </p>
      <ul className="divide-y rounded-lg border text-sm">
        {denials.map((d) => (
          <li key={`${d.slot}/${d.host}`} className="flex items-center gap-3 px-3 py-2">
            <span className="min-w-0 flex-1 truncate font-mono">{d.host}</span>
            <span className="font-mono text-xs text-muted-foreground">
              {agent.name}-{d.slot}
            </span>
            <StatusBadge tone="warning" dot={false}>
              {d.count}×
            </StatusBadge>
            <span className="w-24 text-right text-xs text-muted-foreground">{formatRelative(d.last_seen, now)}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** Model use through the port's gateway (hosted agents with a key). */
export function UsageCard({ agent }: { agent: LibraryAgent }) {
  const usage = useUsage(agent.id)
  const days = usage.data?.days ?? []
  if (usage.isPending || days.length === 0) return null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Activity className="size-4" /> Uso de modelos
        </CardTitle>
        <CardDescription>Llamadas por el gateway del puerto en los últimos 30 días (días en UTC).</CardDescription>
      </CardHeader>
      <CardContent>
        <UsageChart days={days} />
      </CardContent>
    </Card>
  )
}
