import * as React from "react"

import { cn } from "@/lib/utils"
import { toneClasses, type Tone } from "./tone"

/** A status light. `pulse` marks live activity (agent working, linking). */
export function StatusDot({
  tone = "neutral",
  pulse = false,
  className,
  ...props
}: React.ComponentProps<"span"> & { tone?: Tone; pulse?: boolean }) {
  return (
    <span
      data-slot="status-dot"
      className={cn("relative inline-flex size-2 shrink-0", className)}
      {...props}
    >
      {pulse && (
        <span
          className={cn("absolute inset-0 rounded-full motion-safe:animate-signal", toneClasses[tone].dot)}
        />
      )}
      <span className={cn("relative inline-flex size-full rounded-full", toneClasses[tone].dot)} />
    </span>
  )
}

/** A soft pill with a dot: the standard way to show a status. */
export function StatusBadge({
  tone = "neutral",
  pulse,
  dot = true,
  className,
  children,
  ...props
}: React.ComponentProps<"span"> & { tone?: Tone; pulse?: boolean; dot?: boolean }) {
  return (
    <span
      data-slot="status-badge"
      data-tone={tone}
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1.5 rounded-full px-2 text-xs font-medium whitespace-nowrap",
        toneClasses[tone].soft,
        className,
      )}
      {...props}
    >
      {dot && <StatusDot tone={tone} pulse={pulse} className="size-1.5" />}
      {children}
    </span>
  )
}

/** A small counter pill (held messages, queued, unread…). */
export function CountBadge({
  tone = "brand",
  count,
  className,
  ...props
}: React.ComponentProps<"span"> & { tone?: Tone; count: number }) {
  if (count <= 0) return null
  return (
    <span
      data-slot="count-badge"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[0.7rem] font-semibold tabular-nums",
        toneClasses[tone].soft,
        className,
      )}
      {...props}
    >
      {count > 99 ? "99+" : count}
    </span>
  )
}
