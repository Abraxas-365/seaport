import * as React from "react"
import { KeyRound, Pencil, Plus, Trash2 } from "lucide-react"

import { formatFull } from "@/lib/format"
import { useDeleteEnvironmentSecret, useEnvironmentSecrets, useSetEnvironmentSecret } from "@/lib/hooks"
import type { EnvSpec } from "@/lib/types"
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
import { Field, FieldContent, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ConfirmAction, EmptyState, ErrorState, Mono, StatusBadge } from "@/components/seaport"

/** The model providers the port's gateway serves (hosting.Providers), in
 * the order shown; the label is the provider's own name. */
export const providers: { name: string; label: string; keyEnv: string }[] = [
  { name: "anthropic", label: "Anthropic", keyEnv: "ANTHROPIC_API_KEY" },
  { name: "openai", label: "OpenAI", keyEnv: "OPENAI_API_KEY" },
  { name: "openrouter", label: "OpenRouter", keyEnv: "OPENROUTER_API_KEY" },
  { name: "groq", label: "Groq", keyEnv: "GROQ_API_KEY" },
  { name: "deepseek", label: "DeepSeek", keyEnv: "DEEPSEEK_API_KEY" },
]

/** The suggested secret name for a provider's key (`ANTHROPIC_KEY`). */
export const keyNameFor = (p: (typeof providers)[number]) => p.keyEnv.replace(/_API_KEY$/, "_KEY")

export const secretNamePattern = /^[A-Z_][A-Z0-9_]{0,63}$/
export const secretRefPattern = /^\$\{[A-Z_][A-Z0-9_]{0,63}\}$/
const secretRef = /^\$\{([A-Z_][A-Z0-9_]{0,63})\}$/

/** The name a `${NAME}` reference points at, or "" when it isn't one. */
export function secretRefName(ref: string): string {
  return secretRef.exec(ref)?.[1] ?? ""
}

/** The model providers' keys as `${NAME}`: the spec's shape. */
export const asSecretRef = (name: string) => `\${${name}}`

/** The names the spec's model keys and repo credentials refer to, with
 * what names each ("modelo openai", "repo <url>"). */
export function specSecrets(spec: EnvSpec): Map<string, string[]> {
  const out = new Map<string, string[]>()
  const add = (ref: string, use: string) => {
    const m = secretRef.exec(ref)
    if (m) out.set(m[1], [...(out.get(m[1]) ?? []), use])
  }
  for (const [p, v] of Object.entries(spec.models ?? {})) add(v, `modelo ${p}`)
  for (const r of spec.repos) add(r.credential, `repo ${r.url}`)
  return out
}

/**
 * Whether the environment's slots sign in to a Claude subscription
 * instead of using a model key (hosting Spec.SignsIn): Claude Code with
 * no Anthropic key, rness with no model key at all.
 */
export function signsIn(spec: Pick<EnvSpec, "harness" | "models">): boolean {
  const named = Object.entries(spec.models ?? {}).filter(([, v]) => v)
  if (spec.harness === "claude-code") return !named.some(([p]) => p === "anthropic")
  return named.length === 0
}

/** The slot's own variables: the spec's env may not set them. */
const slotVars = new Set([
  "SEAPORT_GATEWAY",
  "HTTPS_PROXY",
  "HTTP_PROXY",
  "https_proxy",
  "http_proxy",
  "NO_PROXY",
  "no_proxy",
  ...providers.flatMap((p) => [p.keyEnv, p.keyEnv.replace(/_API_KEY$/, "_BASE_URL")]),
])

/** `env` as the textarea shows it, NAME=value per line. */
export function envText(env: Record<string, string>): string {
  return Object.entries(env)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n")
}

/** The textarea back to `env`; a line without `=` is a name with an empty
 * value (the port refuses it, so the field shows the problem). */
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split("\n")) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf("=")
    const k = (i < 0 ? line : line.slice(0, i)).trim()
    out[k] = i < 0 ? "" : line.slice(i + 1).trim()
  }
  return out
}

/** What's wrong with an `env` name, or "" when nothing. */
export function envProblem(env: Record<string, string>): string {
  for (const [k, v] of Object.entries(env)) {
    if (!secretNamePattern.test(k) || k.startsWith("SEAPORT_"))
      return `${k}: mayúsculas, dígitos y _; no puede empezar con SEAPORT_.`
    if (slotVars.has(k)) return `${k} la pone el puerto en cada instancia.`
    if (!v) return `${k} no tiene valor.`
  }
  if (Object.keys(env).length > 50) return "Como mucho 50 variables."
  return ""
}

/** The spec's plain variables, edited with the spec. */
export function EnvVarsField({
  spec: s,
  readOnly,
  set,
}: {
  spec: EnvSpec
  readOnly: boolean
  set: (patch: Partial<EnvSpec>) => void
}) {
  // The textarea keeps what's typed (a half-written line), the draft its parse.
  const env = s.env ?? {}
  const [text, setText] = React.useState(() => envText(env))
  const [editing, setEditing] = React.useState(false)
  if (!editing && envText(parseEnv(text)) !== envText(env)) setText(envText(env))
  const problem = envProblem(env)
  return (
    <Field data-invalid={!!problem}>
      <FieldLabel htmlFor="env-vars">Variables</FieldLabel>
      <Textarea
        id="env-vars"
        value={text}
        readOnly={readOnly}
        onFocus={() => setEditing(true)}
        onBlur={() => setEditing(false)}
        onChange={(e) => {
          setText(e.target.value)
          set({ env: parseEnv(e.target.value) })
        }}
        placeholder={"TZ=America/Lima\nFEATURE_FLAGS=beta"}
        className="min-h-28 font-mono text-sm placeholder:text-muted-foreground/50"
      />
      <FieldDescription>
        {problem ||
          "NOMBRE=valor por línea, para todas las instancias. Nada secreto: una credencial es un secreto con «Variable de entorno»."}
      </FieldDescription>
    </Field>
  )
}

/**
 * Picks one of the environment's secrets for a `${NAME}` field (a model
 * key, a repo credential). A value the environment doesn't have (typed in
 * YAML, or lent by the agents) stays selectable and is marked.
 */
export function SecretSelect({
  id,
  label,
  value,
  onChange,
  secrets,
  readOnly,
  none = "Ninguno",
}: {
  id: string
  label: string
  /** The spec's value: `${NAME}` or "". */
  value: string
  onChange: (ref: string) => void
  secrets: string[]
  readOnly?: boolean
  none?: string
}) {
  const name = secretRefName(value)
  const known = secrets.includes(name)
  const bad = !!value && !name
  return (
    <Select value={value || "none"} onValueChange={(v) => onChange(v === "none" ? "" : v)} disabled={readOnly}>
      <SelectTrigger id={id} aria-label={label} aria-invalid={bad || undefined} className="w-full font-mono">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="none" className="text-muted-foreground">
          {none}
        </SelectItem>
        {secrets.map((n) => (
          <SelectItem key={n} value={asSecretRef(n)} className="font-mono">
            {n}
          </SelectItem>
        ))}
        {value && !known && (
          <SelectItem value={value} className="font-mono">
            {name || value} <span className="font-sans text-xs text-muted-foreground">(no está en el entorno)</span>
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  )
}

/**
 * Per model provider, which secret holds the key. Needs the environment's
 * secrets (names); `onNewSecret` opens the secret dialog for a provider.
 */
export function ModelFields({
  spec: s,
  readOnly,
  set,
  secrets,
  onNewSecret,
}: {
  spec: EnvSpec
  readOnly: boolean
  set: (patch: Partial<EnvSpec>) => void
  secrets: string[]
  onNewSecret?: (provider: (typeof providers)[number]) => void
}) {
  const models = s.models ?? {}
  const bad = Object.values(models).some((v) => v && !secretRefPattern.test(v))
  return (
    <div className="space-y-3">
      <div className="space-y-2">
        {providers.map((p) => {
          const v = models[p.name] ?? ""
          return (
            <div key={p.name} className="grid grid-cols-[6rem_1fr] items-center gap-2">
              <FieldLabel htmlFor={`env-model-${p.name}`}>{p.label}</FieldLabel>
              <div className="flex min-w-0 items-center gap-1">
                <SecretSelect
                  id={`env-model-${p.name}`}
                  label={p.label}
                  value={v}
                  secrets={secrets}
                  readOnly={readOnly}
                  none="Sin clave"
                  onChange={(ref) => {
                    const next = { ...models }
                    if (ref) next[p.name] = ref
                    else delete next[p.name]
                    set({ models: next })
                  }}
                />
                {!readOnly && onNewSecret && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Nuevo secreto para ${p.label}`}
                    title="Nuevo secreto"
                    onClick={() => onNewSecret(p)}
                  >
                    <Plus />
                  </Button>
                )}
              </div>
            </div>
          )
        })}
      </div>
      <FieldDescription>
        {bad
          ? "Cada clave nombra un secreto del entorno, como ${NOMBRE}."
          : signsIn(s)
            ? s.harness === "claude-code"
              ? "Sin clave de Anthropic, cada instancia se conecta a una cuenta de Claude (suscripción, experimental) desde la página del agente."
              : "Sin claves, cada instancia se conecta a una cuenta de Claude (suscripción, experimental) desde la página del agente y usa modelos de Anthropic."
            : "La clave pasa por el gateway del puerto: nunca entra al contenedor y el uso queda medido por agente."}
      </FieldDescription>
    </div>
  )
}

/** The environment's secrets: names only; values are written, never read
 * back. Saved apart from the spec (no version). */
export function EnvironmentSecrets({ envId, spec, write }: { envId: string; spec: EnvSpec; write: boolean }) {
  const secrets = useEnvironmentSecrets(envId)
  const remove = useDeleteEnvironmentSecret(envId)
  const named = specSecrets(spec)
  const have = new Set(secrets.data?.secrets.map((s) => s.name) ?? [])
  const missing = [...named.keys()].filter((n) => !have.has(n))
  return (
    <div className="space-y-3">
      {secrets.isPending ? (
        <Skeleton className="h-16 rounded-lg" />
      ) : secrets.isError ? (
        <ErrorState error={secrets.error} onRetry={() => secrets.refetch()} />
      ) : secrets.data.secrets.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title="Sin secretos"
          description="La clave del modelo, el token de un repo, o lo que el harness necesite en su entorno."
          action={write && <EnvironmentSecretDialog envId={envId} />}
        />
      ) : (
        <>
          <ul className="divide-y rounded-lg border" aria-label="Secretos del entorno">
            {secrets.data.secrets.map((s) => (
              <li key={s.name} className="flex flex-wrap items-center gap-3 px-3 py-2">
                <span className="font-mono text-sm">{s.name}</span>
                {s.env && (
                  <StatusBadge dot={false} title="Cada instancia lo recibe como variable de entorno">
                    env
                  </StatusBadge>
                )}
                {named.has(s.name) && (
                  <StatusBadge tone="brand" dot={false} title={`Lo usa: ${named.get(s.name)!.join(", ")}`}>
                    en uso
                  </StatusBadge>
                )}
                <span className="ml-auto text-xs text-muted-foreground">actualizado {formatFull(s.updated_at)}</span>
                {write && (
                  <>
                    <EnvironmentSecretDialog envId={envId} name={s.name} env={s.env} />
                    <ConfirmAction
                      trigger={
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Eliminar ${s.name}`}
                          disabled={named.has(s.name)}
                        >
                          <Trash2 />
                        </Button>
                      }
                      title={`¿Eliminar ${s.name}?`}
                      description={<p>Las instancias que lo reciben reinician sin él al quedar libres.</p>}
                      destructive
                      actionLabel="Eliminar"
                      onConfirm={() => remove.mutateAsync(s.name)}
                    />
                  </>
                )}
              </li>
            ))}
          </ul>
          {write && (
            <div className="flex justify-end">
              <EnvironmentSecretDialog envId={envId} />
            </div>
          )}
        </>
      )}
      {missing.length > 0 && secrets.data && (
        <p className="text-sm text-warning">
          La configuración nombra {missing.map((n, i) => [i > 0 && ", ", <Mono key={n}>{n}</Mono>])} y el entorno no lo
          tiene: lo aporta cada agente alojado, o créalo aquí.
        </p>
      )}
    </div>
  )
}

/**
 * Sets an environment secret (new, or a new value for an existing one).
 * Uncontrolled with its own trigger, or controlled (`open`/`onOpenChange`)
 * with a `preset` name, for the places that lead here ("Añadir clave").
 */
export function EnvironmentSecretDialog({
  envId,
  name: fixed,
  env: wasEnv,
  preset,
  title,
  description,
  open: controlled,
  onOpenChange,
  onSaved,
}: {
  envId: string
  name?: string
  env?: boolean
  /** A suggested name for a new secret (editable). */
  preset?: string
  title?: string
  description?: React.ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /** Called with the name once saved (before closing). */
  onSaved?: (name: string) => void
}) {
  const setSecret = useSetEnvironmentSecret(envId)
  const [own, setOwn] = React.useState(false)
  const open = controlled ?? own
  const setOpen = (o: boolean) => {
    setOwn(o)
    onOpenChange?.(o)
  }
  const [name, setName] = React.useState(fixed ?? preset ?? "")
  const [value, setValue] = React.useState("")
  const [env, setEnv] = React.useState(wasEnv ?? false)
  const nameOk = secretNamePattern.test(name) && !name.startsWith("SEAPORT_")
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName(fixed ?? preset ?? "")
          setEnv(wasEnv ?? false)
        }
        setValue("")
      }}
    >
      {controlled === undefined && (
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
      )}
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setSecret.mutate(
              { name, value, env },
              {
                onSuccess: () => {
                  onSaved?.(name)
                  setOpen(false)
                },
              }
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>{title ?? (fixed ? `Cambiar ${fixed}` : "Nuevo secreto del entorno")}</DialogTitle>
            <DialogDescription>
              {description ??
                (fixed
                  ? "El valor anterior se reemplaza; no se puede ver."
                  : "Para todas sus instancias. No se puede volver a ver.")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-5 py-5">
            <Field data-invalid={!!name && !nameOk}>
              <FieldLabel htmlFor="env-secret-name">Nombre</FieldLabel>
              <Input
                id="env-secret-name"
                value={name}
                readOnly={!!fixed}
                onChange={(e) => setName(e.target.value.trim().toUpperCase())}
                className="font-mono"
                placeholder="ANTHROPIC_KEY"
                autoComplete="off"
              />
              <FieldDescription>
                {!!name && !nameOk
                  ? "Mayúsculas, dígitos y _; no puede empezar con SEAPORT_."
                  : `En YAML se nombra como \${${name || "NOMBRE"}}.`}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="env-secret-value">Valor</FieldLabel>
              <Input
                id="env-secret-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                className="font-mono"
                autoComplete="new-password"
                maxLength={16384}
                autoFocus={!!fixed || !!preset}
              />
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="env-secret-env">Variable de entorno</FieldLabel>
                <FieldDescription>
                  El harness de cada instancia también lo recibe en su entorno al arrancar.
                </FieldDescription>
              </FieldContent>
              <Switch id="env-secret-env" checked={env} onCheckedChange={setEnv} />
            </Field>
          </div>
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
