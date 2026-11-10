import * as React from "react"
import { Link, useNavigate, useParams } from "react-router"
import {
  ArrowLeft,
  Boxes,
  Copy,
  FileCode2,
  FolderCog,
  GitBranch,
  Globe,
  Hammer,
  History,
  HardDrive,
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
  useEnvironmentVersions,
  useHosting,
  useSaveEnvironment,
  useStartBuild,
} from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { EnvRepo, EnvSpec, Environment, ResourceSize } from "@/lib/types"
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
  ConfirmAction,
  ErrorState,
  LogView,
  Mono,
  SegmentedChoice,
  StatusBadge,
} from "@/components/seaport"
import { RuntimeOff, harnessLabel } from "@/pages/environments"
import { HomeSection } from "@/pages/environment-home"

const credentialPattern = /^\$\{[A-Z_][A-Z0-9_]{0,63}\}$/
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
  const remove = useDeleteEnvironment()
  const navigate = useNavigate()
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
        {hosting.data && !hosting.data.runtime && <RuntimeOff />}
        <BuildCard env={env} write={write && !!hosting.data?.runtime} />
        <AgentsOn env={env} />
        <SpecEditor env={env} write={write} />
      </div>
    </div>
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
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <Hammer className="size-4" /> Imagen
            </CardTitle>
            <CardDescription>
              Cada versión se construye una vez sobre la base del harness (paquetes y setup). Probarla aquí evita que el
              primer mensaje espere la construcción.
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
function AgentsOn({ env }: { env: Environment }) {
  const hosting = useHosting()
  const agents = useAgents()
  const on = (hosting.data?.agents ?? []).filter((h) => h.environment_id === env.id)
  if (!hosting.data || on.length === 0) return null
  const names = new Map((agents.data?.items ?? []).map((a) => [a.id, a.name]))
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="text-muted-foreground">Alojados aquí:</span>
      {on.map((h) => (
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
                () => toast.error("No se pudo copiar"),
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
    packages: list(s.packages),
    network: { egress: list(s.network.egress.map((h) => h.toLowerCase())) },
    resources: s.resources.size
      ? { size: s.resources.size, cpus: 0, memory_mb: 0 }
      : { cpus: s.resources.cpus, memory_mb: s.resources.memory_mb },
  }
}

const same = (a: EnvSpec, b: EnvSpec) => JSON.stringify(clean(a)) === JSON.stringify(clean(b))

/** Edits the current spec; saving makes a new version. */
function SpecEditor({ env, write }: { env: Environment; write: boolean }) {
  const [viewing, setViewing] = React.useState(0)
  const past = useEnvironment(env.id, viewing)
  const versions = useEnvironmentVersions(env.id)
  const save = useSaveEnvironment(env.id)
  const [draft, setDraft] = React.useState<EnvSpec>(env.spec)
  const [base, setBase] = React.useState(env.version)
  const [baseline, setBaseline] = React.useState<EnvSpec>(env.spec)
  const dirty = !same(draft, baseline)
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

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <Boxes className="size-4" /> Especificación
            </CardTitle>
            <CardDescription>
              Al guardar, sus agentes reinician con la versión nueva cuando quedan libres (la imagen se construye si hace
              falta).
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
          <SpecFields spec={shown} readOnly={readOnly} set={set} homeCompare={homeCompare} />
        )}
        {write && viewing === 0 && (
          <div className="flex flex-wrap items-center justify-end gap-3 border-t pt-4">
            {(stale || conflict) && (
              <span className="mr-auto text-sm text-warning">
                Alguien guardó la v{env.version} mientras editabas.{" "}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    load(env)
                    save.reset()
                  }}
                >
                  Descartar mis cambios y cargarla
                </button>
              </span>
            )}
            {dirty && !stale && (
              <Button variant="ghost" onClick={() => setDraft(baseline)}>
                Descartar
              </Button>
            )}
            <Button
              disabled={!dirty || stale || save.isPending}
              onClick={() => save.mutate({ base, spec: clean(draft) }, { onSuccess: load })}
            >
              {save.isPending ? <Spinner /> : <Save />} Guardar v{base + 1}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function Section({
  icon: Icon,
  title,
  description,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>
  title: string
  description: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="space-y-3 border-t pt-5 first:border-t-0 first:pt-0">
      <div>
        <h3 className="flex items-center gap-2 text-sm font-medium">
          <Icon className="size-4" /> {title}
        </h3>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

const sizeOptions = [
  { value: "S", label: "S", description: "1 CPU · 2 GB" },
  { value: "M", label: "M", description: "2 CPU · 4 GB" },
  { value: "L", label: "L", description: "4 CPU · 8 GB" },
  { value: "custom", label: "A medida" },
]

function SpecFields({
  spec: s,
  readOnly,
  set,
  homeCompare,
}: {
  spec: EnvSpec
  readOnly: boolean
  set: (patch: Partial<EnvSpec>) => void
  homeCompare: { home?: string; label?: string }
}) {
  const keyOk = !s.models.anthropic || credentialPattern.test(s.models.anthropic)
  const idleOk = idleStopPattern.test(s.idle_stop.trim())
  return (
    <FieldGroup>
      <Section icon={Server} title="Base" description="Qué harness corre y sobre qué imagen.">
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
            <FieldLabel htmlFor="env-image">Imagen base</FieldLabel>
            <Input
              id="env-image"
              value={s.image}
              readOnly={readOnly}
              onChange={(e) => set({ image: e.target.value.trim() })}
              placeholder="la de Seaport para el harness"
              className="font-mono"
              maxLength={255}
            />
            <FieldDescription>Debe derivar de la base de Seaport del mismo harness.</FieldDescription>
          </Field>
        </div>
        <Field>
          <FieldLabel htmlFor="env-description">Descripción</FieldLabel>
          <Input
            id="env-description"
            value={s.description}
            readOnly={readOnly}
            onChange={(e) => set({ description: e.target.value })}
            maxLength={1000}
          />
        </Field>
      </Section>

      <Section icon={Hammer} title="Herramientas" description="Lo que se instala en la imagen al construirla.">
        <div className="grid gap-4 sm:grid-cols-[14rem_1fr]">
          <Field>
            <FieldLabel htmlFor="env-packages">Paquetes (apt)</FieldLabel>
            <Textarea
              id="env-packages"
              value={s.packages.join("\n")}
              readOnly={readOnly}
              onChange={(e) => set({ packages: e.target.value.split("\n") })}
              placeholder={"nodejs\nnpm"}
              className="min-h-32 font-mono text-sm"
            />
            <FieldDescription>Uno por línea; nombre o nombre=versión.</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="env-setup">Setup</FieldLabel>
            <Textarea
              id="env-setup"
              value={s.setup}
              readOnly={readOnly}
              onChange={(e) => set({ setup: e.target.value })}
              placeholder="curl -fsSL https://… | bash"
              className="min-h-32 font-mono text-sm"
              maxLength={16384}
            />
            <FieldDescription>bash como root al construir; el home vuelve al usuario del agente.</FieldDescription>
          </Field>
        </div>
      </Section>

      <Section
        icon={FolderCog}
        title="Configuración del harness"
        description={
          <>
            Tu <Mono>~/{s.harness === "rness" ? ".rness" : ".claude"}</Mono> (instrucciones, ajustes, agentes, skills,
            hooks) horneado en la imagen. Los secretos no van aquí: dalos como secretos env del agente y léelos del
            entorno.
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

      <Section
        icon={GitBranch}
        title="Repos"
        description={
          <>
            Se clonan en <Mono>/workspace</Mono>; cada conversación trabaja en su propio worktree. La credencial es un
            secreto del agente alojado, como <Mono>{"${GITHUB_TOKEN}"}</Mono>.
          </>
        }
      >
        <ReposField repos={s.repos} readOnly={readOnly} onChange={(repos) => set({ repos })} />
        <Field>
          <FieldLabel>Configuración de los repos</FieldLabel>
          <SegmentedChoice
            aria-label="Configuración de los repos"
            value={s.repo_config}
            onChange={(repo_config) => set({ repo_config })}
            options={[
              { value: "trust", label: "Confiar", description: "El harness lee AGENTS.md, CLAUDE.md, .claude/, .rness/" },
              { value: "ignore", label: "Ignorar", description: "El harness no lee la configuración de los repos" },
            ]}
            disabled={readOnly}
            size="sm"
          />
        </Field>
      </Section>

      <Section
        icon={Globe}
        title="Red y modelos"
        description="Con el proxy de salida activo, las instancias solo llegan a estos hosts (y a los de sus repos)."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="env-egress">Hosts permitidos</FieldLabel>
            <Textarea
              id="env-egress"
              value={s.network.egress.join("\n")}
              readOnly={readOnly}
              onChange={(e) => set({ network: { egress: e.target.value.split("\n") } })}
              placeholder={"registry.npmjs.org\n*.example.com"}
              className="min-h-28 font-mono text-sm"
            />
            <FieldDescription>Uno por línea; *.dominio para sus subdominios.</FieldDescription>
          </Field>
          <Field data-invalid={!keyOk}>
            <FieldLabel htmlFor="env-anthropic">Clave de Anthropic</FieldLabel>
            <Input
              id="env-anthropic"
              value={s.models.anthropic}
              readOnly={readOnly}
              onChange={(e) => set({ models: { anthropic: e.target.value.trim() } })}
              placeholder="${ANTHROPIC_API_KEY}"
              className="font-mono"
            />
            <FieldDescription>
              {!keyOk
                ? "Debe nombrar un secreto del agente, como ${NOMBRE}."
                : !s.models.anthropic && s.harness === "claude-code"
                  ? "Sin clave, cada instancia se conecta a una cuenta de Claude (suscripción, experimental) desde la página del agente; el proxy le abre api.anthropic.com y platform.claude.com."
                  : "Un secreto del agente. Pasa por el gateway del puerto: la clave nunca entra al contenedor y el uso queda medido."}
            </FieldDescription>
          </Field>
        </div>
      </Section>

      <Section icon={HardDrive} title="Recursos y espacio de trabajo" description="Por instancia.">
        <Field>
          <FieldLabel>Tamaño</FieldLabel>
          <SegmentedChoice
            aria-label="Tamaño"
            value={s.resources.size ?? "custom"}
            onChange={(v) =>
              set({
                resources:
                  v === "custom"
                    ? { cpus: s.resources.cpus || 1, memory_mb: s.resources.memory_mb || 2048 }
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
                onChange={(e) => set({ resources: { ...s.resources, cpus: Number(e.target.value) } })}
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
                onChange={(e) => set({ resources: { ...s.resources, memory_mb: Number(e.target.value) } })}
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
                { value: "persistent", label: "Persistente", description: "Clones y worktrees sobreviven a los reinicios" },
                { value: "ephemeral", label: "Efímero", description: "Se borra con el contenedor" },
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
              className="font-mono"
            />
            <FieldDescription>
              {idleOk ? "Minutos u horas (30m, 2h); 0 nunca. Un mensaje nuevo la vuelve a arrancar." : "Como 30m o 2h, o 0."}
            </FieldDescription>
          </Field>
        </div>
      </Section>
    </FieldGroup>
  )
}

function ReposField({
  repos,
  readOnly,
  onChange,
}: {
  repos: EnvRepo[]
  readOnly: boolean
  onChange: (r: EnvRepo[]) => void
}) {
  return (
    <div className="space-y-2">
      {repos.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">Sin repos.</p>
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
      {!readOnly && repos.length < 10 && <RepoDialog onSave={(r) => onChange([...repos, r])} />}
    </div>
  )
}

function repoName(url: string) {
  return (url.replace(/\/+$/, "").split("/").pop() ?? "").replace(/\.git$/, "")
}

function RepoDialog({ repo, readOnly, onSave }: { repo?: EnvRepo; readOnly?: boolean; onSave: (r: EnvRepo) => void }) {
  const blank: EnvRepo = { url: "", ref: "", path: "", credential: "", on_start: "" }
  const [open, setOpen] = React.useState(false)
  const [r, setR] = React.useState<EnvRepo>(repo ?? blank)
  const urlOk = /^https:\/\/[^\s/@?#]+\/[^\s?#]+$/.test(r.url.trim())
  const credOk = !r.credential || credentialPattern.test(r.credential)
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
            <DialogDescription>Un repo git por https. El token nunca queda en disco: git lo pide al puerto.</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!r.url && !urlOk}>
              <FieldLabel htmlFor="repo-url">URL</FieldLabel>
              <Input
                id="repo-url"
                value={r.url}
                readOnly={readOnly}
                onChange={(e) => setR({ ...r, url: e.target.value })}
                className="font-mono"
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
                  className="font-mono"
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
                  className="font-mono"
                  placeholder={repoName(r.url) || "app"}
                />
              </Field>
            </div>
            <Field data-invalid={!credOk}>
              <FieldLabel htmlFor="repo-credential">Credencial</FieldLabel>
              <Input
                id="repo-credential"
                value={r.credential}
                readOnly={readOnly}
                onChange={(e) => setR({ ...r, credential: e.target.value.trim() })}
                className="font-mono"
                placeholder="${GITHUB_TOKEN}"
              />
              <FieldDescription>
                {credOk
                  ? "Un secreto de cada agente alojado aquí (vacío: repo público)."
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
                className="min-h-20 font-mono text-sm"
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
