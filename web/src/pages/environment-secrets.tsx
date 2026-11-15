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

export const secretNamePattern = /^[A-Z_][A-Z0-9_]{0,63}$/
export const secretRefPattern = /^\$\{[A-Z_][A-Z0-9_]{0,63}\}$/
const secretRef = /^\$\{([A-Z_][A-Z0-9_]{0,63})\}$/

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
    if (!secretNamePattern.test(k) || k.startsWith("SEAPORT_")) return `${k}: mayúsculas, dígitos y _; no puede empezar con SEAPORT_.`
    if (slotVars.has(k)) return `${k} la pone el puerto en cada instancia.`
    if (!v) return `${k} no tiene valor.`
  }
  if (Object.keys(env).length > 50) return "Como mucho 50 variables."
  return ""
}

/** The spec's plain variables and model providers, edited with the spec. */
export function EnvAndModelsFields({
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
  const models = s.models ?? {}
  const [text, setText] = React.useState(() => envText(env))
  const [editing, setEditing] = React.useState(false)
  if (!editing && envText(parseEnv(text)) !== envText(env)) setText(envText(env))
  const problem = envProblem(env)
  return (
    <div className="grid gap-4 sm:grid-cols-2">
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
          className="min-h-28 font-mono text-sm"
        />
        <FieldDescription>
          {problem ||
            "NOMBRE=valor por línea, para todas las instancias. Nada secreto: una credencial va abajo, como secreto con «Variable de entorno»."}
        </FieldDescription>
      </Field>
      <div className="space-y-3">
        <FieldLabel>Modelos</FieldLabel>
        {providers.map((p) => {
          const v = models[p.name] ?? ""
          const ok = !v || secretRefPattern.test(v)
          return (
            <Field key={p.name} data-invalid={!ok} orientation="horizontal" className="items-center">
              <FieldLabel htmlFor={`env-model-${p.name}`} className="w-24 shrink-0">
                {p.label}
              </FieldLabel>
              <Input
                id={`env-model-${p.name}`}
                value={v}
                readOnly={readOnly}
                onChange={(e) => {
                  const next = { ...models }
                  const v = e.target.value.trim()
                  if (v) next[p.name] = v
                  else delete next[p.name]
                  set({ models: next })
                }}
                placeholder={`\${${p.keyEnv}}`}
                className="font-mono"
              />
            </Field>
          )
        })}
        <FieldDescription>
          {Object.values(models).some((v) => v && !secretRefPattern.test(v))
            ? "Cada clave nombra un secreto del entorno, como ${NOMBRE}."
            : !models.anthropic && s.harness === "claude-code"
              ? "Sin clave de Anthropic, cada instancia se conecta a una cuenta de Claude (suscripción, experimental) desde la página del agente."
              : "El nombre de un secreto de abajo. La clave pasa por el gateway del puerto: nunca entra al contenedor y el uso queda medido por agente."}
        </FieldDescription>
      </div>
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
      <div className="flex flex-wrap items-center justify-between gap-2">
        <FieldLabel>Secretos</FieldLabel>
        {write && <EnvironmentSecretDialog envId={envId} />}
      </div>
      {secrets.isPending ? (
        <Skeleton className="h-16 rounded-lg" />
      ) : secrets.isError ? (
        <ErrorState error={secrets.error} onRetry={() => secrets.refetch()} />
      ) : secrets.data.secrets.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title="Sin secretos"
          description="La clave del modelo, la credencial de un repo, o con «Variable de entorno» lo que el harness necesite en su entorno."
        />
      ) : (
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
                <StatusBadge tone="brand" dot={false} title={`Lo nombra la especificación: ${named.get(s.name)!.join(", ")}`}>
                  en uso
                </StatusBadge>
              )}
              <span className="ml-auto text-xs text-muted-foreground">actualizado {formatFull(s.updated_at)}</span>
              {write && (
                <>
                  <EnvironmentSecretDialog envId={envId} name={s.name} env={s.env} />
                  <ConfirmAction
                    trigger={
                      <Button variant="ghost" size="icon-sm" aria-label={`Eliminar ${s.name}`} disabled={named.has(s.name)}>
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
      )}
      {missing.length > 0 && secrets.data && (
        <p className="text-sm text-warning">
          La especificación nombra {missing.map((n, i) => [i > 0 && ", ", <Mono key={n}>{n}</Mono>])} y el entorno no lo
          tiene: lo aporta cada agente alojado, o créalo aquí.
        </p>
      )}
    </div>
  )
}

/** Sets an environment secret (new, or a new value for an existing one). */
function EnvironmentSecretDialog({ envId, name: fixed, env: wasEnv }: { envId: string; name?: string; env?: boolean }) {
  const setSecret = useSetEnvironmentSecret(envId)
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
            <DialogTitle>{fixed ? `Cambiar ${fixed}` : "Nuevo secreto del entorno"}</DialogTitle>
            <DialogDescription>
              {fixed ? "El valor anterior se reemplaza; no se puede ver." : "Para todas sus instancias. No se puede volver a ver."}
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
                  : `La especificación lo nombra como \${${name || "NOMBRE"}}.`}
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
              />
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="env-secret-env">Variable de entorno</FieldLabel>
                <FieldDescription>El harness de cada instancia también lo recibe en su entorno al arrancar.</FieldDescription>
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
