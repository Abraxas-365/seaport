import * as React from "react"
import { Link, useNavigate } from "react-router"
import { Plus, Vault as VaultIcon } from "lucide-react"

import { useCan, useCreateVault, useVaults } from "@/lib/hooks"
import { formatRelative, plural } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { Vault } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
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
import { Textarea } from "@/components/ui/textarea"
import { EmptyState, ErrorState, Mono, PageHeader } from "@/components/seaport"

/** The vault (and entry) name rule (vault.ValidName). */
export const vaultNamePattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

/** The organization's vaults: shared markdown memory agents read and write. */
export function VaultsPage() {
  const vaults = useVaults()
  const can = useCan()
  const write = can(Perm.VaultsWrite)
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <PageHeader
        title="Bóvedas"
        description="Memoria compartida de tus agentes: notas en Markdown que leen y escriben a través del puerto, con quién escribió cada una y todas sus versiones. Cada bóveda se concede a agentes y a muelles."
        actions={write && <NewVaultDialog />}
      />
      <div className="mx-auto max-w-4xl space-y-4 p-6">
        {vaults.isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-20 rounded-xl" />
            <Skeleton className="h-20 rounded-xl" />
          </div>
        ) : vaults.isError ? (
          <ErrorState error={vaults.error} onRetry={() => vaults.refetch()} />
        ) : vaults.data.items.length === 0 ? (
          <EmptyState
            icon={VaultIcon}
            title="Todavía no hay bóvedas"
            description="Crea una, concédesela a un agente o a un muelle y empezará a recordar lo que importa."
            action={write && <NewVaultDialog />}
          />
        ) : (
          <div className="grid gap-3">
            {vaults.data.items.map((v) => (
              <VaultRow key={v.id} vault={v} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function VaultRow({ vault: v }: { vault: Vault }) {
  return (
    <Link
      to={`/vaults/${v.id}`}
      className="rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <Card className="flex-row items-center gap-3 p-4 transition-colors hover:bg-accent/40">
        <span className="inline-flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <VaultIcon className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium">{v.title || v.name}</span>
            {v.title && <Mono>{v.name}</Mono>}
          </div>
          {v.description && <p className="truncate text-sm text-muted-foreground">{v.description}</p>}
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">{plural(v.entries, "entrada", "entradas")}</span>
          <span>actualizada {formatRelative(v.updated_at)}</span>
        </div>
      </Card>
    </Link>
  )
}

function NewVaultDialog() {
  const create = useCreateVault()
  const navigate = useNavigate()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [title, setTitle] = React.useState("")
  const [description, setDescription] = React.useState("")
  const nameOk = vaultNamePattern.test(name)

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName("")
          setTitle("")
          setDescription("")
        }
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus /> Nueva bóveda
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              { name, title: title.trim(), description: description.trim() },
              {
                onSuccess: (v) => {
                  setOpen(false)
                  navigate(`/vaults/${v.id}`)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Nueva bóveda</DialogTitle>
            <DialogDescription>Un conjunto de notas en Markdown que los agentes comparten.</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field>
              <FieldLabel htmlFor="vault-name">Nombre</FieldLabel>
              <Input
                id="vault-name"
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                placeholder="clientes"
                className="font-mono"
                autoComplete="off"
                aria-invalid={name !== "" && !nameOk}
              />
              <FieldDescription>
                Así lo llaman los agentes. 1–64 letras minúsculas, dígitos, <Mono>-</Mono> o <Mono>_</Mono>, empezando
                por letra o dígito. No se puede cambiar.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="vault-title">Título</FieldLabel>
              <Input id="vault-title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Clientes" />
            </Field>
            <Field>
              <FieldLabel htmlFor="vault-description">Descripción</FieldLabel>
              <Textarea
                id="vault-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Qué se guarda aquí y cuándo conviene consultarla."
                rows={3}
              />
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
