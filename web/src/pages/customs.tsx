import * as React from "react"
import { useNavigate } from "react-router"
import {
  Ban,
  Check,
  CircleHelp,
  Megaphone,
  MessagesSquare,
  Plus,
  ShieldCheck,
  Trash2,
} from "lucide-react"

import {
  flatten,
  useAddRule,
  useCan,
  useCustoms,
  useDocks,
  useInbox,
  useRemoveRule,
  useSetAccess,
  useSetPolicy,
} from "@/lib/hooks"
import { conversationName, formatRelative, formatStamp, shipPreview } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { DefaultAccess, Dock, Effect, GroupMode } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ChannelIcon,
  ChatAvatar,
  ChoiceCards,
  CountBadge,
  EmptyState,
  ErrorState,
  NoAccess,
  PageHeader,
  Section,
  SegmentedChoice,
  StatusBadge,
  type ChoiceOption,
} from "@/components/seaport"

const defaultOptions: ChoiceOption<DefaultAccess>[] = [
  {
    value: "ask",
    label: "Preguntar",
    icon: CircleHelp,
    tone: "warning",
    description: "Los chats nuevos esperan en aduana hasta que los permitas.",
  },
  {
    value: "allow",
    label: "Permitir",
    icon: Check,
    tone: "success",
    description: "Cualquiera que escriba despierta al agente.",
  },
  {
    value: "deny",
    label: "Bloquear",
    icon: Ban,
    tone: "danger",
    description: "Solo entran los chats que permitan tus reglas.",
  },
]

const groupModeOptions: ChoiceOption<Exclude<GroupMode, "">>[] = [
  { value: "mentions", label: "Solo menciones", icon: Megaphone },
  { value: "all", label: "Todo", icon: MessagesSquare },
]

export function CustomsPage() {
  const docks = useDocks()
  const can = useCan()
  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <PageHeader
        title="Aduana"
        description="Decide qué barcos entran al puerto y despiertan a tu agente."
      />
      <div className="mx-auto max-w-5xl space-y-10 p-6">
        {can(Perm.ConversationsRead) && <PendingQueue />}
        {!can(Perm.DocksRead) ? (
          <NoAccess what="ver los muelles" />
        ) : docks.isPending ? (
          <Skeleton className="h-64 rounded-xl" />
        ) : docks.isError ? (
          <ErrorState error={docks.error} onRetry={() => docks.refetch()} />
        ) : (
          docks.data.items.map((d) => <DockCustoms key={d.id} dock={d} />)
        )}
      </div>
    </div>
  )
}

function PendingQueue() {
  const navigate = useNavigate()
  const pending = useInbox({ status: "pending" })
  const setAccess = useSetAccess()
  const can = useCan()
  const items = flatten(pending.data)
  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          Esperando decisión <CountBadge tone="warning" count={pending.data?.pages[0]?.page.total ?? 0} />
        </span>
      }
      description="Chats que escribieron y no tienen permiso todavía. Sus mensajes esperan aquí: al permitir el chat, el agente recibe los de las últimas 24 horas."
    >
      <Card className="gap-0 py-0">
        {pending.isPending ? (
          <CardContent className="p-4">
            <Skeleton className="h-12" />
          </CardContent>
        ) : items.length === 0 ? (
          <EmptyState icon={ShieldCheck} title="Nadie esperando" description="Todo lo que llegó ya tiene decisión." />
        ) : (
          <ul className="divide-y">
            {items.map((c) => (
              <li key={c.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <button
                  type="button"
                  className="flex min-w-0 flex-1 items-center gap-3 text-left"
                  onClick={() => navigate(`/inbox/${c.id}`)}
                >
                  <ChatAvatar name={conversationName(c)} seed={c.id} kind={c.kind} />
                  <span className="min-w-0">
                    <span className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">{conversationName(c)}</span>
                      <CountBadge tone="warning" count={c.held} title={`${c.held} retenidos`} />
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {formatStamp(c.last_activity_at)} · {shipPreview(c.last_ship) || "sin mensajes"}
                    </span>
                  </span>
                </button>
                {can(Perm.ConversationsWrite) && (
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={setAccess.isPending}
                      onClick={() => setAccess.mutate({ id: c.id, access: "deny" })}
                    >
                      <Ban /> Bloquear
                    </Button>
                    <Button
                      size="sm"
                      disabled={setAccess.isPending}
                      onClick={() => setAccess.mutate({ id: c.id, access: "allow" })}
                    >
                      <Check /> Permitir
                    </Button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>
      {pending.hasNextPage && (
        <Button variant="ghost" size="sm" onClick={() => pending.fetchNextPage()}>
          Ver más
        </Button>
      )}
    </Section>
  )
}

function DockCustoms({ dock }: { dock: Dock }) {
  const customs = useCustoms(dock.id)
  const setPolicy = useSetPolicy(dock.id)
  const can = useCan()
  const readOnly = !can(Perm.CustomsWrite)
  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          <ChannelIcon channel={dock.channel} size="sm" /> {dock.name}
        </span>
      }
      description="Política por defecto y reglas de este muelle."
    >
      {customs.isPending ? (
        <Skeleton className="h-48 rounded-xl" />
      ) : customs.isError ? (
        <ErrorState error={customs.error} onRetry={() => customs.refetch()} />
      ) : (
        <Card className="gap-0 py-0">
          <CardContent className="space-y-6 p-4">
            <div className="space-y-2">
              <h3 className="text-sm font-medium">Cuando escribe alguien nuevo</h3>
              <ChoiceCards
                value={customs.data.policy.default}
                options={defaultOptions}
                disabled={readOnly || setPolicy.isPending}
                onChange={(v) => setPolicy.mutate({ default: v })}
              />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h3 className="text-sm font-medium">En grupos</h3>
                <p className="text-xs text-muted-foreground">
                  "Solo menciones" retiene los mensajes que no nombran al agente.
                </p>
              </div>
              <SegmentedChoice
                aria-label="Modo de grupos"
                size="sm"
                value={customs.data.policy.group_mode}
                options={groupModeOptions}
                disabled={readOnly || setPolicy.isPending}
                onChange={(v) => setPolicy.mutate({ group_mode: v })}
              />
            </div>
            <RulesEditor dockId={dock.id} rules={customs.data.rules} readOnly={readOnly} />
          </CardContent>
        </Card>
      )}
    </Section>
  )
}

function RulesEditor({
  dockId,
  rules,
  readOnly,
}: {
  dockId: string
  rules: { id: string; effect: Effect; pattern: string; created_at: string }[]
  readOnly: boolean
}) {
  const add = useAddRule(dockId)
  const remove = useRemoveRule(dockId)
  const [effect, setEffect] = React.useState<Effect>("allow")
  const [pattern, setPattern] = React.useState("")
  const sorted = rules.slice().sort((a, b) => (a.effect === b.effect ? 0 : a.effect === "deny" ? -1 : 1))

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-medium">Reglas</h3>
        <p className="text-xs text-muted-foreground">
          Números (<code>+34600…</code>), repositorios (<code>acme/*</code>), canales de Slack, proyectos de Jira, <code>github:outsider</code> o patrones con <code>*</code>. Bloquear gana
          sobre permitir; un permiso o bloqueo manual en el chat gana sobre todo.
        </p>
      </div>
      {sorted.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {sorted.map((r) => (
            <li key={r.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <StatusBadge tone={r.effect === "allow" ? "success" : "danger"} className="w-20 justify-center">
                {r.effect === "allow" ? "permitir" : "bloquear"}
              </StatusBadge>
              <code className="min-w-0 flex-1 truncate font-mono text-[0.8rem]">{r.pattern}</code>
              <span className="hidden text-xs text-muted-foreground sm:inline">{formatRelative(r.created_at)}</span>
              {!readOnly && (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Quitar regla"
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(r.id)}
                >
                  <Trash2 />
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && (
        <form
          className="flex flex-col gap-2 sm:flex-row"
          onSubmit={(e) => {
            e.preventDefault()
            if (!pattern.trim()) return
            add.mutate({ effect, pattern: pattern.trim() }, { onSuccess: () => setPattern("") })
          }}
        >
          <Select value={effect} onValueChange={(v) => setEffect(v as Effect)}>
            <SelectTrigger className="sm:w-32" aria-label="Efecto">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="allow">Permitir</SelectItem>
              <SelectItem value="deny">Bloquear</SelectItem>
            </SelectContent>
          </Select>
          <Input
            placeholder="+51999123436, 1203…@g.us, +51*"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            className="font-mono text-sm"
          />
          <Button type="submit" variant="secondary" disabled={!pattern.trim() || add.isPending}>
            <Plus /> Agregar
          </Button>
        </form>
      )}
    </div>
  )
}
