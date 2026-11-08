import * as React from "react"
import { Outlet, useNavigate, useParams } from "react-router"
import { Inbox as InboxIcon, Search } from "lucide-react"

import { flatten, useDocks, useInbox, type InboxFilter } from "@/lib/hooks"
import { conversationName } from "@/lib/format"
import type { CustomsStatus } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConversationRow, EmptyState, ErrorState, LiveIndicator } from "@/components/seaport"

type StatusTab = "all" | CustomsStatus

export function InboxPage() {
  const { conversationId } = useParams()
  const navigate = useNavigate()
  const [tab, setTab] = React.useState<StatusTab>("allowed")
  const [dock, setDock] = React.useState<string>("all")
  const [q, setQ] = React.useState("")

  const filter: InboxFilter = {
    status: tab === "all" ? undefined : tab,
    dock_id: dock === "all" ? undefined : dock,
  }
  const inbox = useInbox(filter)
  const docks = useDocks()
  const dockName = React.useMemo(() => {
    const m = new Map(docks.data?.items.map((d) => [d.id, d.name]))
    return (id: string) => m.get(id)
  }, [docks.data])
  const multiDock = (docks.data?.items.length ?? 0) > 1

  const items = flatten(inbox.data).filter((c) => {
    if (!q.trim()) return true
    const needle = q.trim().toLowerCase()
    return (
      conversationName(c).toLowerCase().includes(needle) ||
      c.external_id.toLowerCase().includes(needle) ||
      c.handles.some((h) => h.toLowerCase().includes(needle))
    )
  })

  return (
    <div className="flex min-h-0 flex-1">
      <aside
        className={cn(
          "flex w-full min-w-0 flex-col border-r md:w-[22rem] lg:w-[24rem]",
          conversationId && "hidden md:flex",
        )}
      >
        <div className="space-y-3 border-b p-3">
          <div className="flex items-center justify-between px-1">
            <h1 className="font-heading text-lg font-semibold tracking-tight">Bandeja</h1>
            <LiveIndicator className="hidden md:inline-flex" />
          </div>
          <InputGroup>
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput placeholder="Buscar chat, número…" value={q} onChange={(e) => setQ(e.target.value)} />
          </InputGroup>
          <div className="flex gap-2">
            <Tabs value={tab} onValueChange={(v) => setTab(v as StatusTab)} className="min-w-0 flex-1">
              <TabsList className="w-full">
                <TabsTrigger value="allowed">Activos</TabsTrigger>
                <TabsTrigger value="pending">Aduana</TabsTrigger>
                <TabsTrigger value="denied">Bloq.</TabsTrigger>
                <TabsTrigger value="all">Todos</TabsTrigger>
              </TabsList>
            </Tabs>
            {multiDock && (
              <Select value={dock} onValueChange={setDock}>
                <SelectTrigger className="w-28" aria-label="Muelle">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent align="end">
                  <SelectItem value="all">Todos</SelectItem>
                  {docks.data!.items.map((d) => (
                    <SelectItem key={d.id} value={d.id}>
                      {d.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </div>
        </div>
        <ScrollArea className="min-h-0 flex-1">
          <div className="space-y-0.5 p-2">
            {inbox.isPending ? (
              Array.from({ length: 6 }, (_, i) => <RowSkeleton key={i} />)
            ) : inbox.isError ? (
              <ErrorState error={inbox.error} onRetry={() => inbox.refetch()} />
            ) : items.length === 0 ? (
              <EmptyState
                icon={InboxIcon}
                title={q ? "Nada coincide" : emptyTitle[tab]}
                description={q ? "Prueba con otro nombre o número." : emptyHint[tab]}
              />
            ) : (
              items.map((c) => (
                <ConversationRow
                  key={c.id}
                  item={c}
                  active={c.id === conversationId}
                  dockName={multiDock && dock === "all" ? dockName(c.dock_id) : undefined}
                  onSelect={() => navigate(`/inbox/${c.id}`)}
                />
              ))
            )}
            {inbox.hasNextPage && (
              <Button
                variant="ghost"
                size="sm"
                className="w-full"
                disabled={inbox.isFetchingNextPage}
                onClick={() => inbox.fetchNextPage()}
              >
                {inbox.isFetchingNextPage ? "Cargando…" : "Ver más"}
              </Button>
            )}
          </div>
        </ScrollArea>
      </aside>
      <section className={cn("min-w-0 flex-1 bg-canvas", !conversationId && "hidden md:block")}>
        <Outlet />
      </section>
    </div>
  )
}

const emptyTitle: Record<StatusTab, string> = {
  allowed: "Sin conversaciones activas",
  pending: "La aduana está vacía",
  denied: "Nadie bloqueado",
  all: "Todavía no llegan barcos",
}

const emptyHint: Record<StatusTab, string> = {
  allowed: "Cuando permitas un chat, sus mensajes despiertan a tu agente y aparecen aquí.",
  pending: "Los chats nuevos esperan aquí hasta que decidas si entran.",
  denied: "Los chats bloqueados se guardan pero nunca llegan al agente.",
  all: "Vincula un muelle y escribe al número para ver el primer mensaje.",
}

function RowSkeleton() {
  return (
    <div className="flex items-center gap-3 px-3 py-2.5">
      <Skeleton className="size-9 rounded-full" />
      <div className="flex-1 space-y-2">
        <Skeleton className="h-3.5 w-2/3" />
        <Skeleton className="h-3 w-full" />
      </div>
    </div>
  )
}

export function InboxIndex() {
  return (
    <div className="flex h-full items-center justify-center">
      <EmptyState
        icon={InboxIcon}
        title="Elige una conversación"
        description="Lee lo que llegó, mira qué hace el agente y responde a mano si hace falta."
      />
    </div>
  )
}
