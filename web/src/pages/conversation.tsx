import * as React from "react"
import { Link, useParams } from "react-router"
import {
  ArrowLeft,
  Ban,
  Bot,
  Check,
  Info,
  MessageSquareDashed,
  ShieldQuestion,
  Sparkles,
} from "lucide-react"

import {
  flatten,
  useConversation,
  useCrew,
  useDocks,
  useSend,
  useSetAccess,
  useSetGroupMode,
  useShips,
} from "@/lib/hooks"
import { conversationName, dayLabel, formatFull, prettyHandle } from "@/lib/format"
import type { Access, Conversation, GroupMode, Ship } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ChatAvatar,
  Composer,
  DataItem,
  DataList,
  DayDivider,
  EmptyState,
  ErrorState,
  Mono,
  SegmentedChoice,
  ShipBubble,
  StatusBadge,
  customsLabel,
  customsTone,
  type ChoiceOption,
} from "@/components/puerto"

const accessOptions: ChoiceOption<Access>[] = [
  { value: "auto", label: "Reglas", icon: Sparkles, description: "Decide la aduana del muelle", tone: "brand" },
  { value: "allow", label: "Permitir", icon: Check, description: "Siempre entra", tone: "success" },
  { value: "deny", label: "Bloquear", icon: Ban, description: "Nunca entra", tone: "danger" },
]

const groupOptions: ChoiceOption<GroupMode>[] = [
  { value: "", label: "Como el muelle", description: "Usa el modo de grupos del muelle" },
  { value: "mentions", label: "Solo menciones", description: "Solo entran mensajes que nombran al agente" },
  { value: "all", label: "Todo", description: "Entran todos los mensajes" },
]

export function ConversationPage() {
  const { conversationId = "" } = useParams()
  const conv = useConversation(conversationId)
  const ships = useShips(conversationId)
  const crew = useCrew()
  const send = useSend(conversationId)

  const working = crew.data?.agents.some((a) => a.deliveries.some((d) => d.conversation_id === conversationId))

  if (conv.isError) return <ErrorState error={conv.error} onRetry={() => conv.refetch()} className="h-full" />
  if (!conv.data) return <ConversationSkeleton />

  const c = conv.data
  const allowed = c.status === "allowed"
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ConversationHeader conversation={c} working={!!working} />
      {c.status === "pending" && <PendingBanner conversation={c} />}
      <ShipList
        conversation={c}
        ships={flatten(ships.data)}
        loading={ships.isPending}
        hasMore={!!ships.hasNextPage}
        loadingMore={ships.isFetchingNextPage}
        onLoadMore={() => ships.fetchNextPage()}
        working={!!working}
      />
      <Composer
        key={c.id}
        disabled={!allowed}
        disabledReason={
          c.status === "pending"
            ? "Permite este chat para poder responder."
            : "Este chat está bloqueado; no se le puede escribir."
        }
        onSend={(t) => send.mutateAsync(t)}
      />
    </div>
  )
}

function ConversationHeader({ conversation: c, working }: { conversation: Conversation; working: boolean }) {
  const name = conversationName(c)
  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b bg-background px-3 md:px-4">
      <Button asChild variant="ghost" size="icon" className="md:hidden" aria-label="Volver">
        <Link to="/inbox">
          <ArrowLeft />
        </Link>
      </Button>
      <ChatAvatar name={c.kind === "self" ? "" : name} seed={c.id} kind={c.kind} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-semibold">{name}</div>
        <div className="flex items-center gap-1.5 truncate text-xs text-muted-foreground">
          {working ? (
            <span className="flex items-center gap-1 font-medium text-info">
              <Bot className="size-3.5 motion-safe:animate-pulse" /> el agente está respondiendo…
            </span>
          ) : (
            <span className="truncate">
              {c.kind === "group" ? "Grupo" : prettyHandle(c.external_id)}
            </span>
          )}
        </div>
      </div>
      <StatusBadge tone={customsTone[c.status]} className="hidden sm:inline-flex">
        {customsLabel[c.status]}
      </StatusBadge>
      <DetailsSheet conversation={c} />
    </header>
  )
}

function PendingBanner({ conversation: c }: { conversation: Conversation }) {
  const setAccess = useSetAccess()
  return (
    <div className="flex flex-wrap items-center gap-3 border-b bg-warning/10 px-4 py-2.5 text-sm">
      <ShieldQuestion className="size-4 shrink-0 text-warning" />
      <span className="min-w-0 flex-1">
        <b>En aduana.</b> Sus mensajes se guardan pero no despiertan al agente hasta que lo permitas.
      </span>
      <div className="flex gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={setAccess.isPending}
          onClick={() => setAccess.mutate({ id: c.id, access: "deny" })}
        >
          <Ban /> Bloquear
        </Button>
        <Button size="sm" disabled={setAccess.isPending} onClick={() => setAccess.mutate({ id: c.id, access: "allow" })}>
          <Check /> Permitir
        </Button>
      </div>
    </div>
  )
}

function ShipList({
  conversation,
  ships,
  loading,
  hasMore,
  loadingMore,
  onLoadMore,
  working,
}: {
  conversation: Conversation
  ships: Ship[]
  loading: boolean
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  working: boolean
}) {
  const ref = React.useRef<HTMLDivElement>(null)
  const ordered = React.useMemo(() => ships.slice().reverse(), [ships])
  const newest = ships[0]?.id
  const stick = React.useRef(true)

  // Follow new ships when the user is at the bottom.
  React.useLayoutEffect(() => {
    const el = ref.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [newest, working, loading])

  // Keep position when older pages load at the top.
  const prevHeight = React.useRef(0)
  React.useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    if (loadingMore) prevHeight.current = el.scrollHeight
    else if (prevHeight.current) {
      el.scrollTop += el.scrollHeight - prevHeight.current
      prevHeight.current = 0
    }
  }, [loadingMore, ships.length])

  if (loading)
    return (
      <div className="flex-1 space-y-3 p-6">
        <Skeleton className="h-10 w-2/5 rounded-2xl" />
        <Skeleton className="ml-auto h-14 w-1/2 rounded-2xl" />
        <Skeleton className="h-8 w-1/3 rounded-2xl" />
      </div>
    )

  const group = conversation.kind === "group"
  return (
    <div
      ref={ref}
      onScroll={(e) => {
        const el = e.currentTarget
        stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
      }}
      className="scrollbar-thin min-h-0 flex-1 overflow-y-auto px-3 pb-4 md:px-8"
    >
      {hasMore && (
        <div className="flex justify-center pt-4">
          <Button variant="outline" size="sm" className="rounded-full" disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? "Cargando…" : "Mensajes anteriores"}
          </Button>
        </div>
      )}
      {ordered.length === 0 ? (
        <EmptyState
          icon={MessageSquareDashed}
          title="Sin mensajes"
          description="Cuando llegue un barco a este chat aparecerá aquí."
          className="h-full"
        />
      ) : (
        <div className="mx-auto max-w-3xl">
          {ordered.map((s, i) => {
            const prev = ordered[i - 1]
            const newDay = !prev || dayLabel(prev.sent_at) !== dayLabel(s.sent_at)
            const grouped =
              !newDay &&
              prev.direction === s.direction &&
              prev.sender_id === s.sender_id &&
              new Date(s.sent_at).getTime() - new Date(prev.sent_at).getTime() < 5 * 60_000
            return (
              <React.Fragment key={s.id}>
                {newDay && <DayDivider label={dayLabel(s.sent_at)} />}
                <ShipBubble ship={s} grouped={grouped} showSender={group && !grouped} />
              </React.Fragment>
            )
          })}
          {working && <TypingBubble />}
        </div>
      )}
    </div>
  )
}

function TypingBubble() {
  return (
    <div className="mt-3 flex justify-end">
      <div className="flex items-center gap-1 rounded-2xl rounded-tr-md bg-bubble-out/15 px-3 py-2.5 ring-1 ring-primary/15">
        {[0, 150, 300].map((d) => (
          <span
            key={d}
            className="size-1.5 rounded-full bg-primary/70 motion-safe:animate-bounce"
            style={{ animationDelay: `${d}ms` }}
          />
        ))}
      </div>
    </div>
  )
}

function DetailsSheet({ conversation: c }: { conversation: Conversation }) {
  const docks = useDocks()
  const setAccess = useSetAccess()
  const setGroupMode = useSetGroupMode()
  const dock = docks.data?.items.find((d) => d.id === c.dock_id)
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Detalles">
          <Info />
        </Button>
      </SheetTrigger>
      <SheetContent className="w-full gap-0 sm:max-w-md">
        <SheetHeader className="border-b">
          <div className="flex items-center gap-3">
            <ChatAvatar name={c.kind === "self" ? "" : conversationName(c)} seed={c.id} kind={c.kind} size="lg" />
            <div className="min-w-0">
              <SheetTitle className="truncate">{conversationName(c)}</SheetTitle>
              <SheetDescription>
                {c.kind === "group" ? "Grupo" : c.kind === "self" ? "Tu propio chat" : "Chat directo"}
                {dock && <> · {dock.name}</>}
              </SheetDescription>
            </div>
          </div>
        </SheetHeader>
        <div className="scrollbar-thin flex-1 space-y-6 overflow-y-auto p-4">
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold">Aduana</h3>
              <StatusBadge tone={customsTone[c.status]}>{customsLabel[c.status]}</StatusBadge>
            </div>
            <SegmentedChoice
              aria-label="Acceso"
              value={c.access}
              options={accessOptions}
              disabled={setAccess.isPending}
              onChange={(access) => setAccess.mutate({ id: c.id, access })}
              className="w-full sm:w-full"
            />
            <p className="text-xs text-muted-foreground">
              {c.access === "auto"
                ? "Lo decide la aduana del muelle (reglas y política por defecto)."
                : c.access === "allow"
                  ? "Forzado: sus mensajes siempre despiertan al agente."
                  : "Forzado: sus mensajes se guardan pero nunca llegan al agente."}{" "}
              Permitir no entrega los mensajes retenidos; el agente puede leerlos con <code>history</code>.
            </p>
          </div>
          {c.kind === "group" && (
            <div className="space-y-2">
              <h3 className="text-sm font-semibold">Mensajes del grupo</h3>
              <SegmentedChoice
                aria-label="Modo del grupo"
                size="sm"
                value={c.group_mode ?? ""}
                options={groupOptions}
                disabled={setGroupMode.isPending}
                onChange={(mode) => setGroupMode.mutate({ id: c.id, mode })}
                className="w-full sm:w-full"
              />
            </div>
          )}
          <Separator />
          <DataList>
            <DataItem label="Canal">{c.channel === "whatsapp" ? "WhatsApp" : "API"}</DataItem>
            <DataItem label="Chat">
              <Mono title={c.external_id}>{c.external_id}</Mono>
            </DataItem>
            {c.handles.length > 0 && (
              <DataItem label="Identidades">
                <div className="flex flex-wrap gap-1">
                  {c.handles.map((h) => (
                    <span key={h} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.7rem]" title={h}>
                      {prettyHandle(h)}
                    </span>
                  ))}
                </div>
              </DataItem>
            )}
            <DataItem label="Última actividad">{formatFull(c.last_activity_at)}</DataItem>
            <DataItem label="Desde">{formatFull(c.created_at)}</DataItem>
            <DataItem label="ID">
              <Mono>{c.id}</Mono>
            </DataItem>
          </DataList>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function ConversationSkeleton() {
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-14 items-center gap-3 border-b bg-background px-4">
        <Skeleton className="size-7 rounded-full" />
        <Skeleton className="h-4 w-40" />
      </div>
      <div className="flex-1" />
    </div>
  )
}
