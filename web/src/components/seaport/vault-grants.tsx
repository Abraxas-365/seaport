import * as React from "react"
import { Link } from "react-router"
import { Plus, Trash2, TriangleAlert, Vault as VaultIcon } from "lucide-react"

import { useAgents, useCan, useCustoms, useDocks, useVaultGrants, useVaults, useVaultsOf } from "@/lib/hooks"
import { plural } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import { cn } from "@/lib/utils"
import type { Channel, VaultMode } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { SegmentedChoice, type ChoiceOption } from "./choice"
import { ConfirmAction } from "./confirm"
import { EmptyState, ErrorState } from "./states"
import { VaultModeChip } from "./vault-mode"
import { toneClasses, vaultModeLabel } from "./tone"

export const vaultModeOptions: ChoiceOption<VaultMode>[] = [
  { value: "read", label: vaultModeLabel.read, description: "Puede leer y buscar", tone: "info" },
  { value: "write", label: vaultModeLabel.write, description: "Puede leer, guardar y borrar entradas", tone: "brand" },
]

/** Channels whose docks take messages from strangers when customs let them in. */
const openChannels: Channel[] = ["whatsapp", "email"]

/**
 * Informational note (never blocking) on a dock granted a vault in write
 * mode whose customs default lets strangers in (allow, or ask: they write
 * once approved) on WhatsApp or email. Renders nothing when the policy
 * can't be read (no customs permission) or doesn't apply.
 */
export function OpenDockNote({ dock, channel, mode }: { dock: string; channel: Channel; mode: VaultMode }) {
  const applies = mode === "write" && openChannels.includes(channel)
  const customs = useCustoms(applies ? dock : undefined)
  const policy = customs.data?.policy.default
  if (!applies || !policy || policy === "deny") return null
  return <OpenDockWarning />
}

/** The note itself (presentational, for the catalogue). */
export function OpenDockWarning({ className }: { className?: string }) {
  return (
    <p className={cn("flex items-start gap-1.5 text-xs", toneClasses.warning.text, className)} role="note">
      <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
      <span>
        Este muelle admite desconocidos y la bóveda es de escritura: cualquiera que entre por él puede hacer que el
        agente guarde o borre entradas.
      </span>
    </p>
  )
}

/**
 * "Dar acceso": a grant of a vault to an agent or a dock (granting again
 * changes the mode). Scoped by what the caller already knows: from a vault
 * you pick the agent or dock; from an agent's or dock's page, the vault.
 */
export function GrantVaultDialog({ vault, agent, dock }: { vault?: string; agent?: string; dock?: string }) {
  const { grant } = useVaultGrants()
  const vaults = useVaults()
  const agents = useAgents()
  const docks = useDocks()
  const [open, setOpen] = React.useState(false)
  const [kind, setKind] = React.useState<"agent" | "dock">("agent")
  const [pick, setPick] = React.useState("")
  const [mode, setMode] = React.useState<VaultMode>("read")

  // From a vault the picker lists agents or docks; from an agent or a dock, vaults.
  const options: { id: string; label: string }[] = vault
    ? kind === "agent"
      ? (agents.data?.items ?? []).map((a) => ({ id: a.id, label: `@${a.name}` }))
      : (docks.data?.items ?? []).map((d) => ({ id: d.id, label: d.name }))
    : (vaults.data?.items ?? []).map((v) => ({ id: v.id, label: v.title ? `${v.title} (${v.name})` : v.name }))
  const submit = () => {
    const input = vault
      ? { vault, kind, target: pick, mode }
      : { vault: pick, kind: agent ? ("agent" as const) : ("dock" as const), target: (agent ?? dock)!, mode }
    grant.mutate(input, { onSuccess: () => setOpen(false) })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setKind("agent")
          setPick("")
          setMode("read")
        }
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant="secondary">
          <Plus /> Dar acceso
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>Dar acceso a una bóveda</DialogTitle>
            <DialogDescription>
              {vault
                ? "Un agente la ve en todas partes; un muelle, mientras el agente trabaja una conversación suya. Con ambos, vale el modo más amplio."
                : agent
                  ? "El agente la verá en todas sus conversaciones."
                  : "Los agentes la verán mientras trabajan una conversación de este muelle."}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            {vault && (
              <Field>
                <FieldLabel>A quién</FieldLabel>
                <SegmentedChoice
                  value={kind}
                  onChange={(k) => {
                    setKind(k)
                    setPick("")
                  }}
                  options={[
                    { value: "agent", label: "Agente" },
                    { value: "dock", label: "Muelle" },
                  ]}
                />
              </Field>
            )}
            <Field>
              <FieldLabel>{vault ? (kind === "agent" ? "Agente" : "Muelle") : "Bóveda"}</FieldLabel>
              <Select value={pick} onValueChange={setPick}>
                <SelectTrigger>
                  <SelectValue placeholder="Elige…" />
                </SelectTrigger>
                <SelectContent>
                  {options.map((o) => (
                    <SelectItem key={o.id} value={o.id}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {options.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  {vault ? (kind === "agent" ? "Todavía no hay agentes." : "Todavía no hay muelles.") : "Todavía no hay bóvedas."}
                </p>
              )}
            </Field>
            <Field>
              <FieldLabel>Modo</FieldLabel>
              <SegmentedChoice value={mode} onChange={setMode} options={vaultModeOptions} />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!pick || grant.isPending}>
              {grant.isPending && <Spinner />} Dar acceso
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * The "Bóvedas" card of an agent's or a dock's page: the vaults it is
 * granted with their mode, each linking to the vault; with write access to
 * vaults, grant another or take one away.
 */
export function VaultsCard({ kind, id, channel }: { kind: "agent" | "dock"; id: string; channel?: Channel }) {
  const can = useCan()
  const write = can(Perm.VaultsWrite)
  const grants = useVaultsOf(kind, id)
  const { revoke } = useVaultGrants()
  const list = grants.data?.vaults ?? []
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2">
              <VaultIcon className="size-4" /> Bóvedas
            </CardTitle>
            <CardDescription>
              {kind === "agent"
                ? "Memoria compartida a la que este agente accede en todas sus conversaciones. Los muelles donde trabaja pueden darle más."
                : "Memoria compartida que los agentes ven mientras trabajan una conversación de este muelle."}
            </CardDescription>
          </div>
          {write && <GrantVaultDialog agent={kind === "agent" ? id : undefined} dock={kind === "dock" ? id : undefined} />}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {grants.isPending ? (
          <Skeleton className="h-16 rounded-lg" />
        ) : grants.isError ? (
          <ErrorState error={grants.error} onRetry={() => grants.refetch()} />
        ) : list.length === 0 ? (
          <EmptyState icon={VaultIcon} title="Sin bóvedas" description="Ninguna bóveda está concedida aquí." />
        ) : (
          <ul className="divide-y rounded-lg border">
            {list.map((a) => (
              <li key={a.vault.id} className="flex flex-wrap items-center gap-3 px-3 py-2">
                <Link to={`/vaults/${a.vault.id}`} className="min-w-0 flex-1 hover:underline">
                  <span className="font-medium">{a.vault.title || a.vault.name}</span>{" "}
                  <span className="font-mono text-xs text-muted-foreground">{a.vault.name}</span>
                </Link>
                <span className="text-xs text-muted-foreground">{plural(a.vault.entries, "entrada", "entradas")}</span>
                <VaultModeChip mode={a.mode} />
                {kind === "dock" && channel && (
                  <div className="basis-full">
                    <OpenDockNote dock={id} channel={channel} mode={a.mode} />
                  </div>
                )}
                {write && (
                  <ConfirmAction
                    trigger={
                      <Button variant="ghost" size="icon-sm" aria-label={`Quitar ${a.vault.name}`}>
                        <Trash2 />
                      </Button>
                    }
                    title={`¿Quitar el acceso a ${a.vault.name}?`}
                    description={<p>La bóveda y sus entradas se conservan.</p>}
                    actionLabel="Quitar"
                    onConfirm={() => revoke.mutateAsync({ vault: a.vault.id, kind, target: id })}
                  />
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
