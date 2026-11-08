import { Bot } from "lucide-react"

import { conversationName, formatStamp, shipPreview } from "@/lib/format"
import type { InboxItem } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ChatAvatar } from "./chat-avatar"
import { CountBadge } from "./status"
import { ShipStatusIcon } from "./ship-bubble"
import { customsTone } from "./tone"

/**
 * A conversation in the inbox: avatar with customs light, name, last ship
 * preview, held counter and "agent working" signal.
 */
export function ConversationRow({
  item,
  active,
  onSelect,
  dockName,
}: {
  item: InboxItem
  active?: boolean
  onSelect?: () => void
  dockName?: string
}) {
  const name = conversationName(item)
  const last = item.last_ship
  return (
    <button
      type="button"
      onClick={onSelect}
      data-active={active || undefined}
      className={cn(
        "group/row flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors outline-none",
        "hover:bg-accent/60 focus-visible:ring-3 focus-visible:ring-ring/50",
        "data-active:bg-accent data-active:text-accent-foreground",
      )}
    >
      <ChatAvatar
        name={item.kind === "self" ? "" : name}
        seed={item.id}
        kind={item.kind}
        status={customsTone[item.status]}
        pulse={item.in_flight}
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className="truncate text-sm font-medium">{name}</span>
          <span className="ml-auto shrink-0 text-[0.7rem] text-muted-foreground tabular-nums">
            {formatStamp(item.last_activity_at)}
          </span>
        </span>
        <span className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
          {item.in_flight ? (
            <span className="flex items-center gap-1 font-medium text-info">
              <Bot className="size-3.5 motion-safe:animate-pulse" /> El agente está trabajando…
            </span>
          ) : (
            <>
              {last?.direction === "out" && <ShipStatusIcon ship={last} className="size-3" />}
              <span className="truncate">{shipPreview(last) || "Sin mensajes"}</span>
            </>
          )}
          <span className="ml-auto flex shrink-0 items-center gap-1 pl-1">
            {dockName && (
              <span className="hidden max-w-24 truncate rounded bg-muted px-1.5 py-px text-[0.65rem] lg:inline">
                {dockName}
              </span>
            )}
            <CountBadge tone="warning" count={item.held} title={`${item.held} retenidos en aduana`} />
            <CountBadge tone="brand" count={item.queued} title={`${item.queued} en cola`} />
          </span>
        </span>
      </span>
    </button>
  )
}
