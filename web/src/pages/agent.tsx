import * as React from "react"
import { Link, useParams } from "react-router"
import {
  ArrowLeft,
  Bot,
  History,
  KeyRound,
  Pencil,
  Plug,
  Plus,
  RotateCcw,
  Save,
  ScrollText,
  ShieldCheck,
  Sparkles,
  Trash2,
  X,
} from "lucide-react"

import { ApiError } from "@/lib/api"
import {
  useAgent,
  useCan,
  useCrew,
  useDeleteSecret,
  useProfile,
  useProfileVersions,
  useSaveProfile,
  useSecrets,
  useSetSecret,
} from "@/lib/hooks"
import { formatFull } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { AgentProfile, Approve, LibraryAgent, McpServer, ProfileInput, Skill } from "@/lib/types"
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
import { Textarea } from "@/components/ui/textarea"
import {
  ConfirmAction,
  EmptyState,
  ErrorState,
  Mono,
  SegmentedChoice,
  StatusBadge,
  type ChoiceOption,
} from "@/components/seaport"
import { HostingCard, UsageCard } from "@/pages/agent-hosting"

/** The agent profile rules (agent.SaveProfile.Validate). */
const skillNamePattern = /^[a-z0-9][a-z0-9_-]{0,63}$/
const serverNamePattern = /^[a-z0-9][a-z0-9_-]{0,31}$/
const secretNamePattern = /^[A-Z_][A-Z0-9_]{0,63}$/
const secretRef = /\$\{([A-Z_][A-Z0-9_]{0,63})\}/g

const approveOptions: ChoiceOption<"connector" | "allow" | "reject">[] = [
  { value: "connector", label: "Lo decide el conector", description: "Su flag -approve (rechaza por defecto)" },
  { value: "reject", label: "Rechazar", tone: "danger" },
  { value: "allow", label: "Permitir", tone: "success" },
]

const empty: ProfileInput = { instructions: "", skills: [], mcp_servers: [], approve: "", model: "", effort: "" }

function inputOf(p: AgentProfile): ProfileInput {
  return {
    instructions: p.instructions,
    skills: p.skills ?? [],
    mcp_servers: (p.mcp_servers ?? []).map((s) => ({ ...s, headers: s.headers ?? [] })),
    approve: p.approve,
    model: p.model,
    effort: p.effort,
  }
}

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

function AgentDetail({ agent }: { agent: LibraryAgent }) {
  const can = useCan()
  const crew = useCrew()
  const conns = crew.data?.agents.filter((c) => c.agent_id === agent.id)
  const outdated = conns?.filter((c) => c.outdated).length ?? 0
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <div className="border-b px-6 py-5">
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
          <StatusBadge tone={agent.profile_version ? "brand" : "neutral"} dot={false}>
            {agent.profile_version ? `Perfil v${agent.profile_version}` : "Sin perfil"}
          </StatusBadge>
          {conns !== undefined &&
            (conns.length === 0 ? (
              <StatusBadge>Sin conexión</StatusBadge>
            ) : outdated > 0 ? (
              <StatusBadge tone="warning" title="Reinician el agente con el perfil nuevo al quedar libres">
                {outdated === 1 ? "1 conexión desactualizada" : `${outdated} conexiones desactualizadas`}
              </StatusBadge>
            ) : (
              <StatusBadge tone="success">
                {conns.length === 1 ? "1 conexión al día" : `${conns.length} conexiones al día`}
              </StatusBadge>
            ))}
        </div>
      </div>
      <div className="mx-auto max-w-4xl space-y-6 p-6">
        <ProfileEditor agent={agent} write={can(Perm.AgentsWrite)} />
        <HostingCard agent={agent} />
        <UsageCard agent={agent} />
        <SecretsCard agent={agent} write={can(Perm.AgentsWrite)} />
      </div>
    </div>
  )
}

/** Edits the current profile; saving makes a new version. Past versions
 * open read-only and can be restored (saved again as a new version). */
function ProfileEditor({ agent, write }: { agent: LibraryAgent; write: boolean }) {
  const [viewing, setViewing] = React.useState(0)
  const current = useProfile(agent.id)
  const past = useProfile(agent.id, viewing)
  const versions = useProfileVersions(agent.id)
  const save = useSaveProfile(agent.id)
  const [draft, setDraft] = React.useState<ProfileInput>(empty)
  // base: the version the draft started from; baseline: its content.
  const [base, setBase] = React.useState(-1)
  const [baseline, setBaseline] = React.useState<ProfileInput>(empty)
  const loaded = current.data
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline)
  const load = (p: AgentProfile) => {
    setDraft(inputOf(p))
    setBaseline(inputOf(p))
    setBase(p.version)
  }

  // Load the current profile, and again whenever a new version lands
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

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <ScrollText className="size-4" /> Perfil
            </CardTitle>
            <CardDescription>
              Lo que <Mono>seaport connect</Mono> le da al agente: instrucciones, skills, servidores MCP y permisos. Cada
              cambio es una versión nueva; sus conexiones reinician el agente con ella al quedar libres.
            </CardDescription>
          </div>
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
          <ProfileFields profile={shown} readOnly={readOnly} set={set} />
        )}
        {write && viewing === 0 && (
          <div className="flex flex-wrap items-center justify-end gap-3 border-t pt-4">
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
            {dirty && !stale && (
              <Button variant="ghost" onClick={() => setDraft(baseline)}>
                Descartar
              </Button>
            )}
            <Button
              disabled={!dirty || stale || save.isPending}
              onClick={() =>
                save.mutate(
                  { base, profile: draft },
                  { onSuccess: load },
                )
              }
            >
              {save.isPending ? <Spinner /> : <Save />} Guardar v{base + 1}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function ProfileFields({
  profile: p,
  readOnly,
  set,
}: {
  profile: ProfileInput
  readOnly: boolean
  set: (patch: Partial<ProfileInput>) => void
}) {
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

      <SkillsField skills={p.skills} readOnly={readOnly} onChange={(skills) => set({ skills })} />
      <ServersField servers={p.mcp_servers} readOnly={readOnly} onChange={(mcp_servers) => set({ mcp_servers })} />
    </FieldGroup>
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
  const ok = nameOk && s.description.trim() !== "" && s.body.trim() !== ""
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
            onSave({ name: s.name, description: s.description.trim(), body: s.body.trim() })
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
  const m = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/.exec(s.body)
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
  const used = new Set<string>()
  for (const s of profile.data?.mcp_servers ?? [])
    for (const h of s.headers ?? []) for (const m of h.value.matchAll(secretRef)) used.add(m[1])
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <KeyRound className="size-4" /> Secretos
            </CardTitle>
            <CardDescription>
              Se guardan cifrados y solo salen hacia las conexiones de este agente: nadie los puede volver a leer aquí.
              Úsalos en headers MCP como <Mono>{"${NOMBRE}"}</Mono> o pásalos al harness como variables de entorno.
            </CardDescription>
          </div>
          {write && <SecretDialog agent={agent} />}
        </div>
      </CardHeader>
      <CardContent>
        {secrets.isPending ? (
          <Skeleton className="h-16 rounded-lg" />
        ) : secrets.isError ? (
          <ErrorState error={secrets.error} onRetry={() => secrets.refetch()} />
        ) : secrets.data.secrets.length === 0 ? (
          <EmptyState icon={KeyRound} title="Sin secretos" description="Por ejemplo el token de un servidor MCP." />
        ) : (
          <ul className="divide-y rounded-lg border">
            {secrets.data.secrets.map((s) => (
              <li key={s.name} className="flex flex-wrap items-center gap-3 px-3 py-2">
                <span className="font-mono text-sm">{s.name}</span>
                {s.env && (
                  <StatusBadge dot={false} title="El harness lo recibe como variable de entorno">
                    env
                  </StatusBadge>
                )}
                {used.has(s.name) && (
                  <StatusBadge tone="brand" dot={false} title="Lo usa un header MCP del perfil actual">
                    en uso
                  </StatusBadge>
                )}
                <span className="ml-auto text-xs text-muted-foreground">actualizado {formatFull(s.updated_at)}</span>
                {write && (
                  <>
                    <SecretDialog agent={agent} name={s.name} env={s.env} />
                    <ConfirmAction
                      trigger={
                        <Button variant="ghost" size="icon-sm" aria-label={`Eliminar ${s.name}`} disabled={used.has(s.name)}>
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
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

/** Sets a secret (new, or a new value for an existing one). */
function SecretDialog({ agent, name: fixed, env: wasEnv }: { agent: LibraryAgent; name?: string; env?: boolean }) {
  const setSecret = useSetSecret(agent.id)
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState(fixed ?? "")
  const [value, setValue] = React.useState("")
  const [env, setEnv] = React.useState(wasEnv ?? false)
  const nameOk = secretNamePattern.test(name) && !name.startsWith("SEAPORT_")
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName(fixed ?? "")
          setEnv(wasEnv ?? false)
        }
        setValue("")
      }}
    >
      <DialogTrigger asChild>
        {fixed ? (
          <Button variant="ghost" size="icon-sm" aria-label={`Cambiar ${fixed}`}>
            <Pencil />
          </Button>
        ) : (
          <Button variant="outline" size="sm">
            <Plus /> Secreto
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setSecret.mutate({ name, value, env }, { onSuccess: () => setOpen(false) })
          }}
        >
          <DialogHeader>
            <DialogTitle>{fixed ? `Cambiar ${fixed}` : "Nuevo secreto"}</DialogTitle>
            <DialogDescription>
              {fixed ? "El valor anterior se reemplaza; no se puede ver." : `Para @${agent.name}. No se puede volver a ver.`}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!name && !nameOk}>
              <FieldLabel htmlFor="secret-name">Nombre</FieldLabel>
              <Input
                id="secret-name"
                value={name}
                readOnly={!!fixed}
                onChange={(e) => setName(e.target.value.trim().toUpperCase())}
                className="font-mono"
                placeholder="LINEAR_TOKEN"
                autoComplete="off"
              />
              <FieldDescription>
                {!!name && !nameOk ? "Mayúsculas, dígitos y _; no puede empezar con SEAPORT_." : `Úsalo como \${${name || "NOMBRE"}}.`}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="secret-value">Valor</FieldLabel>
              <Input
                id="secret-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                className="font-mono"
                autoComplete="new-password"
                maxLength={16384}
              />
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="secret-env">Variable de entorno</FieldLabel>
                <FieldDescription>El harness también lo recibe en su entorno al arrancar.</FieldDescription>
              </FieldContent>
              <Switch id="secret-env" checked={env} onCheckedChange={setEnv} />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!nameOk || !value || setSecret.isPending}>
              {setSecret.isPending && <Spinner />} Guardar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
