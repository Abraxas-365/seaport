import * as React from "react"
import { Check, Copy, KeyRound, Plus, Trash2 } from "lucide-react"

import { useCan, useCreateCrewToken, useCrewTokens, useNow, useRevokeCrewToken } from "@/lib/hooks"
import { formatFull, formatRelative } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { CrewToken, IssuedCrewToken } from "@/lib/types"
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
  ConfirmAction,
  EmptyState,
  ErrorState,
  Mono,
  PageHeader,
  SegmentedChoice,
  StatusBadge,
  type ChoiceOption,
} from "@/components/seaport"

type Lifetime = "30" | "90" | "365" | "never"

const lifetimeOptions: ChoiceOption<Lifetime>[] = [
  { value: "30", label: "30 días" },
  { value: "90", label: "90 días" },
  { value: "365", label: "1 año" },
  { value: "never", label: "Sin vencimiento" },
]

/** Crew tokens: the credentials agents connect with (`seaport connect`). */
export function TokensPage() {
  const tokens = useCrewTokens()
  const can = useCan()
  const now = useNow()
  const write = can(Perm.CrewTokensWrite)
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <PageHeader
        title="Tokens de agentes"
        description="Las credenciales con las que se conectan tus agentes. Cada uno actúa solo en tu organización."
        actions={write && <NewTokenDialog />}
      />
      <div className="mx-auto max-w-4xl p-6">
        {tokens.isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-16 rounded-xl" />
            <Skeleton className="h-16 rounded-xl" />
          </div>
        ) : tokens.isError ? (
          <ErrorState error={tokens.error} onRetry={() => tokens.refetch()} />
        ) : tokens.data.items.length === 0 ? (
          <EmptyState
            icon={KeyRound}
            title="Ningún agente tiene token"
            description={
              <>
                Crea uno y conéctate con <code>SEAPORT_CREW_TOKEN=… seaport connect -- rness --acp</code>.
              </>
            }
            action={write && <NewTokenDialog />}
          />
        ) : (
          <ul className="divide-y rounded-xl border bg-card">
            {tokens.data.items.map((t) => (
              <TokenRow key={t.id} token={t} now={now} canRevoke={write} />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function TokenRow({ token: t, now, canRevoke }: { token: CrewToken; now: number; canRevoke: boolean }) {
  const revoke = useRevokeCrewToken()
  const expired = !!t.expires_at && new Date(t.expires_at).getTime() <= now
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3">
      <KeyRound className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{t.name}</span>
          <Mono className="text-xs text-muted-foreground">{t.hint}…</Mono>
          {expired && <StatusBadge tone="danger">Vencido</StatusBadge>}
        </div>
        <div className="truncate text-xs text-muted-foreground">
          <span title={formatFull(t.created_at)}>Creado {formatRelative(t.created_at, now)}</span>
          {t.created_by && <> por {t.created_by}</>}
          {" · "}
          {t.last_used_at ? (
            <span title={formatFull(t.last_used_at)}>usado {formatRelative(t.last_used_at, now)}</span>
          ) : (
            "nunca usado"
          )}
          {" · "}
          {t.expires_at ? (
            <span title={formatFull(t.expires_at)}>
              {expired ? "venció" : "vence"} {formatRelative(t.expires_at, now)}
            </span>
          ) : (
            "sin vencimiento"
          )}
        </div>
      </div>
      {canRevoke && (
        <ConfirmAction
          destructive
          title={`¿Revocar «${t.name}»?`}
          description={
            <p>Los agentes conectados con este token se desconectan ahora y no podrán volver a entrar con él.</p>
          }
          actionLabel="Revocar"
          onConfirm={() => revoke.mutateAsync(t.id)}
          trigger={
            <Button variant="ghost" size="sm" className="text-destructive">
              <Trash2 /> Revocar
            </Button>
          }
        />
      )}
    </li>
  )
}

function NewTokenDialog() {
  const create = useCreateCrewToken()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [lifetime, setLifetime] = React.useState<Lifetime>("90")
  const [issued, setIssued] = React.useState<IssuedCrewToken>()
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      // The value is gone once the dialog closes; it is never shown again.
      setName("")
      setIssued(undefined)
    }
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>
        <Button>
          <Plus /> Nuevo token
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {issued ? (
          <IssuedToken token={issued} onDone={() => close(false)} />
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              const expires_at =
                lifetime === "never" ? undefined : new Date(Date.now() + Number(lifetime) * 86_400_000).toISOString()
              create.mutate({ name: name.trim(), expires_at }, { onSuccess: setIssued })
            }}
          >
            <DialogHeader>
              <DialogTitle>Nuevo token de agente</DialogTitle>
              <DialogDescription>Un token por agente o máquina, para poder revocarlos por separado.</DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field>
                <FieldLabel htmlFor="token-name">Nombre</FieldLabel>
                <Input
                  id="token-name"
                  value={name}
                  placeholder="rness en mi laptop"
                  onChange={(e) => setName(e.target.value)}
                  autoFocus
                />
                <FieldDescription>Para reconocerlo en esta lista y en los registros.</FieldDescription>
              </Field>
              <Field>
                <FieldLabel>Vigencia</FieldLabel>
                <SegmentedChoice
                  aria-label="Vigencia"
                  size="sm"
                  value={lifetime}
                  options={lifetimeOptions}
                  onChange={setLifetime}
                  className="w-full sm:w-full"
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => close(false)}>
                Cancelar
              </Button>
              <Button type="submit" disabled={!name.trim() || create.isPending}>
                {create.isPending && <Spinner />} Crear token
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The value, shown once, with the command to connect. */
function IssuedToken({ token, onDone }: { token: IssuedCrewToken; onDone: () => void }) {
  const [copied, setCopied] = React.useState(false)
  const copy = async () => {
    await navigator.clipboard.writeText(token.value)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>Token «{token.name}» creado</DialogTitle>
        <DialogDescription>Cópialo ahora: no se vuelve a mostrar. Seaport solo guarda su huella.</DialogDescription>
      </DialogHeader>
      <div className="min-w-0 space-y-3 py-5">
        <div className="flex items-center gap-2 rounded-lg border bg-muted/40 p-2">
          <Mono className="min-w-0 flex-1 px-1 text-sm break-all select-all" data-testid="issued-token">
            {token.value}
          </Mono>
          <Button size="sm" variant="outline" className="shrink-0" onClick={copy}>
            {copied ? <Check /> : <Copy />} {copied ? "Copiado" : "Copiar"}
          </Button>
        </div>
        <p className="text-xs break-words text-muted-foreground">
          En la máquina del agente: <code>SEAPORT_CREW_TOKEN=&lt;token&gt; seaport connect -- rness --acp</code>
        </p>
      </div>
      <DialogFooter>
        <Button onClick={onDone}>Listo</Button>
      </DialogFooter>
    </>
  )
}
