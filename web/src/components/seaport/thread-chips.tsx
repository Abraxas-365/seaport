import { Link2 } from "lucide-react"
import { Link } from "react-router"

import { conversationName } from "@/lib/format"
import type { Conversation } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ChannelIcon } from "./brand"

/**
 * The doors of a thread: the conversations linked with the current one,
 * each a chip with its channel glyph that opens it. The current door is
 * marked; the first chip switches between this door and the whole thread
 * when `onWhole` is given.
 */
export function ThreadChips({
  doors,
  current,
  whole,
  onWhole,
  className,
}: {
  doors: Conversation[]
  current: string
  /** showing every door's messages together */
  whole?: boolean
  onWhole?: (whole: boolean) => void
  className?: string
}) {
  if (doors.length < 2) return null
  return (
    <div
      data-slot="thread-chips"
      className={cn("flex items-center gap-1.5 overflow-x-auto border-b bg-muted/40 px-3 py-1.5 text-xs md:px-4", className)}
    >
      {onWhole && (
        <button
          type="button"
          aria-pressed={whole}
          onClick={() => onWhole(!whole)}
          className={cn(
            "inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-2 font-medium transition-colors",
            whole ? "bg-primary text-primary-foreground" : "bg-background ring-1 ring-border hover:bg-accent",
          )}
          title={whole ? "Mostrar solo esta conversación" : "Mostrar todo el hilo"}
        >
          <Link2 className="size-3.5" /> {whole ? "Hilo completo" : "Hilo"} · {doors.length}
        </button>
      )}
      {doors.map((d) => {
        const here = d.id === current
        return (
          <Link
            key={d.id}
            to={`/inbox/${d.id}`}
            aria-current={here ? "page" : undefined}
            className={cn(
              "inline-flex h-6 max-w-56 shrink-0 items-center gap-1.5 rounded-md pr-2 pl-1 transition-colors",
              here ? "bg-background font-medium ring-1 ring-primary/40" : "text-muted-foreground hover:bg-accent hover:text-foreground",
            )}
            title={conversationName(d)}
          >
            <ChannelIcon channel={d.channel} size="sm" className="size-4 rounded [&_svg]:size-2.5" />
            <span className="truncate">{conversationName(d)}</span>
          </Link>
        )
      })}
    </div>
  )
}
