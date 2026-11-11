import * as React from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
import {
  ArrowLeft,
  Check,
  Copy,
  ExternalLink,
  Link2,
  Link2Off,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  Send,
  ShieldCheck,
  Star,
  Trash2,
  Users,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { useQueryClient } from "@tanstack/react-query"

import { api } from "@/lib/api"
import { keys } from "@/lib/query"
import {
  flatten,
  useAgents,
  useAssignAgent,
  useCan,
  useCrew,
  useDock,
  useDockAction,
  useDockTeam,
  useGitHub,
  useJira,
  useInbox,
  useUnassignAgent,
} from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import { formatFull, prettyHandle } from "@/lib/format"
import type { Dock } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  AgentChip,
  ChannelIcon,
  ConfirmAction,
  ConversationRow,
  DataItem,
  DataList,
  EmptyState,
  ErrorState,
  GitHubMark,
  JiraMark,
  Mono,
  PairingPanel,
  StatusBadge,
  channelLabel,
  dockLabel,
  dockTone,
} from "@/components/seaport"

export function DockPage() {
  const { dockId = "" } = useParams()
  const dock = useDock(dockId)
  if (dock.isError) return <ErrorState error={dock.error} onRetry={() => dock.refetch()} className="flex-1" />
  if (!dock.data)
    return (
      <div className="space-y-4 p-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  return <DockDetail dock={dock.data} />
}

function DockDetail({ dock }: { dock: Dock }) {
  const navigate = useNavigate()
  const actions = useDockAction()
  const can = useCan()
  const write = can(Perm.DocksWrite)
  const wa = dock.channel === "whatsapp"
  const needsPairing = wa && dock.status !== "linked" && write

  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <div className="border-b px-6 py-5">
        <Link
          to="/docks"
          className="mb-3 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" /> Muelles
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <ChannelIcon channel={dock.channel} size="lg" />
          <div className="min-w-0 flex-1">
            <RenameTitle dock={dock} editable={write} />
            <div className="mt-0.5 text-sm text-muted-foreground">
              {channelLabel[dock.channel]}
              {dock.address && <> · {prettyHandle(dock.address)}</>}
            </div>
          </div>
          <StatusBadge tone={dockTone[dock.status]} pulse={dock.status === "linking"}>
            {dockLabel[dock.status]}
          </StatusBadge>
          {can(Perm.CustomsRead) && (
            <Button asChild variant="outline" size="sm">
              <Link to="/customs">
                <ShieldCheck /> Aduana
              </Link>
            </Button>
          )}
          {write && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label="Más acciones">
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-52">
                {wa && (
                  <DropdownMenuItem onSelect={() => actions.relink.mutate(dock.id)}>
                    <RefreshCw /> {dock.status === "linked" ? "Volver a vincular" : "Generar nuevo QR"}
                  </DropdownMenuItem>
                )}
                {dock.channel === "github" && (
                  <ConfirmAction
                    trigger={
                      <DropdownMenuItem onSelect={(e) => e.preventDefault()}>
                        <Link2Off /> Desconectar la GitHub App
                      </DropdownMenuItem>
                    }
                    title="¿Desconectar la GitHub App?"
                    description={
                      <>
                        <p>Seaport desinstala la App de las cuentas donde está y olvida sus credenciales.</p>
                        <p>
                          La App sigue existiendo en GitHub (bórrala en sus ajustes si ya no la usas). Las
                          conversaciones se conservan; puedes crear otra App desde este muelle.
                        </p>
                      </>
                    }
                    actionLabel="Desconectar"
                    onConfirm={() => actions.unlink.mutateAsync(dock.id)}
                  />
                )}
                {dock.channel === "jira" && (
                  <ConfirmAction
                    trigger={
                      <DropdownMenuItem onSelect={(e) => e.preventDefault()}>
                        <Link2Off /> Desconectar Jira
                      </DropdownMenuItem>
                    }
                    title="¿Desconectar Jira?"
                    description={
                      <>
                        <p>Seaport olvida el token de API y el secreto del webhook; dejan de llegar comentarios.</p>
                        <p>
                          No se revoca nada en Jira: revoca el token en la cuenta de servicio y borra el webhook o la
                          regla de Automation. Las conversaciones se conservan.
                        </p>
                      </>
                    }
                    actionLabel="Desconectar"
                    onConfirm={() => actions.unlink.mutateAsync(dock.id)}
                  />
                )}
                {wa && dock.status === "linked" && (
                  <ConfirmAction
                    trigger={
                      <DropdownMenuItem onSelect={(e) => e.preventDefault()}>
                        <Link2Off /> Desvincular
                      </DropdownMenuItem>
                    }
                    title="¿Desvincular este WhatsApp?"
                    description={
                      <>
                        <p>Se cierra la sesión del dispositivo en el teléfono y dejan de llegar mensajes.</p>
                        <p>Las conversaciones y el historial se conservan; puedes volver a vincularlo con un QR.</p>
                      </>
                    }
                    actionLabel="Desvincular"
                    onConfirm={() => actions.unlink.mutateAsync(dock.id)}
                  />
                )}
                <DropdownMenuSeparator />
                <ConfirmAction
                  trigger={
                    <DropdownMenuItem variant="destructive" onSelect={(e) => e.preventDefault()}>
                      <Trash2 /> Eliminar muelle
                    </DropdownMenuItem>
                  }
                  title={`¿Eliminar ${dock.name}?`}
                  description={
                    <p>
                      Se borran para siempre el muelle, <b>todas sus conversaciones, mensajes y reglas de aduana</b>.
                      Esto no se puede deshacer.
                    </p>
                  }
                  destructive
                  confirmText={dock.name}
                  actionLabel="Eliminar"
                  onConfirm={() =>
                    actions.remove.mutateAsync(dock.id).then(() => navigate("/docks", { replace: true }))
                  }
                />
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </div>

      <div className="mx-auto max-w-4xl space-y-6 p-6">
        {needsPairing && (
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Link2 className="size-4" />
                {dock.status === "linking" ? "Escanea para vincular" : "Este muelle no está vinculado"}
              </CardTitle>
              <CardDescription>
                {dock.status === "linking"
                  ? "Seaport se conectará como un dispositivo vinculado de tu WhatsApp."
                  : dock.status === "failed"
                    ? "La vinculación falló o el código expiró."
                    : "La sesión se cerró. Genera un QR nuevo para volver a recibir mensajes."}
              </CardDescription>
            </CardHeader>
            <CardContent>
              {dock.status === "linking" ? (
                <PairingPanel qr={dock.qr_code} />
              ) : (
                <PairingPanel
                  failed
                  onRetry={() => actions.relink.mutate(dock.id)}
                  retrying={actions.relink.isPending}
                />
              )}
            </CardContent>
          </Card>
        )}

        {dock.channel === "github" && <GitHubDockCard dock={dock} write={write} />}

        {dock.channel === "jira" && <JiraDockCard dock={dock} write={write} />}

        {can(Perm.AgentsRead) && <DockTeam dock={dock} />}

        {dock.channel === "api" && can(Perm.ShipsWrite) && <ApiDockCard dock={dock} />}

        {can(Perm.ConversationsRead) && <DockConversations dock={dock} />}

        <Card>
          <CardHeader>
            <CardTitle>Detalles</CardTitle>
          </CardHeader>
          <CardContent>
            <DataList>
              <DataItem label="ID">
                <Mono>{dock.id}</Mono>
              </DataItem>
              <DataItem label="Canal">{channelLabel[dock.channel]}</DataItem>
              {dock.address && (
                <DataItem label="Dirección">
                  <Mono title={dock.address}>{dock.address}</Mono>
                </DataItem>
              )}
              <DataItem label="Creado">{formatFull(dock.created_at)}</DataItem>
              <DataItem label="Actualizado">{formatFull(dock.updated_at)}</DataItem>
            </DataList>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function RenameTitle({ dock, editable }: { dock: Dock; editable: boolean }) {
  const { rename } = useDockAction()
  const [editing, setEditing] = React.useState(false)
  const [name, setName] = React.useState(dock.name)
  if (!editable) return <h1 className="truncate font-heading text-xl font-semibold tracking-tight">{dock.name}</h1>
  if (!editing)
    return (
      <div className="group/title flex items-center gap-1">
        <h1 className="truncate font-heading text-xl font-semibold tracking-tight">{dock.name}</h1>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="Renombrar"
          className="opacity-0 group-hover/title:opacity-100 focus-visible:opacity-100"
          onClick={() => {
            setName(dock.name)
            setEditing(true)
          }}
        >
          <Pencil />
        </Button>
      </div>
    )
  return (
    <form
      className="flex max-w-sm items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault()
        const n = name.trim()
        if (!n || n === dock.name) return setEditing(false)
        rename.mutate({ id: dock.id, name: n }, { onSuccess: () => setEditing(false) })
      }}
    >
      <Input
        value={name}
        autoFocus
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && setEditing(false)}
        className="h-8 text-base font-semibold"
      />
      <Button type="submit" size="icon-sm" disabled={rename.isPending} aria-label="Guardar">
        {rename.isPending ? <Spinner /> : <Check />}
      </Button>
    </form>
  )
}

/**
 * The agents working this dock. One gets every message; with several, @name
 * calls one, a chat stays with the agent it was talking to, and the default
 * takes the rest (none: held).
 */
function DockTeam({ dock }: { dock: Dock }) {
  const can = useCan()
  const write = can(Perm.AgentsWrite)
  const team = useDockTeam(dock.id)
  const agents = useAgents()
  const crew = useCrew()
  const assign = useAssignAgent()
  const unassign = useUnassignAgent()
  const [pick, setPick] = React.useState("")
  const members = team.data?.agents ?? []
  const online = (id: string) => (crew.data ? crew.data.agents.some((c) => c.agent_id === id) : undefined)
  const free = (agents.data?.items ?? []).filter((a) => !members.some((m) => m.agent_id === a.id))
  const hasDefault = members.some((m) => m.default)
  const description =
    members.length === 0
      ? "Nadie atiende este muelle: los mensajes que lleguen quedan retenidos."
      : members.length === 1
        ? "Recibe todos los mensajes del muelle."
        : hasDefault
          ? "Se llama a uno con @nombre; un chat sigue con el agente con el que hablaba; el resto va al de por defecto."
          : "Se llama a uno con @nombre; un chat sigue con el agente con el que hablaba; sin agente por defecto, lo que no llama a nadie queda retenido."

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Users className="size-4" /> Agentes
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {team.isPending ? (
          <Skeleton className="h-8 w-48" />
        ) : team.isError ? (
          <ErrorState error={team.error} onRetry={() => team.refetch()} />
        ) : (
          members.length > 0 && (
            <ul className="divide-y rounded-lg border">
              {members.map((m) => (
                <li key={m.agent_id} className="flex items-center gap-2 px-3 py-2">
                  <AgentChip name={m.name} online={online(m.agent_id)} isDefault={m.default} />
                  {m.default && <span className="text-xs text-muted-foreground">por defecto</span>}
                  <span className="flex-1" />
                  {write && !m.default && members.length > 1 && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => assign.mutate({ dock: dock.id, agent: m.agent_id, isDefault: true })}
                    >
                      <Star /> Por defecto
                    </Button>
                  )}
                  {write && m.default && members.length > 1 && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => assign.mutate({ dock: dock.id, agent: m.agent_id, isDefault: false })}
                    >
                      Quitar por defecto
                    </Button>
                  )}
                  {write && (
                    <ConfirmAction
                      trigger={
                        <Button variant="ghost" size="icon-sm" aria-label={`Quitar @${m.name}`}>
                          <X />
                        </Button>
                      }
                      title={`¿Quitar @${m.name} de ${dock.name}?`}
                      description={<p>Los mensajes que esperaban por este agente en el muelle quedan retenidos.</p>}
                      actionLabel="Quitar"
                      onConfirm={() => unassign.mutateAsync({ dock: dock.id, agent: m.agent_id })}
                    />
                  )}
                </li>
              ))}
            </ul>
          )
        )}
        {write && free.length > 0 && (
          <form
            className="flex flex-wrap items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              assign.mutate(
                { dock: dock.id, agent: pick, isDefault: false },
                { onSuccess: () => setPick("") },
              )
            }}
          >
            <Select value={pick} onValueChange={setPick}>
              <SelectTrigger className="w-56" aria-label="Agente">
                <SelectValue placeholder="Agregar un agente" />
              </SelectTrigger>
              <SelectContent>
                {free.map((a) => (
                  <SelectItem key={a.id} value={a.id} className="font-mono">
                    @{a.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="submit" variant="secondary" disabled={!pick || assign.isPending}>
              {assign.isPending ? <Spinner /> : <Plus />} Asignar
            </Button>
          </form>
        )}
        {write && agents.data && agents.data.items.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Todavía no hay agentes. <Link to="/agents" className="text-primary hover:underline">Crea uno</Link>.
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function DockConversations({ dock }: { dock: Dock }) {
  const navigate = useNavigate()
  const inbox = useInbox({ dock_id: dock.id })
  const items = flatten(inbox.data).slice(0, 8)
  const total = inbox.data?.pages[0]?.page.total ?? 0
  return (
    <Card className="gap-0 pb-2">
      <CardHeader className="pb-3">
        <CardTitle>Conversaciones</CardTitle>
        <CardDescription>
          {total === 0 ? "Todavía no llega ningún barco a este muelle." : `${total} en total, las más recientes primero.`}
        </CardDescription>
      </CardHeader>
      <CardContent className="px-2">
        {inbox.isPending ? (
          <Skeleton className="mx-2 h-14" />
        ) : items.length === 0 ? (
          <EmptyState
            title="Sin conversaciones"
            description={
              dock.channel === "whatsapp"
                ? "Escribe a este número desde otro teléfono para probar."
                : dock.channel === "github"
                  ? "Menciona a un agente del equipo en un issue o pull request de un repositorio con la App instalada."
                  : dock.channel === "jira"
                    ? "Menciona a un agente del equipo (o a la cuenta de servicio) en un comentario de un issue de Jira."
                    : "Envía un mensaje de prueba con el formulario de arriba."
            }
          />
        ) : (
          items.map((c) => <ConversationRow key={c.id} item={c} onSelect={() => navigate(`/inbox/${c.id}`)} />)
        )}
      </CardContent>
    </Card>
  )
}

function ApiDockCard({ dock }: { dock: Dock }) {
  const [chat, setChat] = React.useState("demo-chat")
  const [sender, setSender] = React.useState("Cliente de prueba")
  const [text, setText] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const url = `${window.location.origin}/api/v1/docks/${dock.id}/ships`
  const curl = `curl -X POST ${url} \\
  -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"chat_id":"cliente-42","message_id":"m-1","sender_name":"Ana","text":"Hola"}'`

  return (
    <Card>
      <CardHeader>
        <CardTitle>Recibir por HTTP</CardTitle>
        <CardDescription>
          Tu sistema envía cada mensaje entrante a este endpoint; <code>message_id</code> evita duplicados.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="relative">
          <pre className="scrollbar-thin overflow-x-auto rounded-lg bg-muted p-3 pr-12 font-mono text-xs leading-relaxed">
            {curl}
          </pre>
          <Button
            variant="ghost"
            size="icon-sm"
            className="absolute top-2 right-2"
            aria-label="Copiar"
            onClick={() => navigator.clipboard.writeText(curl).then(() => toast.success("Copiado"))}
          >
            <Copy />
          </Button>
        </div>
        <form
          className="space-y-3"
          onSubmit={async (e) => {
            e.preventDefault()
            if (!text.trim()) return
            setBusy(true)
            try {
              await api.inject(dock.id, {
                chat_id: chat.trim() || "demo-chat",
                chat_title: chat.trim() || "demo-chat",
                message_id: crypto.randomUUID(),
                sender_id: sender.trim(),
                sender_name: sender.trim(),
                text: text.trim(),
              })
              setText("")
              toast.success("Barco recibido", { description: "Míralo en la bandeja o en aduana." })
            } catch (err) {
              toast.error("No se pudo enviar", { description: (err as Error).message })
            } finally {
              setBusy(false)
            }
          }}
        >
          <h3 className="text-sm font-medium">Mensaje de prueba</h3>
          <FieldGroup className="grid gap-3 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="t-chat">Chat</FieldLabel>
              <Input id="t-chat" value={chat} onChange={(e) => setChat(e.target.value)} className="font-mono text-sm" />
            </Field>
            <Field>
              <FieldLabel htmlFor="t-sender">Remitente</FieldLabel>
              <Input id="t-sender" value={sender} onChange={(e) => setSender(e.target.value)} />
            </Field>
          </FieldGroup>
          <Textarea placeholder="Hola, ¿tienen stock?" value={text} onChange={(e) => setText(e.target.value)} />
          <Button type="submit" variant="secondary" disabled={!text.trim() || busy}>
            {busy ? <Spinner /> : <Send />} Enviar al puerto
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

/** Posts the App manifest to GitHub (a top-level form POST: GitHub's manifest flow). */
function postManifest(action: string, manifest: string) {
  const form = document.createElement("form")
  form.method = "post"
  form.action = action
  const field = document.createElement("input")
  field.type = "hidden"
  field.name = "manifest"
  field.value = manifest
  form.appendChild(field)
  document.body.appendChild(form)
  form.submit()
}

function GitHubDockCard({ dock, write }: { dock: Dock; write: boolean }) {
  const info = useGitHub(dock.id)
  const [params, setParams] = useSearchParams()
  const [organization, setOrganization] = React.useState("")
  const [name, setName] = React.useState(`Seaport ${dock.name}`)
  const [busy, setBusy] = React.useState(false)

  // GitHub's redirects come back here with the outcome.
  React.useEffect(() => {
    const ok = params.get("github")
    const problem = params.get("github_error")
    if (!ok && !problem) return
    if (problem) toast.error("GitHub no completó el paso", { description: problem })
    else toast.success("GitHub App instalada", { description: "Menciona a tus agentes en un issue o pull request." })
    setParams({}, { replace: true })
  }, [params, setParams])

  if (info.isError) return <ErrorState error={info.error} onRetry={() => info.refetch()} />
  if (!info.data) return <Skeleton className="h-48 rounded-xl" />
  const gh = info.data
  const app = gh.app
  const active = gh.installations.filter((i) => !i.suspended)

  const unreachable = !gh.reachable && (
    <p className="rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
      GitHub no puede llegar a esta dirección (<Mono>{gh.webhook_url}</Mono>): necesita HTTPS público. Configura{" "}
      <code>SEAPORT_PUBLIC_URL</code> o usa un túnel{app ? "; la App se creó con esta dirección, así que vuelve a crearla después." : " antes de crear la App."}
    </p>
  )

  if (!app)
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <GitHubMark className="size-4" /> Conectar con GitHub
          </CardTitle>
          <CardDescription>
            Seaport crea una GitHub App propia de este muelle (con sus permisos sobre issues, pull requests y
            contenido) y luego la instalas en los repositorios que quieras.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {unreachable}
          {write ? (
            <form
              className="space-y-4"
              onSubmit={async (e) => {
                e.preventDefault()
                setBusy(true)
                try {
                  const m = await api.githubManifest(dock.id, {
                    organization: organization.trim() || undefined,
                    name: name.trim() || undefined,
                  })
                  postManifest(m.action_url, m.manifest)
                } catch (err) {
                  toast.error("No se pudo preparar la App", { description: (err as Error).message })
                  setBusy(false)
                }
              }}
            >
              <FieldGroup className="grid gap-3 sm:grid-cols-2">
                <Field>
                  <FieldLabel htmlFor="gh-org">Organización de GitHub</FieldLabel>
                  <Input
                    id="gh-org"
                    value={organization}
                    onChange={(e) => setOrganization(e.target.value)}
                    placeholder="Vacío: tu cuenta personal"
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="gh-name">Nombre de la App</FieldLabel>
                  <Input id="gh-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={34} />
                </Field>
              </FieldGroup>
              <p className="text-xs text-muted-foreground">
                GitHub pide que el nombre sea único. Su identificador (<code>@nombre-de-la-app</code>) también sirve
                para llamar a los agentes.
              </p>
              <Button type="submit" disabled={busy}>
                {busy ? <Spinner /> : <GitHubMark className="size-4" />} Crear GitHub App
              </Button>
            </form>
          ) : (
            <p className="text-sm text-muted-foreground">Este muelle aún no tiene una GitHub App.</p>
          )}
        </CardContent>
      </Card>
    )

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <GitHubMark className="size-4" /> {app.name}
        </CardTitle>
        <CardDescription>
          {active.length === 0
            ? "La App existe pero no está instalada en ninguna cuenta: instálala para recibir menciones."
            : `Menciona @${app.slug} o a un agente del equipo (@nombre) en un issue o pull request; la respuesta llega como comentario.`}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {unreachable}
        <div className="flex flex-wrap gap-2">
          {write && gh.install_url && (
            <Button asChild size="sm" variant={active.length === 0 ? "default" : "outline"}>
              <a href={gh.install_url}>
                <Plus /> {active.length === 0 ? "Instalar la App" : "Instalar en otra cuenta o repositorio"}
              </a>
            </Button>
          )}
          <Button asChild size="sm" variant="ghost">
            <a href={app.html_url} target="_blank" rel="noreferrer">
              <ExternalLink /> Ver en GitHub
            </a>
          </Button>
        </div>
        {gh.installations.length > 0 && (
          <div className="divide-y rounded-lg border">
            {gh.installations.map((i) => (
              <div key={i.id} className="flex items-center gap-3 px-3 py-2 text-sm">
                <span className="font-medium">{i.account}</span>
                <span className="text-muted-foreground">
                  {i.account_type === "Organization" ? "organización" : "usuario"}
                </span>
                <span className="ml-auto">
                  <StatusBadge tone={i.suspended ? "warning" : "success"}>
                    {i.suspended ? "Suspendida" : "Instalada"}
                  </StatusBadge>
                </span>
              </div>
            ))}
          </div>
        )}
        <DataList>
          <DataItem label="Identificador">
            <Mono>@{app.slug}</Mono>
          </DataItem>
          <DataItem label="Propietario">{app.owner}</DataItem>
          <DataItem label="Webhook">
            <Mono title={gh.webhook_url}>{gh.webhook_url}</Mono>
          </DataItem>
        </DataList>
        <p className="text-xs text-muted-foreground">
          Consejo: en Aduana, permite el muelle por defecto y deniega <code>github:outsider</code> para que en
          repositorios públicos solo despierten a tus agentes los miembros y colaboradores.
        </p>
      </CardContent>
    </Card>
  )
}

/** Copy-to-clipboard block for a value shown once (the webhook secret). */
function CopyField({ label, value, mono = true }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className="flex items-center gap-2 rounded-lg bg-muted p-2 pl-3">
        <span className={mono ? "min-w-0 flex-1 truncate font-mono text-xs" : "min-w-0 flex-1 truncate text-sm"} title={value}>
          {value}
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Copiar ${label.toLowerCase()}`}
          onClick={() => navigator.clipboard.writeText(value).then(() => toast.success("Copiado"))}
        >
          <Copy />
        </Button>
      </div>
    </div>
  )
}

function JiraDockCard({ dock, write }: { dock: Dock; write: boolean }) {
  const info = useJira(dock.id)
  const qc = useQueryClient()
  const [site, setSite] = React.useState("")
  const [email, setEmail] = React.useState("")
  const [token, setToken] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [relinking, setRelinking] = React.useState(false)
  // The secret is shown only by the answers that made it: keep it here.
  const [secret, setSecret] = React.useState<string>()

  if (info.isError) return <ErrorState error={info.error} onRetry={() => info.refetch()} />
  if (!info.data) return <Skeleton className="h-48 rounded-xl" />
  const jira = info.data
  const linked = jira.site
  const form = !linked || relinking

  const accept = (next: typeof jira) => {
    qc.setQueryData(keys.jira(dock.id), { ...next, secret: undefined })
    void qc.invalidateQueries({ queryKey: keys.docks, exact: true })
    void qc.invalidateQueries({ queryKey: keys.dock(dock.id) })
    if (next.secret) setSecret(next.secret)
  }

  const unreachable = !jira.reachable && (
    <p className="rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm">
      Jira no puede llegar a esta dirección (<Mono>{jira.webhook_url}</Mono>): necesita HTTPS público. Configura{" "}
      <code>SEAPORT_PUBLIC_URL</code> o usa un túnel.
    </p>
  )

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <JiraMark className="size-4" /> {linked ? linked.site.replace(/^https?:\/\//, "") : "Conectar con Jira"}
        </CardTitle>
        <CardDescription>
          {linked
            ? `Menciona a @${linked.display_name} o a un agente del equipo (@nombre) en un comentario; la respuesta llega como comentario del issue.`
            : "Crea una cuenta de servicio en tu sitio de Jira Cloud, genera su token de API y pégalo aquí. Seaport comprueba las credenciales antes de guardarlas."}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {unreachable}
        {linked && (
          <DataList>
            <DataItem label="Cuenta">{linked.display_name || linked.email}</DataItem>
            <DataItem label="Correo">
              <Mono>{linked.email}</Mono>
            </DataItem>
            <DataItem label="Estado">
              <StatusBadge tone={dockTone[dock.status]}>{dockLabel[dock.status]}</StatusBadge>
            </DataItem>
          </DataList>
        )}
        {write && form && (
          <form
            className="space-y-4"
            onSubmit={async (e) => {
              e.preventDefault()
              setBusy(true)
              try {
                const next = await api.linkJira(dock.id, { site: site.trim(), email: email.trim(), api_token: token.trim() })
                accept(next)
                setToken("")
                setRelinking(false)
                toast.success("Jira conectado", { description: "Configura ahora el webhook con la dirección y el secreto." })
              } catch (err) {
                toast.error("No se pudo conectar", { description: (err as Error).message })
              } finally {
                setBusy(false)
              }
            }}
          >
            <FieldGroup className="grid gap-3 sm:grid-cols-2">
              <Field className="sm:col-span-2">
                <FieldLabel htmlFor="jira-site">Sitio</FieldLabel>
                <Input
                  id="jira-site"
                  value={site}
                  onChange={(e) => setSite(e.target.value)}
                  placeholder={linked?.site ?? "https://tu-empresa.atlassian.net"}
                  className="font-mono text-sm"
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="jira-email">Correo de la cuenta</FieldLabel>
                <Input
                  id="jira-email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="bot@example.com"
                  required
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="jira-token">Token de API</FieldLabel>
                <Input
                  id="jira-token"
                  type="password"
                  autoComplete="off"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  required
                />
              </Field>
            </FieldGroup>
            <div className="flex gap-2">
              <Button type="submit" disabled={busy || !site.trim() || !email.trim() || !token.trim()}>
                {busy ? <Spinner /> : <JiraMark className="size-4" />} {linked ? "Guardar credenciales" : "Conectar"}
              </Button>
              {relinking && (
                <Button type="button" variant="ghost" onClick={() => setRelinking(false)}>
                  Cancelar
                </Button>
              )}
            </div>
          </form>
        )}
        {linked && (
          <div className="space-y-3">
            <CopyField label="URL del webhook" value={jira.webhook_url} />
            {secret ? (
              <>
                <CopyField label="Secreto del webhook" value={secret} />
                <p className="text-xs text-muted-foreground">
                  Cópialo ahora: Seaport lo guarda cifrado y no lo vuelve a mostrar. En el webhook de Jira va como
                  secreto; en una regla de Automation, como <code>Authorization: Bearer …</code>.
                </p>
              </>
            ) : (
              <p className="text-xs text-muted-foreground">
                El secreto del webhook se mostró al conectar. Si lo perdiste, genera uno nuevo (hay que actualizarlo en Jira).
              </p>
            )}
            {write && (
              <div className="flex flex-wrap gap-2">
                {!relinking && (
                  <Button size="sm" variant="outline" onClick={() => setRelinking(true)}>
                    <RefreshCw /> Cambiar credenciales
                  </Button>
                )}
                <ConfirmAction
                  trigger={
                    <Button size="sm" variant="outline">
                      <RefreshCw /> Nuevo secreto
                    </Button>
                  }
                  title="¿Generar un secreto nuevo?"
                  description={<p>El anterior deja de valer: actualiza el webhook o la regla de Automation en Jira.</p>}
                  actionLabel="Generar"
                  onConfirm={async () => {
                    try {
                      accept(await api.rotateJiraSecret(dock.id))
                    } catch (err) {
                      toast.error("No se pudo generar el secreto", { description: (err as Error).message })
                    }
                  }}
                />
              </div>
            )}
          </div>
        )}
        {!linked && !write && <p className="text-sm text-muted-foreground">Este muelle aún no está conectado a Jira.</p>}
      </CardContent>
    </Card>
  )
}
