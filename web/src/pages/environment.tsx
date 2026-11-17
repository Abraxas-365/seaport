import * as React from "react"
import { Link, useNavigate, useParams } from "react-router"
import {
  ArrowLeft,
  Boxes,
  ChevronDown,
  ChevronRight,
  Copy,
  FileCode2,
  FolderCog,
  GitBranch,
  Globe,
  Hammer,
  AppWindow,
  History,
  HardDrive,
  KeyRound,
  ListChecks,
  Pencil,
  Plus,
  RotateCcw,
  Save,
  Server,
  Trash2,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { ApiError } from "@/lib/api"
import { environmentYaml } from "@/lib/env-yaml"
import { formatFull } from "@/lib/format"
import {
  useAgents,
  useBuild,
  useCan,
  useDeleteEnvironment,
  useEnvironment,
  useEnvironmentSecrets,
  useEnvironmentVersions,
  useHost,
  useHosting,
  useSaveEnvironment,
  useStartBuild,
} from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { Build, EnvRepo, EnvSpec, Environment, Hosted, LibraryAgent, ResourceSize } from "@/lib/types"
import { cn } from "@/lib/utils"
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
import { Textarea } from "@/components/ui/textarea"
import {
  AgentChip,
  BuildBadge,
  Checklist,
  ChecklistItem,
  ChipInput,
  ConfirmAction,
  ErrorState,
  LogView,
  Mono,
  SegmentedChoice,
  StatusBadge,
  type ChecklistState,
} from "@/components/seaport"
import { RuntimeOff, harnessLabel } from "@/pages/environments"
import { HomeSection } from "@/pages/environment-home"
import {
  EnvVarsField,
  EnvironmentSecretDialog,
  EnvironmentSecrets,
  ModelFields,
  SecretSelect,
  asSecretRef,
  envProblem,
  keyNameFor,
  providers,
  secretRefPattern,
} from "@/pages/environment-secrets"

const idleStopPattern = /^(0|never|(\d+h)?(\d+m)?)$/

/** One environment: its spec (editable, versioned), image build and agents. */
export function EnvironmentPage() {
  const { envId = "" } = useParams()
  const env = useEnvironment(envId)
  if (env.isError) return <ErrorState error={env.error} onRetry={() => env.refetch()} className="h-full" />
  if (!env.data)
    return (
      <div className="space-y-4 p-6">
        <Skeleton className="h-16 rounded-xl" />
        <Skeleton className="h-96 rounded-xl" />
      </div>
    )
  return <EnvironmentDetail env={env.data} />
}

function EnvironmentDetail({ env }: { env: Environment }) {
  const can = useCan()
  const write = can(Perm.EnvironmentsWrite)
  const hosting = useHosting()
  const build = useBuild(env.id, env.version)
  const remove = useDeleteEnvironment()
  const navigate = useNavigate()
  const runtime = !!hosting.data?.runtime
  // The checklist's "Añadir clave" opens the editor's secret dialog, which
  // names the new secret in the draft and saves.
  const [keyFor, setKeyFor] = React.useState<(typeof providers)[number] | null>(null)
  const hostedHere = (hosting.data?.agents ?? []).filter((h) => h.environment_id === env.id)
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <div className="border-b px-6 py-5">
        <Link
          to="/environments"
          className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" /> Entornos
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <span className="flex size-10 items-center justify-center rounded-xl bg-muted">
            <Server className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="truncate font-mono text-xl font-semibold tracking-tight">{env.name}</h1>
            <p className="mt-0.5 text-sm text-muted-foreground">
              {env.spec.description || <span className="italic">Sin descripción</span>}
            </p>
          </div>
          <StatusBadge tone="brand" dot={false}>
            {harnessLabel[env.spec.harness]} · v{env.version}
          </StatusBadge>
          <YamlDialog env={env} />
          {write && (
            <ConfirmAction
              trigger={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Eliminar entorno"
                  disabled={env.agents > 0}
                  title={env.agents > 0 ? "Tiene agentes alojados: muévelos o deja de alojarlos primero" : undefined}
                >
                  <Trash2 />
                </Button>
              }
              title={`¿Eliminar ${env.name}?`}
              description={<p>Se borran sus versiones y builds. Las imágenes ya construidas quedan en el host.</p>}
              destructive
              confirmText={env.name}
              actionLabel="Eliminar"
              onConfirm={async () => {
                await remove.mutateAsync(env.id)
                navigate("/environments")
              }}
            />
          )}
        </div>
      </div>
      <div className="mx-auto max-w-4xl space-y-6 p-6">
        <SetupCard
          env={env}
          write={write}
          runtime={runtime}
          build={build.data}
          hosted={hostedHere}
          onAddKey={(p) => setKeyFor(p)}
        />
        {hosting.data && !runtime && <RuntimeOff />}
        {runtime && <BuildCard env={env} write={write} />}
        <AgentsOn env={env} hosted={hostedHere} />
        <SpecEditor env={env} write={write} keyFor={keyFor} onKeyFor={setKeyFor} />
        {!runtime && hosting.data && <BuildCard env={env} write={false} />}
      </div>
    </div>
  )
}

/**
 * What's left before agents can run here, computed from the data; one
 * line once everything is in place. The order is the order of work.
 */
function SetupCard({
  env,
  write,
  runtime,
  build,
  hosted,
  onAddKey,
}: {
  env: Environment
  write: boolean
  runtime: boolean
  build: Build | null | undefined
  hosted: Hosted[]
  onAddKey: (p: (typeof providers)[number]) => void
}) {
  const secrets = useEnvironmentSecrets(env.id)
  const names = new Set(secrets.data?.secrets.map((s) => s.name) ?? [])
  const models = env.spec.models ?? {}
  const anthropic = providers[0]
  const modelNamed = Object.values(models).some(Boolean)
  // No model key: every harness signs its slots in to a Claude account instead.
  const keyState: ChecklistState = modelNamed ? "done" : "optional"
  const anthropicKey = models.anthropic ? models.anthropic.slice(2, -1) : ""
  const built = build?.state === "ready"
  const buildState: ChecklistState = built
    ? "done"
    : runtime
      ? build?.state === "failed"
        ? "todo"
        : "optional"
      : "blocked"
  const agentsState: ChecklistState = hosted.length > 0 ? "done" : "todo"
  const ready = agentsState === "done"
  const [open, setOpen] = React.useState<boolean | null>(null)
  const show = open ?? !ready
  const scrollTo = (id: string) => document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" })
  return (
    <Card data-testid="env-setup">
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <ListChecks className="size-4" /> {ready ? "Listo para trabajar" : "Para empezar"}
            </CardTitle>
            <CardDescription>
              {ready
                ? `${hosted.length === 1 ? "Un agente corre" : `${hosted.length} agentes corren`} aquí cuando les llegan mensajes.`
                : "Lo que falta para que un agente corra en este entorno."}
            </CardDescription>
          </div>
          <Button variant="ghost" size="sm" onClick={() => setOpen(!show)} aria-expanded={show}>
            {show ? "Ocultar" : "Ver pasos"}
          </Button>
        </div>
      </CardHeader>
      {show && (
        <CardContent>
          <Checklist aria-label="Pasos">
            <ChecklistItem
              state="done"
              title={`Harness: ${harnessLabel[env.spec.harness]}`}
              detail={env.spec.description || undefined}
            />
            <ChecklistItem
              state={keyState}
              title="Clave del modelo"
              detail={
                modelNamed
                  ? Object.entries(models)
                      .filter(([, v]) => v)
                      .map(
                        ([p, v]) =>
                          `${providers.find((x) => x.name === p)?.label ?? p}: ${v.slice(2, -1)}${names.has(v.slice(2, -1)) || !secrets.data ? "" : " (la aporta cada agente)"}`
                      )
                      .join(" · ")
                  : env.spec.harness === "claude-code"
                    ? "Sin clave, cada instancia inicia sesión con una cuenta de Claude desde la página del agente (experimental)."
                    : "Sin clave, cada instancia inicia sesión con una cuenta de Claude desde la página del agente (experimental) y usa modelos de Anthropic. Con clave, el modelo pasa por el gateway del puerto."
              }
              action={
                write &&
                !modelNamed && (
                  <Button size="sm" onClick={() => onAddKey(anthropic)}>
                    <KeyRound /> Añadir clave de Anthropic
                  </Button>
                )
              }
            />
            {anthropicKey && secrets.data && !names.has(anthropicKey) && (
              <ChecklistItem
                state="todo"
                title={`El secreto ${anthropicKey} no está en el entorno`}
                detail="La configuración lo nombra; cada agente alojado tiene que aportarlo, o créalo aquí para todos."
                action={
                  write && (
                    <Button size="sm" variant="outline" onClick={() => onAddKey(anthropic)}>
                      <Plus /> Crear {anthropicKey}
                    </Button>
                  )
                }
              />
            )}
            <ChecklistItem
              state={env.spec.repos.length > 0 ? "done" : "optional"}
              title="Repos"
              detail={
                env.spec.repos.length > 0
                  ? env.spec.repos.map((r) => r.url.replace(/^https:\/\//, "")).join(", ")
                  : "Opcional: los repos con los que trabaja, clonados en cada instancia."
              }
              action={
                write &&
                env.spec.repos.length === 0 && (
                  <Button size="sm" variant="outline" onClick={() => scrollTo("env-section-repos")}>
                    <GitBranch /> Añadir repo
                  </Button>
                )
              }
            />
            <ChecklistItem
              state={buildState}
              title="Imagen"
              detail={
                built
                  ? "Construida para esta versión."
                  : !runtime
                    ? "Este puerto no construye imágenes (SEAPORT_RUNTIME=off)."
                    : build?.state === "building"
                      ? "Construyendo…"
                      : build?.state === "failed"
                        ? "La última construcción falló."
                        : "Se construye sola con el primer mensaje; construirla antes evita la espera."
              }
              action={
                runtime &&
                !built && (
                  <Button size="sm" variant="outline" onClick={() => scrollTo("env-build")}>
                    <Hammer /> {build?.state === "failed" ? "Ver el error" : "Construir ahora"}
                  </Button>
                )
              }
            />
            <ChecklistItem
              state={agentsState}
              title="Agentes"
              detail={
                hosted.length > 0
                  ? `${hosted.length === 1 ? "Un agente alojado" : `${hosted.length} agentes alojados`} aquí.`
                  : "Ninguno todavía: aloja uno y el puerto lo arranca cuando le llegan mensajes."
              }
              action={write && <HostAgentDialog env={env} />}
            />
          </Checklist>
        </CardContent>
      )}
    </Card>
  )
}

/** Hosts one of the library's agents on this environment (or moves it here). */
function HostAgentDialog({ env }: { env: Environment }) {
  const agents = useAgents()
  const hosting = useHosting()
  const [open, setOpen] = React.useState(false)
  const [agent, setAgent] = React.useState("")
  const [slots, setSlots] = React.useState(1)
  const host = useHost(agent)
  const items = agents.data?.items ?? []
  const where = new Map((hosting.data?.agents ?? []).map((h) => [h.agent_id, h]))
  const current = where.get(agent)
  const moving = !!current && current.environment_id !== env.id
  const running = current?.slots.some((s) => s.state !== "stopped" && s.state !== "failed")
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setAgent("")
          setSlots(1)
        }
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant={env.agents > 0 ? "outline" : "default"}>
          <Plus /> Alojar agente
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            host.mutate({ environment: env.id, slots }, { onSuccess: () => setOpen(false) })
          }}
        >
          <DialogHeader>
            <DialogTitle>Alojar un agente en {env.name}</DialogTitle>
            <DialogDescription>
              El puerto arranca una instancia cuando le llegan mensajes y la detiene sin trabajo. Lo mismo que{" "}
              <Mono>seaport agents host &lt;agente&gt; {env.name}</Mono>.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            {items.length === 0 && agents.data ? (
              <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
                No hay agentes todavía:{" "}
                <Link to="/agents" className="text-foreground underline underline-offset-4">
                  crea uno
                </Link>{" "}
                y vuelve.
              </p>
            ) : (
              <Field>
                <FieldLabel htmlFor="host-agent">Agente</FieldLabel>
                <Select value={agent} onValueChange={setAgent}>
                  <SelectTrigger id="host-agent" aria-label="Agente" className="w-full font-mono">
                    <SelectValue placeholder="Elige un agente" />
                  </SelectTrigger>
                  <SelectContent>
                    {items.map((a: LibraryAgent) => {
                      const h = where.get(a.id)
                      return (
                        <SelectItem key={a.id} value={a.id} className="font-mono">
                          {a.name}
                          {h && (
                            <span className="ml-2 font-sans text-xs text-muted-foreground">
                              {h.environment_id === env.id ? "ya está aquí" : "alojado en otro entorno"}
                            </span>
                          )}
                        </SelectItem>
                      )
                    })}
                  </SelectContent>
                </Select>
                <FieldDescription>
                  {moving
                    ? running
                      ? "Está corriendo en otro entorno: detén sus instancias antes de moverlo."
                      : "Se mueve a este entorno con sus instancias detenidas."
                    : "Sus secretos deben cubrir lo que la configuración nombra y el entorno no tiene."}
                </FieldDescription>
              </Field>
            )}
            <Field className="w-36">
              <FieldLabel htmlFor="host-slots">Instancias</FieldLabel>
              <Select value={String(slots)} onValueChange={(v) => setSlots(Number(v))}>
                <SelectTrigger id="host-slots" aria-label="Instancias">
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
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button
              type="submit"
              disabled={
                !agent ||
                host.isPending ||
                (moving && running) ||
                (!!current && !moving && current.slots.length === slots)
              }
            >
              {host.isPending && <Spinner />} {moving ? "Mover aquí" : current ? "Aplicar" : "Alojar"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The current version's image: build state, log, (re)build. */
function BuildCard({ env, write }: { env: Environment; write: boolean }) {
  const build = useBuild(env.id, env.version)
  const start = useStartBuild(env.id)
  const b = build.data
  const [showLog, setShowLog] = React.useState(false)
  const open = showLog || b?.state === "building" || b?.state === "failed"
  return (
    <Card id="env-build" className="scroll-mt-6">
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <Hammer className="size-4" /> Imagen
            </CardTitle>
            <CardDescription>
              Cada versión se construye una vez: la base del harness más paquetes, setup y configuración.
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            {build.isPending ? <Skeleton className="h-5 w-24" /> : <BuildBadge build={b} />}
            {write && (
              <Button
                size="sm"
                variant={b?.state === "ready" ? "outline" : "default"}
                disabled={start.isPending || b?.state === "building"}
                onClick={() => {
                  setShowLog(true)
                  start.mutate(b?.state === "ready" || b?.state === "failed")
                }}
              >
                {start.isPending || b?.state === "building" ? <Spinner /> : <Hammer />}
                {b?.state === "ready" ? "Reconstruir" : b?.state === "failed" ? "Reintentar" : "Construir"}
              </Button>
            )}
          </div>
        </div>
      </CardHeader>
      {b && (
        <CardContent className="space-y-3">
          <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[auto_1fr]">
            <dt className="text-muted-foreground">Base</dt>
            <dd className="min-w-0 truncate">
              <Mono>{b.base}</Mono>
            </dd>
            {b.image && (
              <>
                <dt className="text-muted-foreground">Imagen</dt>
                <dd className="min-w-0 truncate">
                  <Mono title={b.image}>{b.image}</Mono>
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{b.finished_at ? "Terminó" : "Empezó"}</dt>
            <dd>{formatFull(b.finished_at ?? b.started_at)}</dd>
          </dl>
          {b.state === "failed" && b.detail && <p className="text-sm text-destructive">{b.detail}</p>}
          {open ? (
            <LogView text={b.log} empty={b.state === "building" ? "Esperando la salida…" : "Sin salida."} />
          ) : (
            <Button variant="link" size="sm" className="px-0" onClick={() => setShowLog(true)}>
              Ver registro
            </Button>
          )}
        </CardContent>
      )}
    </Card>
  )
}

/** Agents hosted on this environment. */
function AgentsOn({ env, hosted }: { env: Environment; hosted: Hosted[] }) {
  const agents = useAgents()
  if (hosted.length === 0) return null
  const names = new Map((agents.data?.items ?? []).map((a) => [a.id, a.name]))
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm" data-env={env.id}>
      <span className="text-muted-foreground">Alojados aquí:</span>
      {hosted.map((h) => (
        <Link key={h.agent_id} to={`/agents/${h.agent_id}`}>
          <AgentChip
            name={names.get(h.agent_id) ?? "…"}
            online={h.slots.some((s) => s.state === "running")}
            className="hover:bg-accent"
          />
        </Link>
      ))}
    </div>
  )
}

/** The YAML `seaport env apply -f` takes, to copy. */
function YamlDialog({ env }: { env: Environment }) {
  const yaml = environmentYaml(env.name, env.spec)
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <FileCode2 /> Ver como YAML
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{env.name}.yaml</DialogTitle>
          <DialogDescription>
            Lo mismo que <Mono>seaport env export {env.name}</Mono>; aplícalo con <Mono>seaport env apply -f</Mono>.
          </DialogDescription>
        </DialogHeader>
        <LogView text={yaml} className="max-h-[60vh] text-xs" />
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() =>
              navigator.clipboard.writeText(yaml).then(
                () => toast.success("YAML copiado"),
                () => toast.error("No se pudo copiar")
              )
            }
          >
            <Copy /> Copiar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** The spec as it is saved: empty lines and duplicates out. */
function clean(s: EnvSpec): EnvSpec {
  const list = (xs: string[]) => [...new Set(xs.map((x) => x.trim()).filter(Boolean))]
  return {
    ...s,
    env: Object.fromEntries(Object.entries(s.env ?? {}).filter(([k]) => k)),
    models: Object.fromEntries(Object.entries(s.models ?? {}).filter(([, v]) => v)),
    packages: list(s.packages),
    network: { egress: list(s.network.egress.map((h) => h.toLowerCase())) },
    resources: s.resources.size
      ? { size: s.resources.size, cpus: 0, memory_mb: 0 }
      : { cpus: s.resources.cpus, memory_mb: s.resources.memory_mb },
  }
}

const same = (a: EnvSpec, b: EnvSpec) => JSON.stringify(clean(a)) === JSON.stringify(clean(b))

/** Edits the current spec; saving makes a new version. */
function SpecEditor({
  env,
  write,
  keyFor,
  onKeyFor,
}: {
  env: Environment
  write: boolean
  /** The provider whose key dialog is open (from the checklist or a + here). */
  keyFor: (typeof providers)[number] | null
  onKeyFor: (p: (typeof providers)[number] | null) => void
}) {
  const [viewing, setViewing] = React.useState(0)
  const past = useEnvironment(env.id, viewing)
  const versions = useEnvironmentVersions(env.id)
  const secrets = useEnvironmentSecrets(env.id)
  const save = useSaveEnvironment(env.id)
  const [draft, setDraft] = React.useState<EnvSpec>(env.spec)
  const [base, setBase] = React.useState(env.version)
  const [baseline, setBaseline] = React.useState<EnvSpec>(env.spec)
  const dirty = !same(draft, baseline)
  const invalid =
    !!envProblem(draft.env ?? {}) ||
    Object.values(draft.models ?? {}).some((v) => v && !secretRefPattern.test(v)) ||
    !idleStopPattern.test(draft.idle_stop.trim())
  const load = (e: Environment) => {
    setDraft(e.spec)
    setBaseline(e.spec)
    setBase(e.version)
  }
  // A new version (ours or someone's) loads while nothing is unsaved.
  if (!dirty && env.version !== base) load(env)

  const readOnly = !write || viewing > 0
  const shown = viewing > 0 ? past.data?.spec : draft
  const stale = env.version !== base
  const conflict = save.error instanceof ApiError && save.error.status === 409
  const set = (patch: Partial<EnvSpec>) => setDraft((d) => ({ ...d, ...patch }))
  const doSave = (spec: EnvSpec) => save.mutate({ base, spec: clean(spec) }, { onSuccess: load })

  // The key dialog saved a secret: name it in the draft and, when nothing
  // else was being edited, save right away (one step for the person).
  const keySaved = (provider: string, name: string) => {
    const next = {
      ...draft,
      models: { ...(draft.models ?? {}), [provider]: asSecretRef(name) },
    }
    setDraft(next)
    if (!dirty && !stale && viewing === 0) doSave(next)
  }

  // The home diffs with: the saved version while the draft changes it, else
  // the version before the one shown.
  const draftHome = viewing === 0 && draft.home !== baseline.home
  const prevVersion = draftHome ? 0 : (viewing || base) - 1
  const prev = useEnvironment(prevVersion > 0 ? env.id : "", prevVersion)
  const homeCompare: { home?: string; label?: string } = draftHome
    ? { home: baseline.home, label: `la v${base} guardada` }
    : prevVersion > 0 && prev.data
      ? { home: prev.data.spec.home, label: `la v${prevVersion}` }
      : {}
  const secretNames = secrets.data?.secrets.map((s) => s.name) ?? []

  return (
    <>
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-1">
              <CardTitle className="flex items-center gap-2">
                <Boxes className="size-4" /> Configuración
              </CardTitle>
              <CardDescription>
                Cada guardado es una versión nueva; los agentes alojados reinician con ella al quedar libres.
              </CardDescription>
            </div>
            {(versions.data?.versions.length ?? 0) > 1 && (
              <Select value={String(viewing)} onValueChange={(v) => setViewing(Number(v))}>
                <SelectTrigger size="sm" className="w-56" aria-label="Versión">
                  <History className="size-3.5" />
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="0">{write ? "Actual (editable)" : `Actual (v${env.version})`}</SelectItem>
                  {versions.data?.versions
                    .filter((v) => v.version !== env.version)
                    .map((v) => (
                      <SelectItem key={v.version} value={String(v.version)}>
                        v{v.version} · {v.created_by || "—"} · {formatFull(v.created_at)}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            )}
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {viewing > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed px-3 py-2 text-sm">
              <span className="text-muted-foreground">Viendo la versión {viewing} (solo lectura).</span>
              {write && past.data && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setDraft(past.data.spec)
                    setViewing(0)
                  }}
                >
                  <RotateCcw /> Usarla como borrador
                </Button>
              )}
            </div>
          )}
          {!shown ? (
            <Skeleton className="h-64 rounded-xl" />
          ) : (
            <SpecFields
              envId={env.id}
              spec={shown}
              readOnly={readOnly}
              set={set}
              homeCompare={homeCompare}
              secrets={secretNames}
              secretsCard={<EnvironmentSecrets envId={env.id} spec={shown} write={write} />}
              onAddKey={readOnly ? undefined : onKeyFor}
            />
          )}
          {write && keyFor && (
            // Mounted per opening, so the preset name is fresh each time.
            <EnvironmentSecretDialog
              key={keyFor.name}
              envId={env.id}
              open
              onOpenChange={(o) => !o && onKeyFor(null)}
              preset={keyNameFor(keyFor)}
              title={`Clave de ${keyFor.label}`}
              description="Se guarda cifrada como secreto del entorno y se nombra en Modelos. La clave pasa por el gateway del puerto: nunca entra al contenedor."
              onSaved={(name) => keySaved(keyFor.name, name)}
            />
          )}
        </CardContent>
      </Card>
      {write && viewing === 0 && (dirty || stale || conflict) && (
        <SaveBar
          version={base + 1}
          dirty={dirty}
          invalid={invalid}
          stale={stale || conflict}
          saving={save.isPending}
          onDiscard={() => setDraft(baseline)}
          onReload={() => {
            load(env)
            save.reset()
          }}
          onSave={() => doSave(draft)}
        />
      )}
    </>
  )
}

/**
 * Sticks to the bottom of the viewport while there is something to save,
 * so an edit at the top of a long form always has its button in sight. A
 * sibling of the card, not inside it: the card clips (overflow-hidden), which
 * would pin the bar to the card instead of the viewport.
 */
function SaveBar({
  version,
  dirty,
  invalid,
  stale,
  saving,
  onDiscard,
  onReload,
  onSave,
}: {
  version: number
  dirty: boolean
  invalid: boolean
  stale: boolean
  saving: boolean
  onDiscard: () => void
  onReload: () => void
  onSave: () => void
}) {
  return (
    <div
      role="region"
      aria-label="Cambios sin guardar"
      className="sticky bottom-4 z-10 flex flex-wrap items-center justify-end gap-3 rounded-xl bg-card/95 px-4 py-3 shadow-lg ring-1 ring-foreground/10 backdrop-blur supports-[backdrop-filter]:bg-card/85"
    >
      {stale ? (
        <span className="mr-auto text-sm text-warning">
          Alguien guardó otra versión mientras editabas.{" "}
          <button type="button" className="underline" onClick={onReload}>
            Descartar mis cambios y cargarla
          </button>
        </span>
      ) : (
        <span className="mr-auto text-sm text-muted-foreground">
          {invalid ? "Revisa los campos marcados." : `Cambios sin guardar · será la v${version}`}
        </span>
      )}
      {dirty && !stale && (
        <Button variant="ghost" onClick={onDiscard}>
          Descartar
        </Button>
      )}
      <Button disabled={!dirty || invalid || stale || saving} onClick={onSave}>
        {saving ? <Spinner /> : <Save />} Guardar v{version}
      </Button>
    </div>
  )
}

function Section({
  id,
  icon: Icon,
  title,
  yaml,
  description,
  children,
  className,
}: {
  id?: string
  icon: React.ComponentType<{ className?: string }>
  title: string
  /** The spec's key(s) for this section, so the CLI/YAML mapping stays in sight. */
  yaml?: string
  description: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <section id={id} className={cn("scroll-mt-6 space-y-3 border-t pt-5 first:border-t-0 first:pt-0", className)}>
      <div>
        <h3 className="flex flex-wrap items-center gap-2 text-sm font-medium">
          <Icon className="size-4" /> {title}
          {yaml && (
            <Mono className="text-xs font-normal text-muted-foreground" title="Clave en el YAML del entorno">
              {yaml}
            </Mono>
          )}
        </h3>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

/**
 * The knobs most environments never touch, folded under one line. Open when
 * any of them is off its default, so nothing set is ever hidden.
 */
function Advanced({ open: initial, children }: { open: boolean; children: React.ReactNode }) {
  const [open, setOpen] = React.useState<boolean | null>(null)
  const show = open ?? initial
  return (
    <div className="border-t pt-5">
      <button
        type="button"
        aria-expanded={show}
        aria-controls="env-advanced"
        onClick={() => setOpen(!show)}
        className="flex w-full items-center gap-2 text-left text-sm font-medium hover:text-foreground"
      >
        {show ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        Avanzado
        <span className="text-xs font-normal text-muted-foreground">
          red, navegador, recursos, variables, imagen base
        </span>
      </button>
      {show && (
        <div
          id="env-advanced"
          className="mt-5 space-y-5 border-l-2 border-muted pl-4 [&>section]:first:border-t-0 [&>section]:first:pt-0"
        >
          {children}
        </div>
      )}
    </div>
  )
}

/** Whether a spec differs from the defaults in anything the Avanzado fold holds. */
export function advancedTouched(s: EnvSpec): boolean {
  return (
    s.network.egress.some((h) => h.trim()) ||
    Object.keys(s.env ?? {}).length > 0 ||
    !!s.browser?.engine ||
    s.resources.size !== "S" ||
    s.workspace !== "persistent" ||
    (s.idle_stop.trim() !== "" && s.idle_stop.trim() !== "30m") ||
    s.image.trim() !== ""
  )
}

const sizeOptions = [
  { value: "S", label: "S", description: "1 CPU · 2 GB" },
  { value: "M", label: "M", description: "2 CPU · 4 GB" },
  { value: "L", label: "L", description: "4 CPU · 8 GB" },
  { value: "custom", label: "A medida" },
]

const placeholderMuted = "placeholder:text-muted-foreground/50"

function SpecFields({
  spec: s,
  readOnly,
  set,
  homeCompare,
  secrets,
  secretsCard,
  onAddKey,
}: {
  envId: string
  spec: EnvSpec
  readOnly: boolean
  set: (patch: Partial<EnvSpec>) => void
  homeCompare: { home?: string; label?: string }
  /** The environment's secret names (for the `${NAME}` selects). */
  secrets: string[]
  /** The secrets list, shown next to the model keys. */
  secretsCard: React.ReactNode
  onAddKey?: (p: (typeof providers)[number]) => void
}) {
  const idleOk = idleStopPattern.test(s.idle_stop.trim())
  return (
    <FieldGroup>
      <Section icon={Server} title="Harness" yaml="harness" description="Qué agente corre en cada instancia.">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel>Harness</FieldLabel>
            <SegmentedChoice
              aria-label="Harness"
              value={s.harness}
              onChange={(harness) => set({ harness })}
              options={[
                { value: "rness", label: "rness" },
                { value: "claude-code", label: "Claude Code" },
              ]}
              disabled={readOnly}
              size="sm"
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="env-description">Descripción</FieldLabel>
            <Input
              id="env-description"
              value={s.description}
              readOnly={readOnly}
              onChange={(e) => set({ description: e.target.value })}
              maxLength={1000}
              placeholder="Para qué sirve este entorno"
              className={placeholderMuted}
            />
          </Field>
        </div>
      </Section>

      <Section
        id="env-section-models"
        icon={KeyRound}
        title="Modelos y secretos"
        yaml="models"
        description="La clave de cada proveedor es un secreto del entorno; se guardan cifrados y no se vuelven a leer."
      >
        <div className="grid gap-6 lg:grid-cols-2">
          <Field>
            <FieldLabel>Modelos</FieldLabel>
            <ModelFields spec={s} readOnly={readOnly} set={set} secrets={secrets} onNewSecret={onAddKey} />
          </Field>
          <Field>
            <FieldLabel>Secretos del entorno</FieldLabel>
            {secretsCard}
            <FieldDescription>
              Lo propio de un agente (su token de Linear, su cuenta) va en los secretos del agente.
            </FieldDescription>
          </Field>
        </div>
      </Section>

      <Section
        id="env-section-repos"
        icon={GitBranch}
        title="Repos"
        yaml="repos"
        description={
          <>
            Se clonan en <Mono>/workspace</Mono>; cada conversación trabaja en su propia copia (worktree).
          </>
        }
      >
        <ReposField repos={s.repos} readOnly={readOnly} onChange={(repos) => set({ repos })} secrets={secrets} />
        {(s.repos.length > 0 || s.repo_config !== "trust") && (
          <Field>
            <FieldLabel>
              Configuración de los repos <Mono className="text-xs font-normal text-muted-foreground">repo_config</Mono>
            </FieldLabel>
            <SegmentedChoice
              aria-label="Configuración de los repos"
              value={s.repo_config}
              onChange={(repo_config) => set({ repo_config })}
              options={[
                {
                  value: "trust",
                  label: "Confiar",
                  description: "El harness lee AGENTS.md, CLAUDE.md, .claude/, .rness/",
                },
                {
                  value: "ignore",
                  label: "Ignorar",
                  description: "El harness no lee la configuración de los repos",
                },
              ]}
              disabled={readOnly}
              size="sm"
            />
          </Field>
        )}
      </Section>

      <Section
        icon={Hammer}
        title="Paquetes del sistema"
        yaml="packages · setup"
        description="Lo que se instala en la imagen al construirla."
      >
        <Field>
          <FieldLabel htmlFor="env-packages">Paquetes (apt)</FieldLabel>
          <ChipInput
            id="env-packages"
            aria-label="Paquetes (apt)"
            aria-describedby="env-packages-help"
            value={s.packages}
            readOnly={readOnly}
            onChange={(packages) => set({ packages })}
            placeholder="nodejs"
          />
          <FieldDescription id="env-packages-help">Enter añade; nombre o nombre=versión.</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="env-setup">Setup</FieldLabel>
          <Textarea
            id="env-setup"
            value={s.setup}
            readOnly={readOnly}
            onChange={(e) => set({ setup: e.target.value })}
            placeholder="curl -fsSL https://… | bash"
            className={cn("min-h-24 font-mono text-sm", placeholderMuted)}
            maxLength={16384}
          />
          <FieldDescription>bash como root al construir la imagen. Nada secreto: queda en la imagen.</FieldDescription>
        </Field>
      </Section>

      <Section
        icon={FolderCog}
        title="Configuración del harness"
        yaml="home"
        description={
          <>
            Tu <Mono>~/{s.harness === "rness" ? ".rness" : ".claude"}</Mono> (instrucciones, skills, hooks), horneado en
            la imagen. Sin secretos: esos van en Modelos y secretos.
          </>
        }
      >
        <HomeSection
          harness={s.harness}
          home={s.home}
          compare={homeCompare.home}
          compareLabel={homeCompare.label}
          readOnly={readOnly}
          onChange={(home) => set({ home })}
        />
      </Section>

      <Advanced open={advancedTouched(s)}>
        <Section
          icon={Globe}
          title="Hosts permitidos (salida)"
          yaml="network.egress"
          description="Con el proxy de salida activo, las instancias solo llegan a estos hosts (más los de sus repos y modelos)."
        >
          <Field>
            <FieldLabel htmlFor="env-egress">Hosts permitidos</FieldLabel>
            <ChipInput
              id="env-egress"
              aria-label="Hosts permitidos"
              aria-describedby="env-egress-help"
              value={s.network.egress}
              readOnly={readOnly}
              onChange={(egress) => set({ network: { egress } })}
              placeholder="registry.npmjs.org"
            />
            <FieldDescription id="env-egress-help">
              Enter añade; *.dominio para sus subdominios; * sola para cualquier web pública (búsquedas, cualquier
              página). Las direcciones privadas siguen bloqueadas.
            </FieldDescription>
          </Field>
        </Section>

        <Section
          icon={AppWindow}
          title="Navegador"
          yaml="browser"
          description="Un Chromium por instancia, con perfil persistente que comparten el agente y las personas."
        >
          <Field>
            <FieldLabel>Motor</FieldLabel>
            <SegmentedChoice
              aria-label="Navegador"
              value={s.browser?.engine || "none"}
              onChange={(v) => set({ browser: { engine: v === "none" ? "" : "chromium" } })}
              options={[
                { value: "none", label: "Sin navegador" },
                {
                  value: "chromium",
                  label: "Chromium",
                  description: "Headless, dentro de la instancia",
                },
              ]}
              disabled={readOnly}
              size="sm"
            />
            <FieldDescription>
              Cada agente lo activa en su perfil; una persona inicia sesión en los sitios desde la pestaña Navegador del
              agente. Añade los hosts de esos sitios arriba.
            </FieldDescription>
          </Field>
        </Section>

        <Section
          icon={HardDrive}
          title="Recursos"
          yaml="resources · workspace · idle_stop"
          description="Por instancia."
        >
          <Field>
            <FieldLabel>Tamaño</FieldLabel>
            <SegmentedChoice
              aria-label="Tamaño"
              value={s.resources.size ?? "custom"}
              onChange={(v) =>
                set({
                  resources:
                    v === "custom"
                      ? {
                          cpus: s.resources.cpus || 1,
                          memory_mb: s.resources.memory_mb || 2048,
                        }
                      : { size: v as ResourceSize, cpus: 0, memory_mb: 0 },
                })
              }
              options={sizeOptions}
              disabled={readOnly}
              size="sm"
            />
          </Field>
          {!s.resources.size && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="env-cpus">CPUs</FieldLabel>
                <Input
                  id="env-cpus"
                  type="number"
                  step={0.25}
                  min={0.25}
                  max={64}
                  value={s.resources.cpus}
                  readOnly={readOnly}
                  onChange={(e) =>
                    set({
                      resources: { ...s.resources, cpus: Number(e.target.value) },
                    })
                  }
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="env-memory">Memoria (MB)</FieldLabel>
                <Input
                  id="env-memory"
                  type="number"
                  step={256}
                  min={256}
                  value={s.resources.memory_mb}
                  readOnly={readOnly}
                  onChange={(e) =>
                    set({
                      resources: {
                        ...s.resources,
                        memory_mb: Number(e.target.value),
                      },
                    })
                  }
                />
              </Field>
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <FieldLabel>Espacio de trabajo</FieldLabel>
              <SegmentedChoice
                aria-label="Espacio de trabajo"
                value={s.workspace}
                onChange={(workspace) => set({ workspace })}
                options={[
                  {
                    value: "persistent",
                    label: "Persistente",
                    description: "Lo que el agente clona, descarga o crea sigue ahí en la siguiente ejecución",
                  },
                  {
                    value: "ephemeral",
                    label: "Efímero",
                    description: "Cada arranque empieza de cero (las conversaciones se retoman igual)",
                  },
                ]}
                disabled={readOnly}
                size="sm"
              />
            </Field>
            <Field data-invalid={!idleOk}>
              <FieldLabel htmlFor="env-idle">Detener sin trabajo tras</FieldLabel>
              <Input
                id="env-idle"
                value={s.idle_stop}
                readOnly={readOnly}
                onChange={(e) => set({ idle_stop: e.target.value.trim() })}
                placeholder="30m"
                className={cn("font-mono", placeholderMuted)}
              />
              <FieldDescription>
                {idleOk
                  ? "Minutos u horas (30m, 2h); 0 nunca. Un mensaje nuevo la vuelve a arrancar."
                  : "Como 30m o 2h, o 0."}
              </FieldDescription>
            </Field>
          </div>
        </Section>

        <Section
          icon={Boxes}
          title="Variables"
          yaml="env"
          description="Variables de entorno de cada instancia, además de los secretos marcados como variable."
        >
          <EnvVarsField spec={s} readOnly={readOnly} set={set} />
        </Section>

        <Section
          icon={Server}
          title="Imagen base"
          yaml="image"
          description="Una imagen propia en lugar de la de Seaport. Debe derivar de la base del mismo harness."
        >
          <Field>
            <FieldLabel htmlFor="env-image">Imagen base</FieldLabel>
            <Input
              id="env-image"
              value={s.image}
              readOnly={readOnly}
              onChange={(e) => set({ image: e.target.value.trim() })}
              placeholder="la de Seaport para el harness"
              className={cn("font-mono", placeholderMuted)}
              maxLength={255}
            />
          </Field>
        </Section>
      </Advanced>
    </FieldGroup>
  )
}

function ReposField({
  repos,
  readOnly,
  onChange,
  secrets,
}: {
  repos: EnvRepo[]
  readOnly: boolean
  onChange: (r: EnvRepo[]) => void
  secrets: string[]
}) {
  return (
    <div className="space-y-2">
      {repos.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
          Sin repos.
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {repos.map((r, i) => (
            <li key={i} className="flex items-center gap-3 px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-sm">{r.url}</div>
                <div className="truncate text-xs text-muted-foreground">
                  /workspace/{r.path || repoName(r.url)}
                  {r.ref && ` · ${r.ref}`}
                  {r.credential && ` · ${r.credential}`}
                  {r.on_start && " · on_start"}
                </div>
              </div>
              <RepoDialog
                repo={r}
                readOnly={readOnly}
                secrets={secrets}
                onSave={(n) => onChange(repos.map((x, j) => (j === i ? n : x)))}
              />
              {!readOnly && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Quitar ${r.url}`}
                  onClick={() => onChange(repos.filter((_, j) => j !== i))}
                >
                  <X />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && repos.length < 10 && <RepoDialog secrets={secrets} onSave={(r) => onChange([...repos, r])} />}
    </div>
  )
}

function repoName(url: string) {
  return (url.replace(/\/+$/, "").split("/").pop() ?? "").replace(/\.git$/, "")
}

function RepoDialog({
  repo,
  readOnly,
  secrets,
  onSave,
}: {
  repo?: EnvRepo
  readOnly?: boolean
  secrets: string[]
  onSave: (r: EnvRepo) => void
}) {
  const blank: EnvRepo = {
    url: "",
    ref: "",
    path: "",
    credential: "",
    on_start: "",
  }
  const [open, setOpen] = React.useState(false)
  const [r, setR] = React.useState<EnvRepo>(repo ?? blank)
  const urlOk = /^https:\/\/[^\s/@?#]+\/[^\s?#]+$/.test(r.url.trim())
  const credOk = !r.credential || secretRefPattern.test(r.credential)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setR(repo ?? blank)
      }}
    >
      <DialogTrigger asChild>
        {repo ? (
          <Button variant="ghost" size="icon-sm" aria-label={readOnly ? `Ver ${repo.url}` : `Editar ${repo.url}`}>
            <Pencil />
          </Button>
        ) : (
          <Button variant="outline" size="sm">
            <Plus /> Repo
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (readOnly) return setOpen(false)
            onSave({ ...r, url: r.url.trim(), on_start: r.on_start.trim() })
            setOpen(false)
          }}
        >
          <DialogHeader>
            <DialogTitle>{repo ? repoName(repo.url) : "Nuevo repo"}</DialogTitle>
            <DialogDescription>
              Un repo git por https. El token nunca queda en disco: git lo pide al puerto.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!r.url && !urlOk}>
              <FieldLabel htmlFor="repo-url">URL</FieldLabel>
              <Input
                id="repo-url"
                value={r.url}
                readOnly={readOnly}
                onChange={(e) => setR({ ...r, url: e.target.value })}
                className={cn("font-mono", placeholderMuted)}
                placeholder="https://github.com/acme/app.git"
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="repo-ref">Rama o tag</FieldLabel>
                <Input
                  id="repo-ref"
                  value={r.ref}
                  readOnly={readOnly}
                  onChange={(e) => setR({ ...r, ref: e.target.value.trim() })}
                  className={cn("font-mono", placeholderMuted)}
                  placeholder="la por defecto"
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="repo-path">Carpeta</FieldLabel>
                <Input
                  id="repo-path"
                  value={r.path}
                  readOnly={readOnly}
                  onChange={(e) => setR({ ...r, path: e.target.value.trim() })}
                  className={cn("font-mono", placeholderMuted)}
                  placeholder={repoName(r.url) || "app"}
                />
              </Field>
            </div>
            <Field data-invalid={!credOk}>
              <FieldLabel htmlFor="repo-credential">Credencial</FieldLabel>
              <SecretSelect
                id="repo-credential"
                label="Credencial"
                value={r.credential}
                secrets={secrets}
                readOnly={readOnly}
                none="Ninguna (repo público)"
                onChange={(credential) => setR({ ...r, credential })}
              />
              <FieldDescription>
                {credOk
                  ? "Un secreto del entorno con el token del repo. Créalo en Modelos y secretos si falta."
                  : "Debe nombrar un secreto, como ${NOMBRE}."}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="repo-onstart">Al arrancar</FieldLabel>
              <Textarea
                id="repo-onstart"
                value={r.on_start}
                readOnly={readOnly}
                onChange={(e) => setR({ ...r, on_start: e.target.value })}
                className={cn("min-h-20 font-mono text-sm", placeholderMuted)}
                placeholder="npm ci"
                maxLength={4096}
              />
              <FieldDescription>Se corre en el clon en cada arranque (dependencias).</FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              {readOnly ? "Cerrar" : "Cancelar"}
            </Button>
            {!readOnly && (
              <Button type="submit" disabled={!urlOk || !credOk}>
                {repo ? "Aplicar" : "Agregar"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
