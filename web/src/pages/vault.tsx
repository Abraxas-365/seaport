import * as React from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router"
import { ArrowLeft, History, Pencil, Plus, Save, Search, Trash2, Undo2, Vault as VaultIcon, X } from "lucide-react"

import { ApiError } from "@/lib/api"
import {
  flatten,
  useCan,
  useDeleteVault,
  useDocks,
  useNow,
  useUpdateVault,
  useVault,
  useVaultEntries,
  useVaultEntry,
  useVaultEntryActions,
  useVaultGrants,
  useVaultSearch,
  useVaultVersions,
} from "@/lib/hooks"
import { formatFull, formatRelative, plural } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { Channel, Provenance, VaultDetail, VaultMode } from "@/lib/types"
import { cn } from "@/lib/utils"
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import {
  AgentChip,
  ChannelIcon,
  ConfirmAction,
  EmptyState,
  ErrorState,
  GrantVaultDialog,
  Markdown,
  Mono,
  OpenDockNote,
  ProvenanceLine,
  StatusBadge,
  VaultModeChip,
} from "@/components/seaport"
import { vaultNamePattern } from "@/pages/vaults"

/** A vault: its entries (search, edit, versions) and who may use it. */
export function VaultPage() {
  const { vaultId = "" } = useParams()
  const vault = useVault(vaultId)
  if (vault.isError) return <ErrorState error={vault.error} onRetry={() => vault.refetch()} className="flex-1" />
  if (!vault.data)
    return (
      <div className="space-y-4 p-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  return <VaultDetailView vault={vault.data} />
}

type Tab = "entries" | "access"

function VaultDetailView({ vault }: { vault: VaultDetail }) {
  const navigate = useNavigate()
  const can = useCan()
  const write = can(Perm.VaultsWrite)
  const remove = useDeleteVault()
  const [params, setParams] = useSearchParams()
  const tab: Tab = params.get("tab") === "access" ? "access" : "entries"
  const setTab = (t: Tab) =>
    setParams(
      (p) => {
        const next = new URLSearchParams(p)
        if (t === "entries") next.delete("tab")
        else next.set("tab", t)
        return next
      },
      { replace: true },
    )
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <div className="border-b px-6 py-5">
        <Link
          to="/vaults"
          className="mb-3 inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" /> Bóvedas
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <span className="inline-flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <VaultIcon className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="truncate font-heading text-xl font-semibold tracking-tight">{vault.title || vault.name}</h1>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
              <Mono>{vault.name}</Mono>
              <span>· {plural(vault.entries, "entrada", "entradas")}</span>
            </div>
          </div>
          {write && (
            <div className="flex items-center gap-2">
              <EditVaultDialog vault={vault} />
              <ConfirmAction
                trigger={
                  <Button variant="outline" size="sm" className="text-destructive">
                    <Trash2 /> Eliminar
                  </Button>
                }
                title={`¿Eliminar ${vault.name}?`}
                description={
                  <p>
                    Se borran para siempre la bóveda, <b>todas sus entradas, versiones y accesos</b>. Los agentes que la
                    usaban dejan de verla. Esto no se puede deshacer.
                  </p>
                }
                destructive
                confirmText={vault.name}
                actionLabel="Eliminar"
                onConfirm={async () => {
                  await remove.mutateAsync(vault.id)
                  navigate("/vaults")
                }}
              />
            </div>
          )}
        </div>
        {vault.description && <p className="mt-3 max-w-3xl text-sm text-muted-foreground">{vault.description}</p>}
      </div>
      <div className="mx-auto max-w-5xl space-y-4 p-6">
        <Tabs value={tab} onValueChange={(t) => setTab(t as Tab)}>
          <TabsList>
            <TabsTrigger value="entries">Entradas</TabsTrigger>
            <TabsTrigger value="access">Accesos</TabsTrigger>
          </TabsList>
        </Tabs>
        {tab === "entries" ? <EntriesTab vault={vault} write={write} /> : <AccessTab vault={vault} write={write} />}
      </div>
    </div>
  )
}

function EditVaultDialog({ vault }: { vault: VaultDetail }) {
  const update = useUpdateVault(vault.id)
  const [open, setOpen] = React.useState(false)
  const [title, setTitle] = React.useState(vault.title)
  const [description, setDescription] = React.useState(vault.description)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setTitle(vault.title)
          setDescription(vault.description)
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Pencil /> Editar
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            update.mutate(
              { title: title.trim(), description: description.trim() },
              { onSuccess: () => setOpen(false) },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Editar bóveda</DialogTitle>
            <DialogDescription>
              El nombre <Mono>{vault.name}</Mono> no cambia: los agentes la llaman así.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field>
              <FieldLabel htmlFor="edit-vault-title">Título</FieldLabel>
              <Input id="edit-vault-title" value={title} onChange={(e) => setTitle(e.target.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="edit-vault-description">Descripción</FieldLabel>
              <Textarea
                id="edit-vault-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending && <Spinner />} Guardar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ── Entradas ───────────────────────────────────────────────────────────

/** Docks by id, to say "desde WhatsApp" on an agent's write (empty without docks:read). */
function useDockLookup() {
  const docks = useDocks()
  return React.useCallback(
    (p: Provenance) => {
      const d = p.dock_id ? docks.data?.items.find((x) => x.id === p.dock_id) : undefined
      return d ? { name: d.name, channel: d.channel } : undefined
    },
    [docks.data],
  )
}

function EntriesTab({ vault, write }: { vault: VaultDetail; write: boolean }) {
  const [params, setParams] = useSearchParams()
  const q = params.get("q") ?? ""
  const selected = params.get("entry") ?? undefined
  const [typed, setTyped] = React.useState(q)
  const entries = useVaultEntries(vault.id)
  const hits = useVaultSearch(vault.id, q)
  const dockOf = useDockLookup()
  const now = useNow()

  const patch = React.useCallback(
    (f: (p: URLSearchParams) => void) =>
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          f(next)
          return next
        },
        { replace: true },
      ),
    [setParams],
  )
  // The search box drives ?q= after a short pause.
  React.useEffect(() => {
    const t = setTimeout(() => {
      const v = typed.trim()
      if (v !== q) patch((p) => (v ? p.set("q", v) : p.delete("q")))
    }, 300)
    return () => clearTimeout(t)
  }, [typed, q, patch])

  const select = (id?: string) => patch((p) => (id ? p.set("entry", id) : p.delete("entry")))
  const rows = q
    ? (hits.data?.hits ?? []).map((h) => ({
        id: h.entry_id,
        name: h.name,
        line: h.snippet,
        provenance: h.provenance,
        updated_at: h.updated_at,
      }))
    : flatten(entries.data).map((e) => ({
        id: e.id,
        name: e.name,
        line: e.first_line,
        provenance: e.provenance,
        updated_at: e.updated_at,
      }))
  const list = q ? hits : entries

  return (
    <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
      <Card className="gap-3 p-3">
        <div className="flex items-center gap-2">
          <div className="relative min-w-0 flex-1">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder="Buscar en la bóveda…"
              aria-label="Buscar en la bóveda"
              className="pl-8"
            />
          </div>
          {write && (
            <NewEntryDialog
              vault={vault.id}
              onCreated={(id) => {
                setTyped("")
                patch((p) => {
                  p.delete("q")
                  p.set("entry", id)
                })
              }}
            />
          )}
        </div>
        {list.isPending && (q || entries.isPending) ? (
          <div className="space-y-2">
            <Skeleton className="h-12 rounded-lg" />
            <Skeleton className="h-12 rounded-lg" />
          </div>
        ) : list.isError ? (
          <ErrorState error={list.error} onRetry={() => list.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={q ? Search : VaultIcon}
            title={q ? "Sin resultados" : "Todavía no hay entradas"}
            description={q ? "Prueba con otras palabras." : "Los agentes con acceso de escritura las crean; también puedes hacerlo tú."}
          />
        ) : (
          <ul className="-mx-1 space-y-0.5">
            {rows.map((r) => (
              <li key={r.id}>
                <button
                  type="button"
                  onClick={() => select(r.id)}
                  aria-current={selected === r.id}
                  className={cn(
                    "flex w-full flex-col gap-0.5 rounded-lg px-2 py-1.5 text-left outline-none transition-colors hover:bg-accent/60 focus-visible:ring-3 focus-visible:ring-ring/50",
                    selected === r.id && "bg-accent",
                  )}
                >
                  <span className="flex items-baseline gap-2">
                    <span className="truncate font-mono text-sm font-medium">{r.name}</span>
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">{formatRelative(r.updated_at, now)}</span>
                  </span>
                  {r.line && <span className="line-clamp-2 text-xs text-muted-foreground">{r.line}</span>}
                  <ProvenanceLine provenance={r.provenance} dock={dockOf(r.provenance)} />
                </button>
              </li>
            ))}
          </ul>
        )}
        {!q && entries.hasNextPage && (
          <Button variant="ghost" size="sm" disabled={entries.isFetchingNextPage} onClick={() => entries.fetchNextPage()}>
            {entries.isFetchingNextPage && <Spinner />} Cargar más
          </Button>
        )}
      </Card>
      {selected ? (
        <EntryView key={selected} vault={vault.id} entry={selected} write={write} onGone={() => select(undefined)} dockOf={dockOf} />
      ) : (
        <Card className="py-0">
          <EmptyState title="Elige una entrada" description="Se muestra aquí, con sus versiones." />
        </Card>
      )}
    </div>
  )
}

function NewEntryDialog({ vault, onCreated }: { vault: string; onCreated: (id: string) => void }) {
  const { create } = useVaultEntryActions(vault)
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [body, setBody] = React.useState("")
  const nameOk = vaultNamePattern.test(name)
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) {
          setName("")
          setBody("")
          create.reset()
        }
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus /> Nueva entrada
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate(
              { name, body },
              {
                onSuccess: (en) => {
                  setOpen(false)
                  onCreated(en.id)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>Nueva entrada</DialogTitle>
            <DialogDescription>Una nota en Markdown. Sin contraseñas ni claves: para eso están los secretos del agente.</DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field>
              <FieldLabel htmlFor="entry-name">Nombre</FieldLabel>
              <Input
                id="entry-name"
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                placeholder="proveedores"
                className="font-mono"
                autoComplete="off"
                aria-invalid={name !== "" && !nameOk}
              />
              <FieldDescription>
                1–64 letras minúsculas, dígitos, <Mono>-</Mono> o <Mono>_</Mono>. Si ya existe, no se sobrescribe.
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="entry-body">Contenido</FieldLabel>
              <Textarea id="entry-body" value={body} onChange={(e) => setBody(e.target.value)} rows={10} className="font-mono" />
            </Field>
            {create.error && <FormError error={create.error} />}
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!nameOk || body.trim() === "" || create.isPending}>
              {create.isPending && <Spinner />} Crear
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** An API error next to the form it came from, in the API's own words. */
function FormError({ error }: { error: Error }) {
  const conflict = error instanceof ApiError && error.status === 409
  return (
    <p role="alert" className="text-sm text-destructive">
      {conflict ? "Ya existe una entrada con ese nombre, o alguien la guardó mientras tanto." : error.message}
    </p>
  )
}

function EntryView({
  vault,
  entry: id,
  write,
  onGone,
  dockOf,
}: {
  vault: string
  entry: string
  write: boolean
  onGone: () => void
  dockOf: (p: Provenance) => { name: string; channel: Channel } | undefined
}) {
  const entry = useVaultEntry(vault, id)
  const { save, remove, revert } = useVaultEntryActions(vault)
  const [editing, setEditing] = React.useState(false)
  const [draft, setDraft] = React.useState("")
  const [showVersions, setShowVersions] = React.useState(false)
  const versions = useVaultVersions(vault, id, showVersions)

  if (entry.isError) {
    // Deleted meanwhile (by an agent or someone else): back to the list.
    if (entry.error instanceof ApiError && entry.error.status === 404)
      return (
        <Card className="py-0">
          <EmptyState
            title="La entrada ya no existe"
            description="Alguien la eliminó."
            action={
              <Button variant="outline" size="sm" onClick={onGone}>
                Cerrar
              </Button>
            }
          />
        </Card>
      )
    return <ErrorState error={entry.error} onRetry={() => entry.refetch()} />
  }
  if (!entry.data) return <Skeleton className="h-64 rounded-xl" />
  const e = entry.data
  const conflict = save.error instanceof ApiError && save.error.status === 409

  const startEdit = () => {
    setDraft(e.body)
    save.reset()
    setEditing(true)
  }
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <CardTitle className="truncate font-mono text-base">{e.name}</CardTitle>
            <CardDescription className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
              <span>v{e.version}</span>
              <ProvenanceLine provenance={e.provenance} dock={dockOf(e.provenance)} />
              <span title={formatFull(e.updated_at)}>· {formatRelative(e.updated_at)}</span>
            </CardDescription>
          </div>
          {!editing && (
            <div className="flex items-center gap-1.5">
              <Button variant={showVersions ? "secondary" : "outline"} size="sm" onClick={() => setShowVersions((v) => !v)}>
                <History /> Versiones
              </Button>
              {write && (
                <>
                  <Button variant="outline" size="sm" onClick={startEdit} aria-label={`Editar ${e.name}`}>
                    <Pencil /> Editar
                  </Button>
                  <ConfirmAction
                    trigger={
                      <Button variant="outline" size="sm" className="text-destructive" aria-label={`Eliminar ${e.name}`}>
                        <Trash2 /> Eliminar
                      </Button>
                    }
                    title={`¿Eliminar ${e.name}?`}
                    description={<p>Sus versiones se conservan mientras exista la bóveda, pero la entrada deja de verse.</p>}
                    destructive
                    actionLabel="Eliminar"
                    onConfirm={async () => {
                      await remove.mutateAsync(id)
                      onGone()
                    }}
                  />
                </>
              )}
            </div>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {editing ? (
          <form
            className="space-y-3"
            onSubmit={(ev) => {
              ev.preventDefault()
              save.mutate({ entry: id, body: draft, base: e.version }, { onSuccess: () => setEditing(false) })
            }}
          >
            <Textarea
              value={draft}
              onChange={(ev) => setDraft(ev.target.value)}
              rows={14}
              className="font-mono"
              aria-label={`Contenido de ${e.name}`}
            />
            {save.error &&
              (conflict ? (
                <p role="alert" className="text-sm text-destructive">
                  Alguien guardó mientras tanto; recarga.{" "}
                  <button
                    type="button"
                    className="underline underline-offset-2"
                    onClick={() => {
                      setEditing(false)
                      save.reset()
                      void entry.refetch()
                    }}
                  >
                    Recargar
                  </button>
                </p>
              ) : (
                <FormError error={save.error} />
              ))}
            <div className="flex gap-2">
              <Button type="submit" size="sm" disabled={draft.trim() === "" || draft === e.body || save.isPending}>
                {save.isPending ? <Spinner /> : <Save />} Guardar
              </Button>
              <Button type="button" variant="outline" size="sm" onClick={() => setEditing(false)}>
                <X /> Cancelar
              </Button>
            </div>
          </form>
        ) : (
          <Markdown text={e.body} className="text-sm" />
        )}
        {showVersions && !editing && (
          <div className="space-y-2 border-t pt-4">
            <h3 className="text-sm font-semibold">Versiones</h3>
            {versions.isPending ? (
              <Skeleton className="h-16 rounded-lg" />
            ) : versions.isError ? (
              <ErrorState error={versions.error} onRetry={() => versions.refetch()} />
            ) : (
              <ul className="divide-y rounded-lg border">
                {(versions.data.versions ?? []).map((v) => (
                  <li key={v.version} className="space-y-1.5 px-3 py-2">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="text-sm font-medium">v{v.version}</span>
                      {v.version === e.version && (
                        <StatusBadge tone="success" dot={false}>
                          actual
                        </StatusBadge>
                      )}
                      <ProvenanceLine provenance={v.provenance} dock={dockOf(v.provenance)} />
                      <span className="text-xs text-muted-foreground">{formatFull(v.created_at)}</span>
                      {write && v.version !== e.version && (
                        <ConfirmAction
                          trigger={
                            <Button variant="ghost" size="sm" className="ml-auto">
                              <Undo2 /> Revertir a esta versión
                            </Button>
                          }
                          title={`¿Revertir a la v${v.version}?`}
                          description={<p>Su texto se guarda como una versión nueva; las demás se conservan.</p>}
                          actionLabel="Revertir"
                          onConfirm={() => revert.mutateAsync({ entry: id, version: v.version })}
                        />
                      )}
                    </div>
                    <details className="text-xs text-muted-foreground">
                      <summary className="cursor-pointer select-none">Ver texto</summary>
                      <pre className="mt-1.5 max-h-60 overflow-auto rounded-md bg-muted px-2.5 py-2 font-mono whitespace-pre-wrap">
                        {v.body}
                      </pre>
                    </details>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// ── Accesos ────────────────────────────────────────────────────────────

function AccessTab({ vault, write }: { vault: VaultDetail; write: boolean }) {
  const { grant, revoke } = useVaultGrants()
  const agents = vault.agents ?? []
  const docks = vault.docks ?? []
  const other = (m: VaultMode): VaultMode => (m === "write" ? "read" : "write")
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Un agente ve la bóveda en todas partes; un muelle, mientras el agente trabaja una conversación suya. Si tiene
          ambos accesos, vale el modo más amplio.
        </p>
        {write && <GrantVaultDialog vault={vault.id} />}
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Agentes</CardTitle>
        </CardHeader>
        <CardContent>
          {agents.length === 0 ? (
            <p className="text-sm text-muted-foreground">Ningún agente la tiene concedida directamente.</p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {agents.map((g) => (
                <li key={g.agent_id} className="flex flex-wrap items-center gap-3 px-3 py-2">
                  <Link to={`/agents/${g.agent_id}`}>
                    <AgentChip name={g.name} />
                  </Link>
                  <VaultModeChip mode={g.mode} />
                  <span className="flex-1" />
                  {write && (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={grant.isPending}
                        onClick={() => grant.mutate({ vault: vault.id, kind: "agent", target: g.agent_id, mode: other(g.mode) })}
                      >
                        Pasar a {other(g.mode) === "write" ? "escritura" : "lectura"}
                      </Button>
                      <ConfirmAction
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Quitar a ${g.name}`}>
                            <Trash2 />
                          </Button>
                        }
                        title={`¿Quitar el acceso de @${g.name}?`}
                        description={<p>Puede seguir viéndola por los muelles que la tengan.</p>}
                        actionLabel="Quitar"
                        onConfirm={() => revoke.mutateAsync({ vault: vault.id, kind: "agent", target: g.agent_id })}
                      />
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Muelles</CardTitle>
        </CardHeader>
        <CardContent>
          {docks.length === 0 ? (
            <p className="text-sm text-muted-foreground">Ningún muelle la tiene concedida.</p>
          ) : (
            <ul className="divide-y rounded-lg border">
              {docks.map((g) => (
                <li key={g.dock_id} className="flex flex-wrap items-center gap-3 px-3 py-2">
                  <Link to={`/docks/${g.dock_id}`} className="flex min-w-0 items-center gap-2 hover:underline">
                    <ChannelIcon channel={g.channel} size="sm" className="rounded-md" />
                    <span className="truncate text-sm font-medium">{g.name}</span>
                  </Link>
                  <VaultModeChip mode={g.mode} />
                  <span className="flex-1" />
                  {write && (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={grant.isPending}
                        onClick={() => grant.mutate({ vault: vault.id, kind: "dock", target: g.dock_id, mode: other(g.mode) })}
                      >
                        Pasar a {other(g.mode) === "write" ? "escritura" : "lectura"}
                      </Button>
                      <ConfirmAction
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Quitar ${g.name}`}>
                            <Trash2 />
                          </Button>
                        }
                        title={`¿Quitar el acceso de ${g.name}?`}
                        description={<p>Los agentes dejan de verla en sus conversaciones, salvo que la tengan por sí mismos.</p>}
                        actionLabel="Quitar"
                        onConfirm={() => revoke.mutateAsync({ vault: vault.id, kind: "dock", target: g.dock_id })}
                      />
                    </>
                  )}
                  <div className="basis-full empty:hidden">
                    <OpenDockNote dock={g.dock_id} channel={g.channel} mode={g.mode} />
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
