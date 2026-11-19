import * as React from "react"
import { useLocation } from "react-router"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Activity,
  AppWindow,
  Check,
  Copy,
  ExternalLink,
  Hammer,
  KeyRound,
  Laptop,
  Play,
  Plus,
  RefreshCw,
  Server,
  ShieldAlert,
  Square,
  TerminalSquare,
  Trash2,
  Unplug,
} from "lucide-react"
import { toast } from "sonner"

import { api } from "@/lib/api"
import {
  useBuild,
  useCan,
  useCreateCrewToken,
  useCrewTokens,
  useEgress,
  useHost,
  useHosted,
  useHosting,
  useLoginCode,
  useNow,
  useRevokeCrewToken,
  useSlotAccount,
  useSlotAction,
  useSlotLog,
  useSlotLogin,
  useStartBuild,
  useStartLogin,
  useUnhost,
  useUsage,
} from "@/lib/hooks"
import { formatFull, formatRelative } from "@/lib/format"
import { harnessLabel, signsIn } from "@/lib/machine"
import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type { AgentProfile, CrewToken, Hosted, IssuedCrewToken, LibraryAgent, Slot } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  AccountBadge,
  BrowserViewer,
  BuildBadge,
  ConfirmAction,
  EmptyState,
  ErrorState,
  LogView,
  Mono,
  SegmentedChoice,
  SlotRow,
  StatusBadge,
  Takeovers,
  UsageChart,
  type ChoiceOption,
} from "@/components/seaport"

/** Whether a slot holds resources (must be stopped before it goes). */
const up = (s: Slot) => s.state !== "stopped" && s.state !== "failed"

/**
 * Where the agent runs, one of two: hosted (the port runs its instances
 * from the config's machine) or remote (someone runs `seaport connect`
 * with one of its tokens). Switching asks for what the other mode holds:
 * live tokens are revoked before hosting, instances stopped before
 * leaving.
 */
export function WhereTab({
  agent,
  profile,
  onConfigure,
}: {
  agent: LibraryAgent
  profile: AgentProfile | undefined
  /** Opens the Configuración tab (to give the config a machine). */
  onConfigure: () => void
}) {
  const can = useCan()
  const hosted = useHosted(agent.id)
  if (!can(Perm.HostingRead)) return <RemotePanel agent={agent} profile={profile} onConfigure={onConfigure} />
  if (hosted.isError) return <ErrorState error={hosted.error} onRetry={() => hosted.refetch()} />
  if (hosted.isPending || !profile) return <Skeleton className="h-48 rounded-xl" />
  return hosted.data ? (
    <HostedPanel agent={agent} hosted={hosted.data} profile={profile} />
  ) : (
    <RemotePanel agent={agent} profile={profile} onConfigure={onConfigure} />
  )
}

function RuntimeOff() {
  return (
    <p className="rounded-lg border border-dashed px-3 py-2 text-sm text-muted-foreground">
      Este puerto no corre agentes (<Mono>SEAPORT_RUNTIME=off</Mono>): las instancias no arrancan ni se construyen
      imágenes.
    </p>
  )
}

/** A hosted agent: its instances, image and what the proxy refused. */
function HostedPanel({ agent, hosted, profile }: { agent: LibraryAgent; hosted: Hosted; profile: AgentProfile }) {
  const can = useCan()
  const status = useHosting()
  const write = can(Perm.HostingWrite)
  const runtime = status.data?.runtime ?? true
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-1">
              <CardTitle className="flex items-center gap-2">
                <Server className="size-4" /> Alojado en el puerto
              </CardTitle>
              <CardDescription>
                El puerto corre {profile.harness ? harnessLabel[profile.harness] : "su harness"} con la máquina de su
                configuración: arranca una instancia cuando le llegan mensajes y la detiene sin trabajo.
              </CardDescription>
            </div>
            {write && <UnhostButton agent={agent} hosted={hosted} />}
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {!runtime && <RuntimeOff />}
          {write && <SlotsForm agent={agent} hosted={hosted} />}
          <Slots agent={agent} hosted={hosted} profile={profile} write={write} runtime={runtime} />
          <Denials agent={agent} />
        </CardContent>
      </Card>
      <BuildCard agent={agent} write={write} runtime={runtime} />
    </div>
  )
}

/** How many instances the agent has. */
function SlotsForm({ agent, hosted }: { agent: LibraryAgent; hosted: Hosted }) {
  const host = useHost(agent.id)
  const [slots, setSlots] = React.useState(hosted.slots.length)
  const [seen, setSeen] = React.useState(hosted)
  if (seen !== hosted) {
    // Someone (or a save of ours) changed it: show what is stored.
    setSeen(hosted)
    setSlots(hosted.slots.length)
  }
  const changed = slots !== hosted.slots.length
  const dropping = hosted.slots.slice(slots).some(up)
  return (
    <div className="flex flex-wrap items-end gap-3">
      <Field className="w-36">
        <FieldLabel>Instancias</FieldLabel>
        <SlotCount value={slots} onChange={setSlots} />
      </Field>
      {changed && (
        <>
          <Button
            disabled={host.isPending || dropping}
            title={dropping ? "Detén primero las instancias que sobran" : undefined}
            onClick={() => host.mutate({ slots })}
          >
            {host.isPending && <Spinner />} Aplicar
          </Button>
          <Button variant="ghost" onClick={() => setSlots(hosted.slots.length)}>
            Descartar
          </Button>
        </>
      )}
    </div>
  )
}

export function SlotCount({ value, onChange }: { value: number; onChange: (n: number) => void }) {
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
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
  )
}

function UnhostButton({ agent, hosted }: { agent: LibraryAgent; hosted: Hosted }) {
  const unhost = useUnhost(agent.id)
  const running = hosted.slots.some(up)
  return (
    <ConfirmAction
      trigger={
        <Button variant="outline" size="sm" disabled={running} title={running ? "Detén sus instancias primero" : undefined}>
          <Unplug /> Pasar a remoto
        </Button>
      }
      title={`¿Dejar de alojar a @${agent.name}?`}
      description={
        <p>
          Se borran sus instancias y sus volúmenes de trabajo. Su configuración (y su máquina) se queda; después trabaja
          donde alguien corra <Mono>seaport connect</Mono> con uno de sus tokens.
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
  profile,
  write,
  runtime,
}: {
  agent: LibraryAgent
  hosted: Hosted
  profile: AgentProfile
  write: boolean
  runtime: boolean
}) {
  const now = useNow(10_000)
  const action = useSlotAction(agent.id)
  const busy = (s: Slot) => action.isPending && action.variables?.slot === s.index
  // A Claude account instead of a key: each slot signs in.
  const account = signsIn(profile.harness, profile.machine?.models)
  return (
    <ul className="divide-y rounded-lg border" aria-label="Instancias">
      {hosted.slots.map((s) => {
        const on = s.state === "running" || s.state === "starting"
        return (
          <li key={s.index}>
            <SlotRow
              name={`${agent.name}-${s.index}`}
              slot={s}
              now={now}
              actions={
                <>
                  {account && s.state === "running" && <SlotAccountBadge agent={agent} slot={s} />}
                  {write && account && runtime && s.state !== "stopping" && <SignInDialog agent={agent} slot={s} />}
                  {write && (
                    <>
                      <SlotLogDialog agent={agent} slot={s} />
                      {runtime && (
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={on ? `Detener ${agent.name}-${s.index}` : `Arrancar ${agent.name}-${s.index}`}
                          title={on ? "Detener" : "Arrancar"}
                          disabled={busy(s) || s.state === "stopping"}
                          onClick={() => action.mutate({ slot: s.index, action: on ? "stop" : "start" })}
                        >
                          {busy(s) ? <Spinner /> : on ? <Square /> : <Play />}
                        </Button>
                      )}
                    </>
                  )}
                </>
              }
            />
          </li>
        )
      })}
    </ul>
  )
}

function SlotAccountBadge({ agent, slot }: { agent: LibraryAgent; slot: Slot }) {
  const account = useSlotAccount(agent.id, slot.index, true)
  if (account.isError) return null
  return <AccountBadge account={account.data} />
}

const live = (state: string | undefined) => state === "starting" || state === "waiting" || state === "verifying"

/**
 * Signs a slot (Claude Code or rness) in to a Claude subscription
 * (experimental): the harness's own login runs in the slot; the person
 * opens its URL, signs in and pastes the code the page shows. A slot that
 * isn't running is started first (tokens live in its state volume), and
 * the login begins once it runs.
 */
function SignInDialog({ agent, slot }: { agent: LibraryAgent; slot: Slot }) {
  const [open, setOpen] = React.useState(false)
  const [code, setCode] = React.useState("")
  const name = `${agent.name}-${slot.index}`
  const running = slot.state === "running"
  const login = useSlotLogin(agent.id, slot.index, open && running)
  const start = useStartLogin(agent.id, slot.index)
  const send = useLoginCode(agent.id, slot.index)
  const action = useSlotAction(agent.id)
  const account = useSlotAccount(agent.id, slot.index, running)
  // Whether the slot had an account when the dialog opened: the harness
  // reads its sign-in at start, so a first account starts the work and a
  // change waits for the current turn to end (the connector restarts it).
  const [before, setBefore] = React.useState<boolean | undefined>(undefined)
  const l = login.data
  const state = l?.state
  const refetchAccount = account.refetch
  React.useEffect(() => {
    if (state === "done") refetchAccount()
  }, [state, refetchAccount])
  // Opened on a stopped slot: once it runs (slot.updated), begin the login.
  const pending = React.useRef(false)
  const begin = start.mutate
  React.useEffect(() => {
    if (open && running && pending.current && login.isFetched && !live(login.data?.state)) {
      pending.current = false
      begin()
    }
  }, [open, running, login.isFetched, login.data?.state, begin])
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) return
        setCode("")
        setBefore(running ? account.data?.logged_in : false)
        if (running) {
          if (!live(login.data?.state)) start.mutate()
        } else {
          pending.current = true
          if (slot.state === "stopped" || slot.state === "failed") action.mutate({ slot: slot.index, action: "start" })
        }
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Conectar cuenta de ${name}`}
          title={running ? "Conectar cuenta" : "Arrancar y conectar cuenta"}
        >
          <KeyRound />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Conectar {name} a una cuenta de Claude</DialogTitle>
          <DialogDescription>
            Experimental. La instancia usa la suscripción de esa cuenta (sus límites y condiciones); Seaport no guarda
            tokens: quedan en el volumen de la instancia. También con <Mono>seaport agents login {name}</Mono>.
          </DialogDescription>
        </DialogHeader>
        {action.isError ? (
          <ErrorState error={action.error} onRetry={() => action.mutate({ slot: slot.index, action: "start" })} />
        ) : !running ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> Arrancando la instancia…
          </p>
        ) : start.isError ? (
          <ErrorState error={start.error} onRetry={() => start.mutate()} />
        ) : !l || state === "starting" || start.isPending ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> Iniciando el login en la instancia…
          </p>
        ) : state === "waiting" ? (
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (code.trim()) send.mutate(code.trim())
            }}
          >
            <ol className="list-decimal space-y-3 pl-5 text-sm">
              <li>
                <a
                  href={l.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 font-medium text-foreground underline underline-offset-4"
                >
                  Abre Claude e inicia sesión <ExternalLink className="size-3.5" />
                </a>{" "}
                con la cuenta que debe usar.
              </li>
              <li>Copia el código que muestra la página y pégalo aquí.</li>
            </ol>
            <Field>
              <FieldLabel htmlFor={`code-${name}`}>Código</FieldLabel>
              <Input
                id={`code-${name}`}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
              />
              <FieldDescription>Vale una sola vez y caduca en minutos.</FieldDescription>
            </Field>
            <Button type="submit" disabled={!code.trim() || send.isPending}>
              {send.isPending && <Spinner />} Conectar
            </Button>
          </form>
        ) : state === "verifying" ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> Verificando el código…
          </p>
        ) : state === "done" ? (
          <p className="text-sm">
            {before
              ? "Conectada. El agente cambia a esta cuenta cuando termine lo que esté haciendo."
              : "Conectada. El agente empieza a trabajar con esta cuenta."}
          </p>
        ) : (
          <div className="space-y-3">
            <p className="text-sm text-destructive">{l.detail || "No se pudo conectar."}</p>
            <Button variant="outline" onClick={() => start.mutate()}>
              Intentar de nuevo
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The slot's recent output (harness, git, connector); may show secrets
 * the harness echoes, hence hosting:write. */
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
        Hosts que sus instancias intentaron y el proxy rechazó. Si los necesita, agrégalos en Configuración → Salida.
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

/** The image of the agent's current machine: built once per content. */
function BuildCard({ agent, write, runtime }: { agent: LibraryAgent; write: boolean; runtime: boolean }) {
  const build = useBuild(agent.id)
  const start = useStartBuild(agent.id)
  const [showLog, setShowLog] = React.useState(false)
  const b = build.data
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <Hammer className="size-4" /> Imagen
              {!build.isPending && <BuildBadge build={b} />}
            </CardTitle>
            <CardDescription>
              Se construye sola al arrancar una instancia con una máquina nueva; agentes con la misma máquina comparten
              imagen.
            </CardDescription>
          </div>
          {write && runtime && (
            <Button
              variant="outline"
              size="sm"
              disabled={start.isPending || b?.state === "building"}
              onClick={() => start.mutate(b?.state === "ready" || b?.state === "failed")}
            >
              {start.isPending ? <Spinner /> : <RefreshCw />} {b ? "Reconstruir" : "Construir"}
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {build.isError ? (
          <ErrorState error={build.error} onRetry={() => build.refetch()} />
        ) : b ? (
          <>
            <p className="text-sm text-muted-foreground">
              <Mono>{b.image || b.digest.slice(0, 12)}</Mono>
              {" · "}
              {b.finished_at ? `terminó ${formatFull(b.finished_at)}` : `empezó ${formatFull(b.started_at)}`}
            </p>
            {b.state === "failed" && b.detail && <p className="text-sm text-destructive">{b.detail}</p>}
            {b.log && (
              <Button variant="ghost" size="sm" onClick={() => setShowLog(!showLog)}>
                <TerminalSquare /> {showLog ? "Ocultar registro" : "Ver registro"}
              </Button>
            )}
            {(showLog || b.state === "building") && b.log && <LogView text={b.log} className="max-h-80" />}
          </>
        ) : (
          <p className="text-sm text-muted-foreground">Todavía no se construyó la imagen de esta máquina.</p>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * A remote agent: it works wherever someone runs `seaport connect` with one
 * of its tokens. From here it can also move into the port (hosting needs
 * a harness and a machine in its config).
 */
function RemotePanel({
  agent,
  profile,
  onConfigure,
}: {
  agent: LibraryAgent
  profile: AgentProfile | undefined
  onConfigure: () => void
}) {
  const can = useCan()
  return (
    <div className="space-y-6">
      {can(Perm.CrewTokensRead) && <TokensCard agent={agent} />}
      <ConnectCard agent={agent} profile={profile} />
      {can(Perm.HostingWrite) && profile && <HostCard agent={agent} profile={profile} onConfigure={onConfigure} />}
    </div>
  )
}

/** The agent's tokens (people's; a slot's own never show). */
function TokensCard({ agent }: { agent: LibraryAgent }) {
  const tokens = useCrewTokens()
  const can = useCan()
  const now = useNow()
  const write = can(Perm.CrewTokensWrite)
  const mine = (tokens.data?.items ?? []).filter((t) => t.agent_id === agent.id)
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4" /> Tokens
            </CardTitle>
            <CardDescription>
              Con uno de estos, <Mono>seaport connect</Mono> trabaja como @{agent.name}. Uno por máquina, para revocarlos
              por separado.
            </CardDescription>
          </div>
          {write && <NewTokenDialog agent={agent} />}
        </div>
      </CardHeader>
      <CardContent>
        {tokens.isPending ? (
          <Skeleton className="h-14 rounded-lg" />
        ) : tokens.isError ? (
          <ErrorState error={tokens.error} onRetry={() => tokens.refetch()} />
        ) : mine.length === 0 ? (
          <EmptyState icon={KeyRound} title="Sin tokens" description="Crea uno para la máquina donde correrá el agente." />
        ) : (
          <ul className="divide-y rounded-lg border" aria-label="Tokens">
            {mine.map((t) => (
              <TokenRow key={t.id} token={t} agent={agent} now={now} canRevoke={write} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function TokenRow({
  token: t,
  agent,
  now,
  canRevoke,
}: {
  token: CrewToken
  agent: LibraryAgent
  now: number
  canRevoke: boolean
}) {
  const revoke = useRevokeCrewToken()
  const expired = !!t.expires_at && new Date(t.expires_at).getTime() <= now
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2.5">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{t.name}</span>
          <Mono className="text-xs text-muted-foreground">{t.hint}…</Mono>
          {expired && <StatusBadge tone="danger">Vencido</StatusBadge>}
        </div>
        <div className="truncate text-xs text-muted-foreground">
          <span title={formatFull(t.created_at)}>Creado {formatRelative(t.created_at, now)}</span>
          {t.created_by && <> por {t.created_by}</>}
          {" · "}
          {t.last_used_at ? (
            <span title={formatFull(t.last_used_at)}>usado {formatRelative(t.last_used_at, now)}</span>
          ) : (
            "nunca usado"
          )}
          {" · "}
          {t.expires_at ? (
            <span title={formatFull(t.expires_at)}>
              {expired ? "venció" : "vence"} {formatRelative(t.expires_at, now)}
            </span>
          ) : (
            "sin vencimiento"
          )}
        </div>
      </div>
      {canRevoke && (
        <ConfirmAction
          destructive
          title={`¿Revocar «${t.name}» de @${agent.name}?`}
          description={<p>Quien esté conectado con este token se desconecta ahora y no podrá volver a entrar con él.</p>}
          actionLabel="Revocar"
          onConfirm={() => revoke.mutateAsync(t.id)}
          trigger={
            <Button variant="ghost" size="sm" className="text-destructive">
              <Trash2 /> Revocar
            </Button>
          }
        />
      )}
    </li>
  )
}

type Lifetime = "30" | "90" | "365" | "never"

const lifetimeOptions: ChoiceOption<Lifetime>[] = [
  { value: "30", label: "30 días" },
  { value: "90", label: "90 días" },
  { value: "365", label: "1 año" },
  { value: "never", label: "Sin vencimiento" },
]

function NewTokenDialog({ agent }: { agent: LibraryAgent }) {
  const create = useCreateCrewToken()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [lifetime, setLifetime] = React.useState<Lifetime>("90")
  const [issued, setIssued] = React.useState<IssuedCrewToken>()
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      // The value is gone once the dialog closes; it is never shown again.
      setName("")
      setIssued(undefined)
    }
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus /> Nuevo token
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {issued ? (
          <IssuedToken token={issued} onDone={() => close(false)} />
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              const expires_at =
                lifetime === "never" ? undefined : new Date(Date.now() + Number(lifetime) * 86_400_000).toISOString()
              create.mutate({ agent_id: agent.id, name: name.trim(), expires_at }, { onSuccess: setIssued })
            }}
          >
            <DialogHeader>
              <DialogTitle>Nuevo token para @{agent.name}</DialogTitle>
              <DialogDescription>Quien se conecte con él trabaja como este agente.</DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field>
                <FieldLabel htmlFor="token-name">Nombre</FieldLabel>
                <Input
                  id="token-name"
                  value={name}
                  placeholder="rness en mi laptop"
                  onChange={(e) => setName(e.target.value)}
                  autoFocus
                />
                <FieldDescription>Para reconocerlo en la lista y en los registros.</FieldDescription>
              </Field>
              <Field>
                <FieldLabel>Vigencia</FieldLabel>
                <SegmentedChoice
                  aria-label="Vigencia"
                  size="sm"
                  value={lifetime}
                  options={lifetimeOptions}
                  onChange={setLifetime}
                  className="w-full sm:w-full"
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => close(false)}>
                Cancelar
              </Button>
              <Button type="submit" disabled={!name.trim() || create.isPending}>
                {create.isPending && <Spinner />} Crear token
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The value, shown once, with the command to connect. */
function IssuedToken({ token, onDone }: { token: IssuedCrewToken; onDone: () => void }) {
  const [copied, setCopied] = React.useState(false)
  const copy = async () => {
    await navigator.clipboard.writeText(token.value)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>Token de @{token.agent_name}</DialogTitle>
        <DialogDescription>Cópialo ahora: no se vuelve a mostrar. Seaport solo guarda su huella.</DialogDescription>
      </DialogHeader>
      <div className="min-w-0 space-y-3 py-5">
        <div className="flex items-center gap-2 rounded-lg border bg-muted/40 p-2">
          <Mono className="min-w-0 flex-1 px-1 text-sm break-all select-all" data-testid="issued-token">
            {token.value}
          </Mono>
          <Button size="sm" variant="outline" className="shrink-0" onClick={copy}>
            {copied ? <Check /> : <Copy />} {copied ? "Copiado" : "Copiar"}
          </Button>
        </div>
        <p className="text-xs break-words text-muted-foreground">
          En la máquina del agente: <code>SEAPORT_CREW_TOKEN=&lt;token&gt; seaport connect -- rness --acp</code>
        </p>
      </div>
      <DialogFooter>
        <Button onClick={onDone}>Listo</Button>
      </DialogFooter>
    </>
  )
}

/** The command that connects the agent from a machine. */
function ConnectCard({ agent, profile }: { agent: LibraryAgent; profile: AgentProfile | undefined }) {
  const config = useQuery({ queryKey: keys.config, queryFn: api.config, staleTime: Infinity })
  const harness = profile?.harness === "claude-code" ? "claude-agent-acp" : "rness --acp"
  // Open mode has no tokens: the connector names its agent.
  const cmd =
    config.data?.auth === "none"
      ? `seaport connect -server ${window.location.origin} -agent ${agent.name} -- ${harness}`
      : `SEAPORT_CREW_TOKEN=… seaport connect -server ${window.location.origin} -- ${harness}`
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Laptop className="size-4" /> Conectar desde una máquina
        </CardTitle>
        <CardDescription>
          Corre esto donde vive el agente: se conecta hacia afuera (no abre puertos), toma su configuración y se despierta
          con cada mensaje de sus muelles.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="relative">
          <pre className="scrollbar-thin overflow-x-auto rounded-lg bg-muted p-3 pr-12 font-mono text-xs">{cmd}</pre>
          <Button
            variant="ghost"
            size="icon-sm"
            className="absolute top-2 right-2"
            aria-label="Copiar"
            onClick={() => navigator.clipboard.writeText(cmd).then(() => toast.success("Copiado"))}
          >
            <Copy />
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

/**
 * Moves a remote agent into the port. Its config must have a harness and a
 * machine; people's live tokens are revoked first (the modes exclude each
 * other, and the port refuses otherwise).
 */
function HostCard({
  agent,
  profile,
  onConfigure,
}: {
  agent: LibraryAgent
  profile: AgentProfile
  onConfigure: () => void
}) {
  const host = useHost(agent.id)
  const tokens = useCrewTokens()
  const status = useHosting()
  const qc = useQueryClient()
  const now = useNow()
  const [slots, setSlots] = React.useState(1)
  const ready = !!profile.harness && !!profile.machine
  const liveTokens = (tokens.data?.items ?? []).filter(
    (t) => t.agent_id === agent.id && (!t.expires_at || new Date(t.expires_at).getTime() > now),
  )
  const doHost = async () => {
    for (const t of liveTokens) await api.revokeCrewToken(t.id)
    if (liveTokens.length) qc.invalidateQueries({ queryKey: keys.crewTokens })
    await host.mutateAsync({ slots })
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Server className="size-4" /> Alojar en el puerto
        </CardTitle>
        <CardDescription>
          El puerto corre sus instancias con la máquina de su configuración: nadie tiene que dejar una terminal abierta.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {status.data && !status.data.runtime && <RuntimeOff />}
        {!ready ? (
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-dashed px-3 py-3 text-sm">
            <span className="text-muted-foreground">
              Su configuración no tiene {profile.harness ? "máquina" : "harness ni máquina"}: elige harness, modelo y lo
              que necesita en Configuración.
            </span>
            <Button variant="outline" size="sm" onClick={onConfigure}>
              Ir a Configuración
            </Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-end gap-3">
            <Field className="w-36">
              <FieldLabel>Instancias</FieldLabel>
              <SlotCount value={slots} onChange={setSlots} />
            </Field>
            {liveTokens.length > 0 ? (
              <ConfirmAction
                trigger={<Button disabled={host.isPending}>{host.isPending && <Spinner />} Alojar</Button>}
                title={`¿Alojar a @${agent.name}?`}
                description={
                  <p>
                    Un agente alojado no se conecta con tokens: se {liveTokens.length === 1 ? "revoca su token" : `revocan sus ${liveTokens.length} tokens`}{" "}
                    y quien esté conectado con {liveTokens.length === 1 ? "él" : "ellos"} se desconecta.
                  </p>
                }
                actionLabel="Revocar y alojar"
                destructive
                onConfirm={doHost}
              />
            ) : (
              <Button disabled={host.isPending} onClick={() => doHost().catch(() => {})}>
                {host.isPending && <Spinner />} Alojar
              </Button>
            )}
          </div>
        )}
      </CardContent>
    </Card>
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

/** Whether the agent's Navegador tab has anything to show. */
export function hasBrowserTab(agent: LibraryAgent, profile: AgentProfile | undefined): boolean {
  return !!profile?.machine?.browser?.engine || (agent.takeovers ?? []).length > 0
}

/**
 * The agent's managed browser: when its machine runs one and its profile
 * uses it, a person opens a running slot's browser here to sign in or take
 * over at the sites the agent asked for (ask_takeover) or any other.
 */
export function BrowserCard({ agent, profile }: { agent: LibraryAgent; profile: AgentProfile | undefined }) {
  const can = useCan()
  const hosted = useHosted(agent.id)
  const [open, setOpen] = React.useState<{ slot: number; url?: string } | null>(null)
  const use = can(Perm.BrowserUse)
  // Takeover notices link `/agents/{id}#navegador`: scroll here once.
  const { hash } = useLocation()
  const scrolled = React.useRef(false)
  const anchor = React.useCallback(
    (el: HTMLDivElement | null) => {
      if (!el || scrolled.current || hash !== "#navegador") return
      scrolled.current = true
      el.scrollIntoView({ block: "start" })
    },
    [hash],
  )
  const hasBrowser = !!profile?.machine?.browser?.engine
  const slots = hosted.data?.slots ?? []
  const running = slots.filter((s) => s.state === "running")
  const profileOn = profile?.browser ?? false
  const slot = open ? slots.find((s) => s.index === open.slot) : undefined
  const openOn = (url?: string) => running[0] && setOpen({ slot: running[0].index, url })
  return (
    <Card id="navegador" ref={anchor} className="scroll-mt-4">
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <AppWindow className="size-4" /> Navegador
            </CardTitle>
            <CardDescription>
              {!hasBrowser
                ? "Su máquina no tiene navegador: el agente pidió que alguien tome el control, pero no hay dónde."
                : !hosted.data
                  ? "El navegador corre en sus instancias: alójalo para usarlo."
                  : !profileOn
                    ? "Su máquina tiene navegador; enciende «Navegador» en su configuración para que el agente lo use."
                    : "Cada instancia corre un Chromium con perfil persistente que el agente y tú compartís: lo que inicies sesión aquí, el agente lo usa."}
            </CardDescription>
          </div>
          {use && hasBrowser && hosted.data && !open && (
            <Button
              size="sm"
              disabled={running.length === 0}
              title={running.length === 0 ? "Arranca una instancia primero" : undefined}
              onClick={() => openOn()}
            >
              <AppWindow /> Abrir navegador
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <Takeovers
          agent={agent}
          canClear={use}
          onOpen={use && hasBrowser && running.length > 0 ? (host) => openOn(`https://${host}`) : undefined}
        />
        {open && slot && slot.state === "running" ? (
          <BrowserViewer
            key={`${slot.index}-${open.url ?? ""}`}
            agent={agent}
            slot={slot}
            startUrl={open.url}
            onClose={() => setOpen(null)}
          />
        ) : open ? (
          <p className="text-sm text-muted-foreground">La instancia se detuvo.</p>
        ) : null}
      </CardContent>
    </Card>
  )
}
