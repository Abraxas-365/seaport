import * as React from "react"
import { Check, Circle, CircleDashed, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

export type ChecklistState = "done" | "todo" | "optional" | "blocked"

const stateIcon: Record<ChecklistState, LucideIcon> = {
  done: Check,
  todo: Circle,
  optional: CircleDashed,
  blocked: CircleDashed,
}

/**
 * One step of a setup checklist: what it is, where it stands, and the one
 * action that moves it (a button, a link). `done` steps dim; `todo` steps
 * lead; `optional`/`blocked` steps explain themselves in `detail`.
 */
export function ChecklistItem({
  state,
  title,
  detail,
  action,
  className,
}: {
  state: ChecklistState
  title: React.ReactNode
  detail?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  const Icon = stateIcon[state]
  return (
    <li
      data-state={state}
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3",
        state === "done" && "text-muted-foreground",
        className
      )}
    >
      <span
        aria-hidden
        className={cn(
          "flex size-5 shrink-0 items-center justify-center rounded-full border",
          state === "done" && "border-success bg-success text-success-foreground",
          state === "todo" && "border-foreground/40",
          (state === "optional" || state === "blocked") &&
            "border-dashed border-muted-foreground/60 text-muted-foreground"
        )}
      >
        <Icon className={cn("size-3", state !== "done" && "hidden")} />
      </span>
      <div className="min-w-0 flex-1">
        <div className={cn("text-sm", state === "todo" && "font-medium")}>{title}</div>
        {detail && <div className="text-xs text-muted-foreground">{detail}</div>}
      </div>
      {action && <div className="flex items-center gap-2">{action}</div>}
    </li>
  )
}

/** The list the items go in (bordered, divided). */
export function Checklist({ className, children, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul className={cn("divide-y rounded-lg border", className)} {...props}>
      {children}
    </ul>
  )
}
