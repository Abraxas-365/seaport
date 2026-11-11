import * as React from "react"
import { useNavigate } from "react-router"
import { Anchor, Plus } from "lucide-react"

import { useCan, useCreateDock, useDocks, useNow } from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import type { Channel } from "@/lib/types"
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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  ChoiceCards,
  DockCard,
  EmptyState,
  ErrorState,
  GitHubMark,
  JiraMark,
  PageHeader,
  SlackMark,
  type ChoiceOption,
} from "@/components/seaport"
import { MessageCircle, Webhook } from "lucide-react"

const channelOptions: ChoiceOption<Channel>[] = [
  {
    value: "whatsapp",
    label: "WhatsApp",
    icon: MessageCircle,
    tone: "success",
    description: "Vincula un número escaneando un QR, como WhatsApp Web.",
  },
  {
    value: "api",
    label: "API",
    icon: Webhook,
    tone: "brand",
    description: "Recibe mensajes por HTTP desde tu propio sistema.",
  },
  {
    value: "github",
    label: "GitHub",
    icon: GitHubMark,
    tone: "neutral",
    description: "Una GitHub App: menciona a tus agentes en issues y pull requests.",
  },
  {
    value: "jira",
    label: "Jira",
    icon: JiraMark,
    tone: "info",
    description: "Una cuenta de servicio de Jira Cloud: menciona a tus agentes en los comentarios de un issue.",
  },
  {
    value: "slack",
    label: "Slack",
    icon: SlackMark,
    tone: "neutral",
    description: "Una Slack App: menciona a tus agentes en un canal o escríbeles por mensaje directo.",
  },
]

const defaultName: Record<Channel, string> = {
  whatsapp: "WhatsApp",
  api: "API",
  github: "GitHub",
  jira: "Jira",
  slack: "Slack",
}
const namePlaceholder: Record<Channel, string> = {
  whatsapp: "Mi WhatsApp",
  api: "Formulario web",
  github: "QA de pull requests",
  jira: "Soporte en Jira",
  slack: "Soporte en Slack",
}

export function DocksPage() {
  const docks = useDocks()
  const now = useNow()
  const can = useCan()
  const write = can(Perm.DocksWrite)
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <PageHeader
        title="Muelles"
        description="Por donde llegan los barcos: cada muelle es un canal conectado."
        actions={write && <NewDockDialog />}
      />
      <div className="mx-auto max-w-4xl p-6">
        {docks.isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-20 rounded-xl" />
            <Skeleton className="h-20 rounded-xl" />
          </div>
        ) : docks.isError ? (
          <ErrorState error={docks.error} onRetry={() => docks.refetch()} />
        ) : docks.data.items.length === 0 ? (
          <EmptyState
            icon={Anchor}
            title="El puerto no tiene muelles"
            description={write ? "Crea uno para empezar a recibir mensajes de WhatsApp." : "Aún no hay muelles en tu organización."}
            action={write && <NewDockDialog />}
          />
        ) : (
          <div className="grid gap-3">
            {docks.data.items.map((d) => (
              <DockCard key={d.id} dock={d} now={now} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function NewDockDialog() {
  const navigate = useNavigate()
  const create = useCreateDock()
  const [open, setOpen] = React.useState(false)
  const [channel, setChannel] = React.useState<Channel>("whatsapp")
  const [name, setName] = React.useState("")
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setName("")
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus /> Nuevo muelle
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              { channel, name: name.trim() || defaultName[channel] },
              {
                onSuccess: (d) => {
                  setOpen(false)
                  navigate(`/docks/${d.id}`)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Nuevo muelle</DialogTitle>
            <DialogDescription>Elige el canal por el que llegarán los mensajes.</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <ChoiceCards value={channel} options={channelOptions} onChange={setChannel} className="sm:grid-cols-2" />
            <Field>
              <FieldLabel htmlFor="dock-name">Nombre</FieldLabel>
              <Input
                id="dock-name"
                value={name}
                placeholder={namePlaceholder[channel]}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
              <FieldDescription>Solo para reconocerlo aquí; se puede cambiar después.</FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending && <Spinner />}
              {channel === "api" ? "Crear" : "Crear y vincular"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
