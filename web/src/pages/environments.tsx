import * as React from "react"
import { Link, useNavigate } from "react-router"
import { Boxes, Code2, Cpu, FileCode2, Plug, Plus, Server } from "lucide-react"

import { useBuild, useCan, useCreateEnvironment, useEnvironments, useHosting } from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { EnvSpec, Environment } from "@/lib/types"
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
  StatusBadge,
  type ChoiceOption,
} from "@/components/seaport"

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
              <EnvironmentRow key={e.id} env={e} />
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

function EnvironmentRow({ env }: { env: Environment }) {
  const build = useBuild(env.id, env.version)
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
            {!build.isPending && <BuildBadge build={build.data} />}
            <StatusBadge tone={env.agents ? "success" : "neutral"} dot={false}>
              {env.agents ? plural(env.agents, "agente", "agentes") : "Sin agentes"}
            </StatusBadge>
          </div>
        </CardContent>
      </Link>
    </Card>
  )
}

function NewEnvironmentDialog() {
  const create = useCreateEnvironment()
  const navigate = useNavigate()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [template, setTemplate] = React.useState("node")
  const [harness, setHarness] = React.useState<EnvSpec["harness"]>("rness")
  const nameOk = envNamePattern.test(name)
  const templateOptions: ChoiceOption<string>[] = Object.entries(templates).map(([value, t]) => ({
    value,
    label: t.label,
    description: t.description,
    icon: templateIcons[value as keyof typeof templateIcons],
  }))
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName("")
          setTemplate("node")
          setHarness("rness")
        }
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus /> Entorno
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              { name, spec: { ...templates[template].spec, harness } },
              {
                onSuccess: (env) => {
                  setOpen(false)
                  navigate(`/environments/${env.id}`)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Nuevo entorno</DialogTitle>
            <DialogDescription>Parte de una plantilla; luego ajusta repos, red y recursos.</DialogDescription>
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
                {!!name && !nameOk ? "Minúsculas, dígitos, - o _ (hasta 64)." : "Con él lo nombra la CLI: seaport agents host pm --env …"}
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
            <Button type="submit" disabled={!nameOk || create.isPending}>
              {create.isPending && <Spinner />} Crear
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
