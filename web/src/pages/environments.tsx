import * as React from "react"
import { Link, useNavigate } from "react-router"
import { ArrowLeft, ArrowRight, Boxes, Code2, Cpu, FileCode2, Plug, Plus, Server } from "lucide-react"

import { useBuild, useCan, useEnvironmentSecrets, useEnvironments, useHosting, useNewEnvironment } from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { Build, EnvSpec, Environment } from "@/lib/types"
import { formatRelative, plural } from "@/lib/format"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  BuildBadge,
  ChoiceCards,
  EmptyState,
  ErrorState,
  Mono,
  PageHeader,
  SegmentedChoice,
  StatusBadge,
  type ChoiceOption,
  type Tone,
} from "@/components/seaport"
import { keyNameFor, providers, secretNamePattern } from "@/pages/environment-secrets"

/** The environment name rule (hosting envName). */
export const envNamePattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

export const harnessLabel: Record<EnvSpec["harness"], string> = { rness: "rness", "claude-code": "Claude Code" }

/** Starting points for a new environment (the server fills the rest). */
export const templates: Record<string, { label: string; description: string; spec: Partial<EnvSpec> }> = {
  node: {
    label: "Node",
    description: "Node.js y npm; registro de npm abierto.",
    spec: {
      description: "Proyectos Node.js",
      packages: ["nodejs", "npm"],
      network: { egress: ["registry.npmjs.org"] },
    },
  },
  python: {
    label: "Python",
    description: "Python 3 con venv y pip; PyPI abierto.",
    spec: {
      description: "Proyectos Python",
      packages: ["python3", "python3-venv", "python3-pip"],
      network: { egress: ["pypi.org", "files.pythonhosted.org"] },
    },
  },
  go: {
    label: "Go",
    description: "Go y make; el proxy de módulos abierto.",
    spec: {
      description: "Proyectos Go",
      packages: ["golang-go", "make"],
      network: { egress: ["proxy.golang.org", "sum.golang.org"] },
    },
  },
  mcp: {
    label: "Solo MCP",
    description: "Sin repos ni herramientas: el agente trabaja con sus servidores MCP.",
    spec: { description: "Solo herramientas MCP", workspace: "ephemeral" },
  },
  blank: { label: "En blanco", description: "La base del harness, nada más.", spec: {} },
}

const templateIcons = { node: Code2, python: FileCode2, go: Cpu, mcp: Plug, blank: Boxes }

/** Where hosted agents run: the organization's environments. */
export function EnvironmentsPage() {
  const envs = useEnvironments()
  const hosting = useHosting()
  const can = useCan()
  const write = can(Perm.EnvironmentsWrite)
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <PageHeader
        title="Entornos"
        description="Dónde corren los agentes que aloja el puerto: harness, imagen, herramientas, repos, red y recursos. Cada cambio es una versión nueva."
        actions={write && <NewEnvironmentDialog />}
      />
      <div className="mx-auto max-w-4xl space-y-4 p-6">
        {hosting.data && !hosting.data.runtime && <RuntimeOff />}
        {envs.isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-24 rounded-xl" />
            <Skeleton className="h-24 rounded-xl" />
          </div>
        ) : envs.isError ? (
          <ErrorState error={envs.error} onRetry={() => envs.refetch()} />
        ) : envs.data.items.length === 0 ? (
          <EmptyState
            icon={Server}
            title="Todavía no hay entornos"
            description="Crea uno y aloja un agente en él: el puerto lo arranca cuando le llegan mensajes."
            action={write && <NewEnvironmentDialog />}
          />
        ) : (
          <div className="grid gap-3">
            {envs.data.items.map((e) => (
              <EnvironmentRow key={e.id} env={e} runtime={hosting.data?.runtime ?? false} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/** SEAPORT_RUNTIME=off: environments can be written, nothing runs. */
export function RuntimeOff() {
  return (
    <div className="rounded-lg border border-dashed px-4 py-3 text-sm text-muted-foreground">
      Este puerto no aloja agentes (<Mono>SEAPORT_RUNTIME=off</Mono>): puedes preparar entornos, pero construir imágenes y
      arrancar instancias necesita <Mono>SEAPORT_RUNTIME=docker</Mono>.
    </div>
  )
}

/**
 * What an environment still needs before an agent runs there, as the list
 * shows it: the one thing to do next, else how many agents it hosts.
 */
export function readiness(
  env: Environment,
  secrets: string[] | undefined,
  build: Build | null | undefined,
  runtime: boolean
): { tone: Tone; label: string; detail?: string } {
  const models = env.spec.models ?? {}
  const named = Object.values(models).filter(Boolean)
  if (named.length === 0 && env.spec.harness !== "claude-code")
    return {
      tone: "warning",
      label: "Falta la clave del modelo",
      detail: "Sin ella ningún agente puede trabajar aquí.",
    }
  const missing = secrets && named.map((v) => v.slice(2, -1)).find((n) => !secrets.includes(n))
  if (missing)
    return {
      tone: "warning",
      label: `Falta el secreto ${missing}`,
      detail: "La configuración lo nombra; el entorno no lo tiene.",
    }
  if (!env.agents) return { tone: "neutral", label: "Sin agentes", detail: "Aloja uno desde su página." }
  if (runtime && build?.state === "failed") return { tone: "danger", label: "La imagen falló" }
  if (runtime && !build)
    return { tone: "neutral", label: "Sin construir", detail: "Se construye con el primer mensaje." }
  return { tone: "success", label: plural(env.agents, "agente", "agentes") }
}

function EnvironmentRow({ env, runtime }: { env: Environment; runtime: boolean }) {
  const build = useBuild(env.id, env.version)
  const secrets = useEnvironmentSecrets(env.id)
  const state = readiness(
    env,
    secrets.data?.secrets.map((s) => s.name),
    build.data,
    runtime
  )
  return (
    <Card className="py-0 transition-colors hover:bg-muted/40">
      <Link to={`/environments/${env.id}`} className="block">
        <CardContent className="flex flex-wrap items-center gap-x-4 gap-y-2 p-4">
          <span className="flex size-9 items-center justify-center rounded-lg bg-muted">
            <Server className="size-4" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono font-semibold">{env.name}</span>
              <StatusBadge tone="brand" dot={false}>
                {harnessLabel[env.spec.harness]}
              </StatusBadge>
              <span className="text-xs text-muted-foreground">
                v{env.version} · {formatRelative(env.updated_at)}
              </span>
            </div>
            <p className="truncate text-sm text-muted-foreground">
              {env.spec.description || <span className="italic">Sin descripción</span>}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {runtime && !build.isPending && <BuildBadge build={build.data} />}
            {(!build.isPending || !runtime) && (
              <StatusBadge tone={state.tone} dot={state.tone !== "success"} title={state.detail}>
                {state.label}
              </StatusBadge>
            )}
          </div>
        </CardContent>
      </Link>
    </Card>
  )
}

function NewEnvironmentDialog() {
  const create = useNewEnvironment()
  const navigate = useNavigate()
  const [open, setOpen] = React.useState(false)
  const [step, setStep] = React.useState<"what" | "key">("what")
  const [name, setName] = React.useState("")
  const [template, setTemplate] = React.useState("node")
  const [harness, setHarness] = React.useState<EnvSpec["harness"]>("rness")
  const [provider, setProvider] = React.useState(providers[0].name)
  const [secret, setSecret] = React.useState(keyNameFor(providers[0]))
  const [value, setValue] = React.useState("")
  const nameOk = envNamePattern.test(name)
  const secretOk = secretNamePattern.test(secret) && !secret.startsWith("SEAPORT_")
  const templateOptions: ChoiceOption<string>[] = Object.entries(templates).map(([value, t]) => ({
    value,
    label: t.label,
    description: t.description,
    icon: templateIcons[value as keyof typeof templateIcons],
  }))
  const submit = (withKey: boolean) =>
    create.mutate(
      {
        name,
        spec: { ...templates[template].spec, harness },
        key: withKey ? { provider, secret, value } : undefined,
      },
      {
        onSuccess: (env) => {
          setOpen(false)
          navigate(`/environments/${env.id}`)
        },
      }
    )
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setStep("what")
          setName("")
          setTemplate("node")
          setHarness("rness")
          setProvider(providers[0].name)
          setSecret(keyNameFor(providers[0]))
        }
        setValue("")
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus /> Entorno
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        {step === "what" ? (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (nameOk) setStep("key")
            }}
          >
            <DialogHeader>
              <DialogTitle>Nuevo entorno</DialogTitle>
              <DialogDescription>
                Paso 1 de 2 · Parte de una plantilla; luego ajusta repos, red y recursos.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field data-invalid={!!name && !nameOk}>
                <FieldLabel htmlFor="env-name">Nombre</FieldLabel>
                <Input
                  id="env-name"
                  value={name}
                  onChange={(e) => setName(e.target.value.trim().toLowerCase())}
                  className="font-mono"
                  placeholder="backend-dev"
                  autoComplete="off"
                  autoFocus
                />
                <FieldDescription>
                  {!!name && !nameOk
                    ? "Minúsculas, dígitos, - o _ (hasta 64)."
                    : `Con él lo nombra la CLI: seaport agents host pm ${name || "backend-dev"}`}
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel>Harness</FieldLabel>
                <ChoiceCards
                  className="sm:grid-cols-2"
                  value={harness}
                  onChange={(v) => setHarness(v as EnvSpec["harness"])}
                  options={[
                    { value: "rness", label: "rness", description: "El harness de Seaport; modelos por el gateway." },
                    { value: "claude-code", label: "Claude Code", description: "Con clave de API por el gateway." },
                  ]}
                />
              </Field>
              <Field>
                <FieldLabel>Plantilla</FieldLabel>
                <ChoiceCards value={template} onChange={setTemplate} options={templateOptions} />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancelar
              </Button>
              <Button type="submit" disabled={!nameOk}>
                Siguiente <ArrowRight />
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (secretOk && value) submit(true)
            }}
          >
            <DialogHeader>
              <DialogTitle>Clave del modelo</DialogTitle>
              <DialogDescription>
                Paso 2 de 2 · Se guarda cifrada como secreto de <Mono>{name}</Mono> y se nombra en Modelos. Pasa por el
                gateway del puerto: nunca entra al contenedor.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field>
                <FieldLabel>Proveedor</FieldLabel>
                <SegmentedChoice
                  aria-label="Proveedor"
                  value={provider}
                  onChange={(v) => {
                    const p = providers.find((x) => x.name === v) ?? providers[0]
                    setProvider(p.name)
                    setSecret(keyNameFor(p))
                  }}
                  options={providers.map((p) => ({ value: p.name, label: p.label }))}
                  size="sm"
                />
              </Field>
              <div className="grid gap-4 sm:grid-cols-[1fr_1.5fr]">
                <Field data-invalid={!!secret && !secretOk}>
                  <FieldLabel htmlFor="env-key-name">Nombre del secreto</FieldLabel>
                  <Input
                    id="env-key-name"
                    value={secret}
                    onChange={(e) => setSecret(e.target.value.trim().toUpperCase())}
                    className="font-mono"
                    autoComplete="off"
                  />
                  <FieldDescription>
                    {!!secret && !secretOk ? "Mayúsculas, dígitos y _." : `En YAML: \${${secret || "NOMBRE"}}`}
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="env-key-value">Clave</FieldLabel>
                  <Input
                    id="env-key-value"
                    type="password"
                    value={value}
                    onChange={(e) => setValue(e.target.value)}
                    className="font-mono"
                    autoComplete="new-password"
                    maxLength={16384}
                    autoFocus
                  />
                  <FieldDescription>No se puede volver a ver.</FieldDescription>
                </Field>
              </div>
              <FieldDescription>
                {harness === "claude-code"
                  ? "Sin clave, cada instancia inicia sesión con una cuenta de Claude desde la página del agente (experimental)."
                  : "Sin clave el entorno queda creado; su página dice lo que falta y la clave se añade ahí en un paso."}
              </FieldDescription>
            </FieldGroup>
            <DialogFooter className="sm:justify-between">
              <Button type="button" variant="ghost" onClick={() => setStep("what")} disabled={create.isPending}>
                <ArrowLeft /> Atrás
              </Button>
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => submit(false)} disabled={create.isPending}>
                  {create.isPending && !create.variables?.key && <Spinner />} Luego
                </Button>
                <Button type="submit" disabled={!secretOk || !value || create.isPending}>
                  {create.isPending && !!create.variables?.key && <Spinner />} Crear con clave
                </Button>
              </div>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
