import * as React from "react"
import { Link, useNavigate, useParams } from "react-router"
import {
  ArrowLeft,
  Check,
  Copy,
  Link2,
  Link2Off,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Send,
  ShieldCheck,
  Trash2,
} from "lucide-react"
import { toast } from "sonner"

import { api } from "@/lib/api"
import { flatten, useCan, useDock, useDockAction, useInbox } from "@/lib/hooks"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  ChannelIcon,
  ConfirmAction,
  ConversationRow,
  DataItem,
  DataList,
  EmptyState,
  ErrorState,
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
