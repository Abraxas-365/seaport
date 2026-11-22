import * as React from "react"
import {
  AppWindow,
  Boxes,
  ChevronDown,
  ChevronRight,
  FolderCog,
  GitBranch,
  Globe,
  Hammer,
  HardDrive,
  KeyRound,
  MessagesSquare,
  Pencil,
  Plus,
  Server,
  UserRound,
  X,
} from "lucide-react"

import {
  asSecretRef,
  blankRepo,
  claudeAccess,
  concurrencyAdvice,
  envProblem,
  envText,
  harnessLabel,
  idleStopPattern,
  keyNameFor,
  maxConcurrency,
  modelsProblem,
  parseEnv,
  providers,
  repoName,
  repoURLPattern,
  secretRefPattern,
  subscription,
  type ClaudeAccess,
} from "@/lib/machine"
import type { Harness, Machine, MachineRepo, ResourceSize } from "@/lib/types"
import { cn } from "@/lib/utils"
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
import { Textarea } from "@/components/ui/textarea"
import { ChipInput, ChoiceCards, Mono, SecretPicker, SegmentedChoice, toneClasses, type ChoiceOption } from "@/components/seaport"
import { HomeSection } from "@/pages/agent-home"

const placeholderMuted = "placeholder:text-muted-foreground/50"

/** What the secret pickers need to list and make the agent's secrets. */
export interface SecretsCtx {
  agentId: string
  agentName: string
  /** The agent's secret names. */
  names: string[]
  /** May make new secrets (agents:write). */
  canCreate: boolean
}

/** A titled part of the config form, with its YAML key in sight. */
export function ConfigSection({
  id,
  icon: Icon,
  title,
  yaml,
  description,
  badge,
  children,
  className,
}: {
  id?: string
  icon: React.ComponentType<{ className?: string }>
  title: string
  /** The config's key(s) for this section (`seaport agents config`). */
  yaml?: string
  description: React.ReactNode
  badge?: React.ReactNode
  children?: React.ReactNode
  className?: string
}) {
  return (
    <section id={id} className={cn("scroll-mt-6 space-y-3 border-t pt-5 first:border-t-0 first:pt-0", className)}>
      <div>
        <h3 className="flex flex-wrap items-center gap-2 text-sm font-medium">
          <Icon className="size-4" /> {title}
          {yaml && (
            <Mono className="text-xs font-normal text-muted-foreground" title="Clave en el YAML del agente">
              {yaml}
            </Mono>
          )}
          {badge}
        </h3>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>
      {children}
    </section>
  )
}

const claudeOptions: ChoiceOption<ClaudeAccess>[] = [
  {
    value: "account",
    label: "Cuenta de Claude",
    description: "Suscripción (Pro, Max, Team): cada instancia inicia sesión. Experimental.",
    icon: UserRound,
  },
  {
    value: "key",
    label: "Clave de API",
    description: "Un secreto del agente; solo la usa el gateway del puerto, que mide el uso.",
    icon: KeyRound,
  },
]

/**
 * How a hosted agent reaches Claude, all in one place: a Claude account
 * each instance signs in to (with the instances' accounts), or a key.
 * Other providers' keys are YAML-only for now: the form keeps them and
 * names them, but doesn't edit them.
 */
export function ModelAccess({
  harness,
  models,
  onChange,
  secrets,
  readOnly,
  accounts,
  onPending,
}: {
  harness: Harness | ""
  models: Record<string, string>
  onChange: (models: Record<string, string>) => void
  secrets: SecretsCtx
  readOnly: boolean
  /** The instances' accounts and sign-in (shown with a Claude account). */
  accounts?: React.ReactNode
  /** Whether a key was chosen for Claude but no secret yet (blocks saving). */
  onPending?: (pending: boolean) => void
}) {
  const problem = modelsProblem(harness, models)
  const set = (provider: string, value: string) => {
    const next = { ...models }
    if (value) next[provider] = value
    else delete next[provider]
    onChange(next)
  }
  // "Clave de API" before a secret is picked: without the local choice the
  // port's default (a Claude account on Claude Code) would show instead.
  const [wantKey, setWantKey] = React.useState(false)
  const [seen, setSeen] = React.useState(models.anthropic ?? "")
  if (seen !== (models.anthropic ?? "")) {
    // Picked, discarded or loaded: what the config says wins.
    setSeen(models.anthropic ?? "")
    setWantKey(false)
  }
  const anthropic = providers[0]
  const picking = wantKey && !models.anthropic && !readOnly
  const claude: ClaudeAccess = picking ? "key" : claudeAccess(harness, models)
  React.useEffect(() => {
    onPending?.(picking)
    return () => onPending?.(false)
  }, [picking, onPending])
  // Other providers' keys come from the YAML only: kept as they are, named.
  const keyed = providers.slice(1).filter((p) => models[p.name])
  const choose = (v: ClaudeAccess) => {
    setWantKey(false)
    if (v === "account") return set("anthropic", subscription)
    const was = models.anthropic ?? ""
    if (secretRefPattern.test(was)) return
    const suggested = keyNameFor(anthropic)
    if (secrets.names.includes(suggested)) return set("anthropic", asSecretRef(suggested))
    // No key yet: drop the account and wait for the secret (the change of
    // value is ours, so it doesn't reset the choice).
    setSeen("")
    setWantKey(true)
    if (was) set("anthropic", "")
  }
  return (
    <div className="space-y-3" data-testid="model-access">
      <ChoiceCards
        aria-label="Acceso a Claude"
        value={claude}
        onChange={choose}
        options={claudeOptions}
        disabled={readOnly}
        className="sm:grid-cols-2"
      />
      {claude === "key" && (
        <div className="grid grid-cols-[6.5rem_1fr] items-center gap-2 pt-1">
          <FieldLabel htmlFor={`model-${anthropic.name}`}>Clave</FieldLabel>
          <SecretPicker
            id={`model-${anthropic.name}`}
            label={anthropic.label}
            value={models.anthropic ?? ""}
            onChange={(v) => set("anthropic", v)}
            agentId={secrets.agentId}
            agentName={secrets.agentName}
            secrets={secrets.names}
            canCreate={secrets.canCreate}
            readOnly={readOnly}
            suggested={keyNameFor(anthropic)}
            none="Elige un secreto…"
          />
        </div>
      )}
      {claude === "account" && accounts}
      <FieldDescription>
        {problem ||
          (picking
            ? "Elige el secreto con la clave de Anthropic, o crea uno: hasta entonces no se guarda."
            : accessSummary(claude, keyed.map((p) => p.label)))}
      </FieldDescription>
    </div>
  )
}

/** One line on how the instances reach their models. */
function accessSummary(claude: ClaudeAccess, keyed: string[]): string {
  const yaml = keyed.length ? ` También ${keyed.join(", ")} con clave, desde el YAML.` : ""
  if (claude === "account")
    return `Claude con la cuenta de cada instancia: usa los límites de esa suscripción y Seaport no guarda sus tokens.${yaml}`
  if (claude === "key")
    return `Claude con clave: pasa por el gateway del puerto, nunca entra al contenedor y el uso queda medido por agente.${yaml}`
  return `Sin Claude: ${keyed.join(", ")} con clave, desde el YAML. Elige una opción para usar Claude.`
}

/** Whether a machine differs from the defaults in anything "Avanzado" holds. */
export function advancedTouched(m: Machine): boolean {
  return (
    (m.network?.egress ?? []).some((h) => h.trim()) ||
    Object.keys(m.env ?? {}).length > 0 ||
    (m.resources?.size ?? "") !== "S" ||
    m.workspace !== "persistent" ||
    (m.idle_stop.trim() !== "" && m.idle_stop.trim() !== "30m") ||
    m.image.trim() !== ""
  )
}

const sizeOptions = [
  { value: "S", label: "S", description: "1 CPU · 2 GB" },
  { value: "M", label: "M", description: "2 CPU · 4 GB" },
  { value: "L", label: "L", description: "4 CPU · 8 GB" },
  { value: "custom", label: "A medida" },
]

/**
 * The machine of a hosted agent: what its instances run. Every change here
 * makes another image (or reuses one) and restarts the instances once idle.
 */
export function MachineSections({
  harness,
  machine: m,
  set,
  secrets,
  readOnly,
  homeCompare,
}: {
  harness: Harness
  machine: Machine
  set: (patch: Partial<Machine>) => void
  secrets: SecretsCtx
  readOnly: boolean
  homeCompare: { home?: string; label?: string }
}) {
  const idleOk = idleStopPattern.test(m.idle_stop.trim())
  return (
    <>
      <ConfigSection
        id="config-repos"
        icon={GitBranch}
        title="Repos"
        yaml="machine.repos"
        description={
          <>
            Se clonan en <Mono>/workspace</Mono>; cada conversación trabaja en su propia copia (worktree).
          </>
        }
      >
        <ReposField repos={m.repos ?? []} readOnly={readOnly} onChange={(repos) => set({ repos })} secrets={secrets} />
        {((m.repos ?? []).length > 0 || m.repo_config !== "trust") && (
          <Field>
            <FieldLabel>
              Configuración de los repos <Mono className="text-xs font-normal text-muted-foreground">repo_config</Mono>
            </FieldLabel>
            <SegmentedChoice
              aria-label="Configuración de los repos"
              value={m.repo_config}
              onChange={(repo_config) => set({ repo_config })}
              options={[
                { value: "trust", label: "Confiar", description: "El harness lee AGENTS.md, CLAUDE.md, .claude/, .rness/" },
                { value: "ignore", label: "Ignorar", description: "El harness no lee la configuración de los repos" },
              ]}
              disabled={readOnly}
              size="sm"
            />
          </Field>
        )}
      </ConfigSection>

      <ConfigSection
        icon={Hammer}
        title="Paquetes del sistema"
        yaml="machine.packages · setup"
        description="Lo que se instala en la imagen al construirla."
      >
        <Field>
          <FieldLabel htmlFor="machine-packages">Paquetes (apt)</FieldLabel>
          <ChipInput
            id="machine-packages"
            aria-label="Paquetes (apt)"
            aria-describedby="machine-packages-help"
            value={m.packages ?? []}
            readOnly={readOnly}
            onChange={(packages) => set({ packages })}
            placeholder="nodejs"
          />
          <FieldDescription id="machine-packages-help">Enter añade; nombre o nombre=versión.</FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor="machine-setup">Setup</FieldLabel>
          <Textarea
            id="machine-setup"
            value={m.setup}
            readOnly={readOnly}
            onChange={(e) => set({ setup: e.target.value })}
            placeholder="curl -fsSL https://… | bash"
            className={cn("min-h-24 font-mono text-sm", placeholderMuted)}
            maxLength={16384}
          />
          <FieldDescription>bash como root al construir la imagen. Nada secreto: queda en la imagen.</FieldDescription>
        </Field>
      </ConfigSection>

      <ConfigSection
        icon={FolderCog}
        title="Archivos del harness"
        yaml="machine.home"
        description={
          <>
            Tu <Mono>~/{harness === "rness" ? ".rness" : ".claude"}</Mono> (skills, hooks, ajustes), horneado en la
            imagen. Sin secretos: esos son secretos del agente.
          </>
        }
      >
        <HomeSection
          harness={harness}
          home={m.home}
          compare={homeCompare.home}
          compareLabel={homeCompare.label}
          readOnly={readOnly}
          onChange={(home) => set({ home })}
        />
      </ConfigSection>

      <ConfigSection
        icon={AppWindow}
        title="Motor del navegador"
        yaml="machine.browser"
        description="Un Chromium por instancia, con perfil persistente que comparten el agente y las personas."
      >
        <Field>
          <FieldLabel>Motor</FieldLabel>
          <SegmentedChoice
            aria-label="Motor del navegador"
            value={m.browser?.engine || "none"}
            onChange={(v) => set({ browser: { engine: v === "none" ? "" : "chromium" } })}
            options={[
              { value: "none", label: "Sin navegador" },
              { value: "chromium", label: "Chromium", description: "Dentro de la instancia" },
            ]}
            disabled={readOnly}
            size="sm"
          />
          <FieldDescription>
            El agente lo usa con «Navegador» encendido (arriba); una persona inicia sesión en los sitios desde su pestaña
            Navegador. Añade los hosts de esos sitios en Salida.
          </FieldDescription>
        </Field>
      </ConfigSection>

      <ConfigSection
        icon={MessagesSquare}
        title="Conversaciones a la vez"
        yaml="machine.concurrency"
        description="Cuántos chats atiende cada instancia en paralelo. Los demás esperan en cola, sin perderse."
      >
        <ConcurrencyField
          value={m.concurrency}
          size={m.resources?.size ?? ""}
          browser={!!m.browser?.engine}
          readOnly={readOnly}
          onChange={(concurrency) => set({ concurrency })}
        />
      </ConfigSection>

      <Advanced open={advancedTouched(m)}>
        <ConfigSection
          icon={Globe}
          title="Salida"
          yaml="machine.network.egress"
          description="Con el proxy de salida activo, las instancias solo llegan a estos hosts (más los de sus repos y modelos)."
        >
          <Field>
            <FieldLabel htmlFor="machine-egress">Hosts permitidos</FieldLabel>
            <ChipInput
              id="machine-egress"
              aria-label="Hosts permitidos"
              aria-describedby="machine-egress-help"
              value={m.network?.egress ?? []}
              readOnly={readOnly}
              onChange={(egress) => set({ network: { egress } })}
              placeholder="registry.npmjs.org"
            />
            <FieldDescription id="machine-egress-help">
              Enter añade; *.dominio para sus subdominios; * sola para cualquier web pública (búsquedas, cualquier
              página). Las direcciones privadas siguen bloqueadas.
            </FieldDescription>
          </Field>
        </ConfigSection>

        <ConfigSection
          icon={Boxes}
          title="Variables"
          yaml="machine.env"
          description="Variables de entorno de cada instancia, además de los secretos marcados como variable."
        >
          <EnvVarsField env={m.env ?? {}} readOnly={readOnly} onChange={(env) => set({ env })} />
        </ConfigSection>

        <ConfigSection
          icon={HardDrive}
          title="Recursos"
          yaml="machine.resources · workspace · idle_stop"
          description="Por instancia."
        >
          <Field>
            <FieldLabel>Tamaño</FieldLabel>
            <SegmentedChoice
              aria-label="Tamaño"
              value={m.resources?.size || "custom"}
              onChange={(v) =>
                set({
                  resources:
                    v === "custom"
                      ? { cpus: m.resources?.cpus || 1, memory_mb: m.resources?.memory_mb || 2048 }
                      : { size: v as ResourceSize, cpus: 0, memory_mb: 0 },
                })
              }
              options={sizeOptions}
              disabled={readOnly}
              size="sm"
            />
          </Field>
          {!m.resources?.size && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="machine-cpus">CPUs</FieldLabel>
                <Input
                  id="machine-cpus"
                  type="number"
                  step={0.25}
                  min={0.25}
                  max={64}
                  value={m.resources?.cpus ?? 0}
                  readOnly={readOnly}
                  onChange={(e) => set({ resources: { ...m.resources, cpus: Number(e.target.value) } })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="machine-memory">Memoria (MB)</FieldLabel>
                <Input
                  id="machine-memory"
                  type="number"
                  step={256}
                  min={256}
                  value={m.resources?.memory_mb ?? 0}
                  readOnly={readOnly}
                  onChange={(e) => set({ resources: { ...m.resources, memory_mb: Number(e.target.value) } })}
                />
              </Field>
            </div>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <FieldLabel>Espacio de trabajo</FieldLabel>
              <SegmentedChoice
                aria-label="Espacio de trabajo"
                value={m.workspace || "persistent"}
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
              <FieldLabel htmlFor="machine-idle">Detener sin trabajo tras</FieldLabel>
              <Input
                id="machine-idle"
                value={m.idle_stop}
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
        </ConfigSection>

        <ConfigSection
          icon={Server}
          title="Imagen base"
          yaml="machine.image"
          description={`Una imagen propia en lugar de la de Seaport. Debe derivar de la base de ${harnessLabel[harness]}.`}
        >
          <Field>
            <FieldLabel htmlFor="machine-image">Imagen base</FieldLabel>
            <Input
              id="machine-image"
              value={m.image}
              readOnly={readOnly}
              onChange={(e) => set({ image: e.target.value.trim() })}
              placeholder="la de Seaport para el harness"
              className={cn("font-mono", placeholderMuted)}
              maxLength={255}
            />
          </Field>
        </ConfigSection>
      </Advanced>
    </>
  )
}

/** The knobs most agents never touch, folded; open when one is set. */
function Advanced({ open: initial, children }: { open: boolean; children: React.ReactNode }) {
  const [open, setOpen] = React.useState<boolean | null>(null)
  const show = open ?? initial
  return (
    <div className="border-t pt-5">
      <button
        type="button"
        aria-expanded={show}
        aria-controls="machine-advanced"
        onClick={() => setOpen(!show)}
        className="flex w-full items-center gap-2 text-left text-sm font-medium hover:text-foreground"
      >
        {show ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        Avanzado
        <span className="text-xs font-normal text-muted-foreground">salida, variables, recursos, imagen base</span>
      </button>
      {show && (
        <div
          id="machine-advanced"
          className="mt-5 space-y-5 border-l-2 border-muted pl-4 [&>section]:first:border-t-0 [&>section]:first:pt-0"
        >
          {children}
        </div>
      )}
    </div>
  )
}

/** The machine's plain variables, NAME=value per line. */
function EnvVarsField({
  env,
  readOnly,
  onChange,
}: {
  env: Record<string, string>
  readOnly: boolean
  onChange: (env: Record<string, string>) => void
}) {
  // The textarea keeps what's typed (a half-written line), the draft its parse.
  const [text, setText] = React.useState(() => envText(env))
  const [editing, setEditing] = React.useState(false)
  if (!editing && envText(parseEnv(text)) !== envText(env)) setText(envText(env))
  const problem = envProblem(env)
  return (
    <Field data-invalid={!!problem}>
      <FieldLabel htmlFor="machine-env">Variables</FieldLabel>
      <Textarea
        id="machine-env"
        value={text}
        readOnly={readOnly}
        onFocus={() => setEditing(true)}
        onBlur={() => setEditing(false)}
        onChange={(e) => {
          setText(e.target.value)
          onChange(parseEnv(e.target.value))
        }}
        placeholder={"TZ=America/Lima\nFEATURE_FLAGS=beta"}
        className={cn("min-h-28 font-mono text-sm", placeholderMuted)}
      />
      <FieldDescription>
        {problem ||
          "NOMBRE=valor por línea, para todas las instancias. Nada secreto: una credencial es un secreto con «Variable de entorno»."}
      </FieldDescription>
    </Field>
  )
}

/**
 * machine.concurrency: a number with what it means for the people writing,
 * and a warning when it is a lot for the instance's size or its browser.
 */
export function ConcurrencyField({
  value,
  size,
  browser,
  readOnly,
  onChange,
}: {
  value: number
  size: string
  browser: boolean
  readOnly: boolean
  onChange: (n: number) => void
}) {
  const ok = Number.isInteger(value) && value >= 1 && value <= maxConcurrency
  const advice = ok ? concurrencyAdvice(value, size, browser) : null
  return (
    <Field data-invalid={!ok}>
      <FieldLabel htmlFor="machine-concurrency">Por instancia</FieldLabel>
      <Input
        id="machine-concurrency"
        type="number"
        min={1}
        max={maxConcurrency}
        step={1}
        value={Number.isFinite(value) ? value : ""}
        readOnly={readOnly}
        onChange={(e) => onChange(e.target.value === "" ? Number.NaN : Number(e.target.value))}
        className="w-24 font-mono"
        aria-describedby="machine-concurrency-help"
      />
      <FieldDescription id="machine-concurrency-help">
        {advice ? advice.note : `Entre 1 y ${maxConcurrency}.`} Cada conversación tiene su propia sesión, y un mensaje
        nuevo en un chat que ya se está atendiendo entra en ese mismo turno.
      </FieldDescription>
      {advice?.warn && (
        <p role="note" className={cn("rounded-lg px-3 py-2 text-sm", toneClasses.warning.soft)}>
          {advice.warn}
        </p>
      )}
    </Field>
  )
}

function ReposField({
  repos,
  readOnly,
  onChange,
  secrets,
}: {
  repos: MachineRepo[]
  readOnly: boolean
  onChange: (r: MachineRepo[]) => void
  secrets: SecretsCtx
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

function RepoDialog({
  repo,
  readOnly,
  secrets,
  onSave,
}: {
  repo?: MachineRepo
  readOnly?: boolean
  secrets: SecretsCtx
  onSave: (r: MachineRepo) => void
}) {
  const [open, setOpen] = React.useState(false)
  const [r, setR] = React.useState<MachineRepo>(repo ?? blankRepo)
  const urlOk = repoURLPattern.test(r.url.trim())
  const credOk = !r.credential || secretRefPattern.test(r.credential)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) setR(repo ?? blankRepo)
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
              <SecretPicker
                id="repo-credential"
                label="Credencial"
                value={r.credential}
                onChange={(credential) => setR({ ...r, credential })}
                agentId={secrets.agentId}
                agentName={secrets.agentName}
                secrets={secrets.names}
                canCreate={secrets.canCreate}
                readOnly={readOnly}
                suggested="GIT_TOKEN"
                none="Ninguna (repo público)"
              />
              <FieldDescription>
                {credOk ? "Un secreto del agente con el token del repo." : "Debe nombrar un secreto, como ${NOMBRE}."}
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
