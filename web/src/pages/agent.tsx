import * as React from "react"
import { Link, Navigate, useLocation, useNavigate, useParams } from "react-router"
import {
  ArrowLeft,
  Bot,
  AppWindow,
  Copy,
  Cpu,
  FileCode2,
  History,
  KeyRound,
  Pencil,
  Plug,
  Plus,
  RotateCcw,
  Save,
  ScrollText,
  Server,
  ShieldCheck,
  Sparkles,
  Trash2,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { ApiError } from "@/lib/api"
import { configYaml } from "@/lib/config-yaml"
import {
  flatten,
  useAgent,
  useCan,
  useCloneAgent,
  useCrew,
  useDeleteSecret,
  useDocksDestinations,
  useHosted,
  useInbox,
  useNow,
  useProfile,
  useProfileVersions,
  useSaveProfile,
  useSecrets,
} from "@/lib/hooks"
import { conversationName, formatFull } from "@/lib/format"
import {
  agentNamePattern,
  cleanMachine,
  configSecrets,
  emptyConfig,
  fullMachine,
  harnessLabel,
  machineProblem,
  newMachine,
} from "@/lib/machine"
import { Perm } from "@/lib/permissions.gen"
import type { Agent, AgentProfile, Approve, Harness, LibraryAgent, Machine, McpServer, ProfileInput, Skill } from "@/lib/types"
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
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import {
  AgentCard,
  ConfirmAction,
  EmptyState,
  ErrorState,
  LogView,
  Mono,
  SecretDialog,
  SegmentedChoice,
  StatusBadge,
  VaultsCard,
  type ChoiceOption,
} from "@/components/seaport"
import { BrowserCard, SlotAccounts, UsageCard, WhereTab, hasBrowserTab } from "@/pages/agent-hosting"
import { ConfigSection, MachineSections, ModelAccess, type SecretsCtx } from "@/pages/agent-machine"

/** The agent profile rules (agent.SaveProfile.Validate). */
const skillNamePattern = /^[a-z0-9][a-z0-9_-]{0,63}$/
const serverNamePattern = /^[a-z0-9][a-z0-9_-]{0,31}$/

const approveOptions: ChoiceOption<"connector" | "allow" | "reject">[] = [
  { value: "connector", label: "Lo decide el conector", description: "Su flag -approve (rechaza por defecto)" },
  { value: "reject", label: "Rechazar", tone: "danger" },
  { value: "allow", label: "Permitir", tone: "success" },
]

const empty = emptyConfig

function inputOf(p: AgentProfile): ProfileInput {
  return {
    instructions: p.instructions,
    skills: p.skills ?? [],
    mcp_servers: (p.mcp_servers ?? []).map((s) => ({ ...s, headers: s.headers ?? [] })),
    approve: p.approve,
    model: p.model,
    effort: p.effort,
    browser: p.browser,
    browser_assist: p.browser_assist ?? "",
    browser_detect: p.browser_detect ?? false,
    browser_ignore_hosts: p.browser_ignore_hosts ?? [],
    harness: p.harness ?? "",
    machine: p.machine ? fullMachine(p.machine) : null,
  }
}

/** The config as it is sent: the machine cleaned of blank entries. */
function outOf(p: ProfileInput): ProfileInput {
  return { ...p, machine: p.machine ? cleanMachine(p.machine) : null }
}

/** The agent page's tabs; the URL's hash picks one (`#navegador`). */
const tabs = ["resumen", "configuracion", "secretos", "donde", "navegador"] as const
type Tab = (typeof tabs)[number]

/** An agent: its profile (what its harness is told and given) and secrets. */
export function AgentPage() {
  const { agentId = "" } = useParams()
  const agent = useAgent(agentId)
  if (agent.isError) return <ErrorState error={agent.error} onRetry={() => agent.refetch()} className="flex-1" />
  if (!agent.data)
    return (
      <div className="space-y-4 p-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  return <AgentDetail agent={agent.data} />
}

/**
 * Takeover notices sent before the agent page had anchors link
 * `/agents/{id}/hosting`: send them to the browser card.
 */
export function AgentHostingRedirect() {
  const { agentId = "" } = useParams()
  return <Navigate to={`/agents/${agentId}#navegador`} replace />
}

function AgentDetail({ agent }: { agent: LibraryAgent }) {
  const can = useCan()
  const crew = useCrew()
  const hosted = useHosted(agent.id)
  const profile = useProfile(agent.id)
  const { hash } = useLocation()
  const navigate = useNavigate()
  const fromHash = hash.slice(1) as Tab
  const tab: Tab = tabs.includes(fromHash) ? fromHash : "resumen"
  const setTab = (t: string) => navigate({ hash: t === "resumen" ? "" : t }, { replace: true })
  const conns = crew.data?.agents.filter((c) => c.agent_id === agent.id)
  const outdated = conns?.filter((c) => c.outdated).length ?? 0
  const p = profile.data
  const browserTab = hasBrowserTab(agent, p) || tab === "navegador"
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <div className="border-b px-6 pt-5">
        <Link
          to="/agents"
          className="mb-3 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" /> Agentes
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <span className="inline-flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <Bot className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="truncate font-mono text-xl font-semibold tracking-tight">@{agent.name}</h1>
            <p className="mt-0.5 text-sm text-muted-foreground">
              {agent.description || <span className="italic">Sin descripción</span>}
            </p>
          </div>
          {hosted.data !== undefined && (
            <StatusBadge tone={hosted.data ? "brand" : "neutral"} dot={false}>
              {hosted.data
                ? `Alojado · ${hosted.data.slots.length} ${hosted.data.slots.length === 1 ? "instancia" : "instancias"}`
                : "Remoto"}
            </StatusBadge>
          )}
          <StatusBadge tone={agent.profile_version ? "brand" : "neutral"} dot={false}>
            {agent.profile_version ? `Config v${agent.profile_version}` : "Sin configuración"}
          </StatusBadge>
          {can(Perm.AgentsWrite) && <CloneDialog agent={agent} />}
        </div>
        <Tabs value={tab} onValueChange={setTab} className="mt-4">
          <TabsList variant="line" className="-mb-px">
            <TabsTrigger value="resumen">Resumen</TabsTrigger>
            <TabsTrigger value="configuracion">Configuración</TabsTrigger>
            <TabsTrigger value="secretos">Secretos</TabsTrigger>
            <TabsTrigger value="donde">Dónde corre</TabsTrigger>
            {browserTab && <TabsTrigger value="navegador">Navegador</TabsTrigger>}
          </TabsList>
        </Tabs>
      </div>
      <Tabs value={tab} onValueChange={setTab}>
        <div className="mx-auto max-w-4xl p-6">
          <TabsContent value="resumen" className="space-y-6">
            <Connections agent={agent} conns={conns} outdated={outdated} hosted={!!hosted.data} />
            <UsageCard agent={agent} />
            {can(Perm.VaultsRead) && <VaultsCard kind="agent" id={agent.id} />}
          </TabsContent>
          <TabsContent value="configuracion">
            <ConfigEditor agent={agent} write={can(Perm.AgentsWrite)} hosted={!!hosted.data} />
          </TabsContent>
          <TabsContent value="secretos">
            <SecretsCard agent={agent} write={can(Perm.AgentsWrite)} />
          </TabsContent>
          <TabsContent value="donde">
            <WhereTab agent={agent} profile={p} onConfigure={() => setTab("configuracion")} />
          </TabsContent>
          {browserTab && (
            <TabsContent value="navegador">
              <BrowserCard agent={agent} profile={p} />
            </TabsContent>
          )}
        </div>
      </Tabs>
    </div>
  )
}

/** Who works as the agent right now: its connections (slots or machines). */
function Connections({
  agent,
  conns,
  outdated,
  hosted,
}: {
  agent: LibraryAgent
  conns: Agent[] | undefined
  outdated: number
  hosted: boolean
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Plug className="size-4" /> Conexiones
          {conns !== undefined && conns.length > 0 && outdated > 0 && (
            <StatusBadge tone="warning" title="Reinician el agente con la configuración nueva al quedar libres">
              {outdated === 1 ? "1 desactualizada" : `${outdated} desactualizadas`}
            </StatusBadge>
          )}
        </CardTitle>
        <CardDescription>
          {hosted
            ? "Sus instancias se conectan solas cuando le llegan mensajes."
            : "Quien corre seaport connect con uno de sus tokens."}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {conns === undefined ? (
          <Skeleton className="h-12 rounded-lg" />
        ) : conns.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Sin conexión: los mensajes para @{agent.name} esperan
            {hosted ? " a que arranque una instancia." : " a que alguien lo conecte."}
          </p>
        ) : (
          <ConnectionCards conns={conns} />
        )}
      </CardContent>
    </Card>
  )
}

/** Each connection with its capacity and the conversations it's working. */
function ConnectionCards({ conns }: { conns: Agent[] }) {
  const now = useNow(1000)
  const inbox = useInbox({})
  const titles = new Map(flatten(inbox.data).map((c) => [c.id, conversationName(c)]))
  return (
    <div className="grid gap-3" aria-label="Conexiones" role="list">
      {conns.map((c) => (
        <div key={c.session} role="listitem">
          <AgentCard agent={c} now={now} titleOf={(id) => titles.get(id)} />
        </div>
      ))}
    </div>
  )
}

/** The config as `seaport agents config show` prints it, to copy. */
function YamlDialog({ agent, config }: { agent: LibraryAgent; config: ProfileInput }) {
  const yaml = configYaml(config)
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <FileCode2 /> Ver como YAML
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{agent.name}.yaml</DialogTitle>
          <DialogDescription>
            Lo mismo que <Mono>seaport agents config show {agent.name}</Mono>; aplícalo con{" "}
            <Mono>seaport agents config apply {agent.name} -f</Mono>. Los secretos van por nombre, nunca su valor.
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

/** A new agent with this one's config (and, if asked, its secrets). */
export function CloneDialog({ agent, trigger }: { agent: LibraryAgent; trigger?: React.ReactNode }) {
  const clone = useCloneAgent()
  const navigate = useNavigate()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [secrets, setSecrets] = React.useState(true)
  const ok = agentNamePattern.test(name)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setName(`${agent.name}-copia`.slice(0, 32))
      }}
    >
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm">
            <Copy /> Clonar
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            clone.mutate(
              { id: agent.id, name, secrets },
              {
                onSuccess: (a) => {
                  setOpen(false)
                  navigate(`/agents/${a.id}`)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Clonar @{agent.name}</DialogTitle>
            <DialogDescription>
              Un agente nuevo con su configuración actual. No hereda muelles, tokens ni alojamiento.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!name && !ok}>
              <FieldLabel htmlFor="clone-name">Nombre</FieldLabel>
              <Input
                id="clone-name"
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                className="font-mono"
                autoFocus
              />
              <FieldDescription>Su @mención: minúsculas, dígitos, - y _.</FieldDescription>
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="clone-secrets">Copiar sus secretos</FieldLabel>
                <FieldDescription>Los valores se copian cifrados; nadie los ve.</FieldDescription>
              </FieldContent>
              <Switch id="clone-secrets" checked={secrets} onCheckedChange={setSecrets} />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!ok || clone.isPending}>
              {clone.isPending && <Spinner />} Clonar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Edits the agent's config (profile + harness + machine); saving makes a
 * new version. Past versions open read-only and can be restored (saved
 * again as a new version).
 */
function ConfigEditor({ agent, write, hosted }: { agent: LibraryAgent; write: boolean; hosted: boolean }) {
  const [viewing, setViewing] = React.useState(0)
  const current = useProfile(agent.id)
  const past = useProfile(agent.id, viewing)
  const versions = useProfileVersions(agent.id)
  const save = useSaveProfile(agent.id)
  const secretList = useSecrets(agent.id)
  const [draft, setDraft] = React.useState<ProfileInput>(empty)
  // base: the version the draft started from; baseline: its content.
  const [base, setBase] = React.useState(-1)
  const [baseline, setBaseline] = React.useState<ProfileInput>(empty)
  // Claude's "Clave de API" chosen with no secret yet (ModelAccess).
  const [pendingKey, setPendingKey] = React.useState(false)
  // Bumped by a load or a discard: the form's local choices start over.
  const [round, setRound] = React.useState(0)
  const loaded = current.data
  const dirty = pendingKey || JSON.stringify(draft) !== JSON.stringify(baseline)
  const load = (p: AgentProfile) => {
    setDraft(inputOf(p))
    setBaseline(inputOf(p))
    setBase(p.version)
    setRound((r) => r + 1)
  }

  // Load the current config, and again whenever a new version lands
  // (ours or someone else's) while there's nothing unsaved (state
  // adjusted during render: React re-renders before painting).
  if (loaded && (base < 0 || (!dirty && loaded.version !== base))) load(loaded)

  if (current.isError) return <ErrorState error={current.error} onRetry={() => current.refetch()} />
  if (!loaded || base < 0) return <Skeleton className="h-96 rounded-xl" />

  const readOnly = !write || viewing > 0
  const shown = viewing > 0 ? (past.data ? inputOf(past.data) : undefined) : draft
  const stale = loaded.version !== base
  const conflict = save.error instanceof ApiError && save.error.status === 409
  const set = (patch: Partial<ProfileInput>) => setDraft((d) => ({ ...d, ...patch }))
  const setMachine = (patch: Partial<Machine>) =>
    setDraft((d) => ({ ...d, machine: { ...(d.machine ?? newMachine()), ...patch } }))
  const problem = pendingKey
    ? "Elige el secreto con la clave de Anthropic."
    : draft.machine
      ? machineProblem(draft.harness, draft.machine)
      : hosted
        ? "Un agente alojado necesita su máquina."
        : ""
  const secrets: SecretsCtx = {
    agentId: agent.id,
    agentName: agent.name,
    names: (secretList.data?.secrets ?? []).map((s) => s.name),
    canCreate: write,
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <ScrollText className="size-4" /> Configuración
            </CardTitle>
            <CardDescription>
              Todo el agente en un documento: lo que se le dice, sus herramientas y, si el puerto lo aloja, su máquina.
              Cada cambio es una versión nueva (también con <Mono>seaport agents config</Mono>); sus conexiones lo toman
              al quedar libres.
            </CardDescription>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {shown && <YamlDialog agent={agent} config={shown} />}
            {(versions.data?.versions.length ?? 0) > 0 && (
              <Select value={String(viewing)} onValueChange={(v) => setViewing(Number(v))}>
                <SelectTrigger size="sm" className="w-56" aria-label="Versión">
                  <History className="size-3.5" />
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="0">{write ? "Actual (editable)" : `Actual (v${loaded.version})`}</SelectItem>
                  {versions.data?.versions
                    .filter((v) => v.version !== loaded.version)
                    .map((v) => (
                      <SelectItem key={v.version} value={String(v.version)}>
                        v{v.version} · {v.created_by || "—"} · {formatFull(v.created_at)}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            )}
          </div>
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
                  setDraft(inputOf(past.data))
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
          <>
            <HarnessSection
              key={round}
              agent={agent}
              profile={shown}
              hosted={hosted}
              readOnly={readOnly}
              set={set}
              secrets={secrets}
              onPending={setPendingKey}
            />
            <ProfileFields
              profile={shown}
              docks={agent.docks.map((d) => d.dock_id)}
              readOnly={readOnly}
              set={set}
            />
            {shown.machine && shown.harness && (
              <MachineSections
                harness={shown.harness}
                machine={shown.machine}
                set={setMachine}
                secrets={secrets}
                readOnly={readOnly}
                homeCompare={
                  viewing > 0
                    ? { home: loaded.machine?.home ?? "", label: `la v${loaded.version} actual` }
                    : { home: baseline.machine?.home ?? "", label: `la v${base} guardada` }
                }
              />
            )}
          </>
        )}
        {write && viewing === 0 && (
          <div className="sticky bottom-0 -mx-6 -mb-6 flex flex-wrap items-center justify-end gap-3 rounded-b-xl border-t bg-card px-6 py-4">
            {(stale || conflict) && (
              <span className="mr-auto text-sm text-warning">
                Alguien guardó la v{loaded.version} mientras editabas.{" "}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    load(loaded)
                    save.reset()
                  }}
                >
                  Descartar mis cambios y cargarla
                </button>
              </span>
            )}
            {dirty && !stale && problem && <span className="mr-auto text-sm text-destructive">{problem}</span>}
            {dirty && !stale && (
              <Button
                variant="ghost"
                onClick={() => {
                  setDraft(baseline)
                  setRound((r) => r + 1)
                }}
              >
                Descartar
              </Button>
            )}
            <Button
              disabled={!dirty || stale || !!problem || save.isPending}
              onClick={() => save.mutate({ base, profile: outOf(draft) }, { onSuccess: load })}
            >
              {save.isPending ? <Spinner /> : <Save />} Guardar v{base + 1}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

const harnessOptions: ChoiceOption<Harness | "none">[] = [
  { value: "rness", label: "rness", description: "Cualquier proveedor de modelos" },
  { value: "claude-code", label: "Claude Code", description: "Modelos de Anthropic" },
  { value: "none", label: "Lo elige quien conecta", description: "Solo para agentes remotos" },
]

/**
 * The harness and, for an agent the port runs (or will), whether it has a
 * machine and how it reaches its models.
 */
function HarnessSection({
  agent,
  profile: p,
  hosted,
  readOnly,
  set,
  secrets,
  onPending,
}: {
  agent: LibraryAgent
  profile: ProfileInput
  hosted: boolean
  readOnly: boolean
  set: (patch: Partial<ProfileInput>) => void
  secrets: SecretsCtx
  onPending: (pending: boolean) => void
}) {
  const options = hosted ? harnessOptions.filter((o) => o.value !== "none") : harnessOptions
  return (
    <div className="space-y-6">
      <ConfigSection
        icon={Cpu}
        title="Harness"
        yaml="harness"
        description={
          hosted
            ? "Lo que corre en sus instancias."
            : "Lo que debe correr quien lo conecta; lo necesita para que el puerto pueda alojarlo."
        }
      >
        <SegmentedChoice
          aria-label="Harness"
          value={p.harness || "none"}
          onChange={(v) => {
            const harness = v === "none" ? "" : v
            set({ harness, ...(harness === "" ? { machine: null } : {}) })
          }}
          options={options}
          disabled={readOnly}
          size="sm"
        />
      </ConfigSection>
      {p.harness && (
        <ConfigSection
          icon={Server}
          title="Máquina"
          yaml="machine"
          description={
            p.machine
              ? `Con lo que el puerto construye la imagen de sus instancias de ${harnessLabel[p.harness]}.`
              : "Sin máquina el puerto no puede alojarlo: corre donde alguien lo conecta."
          }
          badge={
            !hosted &&
            !readOnly && (
              <Switch
                aria-label="Máquina"
                checked={!!p.machine}
                onCheckedChange={(on) => set({ machine: on ? newMachine() : null })}
                className="ml-auto"
              />
            )
          }
        />
      )}
      {p.harness && p.machine && (
        <ConfigSection
          id="acceso-ia"
          icon={KeyRound}
          title="Acceso a la IA"
          yaml="machine.models"
          description="Cómo sus instancias llegan a los modelos: con la cuenta de Claude de cada una, con claves de API, o con ambas."
        >
          <ModelAccess
            harness={p.harness}
            models={p.machine.models ?? {}}
            onChange={(models) => set({ machine: { ...(p.machine ?? newMachine()), models } })}
            secrets={secrets}
            readOnly={readOnly}
            accounts={<SlotAccounts agent={agent} />}
            onPending={onPending}
          />
        </ConfigSection>
      )}
    </div>
  )
}

/** hostsOf parses a comma-separated host list: lowercase, trimmed, unique. */
export function hostsOf(text: string): string[] {
  const out: string[] = []
  for (const raw of text.split(/[,\s]+/)) {
    const h = raw.trim().toLowerCase()
    if (h && !out.includes(h)) out.push(h)
  }
  return out
}

function ProfileFields({
  profile: p,
  docks,
  readOnly,
  set,
}: {
  profile: ProfileInput
  /** The agent's docks: where browser_assist's destinations come from. */
  docks: string[]
  readOnly: boolean
  set: (patch: Partial<ProfileInput>) => void
}) {
  // The ignore list is typed as text and parsed on blur (a half-typed host
  // shouldn't be refused while typing).
  const ignoreKey = p.browser_ignore_hosts.join(",")
  const [ignore, setIgnore] = React.useState({ key: ignoreKey, text: p.browser_ignore_hosts.join(", ") })
  // The list changed under us (saved, discarded, reloaded): show it.
  if (ignore.key !== ignoreKey) setIgnore({ key: ignoreKey, text: p.browser_ignore_hosts.join(", ") })
  const ignoreText = ignore.text
  const setIgnoreText = (text: string) => setIgnore({ key: ignoreKey, text })
  return (
    <FieldGroup>
      <Field>
        <FieldLabel htmlFor="profile-instructions">Instrucciones</FieldLabel>
        <Textarea
          id="profile-instructions"
          value={p.instructions}
          readOnly={readOnly}
          onChange={(e) => set({ instructions: e.target.value })}
          placeholder="Eres el PM del equipo. Convierte lo que te cuentan en tickets claros…"
          className="min-h-48 font-mono text-sm"
          maxLength={32768}
        />
        <FieldDescription>
          Van como system prompt si el harness lo acepta (Claude Code); si no, con el primer mensaje de cada sesión.
        </FieldDescription>
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor="profile-model">Modelo</FieldLabel>
          <Input
            id="profile-model"
            value={p.model}
            readOnly={readOnly}
            onChange={(e) => set({ model: e.target.value.trim() })}
            placeholder="el del harness"
            className="font-mono"
            maxLength={100}
          />
          <FieldDescription>Entre las opciones de sesión del harness (ACP), si lo ofrece.</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="profile-effort">Esfuerzo de razonamiento</FieldLabel>
          <Input
            id="profile-effort"
            value={p.effort}
            readOnly={readOnly}
            onChange={(e) => set({ effort: e.target.value.trim() })}
            placeholder="el del harness"
            className="font-mono"
            maxLength={100}
          />
          <FieldDescription>Por ejemplo low, medium o high.</FieldDescription>
        </Field>
      </div>

      <Field>
        <FieldLabel>
          <ShieldCheck className="size-4" /> Permisos del harness
        </FieldLabel>
        <SegmentedChoice
          aria-label="Permisos"
          value={p.approve === "" ? "connector" : p.approve}
          onChange={(v) => set({ approve: (v === "connector" ? "" : v) as Approve })}
          options={approveOptions}
          disabled={readOnly}
          size="sm"
        />
        <FieldDescription>
          Cómo responde el conector cuando el agente pide permiso para una herramienta. El flag -approve del conector
          manda sobre esto.
        </FieldDescription>
      </Field>

      <Field orientation="horizontal">
        <FieldContent>
          <FieldLabel htmlFor="profile-browser">
            <AppWindow className="size-4" /> Navegador
          </FieldLabel>
          <FieldDescription>
            El agente usa el navegador de su máquina (herramientas MCP de Chrome DevTools y la herramienta{" "}
            <Mono>ask_takeover</Mono>). Las sesiones viven en la instancia; cuando un sitio pide algo que solo una persona
            puede hacer (iniciar sesión, un captcha, un código), ella toma el control desde la pestaña Navegador. Solo
            surte efecto en agentes alojados.
          </FieldDescription>
        </FieldContent>
        <Switch
          id="profile-browser"
          checked={p.browser}
          disabled={readOnly}
          onCheckedChange={(browser) =>
            set({
              browser,
              // Turning the browser on turns detection on too (the usual
              // case), and the machine's engine (the port refuses a hosted
              // agent's browser without one); off clears what needs it.
              ...(browser
                ? {
                    browser_detect: true,
                    ...(p.machine && !p.machine.browser?.engine
                      ? { machine: { ...p.machine, browser: { engine: "chromium" as const } } }
                      : {}),
                  }
                : { browser_assist: "", browser_detect: false, browser_ignore_hosts: [] }),
            })
          }
        />
      </Field>

      {p.browser && (
        <Field orientation="horizontal">
          <FieldContent>
            <FieldLabel htmlFor="profile-browser-detect">Detectar páginas que piden a una persona</FieldLabel>
            <FieldDescription>
              Seaport mira las páginas que el navegador carga y, cuando una pide iniciar sesión, un captcha o un
              código de verificación, registra el takeover y avisa por sí mismo, sin esperar a que el agente lo pida.
              El agente recibe una nota para que no intente pasarla.
            </FieldDescription>
          </FieldContent>
          <Switch
            id="profile-browser-detect"
            checked={p.browser_detect}
            disabled={readOnly}
            onCheckedChange={(browser_detect) => set({ browser_detect })}
          />
        </Field>
      )}

      {p.browser && p.browser_detect && (
        <Field>
          <FieldLabel htmlFor="profile-browser-ignore">Sitios que no se detectan</FieldLabel>
          <Input
            id="profile-browser-ignore"
            value={ignoreText}
            readOnly={readOnly}
            onChange={(e) => setIgnoreText(e.target.value)}
            onBlur={() => set({ browser_ignore_hosts: hostsOf(ignoreText) })}
            placeholder="intranet.example.com, otro.example.com"
            className="font-mono"
          />
          <FieldDescription>
            Sitios (y sus subdominios) donde el detector nunca pide un takeover, separados por comas: una página con
            campo de contraseña que el agente no necesita pasar. Como mucho 32.
          </FieldDescription>
        </Field>
      )}

      {p.browser && (
        <Field>
          <FieldLabel htmlFor="profile-browser-assist">Avisar a una persona</FieldLabel>
          <AssistSelect value={p.browser_assist} docks={docks} readOnly={readOnly} onChange={(browser_assist) => set({ browser_assist })} />
          <FieldDescription>
            Cuando el agente pide que alguien tome el control (<Mono>ask_takeover</Mono>) o el detector encuentra una
            página que lo necesita, Seaport lo anuncia con un enlace a su navegador: a nadie (el aviso queda solo en
            esta página), en la conversación que el agente trabaja, o en un destino de uno de sus muelles, donde abre
            una conversación. Al pulsar Listo, el agente recibe un mensaje de sistema en el mismo sitio y sigue.
          </FieldDescription>
        </Field>
      )}

      <SkillsField skills={p.skills} readOnly={readOnly} onChange={(skills) => set({ skills })} />
      <ServersField servers={p.mcp_servers} readOnly={readOnly} onChange={(mcp_servers) => set({ mcp_servers })} />
    </FieldGroup>
  )
}

/**
 * Where takeover notices go (browser_assist): nobody, the conversation, or
 * a destination of the agent's docks. A saved name no dock has any more
 * stays listed (marked) so the select never hides what the profile says.
 */
function AssistSelect({
  value,
  docks,
  readOnly,
  onChange,
}: {
  value: string
  docks: string[]
  readOnly: boolean
  onChange: (v: string) => void
}) {
  const names = useDocksDestinations(docks)
  const none = "__none"
  const conversation = "conversation"
  const listed = names ?? []
  // A saved name not in the lists (still loading, or no dock has it now).
  const extra = value !== "" && value !== conversation && !listed.includes(value)
  return (
    <Select value={value || none} onValueChange={(v) => onChange(v === none ? "" : v)} disabled={readOnly}>
      <SelectTrigger id="profile-browser-assist" className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={none}>Nadie</SelectItem>
        <SelectItem value={conversation}>La conversación que trabaja</SelectItem>
        {listed.map((n) => (
          <SelectItem key={n} value={n}>
            Destino <span className="font-mono">{n}</span>
          </SelectItem>
        ))}
        {extra && (
          <SelectItem value={value}>
            Destino <span className="font-mono">{value}</span>
            {names !== undefined && " (no está en sus muelles)"}
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  )
}

function SkillsField({
  skills,
  readOnly,
  onChange,
}: {
  skills: Skill[]
  readOnly: boolean
  onChange: (s: Skill[]) => void
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="flex items-center gap-2 text-sm font-medium">
            <Sparkles className="size-4" /> Skills
          </h3>
          <p className="text-sm text-muted-foreground">
            El agente ve su nombre y cuándo usarlas, y carga las instrucciones con la herramienta <Mono>skill</Mono> de
            Seaport cuando las necesita. Funciona en cualquier harness con MCP.
          </p>
        </div>
        {!readOnly && <SkillDialog taken={skills.map((s) => s.name)} onSave={(s) => onChange([...skills, s])} />}
      </div>
      {skills.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">Sin skills.</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {skills.map((s, i) => (
            <li key={s.name} className="flex items-center gap-3 px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="font-mono text-sm">{s.name}</div>
                <div className="truncate text-xs text-muted-foreground">{s.description}</div>
              </div>
              <SkillDialog
                skill={s}
                readOnly={readOnly}
                taken={skills.filter((_, j) => j !== i).map((x) => x.name)}
                onSave={(n) => onChange(skills.map((x, j) => (j === i ? n : x)))}
              />
              {!readOnly && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Quitar ${s.name}`}
                  onClick={() => onChange(skills.filter((_, j) => j !== i))}
                >
                  <X />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Adds or edits a skill: paste a SKILL.md or fill the fields. */
function SkillDialog({
  skill,
  taken,
  readOnly,
  onSave,
}: {
  skill?: Skill
  taken: string[]
  readOnly?: boolean
  onSave: (s: Skill) => void
}) {
  const [open, setOpen] = React.useState(false)
  const [s, setS] = React.useState<Skill>(skill ?? { name: "", description: "", body: "" })
  const nameOk = skillNamePattern.test(s.name) && !taken.includes(s.name)
  const ok = nameOk && s.description.trim() !== "" && (s.body ?? "").trim() !== ""
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setS(skill ?? { name: "", description: "", body: "" })
      }}
    >
      <DialogTrigger asChild>
        {skill ? (
          <Button variant="ghost" size="icon-sm" aria-label={readOnly ? `Ver ${skill.name}` : `Editar ${skill.name}`}>
            <Pencil />
          </Button>
        ) : (
          <Button variant="outline" size="sm">
            <Plus /> Skill
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (readOnly) return setOpen(false)
            onSave({ name: s.name, description: s.description.trim(), body: (s.body ?? "").trim() })
            setOpen(false)
          }}
        >
          <DialogHeader>
            <DialogTitle>{skill ? skill.name : "Nueva skill"}</DialogTitle>
            <DialogDescription>
              Pega un SKILL.md completo en las instrucciones: su front matter (name, description) llena los campos.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <div className="grid gap-4 sm:grid-cols-[12rem_1fr]">
              <Field data-invalid={!!s.name && !nameOk}>
                <FieldLabel htmlFor="skill-name">Nombre</FieldLabel>
                <Input
                  id="skill-name"
                  value={s.name}
                  readOnly={readOnly}
                  onChange={(e) => setS({ ...s, name: e.target.value.trim().toLowerCase() })}
                  className="font-mono"
                  placeholder="bug-triage"
                  autoComplete="off"
                />
                {!!s.name && !nameOk && (
                  <FieldDescription>
                    {taken.includes(s.name) ? "Ya hay una skill con ese nombre." : "Minúsculas, dígitos, - o _."}
                  </FieldDescription>
                )}
              </Field>
              <Field>
                <FieldLabel htmlFor="skill-description">Cuándo usarla</FieldLabel>
                <Input
                  id="skill-description"
                  value={s.description}
                  readOnly={readOnly}
                  onChange={(e) => setS({ ...s, description: e.target.value })}
                  placeholder="Cuando un cliente reporta un bug"
                  maxLength={1024}
                />
              </Field>
            </div>
            <Field>
              <FieldLabel htmlFor="skill-body">Instrucciones</FieldLabel>
              <Textarea
                id="skill-body"
                value={s.body}
                readOnly={readOnly}
                onChange={(e) => setS(fromSkillMd({ ...s, body: e.target.value }))}
                className="min-h-64 font-mono text-sm"
                maxLength={65536}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              {readOnly ? "Cerrar" : "Cancelar"}
            </Button>
            {!readOnly && (
              <Button type="submit" disabled={!ok}>
                {skill ? "Aplicar" : "Agregar"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** A pasted SKILL.md: its front matter fills name and description. */
function fromSkillMd(s: Skill): Skill {
  const m = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/.exec(s.body ?? "")
  if (!m) return s
  const out = { ...s, body: m[2].replace(/^\s+/, "") }
  for (const line of m[1].split(/\r?\n/)) {
    const [k, ...rest] = line.split(":")
    const v = rest.join(":").trim().replace(/^["']|["']$/g, "")
    if (k.trim() === "name" && v) out.name = v.toLowerCase()
    if (k.trim() === "description" && v) out.description = v
  }
  return out
}

function ServersField({
  servers,
  readOnly,
  onChange,
}: {
  servers: McpServer[]
  readOnly: boolean
  onChange: (s: McpServer[]) => void
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="flex items-center gap-2 text-sm font-medium">
            <Plug className="size-4" /> Servidores MCP
          </h3>
          <p className="text-sm text-muted-foreground">
            Se suman a cada sesión junto al de Seaport (HTTP). En los headers, <Mono>{"${NOMBRE}"}</Mono> usa un secreto
            del agente.
          </p>
        </div>
        {!readOnly && <ServerDialog taken={servers.map((s) => s.name)} onSave={(s) => onChange([...servers, s])} />}
      </div>
      {servers.length === 0 ? (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
          Sin servidores MCP propios.
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {servers.map((s, i) => (
            <li key={s.name} className="flex items-center gap-3 px-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="font-mono text-sm">{s.name}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {s.url}
                  {s.headers.length > 0 && ` · ${s.headers.map((h) => h.name).join(", ")}`}
                </div>
              </div>
              <ServerDialog
                server={s}
                readOnly={readOnly}
                taken={servers.filter((_, j) => j !== i).map((x) => x.name)}
                onSave={(n) => onChange(servers.map((x, j) => (j === i ? n : x)))}
              />
              {!readOnly && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Quitar ${s.name}`}
                  onClick={() => onChange(servers.filter((_, j) => j !== i))}
                >
                  <X />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ServerDialog({
  server,
  taken,
  readOnly,
  onSave,
}: {
  server?: McpServer
  taken: string[]
  readOnly?: boolean
  onSave: (s: McpServer) => void
}) {
  const blank: McpServer = { name: "", url: "", headers: [] }
  const [open, setOpen] = React.useState(false)
  const [s, setS] = React.useState<McpServer>(server ?? blank)
  const nameOk = serverNamePattern.test(s.name) && s.name !== "seaport" && !taken.includes(s.name)
  const urlOk = /^https?:\/\/[^\s/@]+/.test(s.url)
  const headersOk = s.headers.every((h) => /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,100}$/.test(h.name))
  const setHeader = (i: number, patch: Partial<McpServer["headers"][number]>) =>
    setS({ ...s, headers: s.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) })
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setS(server ?? blank)
      }}
    >
      <DialogTrigger asChild>
        {server ? (
          <Button variant="ghost" size="icon-sm" aria-label={readOnly ? `Ver ${server.name}` : `Editar ${server.name}`}>
            <Pencil />
          </Button>
        ) : (
          <Button variant="outline" size="sm">
            <Plus /> Servidor
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (readOnly) return setOpen(false)
            onSave({ ...s, url: s.url.trim(), headers: s.headers.filter((h) => h.name) })
            setOpen(false)
          }}
        >
          <DialogHeader>
            <DialogTitle>{server ? server.name : "Nuevo servidor MCP"}</DialogTitle>
            <DialogDescription>Un servidor MCP por HTTP (streamable). El agente lo usa con estos headers.</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!s.name && !nameOk}>
              <FieldLabel htmlFor="server-name">Nombre</FieldLabel>
              <Input
                id="server-name"
                value={s.name}
                readOnly={readOnly}
                onChange={(e) => setS({ ...s, name: e.target.value.trim().toLowerCase() })}
                className="font-mono"
                placeholder="linear"
                autoComplete="off"
              />
              {!!s.name && !nameOk && (
                <FieldDescription>
                  {taken.includes(s.name) || s.name === "seaport"
                    ? "Ese nombre ya está en uso."
                    : "Hasta 32 minúsculas, dígitos, - o _."}
                </FieldDescription>
              )}
            </Field>
            <Field data-invalid={!!s.url && !urlOk}>
              <FieldLabel htmlFor="server-url">URL</FieldLabel>
              <Input
                id="server-url"
                value={s.url}
                readOnly={readOnly}
                onChange={(e) => setS({ ...s, url: e.target.value })}
                className="font-mono"
                placeholder="https://mcp.example.com/mcp"
              />
            </Field>
            <Field>
              <FieldLabel>Headers</FieldLabel>
              <div className="space-y-2">
                {s.headers.map((h, i) => (
                  <div key={i} className="flex gap-2">
                    <Input
                      aria-label="Header"
                      value={h.name}
                      readOnly={readOnly}
                      onChange={(e) => setHeader(i, { name: e.target.value.trim() })}
                      className="w-40 font-mono"
                      placeholder="Authorization"
                    />
                    <Input
                      aria-label="Valor"
                      value={h.value}
                      readOnly={readOnly}
                      onChange={(e) => setHeader(i, { value: e.target.value })}
                      className="flex-1 font-mono"
                      placeholder="Bearer ${LINEAR_TOKEN}"
                    />
                    {!readOnly && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label="Quitar header"
                        onClick={() => setS({ ...s, headers: s.headers.filter((_, j) => j !== i) })}
                      >
                        <X />
                      </Button>
                    )}
                  </div>
                ))}
                {!readOnly && s.headers.length < 20 && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setS({ ...s, headers: [...s.headers, { name: "", value: "" }] })}
                  >
                    <Plus /> Header
                  </Button>
                )}
              </div>
              <FieldDescription>
                No pegues tokens aquí: crea un secreto y úsalo como <Mono>{"${NOMBRE}"}</Mono>. El perfil es visible para
                quien puede ver agentes; los secretos no.
              </FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              {readOnly ? "Cerrar" : "Cancelar"}
            </Button>
            {!readOnly && (
              <Button type="submit" disabled={!nameOk || !urlOk || !headersOk}>
                {server ? "Aplicar" : "Agregar"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The agent's secrets: names only; values are written, never read back. */
function SecretsCard({ agent, write }: { agent: LibraryAgent; write: boolean }) {
  const secrets = useSecrets(agent.id)
  const profile = useProfile(agent.id)
  const remove = useDeleteSecret(agent.id)
  const used = configSecrets(profile.data ? inputOf(profile.data) : empty)
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4" /> Secretos
            </CardTitle>
            <CardDescription>
              Las credenciales del agente: claves de modelo, tokens de repos y de headers MCP (
              <Mono>{"${NOMBRE}"}</Mono>) y lo que su harness necesite como variable. Se guardan cifrados y solo salen
              hacia sus conexiones (las claves de modelo, ni eso: las usa el gateway del puerto).
            </CardDescription>
          </div>
          {write && <SecretDialog agentId={agent.id} agentName={agent.name} trigger="default" />}
        </div>
      </CardHeader>
      <CardContent>
        {secrets.isPending ? (
          <Skeleton className="h-16 rounded-lg" />
        ) : secrets.isError ? (
          <ErrorState error={secrets.error} onRetry={() => secrets.refetch()} />
        ) : secrets.data.secrets.length === 0 ? (
          <EmptyState icon={KeyRound} title="Sin secretos" description="Por ejemplo una clave de modelo o el token de un repo." />
        ) : (
          <ul className="divide-y rounded-lg border" aria-label="Secretos">
            {secrets.data.secrets.map((s) => {
              const uses = used.get(s.name) ?? []
              return (
                <li key={s.name} className="flex flex-wrap items-center gap-3 px-3 py-2">
                  <span className="font-mono text-sm">{s.name}</span>
                  {s.env && (
                    <StatusBadge dot={false} title="El harness lo recibe como variable de entorno">
                      env
                    </StatusBadge>
                  )}
                  {uses.length > 0 && (
                    <StatusBadge tone="brand" dot={false} title={`Lo usa la configuración actual: ${uses.join(", ")}`}>
                      {uses.length === 1 ? uses[0] : `en uso (${uses.length})`}
                    </StatusBadge>
                  )}
                  <span className="ml-auto text-xs text-muted-foreground">actualizado {formatFull(s.updated_at)}</span>
                  {write && (
                    <>
                      <SecretDialog agentId={agent.id} agentName={agent.name} name={s.name} env={s.env} trigger="edit" />
                      <ConfirmAction
                        trigger={
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Eliminar ${s.name}`}
                            disabled={uses.length > 0}
                            title={uses.length > 0 ? "Quítalo antes de la configuración" : undefined}
                          >
                            <Trash2 />
                          </Button>
                        }
                        title={`¿Eliminar ${s.name}?`}
                        description={<p>Las conexiones del agente reinician sin él al quedar libres.</p>}
                        destructive
                        actionLabel="Eliminar"
                        onConfirm={() => remove.mutateAsync(s.name)}
                      />
                    </>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
