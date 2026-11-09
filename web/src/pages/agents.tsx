import * as React from "react"
import { Link } from "react-router"
import { Anchor, Bot, KeyRound, MoreHorizontal, Pencil, Plus, ScrollText, Star, Trash2, X } from "lucide-react"

import {
  useAgents,
  useAssignAgent,
  useCan,
  useCreateAgent,
  useCrew,
  useCrewTokens,
  useDeleteAgent,
  useDocks,
  useUnassignAgent,
  useUpdateAgent,
} from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { Dock, LibraryAgent } from "@/lib/types"
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  ChannelIcon,
  ConfirmAction,
  EmptyState,
  ErrorState,
  PageHeader,
  StatusBadge,
  StatusDot,
} from "@/components/seaport"

/** The agent name rule (agent.ValidName): also its @mention. */
const namePattern = /^[a-z0-9][a-z0-9_-]{0,31}$/

/** The organization's agent library: who works which docks. */
export function AgentsPage() {
  const agents = useAgents()
  const can = useCan()
  const write = can(Perm.AgentsWrite)
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <PageHeader
        title="Agentes"
        description="Tu biblioteca de agentes. Cada uno trabaja solo en los muelles a los que lo asignas; en un muelle con varios, se le llama con @nombre."
        actions={write && <AgentDialog />}
      />
      <div className="mx-auto max-w-4xl p-6">
        {agents.isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-28 rounded-xl" />
            <Skeleton className="h-28 rounded-xl" />
          </div>
        ) : agents.isError ? (
          <ErrorState error={agents.error} onRetry={() => agents.refetch()} />
        ) : agents.data.items.length === 0 ? (
          <EmptyState
            icon={Bot}
            title="Todavía no hay agentes"
            description="Crea un agente, asígnalo a un muelle y dale un token para conectarse."
            action={write && <AgentDialog />}
          />
        ) : (
          <div className="grid gap-3">
            {agents.data.items.map((a) => (
              <AgentRow key={a.id} agent={a} write={write} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function AgentRow({ agent: a, write }: { agent: LibraryAgent; write: boolean }) {
  const can = useCan()
  const docks = useDocks()
  const crew = useCrew()
  const tokens = useCrewTokens()
  const unassign = useUnassignAgent()
  const assign = useAssignAgent()
  const remove = useDeleteAgent()
  const byId = new Map((docks.data?.items ?? []).map((d) => [d.id, d]))
  const connections = crew.data?.agents.filter((c) => c.agent_id === a.id).length
  const tokenCount = tokens.data?.items.filter((t) => t.agent_id === a.id).length
  const inactive = a.docks.length === 0

  return (
    <Card className="gap-0 py-0">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start gap-3">
          <span className="relative inline-flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <Bot className="size-5" />
            {connections !== undefined && (
              <span className="absolute -top-0.5 -right-0.5 rounded-full ring-2 ring-card">
                <StatusDot tone={connections ? "success" : "neutral"} />
              </span>
            )}
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <Link to={`/agents/${a.id}`} className="truncate font-mono font-medium hover:underline">
                @{a.name}
              </Link>
              {a.profile_version > 0 ? (
                <StatusBadge tone="brand" dot={false}>
                  Perfil v{a.profile_version}
                </StatusBadge>
              ) : (
                <StatusBadge dot={false}>Sin perfil</StatusBadge>
              )}
              {inactive && <StatusBadge tone="warning">Inactivo</StatusBadge>}
              {connections !== undefined &&
                (connections ? (
                  <StatusBadge tone="success">
                    {connections === 1 ? "1 conexión" : `${connections} conexiones`}
                  </StatusBadge>
                ) : (
                  <StatusBadge>Sin conexión</StatusBadge>
                ))}
            </div>
            <p className="mt-0.5 text-sm text-muted-foreground">
              {a.description || <span className="italic">Sin descripción</span>}
            </p>
          </div>
          {write && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={`Acciones de ${a.name}`}>
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-48">
                <DropdownMenuItem asChild>
                  <Link to={`/agents/${a.id}`}>
                    <ScrollText /> Perfil y secretos
                  </Link>
                </DropdownMenuItem>
                <AgentDialog
                  agent={a}
                  trigger={
                    <DropdownMenuItem onSelect={(e) => e.preventDefault()}>
                      <Pencil /> Editar
                    </DropdownMenuItem>
                  }
                />
                <DropdownMenuSeparator />
                <ConfirmAction
                  trigger={
                    <DropdownMenuItem variant="destructive" onSelect={(e) => e.preventDefault()}>
                      <Trash2 /> Eliminar
                    </DropdownMenuItem>
                  }
                  title={`¿Eliminar @${a.name}?`}
                  description={
                    <p>
                      Se revocan sus tokens, se cierran sus conexiones y lo que esperaba por él queda retenido. El
                      historial de sus conversaciones se conserva.
                    </p>
                  }
                  destructive
                  confirmText={a.name}
                  actionLabel="Eliminar"
                  onConfirm={() => remove.mutateAsync(a.id)}
                />
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {a.docks.map((as) => {
            const d = byId.get(as.dock_id)
            return (
              <span
                key={as.dock_id}
                className="inline-flex h-7 items-center gap-1.5 rounded-lg border bg-muted/40 pr-1 pl-2 text-sm"
              >
                {d && <ChannelIcon channel={d.channel} size="sm" className="size-5 rounded-md" />}
                <Link to={`/docks/${as.dock_id}`} className="max-w-48 truncate hover:underline">
                  {d?.name ?? "Muelle"}
                </Link>
                {as.default ? (
                  <Star className="size-3.5 fill-current text-primary" aria-label="por defecto" />
                ) : (
                  write && (
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      title="Hacerlo el agente por defecto de este muelle"
                      aria-label="Hacer por defecto"
                      onClick={() => assign.mutate({ dock: as.dock_id, agent: a.id, isDefault: true })}
                    >
                      <Star />
                    </Button>
                  )
                )}
                {write && (
                  <ConfirmAction
                    trigger={
                      <Button variant="ghost" size="icon-xs" aria-label={`Quitar de ${d?.name ?? "este muelle"}`}>
                        <X />
                      </Button>
                    }
                    title={`¿Quitar @${a.name} de ${d?.name ?? "este muelle"}?`}
                    description={<p>Los mensajes que esperaban por este agente en el muelle quedan retenidos.</p>}
                    actionLabel="Quitar"
                    onConfirm={() => unassign.mutateAsync({ dock: as.dock_id, agent: a.id })}
                  />
                )}
              </span>
            )
          })}
          {write && can(Perm.DocksRead) && (
            <AssignDialog agent={a} docks={(docks.data?.items ?? []).filter((d) => !a.docks.some((x) => x.dock_id === d.id))} />
          )}
          {inactive && !write && <span className="text-sm text-muted-foreground">No trabaja en ningún muelle.</span>}
        </div>

        {tokenCount !== undefined && (
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <KeyRound className="size-3.5" />
            <Link to="/tokens" className="hover:underline">
              {tokenCount === 0 ? "Sin tokens: créale uno para conectarlo" : tokenCount === 1 ? "1 token" : `${tokenCount} tokens`}
            </Link>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/** Puts an agent on a dock it doesn't work yet. */
function AssignDialog({ agent, docks }: { agent: LibraryAgent; docks: Dock[] }) {
  const assign = useAssignAgent()
  const [open, setOpen] = React.useState(false)
  const [dock, setDock] = React.useState("")
  const [isDefault, setDefault] = React.useState(false)
  if (docks.length === 0) return null
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) {
          setDock("")
          setDefault(false)
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Anchor /> Asignar a muelle
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            assign.mutate({ dock, agent: agent.id, isDefault }, { onSuccess: () => setOpen(false) })
          }}
        >
          <DialogHeader>
            <DialogTitle>Asignar @{agent.name}</DialogTitle>
            <DialogDescription>
              Recibe los mensajes del muelle que lo llamen con @{agent.name}; si es el único agente, todos.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field>
              <FieldLabel>Muelle</FieldLabel>
              <Select value={dock} onValueChange={setDock}>
                <SelectTrigger aria-label="Muelle">
                  <SelectValue placeholder="Elige un muelle" />
                </SelectTrigger>
                <SelectContent>
                  {docks.map((d) => (
                    <SelectItem key={d.id} value={d.id}>
                      {d.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="assign-default">Por defecto</FieldLabel>
                <FieldDescription>Atiende los mensajes que no llaman a nadie (reemplaza al anterior).</FieldDescription>
              </FieldContent>
              <Switch id="assign-default" checked={isDefault} onCheckedChange={setDefault} />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!dock || assign.isPending}>
              {assign.isPending && <Spinner />} Asignar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Creates an agent, or edits one. */
function AgentDialog({ agent, trigger }: { agent?: LibraryAgent; trigger?: React.ReactNode }) {
  const create = useCreateAgent()
  const update = useUpdateAgent()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState(agent?.name ?? "")
  const [description, setDescription] = React.useState(agent?.description ?? "")
  const n = name.trim().toLowerCase()
  const validName = namePattern.test(n)
  const busy = create.isPending || update.isPending
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName(agent?.name ?? "")
          setDescription(agent?.description ?? "")
        }
      }}
    >
      <DialogTrigger asChild>
        {trigger ?? (
          <Button>
            <Plus /> Nuevo agente
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            const done = { onSuccess: () => setOpen(false) }
            if (agent) {
              const patch: { name?: string; description?: string } = {}
              if (n !== agent.name) patch.name = n
              if (description.trim() !== agent.description) patch.description = description.trim()
              if (Object.keys(patch).length === 0) return setOpen(false)
              update.mutate({ id: agent.id, ...patch }, done)
            } else create.mutate({ name: n, description: description.trim() }, done)
          }}
        >
          <DialogHeader>
            <DialogTitle>{agent ? `Editar @${agent.name}` : "Nuevo agente"}</DialogTitle>
            <DialogDescription>
              {agent
                ? "Cambiar el nombre cambia cómo se le llama (@nombre) en sus muelles."
                : "Un agente es una identidad: se conecta con sus tokens y trabaja en los muelles que le asignes."}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!n && !validName}>
              <FieldLabel htmlFor="agent-name">Nombre</FieldLabel>
              <Input
                id="agent-name"
                value={name}
                placeholder="pm"
                onChange={(e) => setName(e.target.value)}
                className="font-mono"
                autoComplete="off"
                autoFocus
              />
              <FieldDescription>
                {n && !validName
                  ? "Hasta 32 minúsculas, dígitos, - o _, empezando por letra o dígito."
                  : `Se le llama con @${n || "nombre"}.`}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="agent-description">Descripción</FieldLabel>
              <Textarea
                id="agent-description"
                value={description}
                placeholder="Arma tickets a partir de lo que le cuentan."
                onChange={(e) => setDescription(e.target.value)}
                maxLength={1000}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!validName || busy}>
              {busy && <Spinner />} {agent ? "Guardar" : "Crear agente"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
