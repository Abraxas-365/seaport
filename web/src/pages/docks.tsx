import * as React from "react"
import { useNavigate } from "react-router"
import { Anchor, Plus } from "lucide-react"

import { useCreateDock, useDocks, useNow } from "@/lib/hooks"
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
  PageHeader,
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
]

export function DocksPage() {
  const docks = useDocks()
  const now = useNow()
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <PageHeader
        title="Muelles"
        description="Por donde llegan los barcos: cada muelle es un canal conectado."
        actions={<NewDockDialog />}
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
            description="Crea uno para empezar a recibir mensajes de WhatsApp."
            action={<NewDockDialog />}
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
              { channel, name: name.trim() || (channel === "whatsapp" ? "WhatsApp" : "API") },
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
                placeholder={channel === "whatsapp" ? "Mi WhatsApp" : "Formulario web"}
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
              {channel === "whatsapp" ? "Crear y vincular" : "Crear"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
