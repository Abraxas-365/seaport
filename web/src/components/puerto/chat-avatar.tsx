import * as React from "react"
import { UsersRound, UserRound, Bookmark } from "lucide-react"

import { hueOf, initials } from "@/lib/format"
import type { ConversationKind } from "@/lib/types"
import { cn } from "@/lib/utils"
import { StatusDot } from "./status"
import type { Tone } from "./tone"

const sizes = {
  sm: "size-7 text-[0.65rem]",
  md: "size-9 text-xs",
  lg: "size-12 text-sm",
} as const

/**
 * A chat's avatar: initials on a tint derived from its id (stable across
 * sessions), a group/self glyph, and an optional status light.
 */
export function ChatAvatar({
  name,
  seed,
  kind = "direct",
  size = "md",
  status,
  pulse,
  className,
}: {
  name: string
  seed: string
  kind?: ConversationKind
  size?: keyof typeof sizes
  status?: Tone
  pulse?: boolean
  className?: string
}) {
  const hue = hueOf(seed)
  const style = {
    "--avatar-h": hue,
  } as React.CSSProperties
  return (
    <span className={cn("relative inline-flex shrink-0", className)} style={style}>
      <span
        className={cn(
          "inline-flex items-center justify-center rounded-full font-semibold select-none",
          "bg-[oklch(0.93_0.04_var(--avatar-h))] text-[oklch(0.38_0.09_var(--avatar-h))]",
          "dark:bg-[oklch(0.32_0.06_var(--avatar-h))] dark:text-[oklch(0.9_0.05_var(--avatar-h))]",
          sizes[size],
        )}
        aria-hidden
      >
        {kind === "self" ? (
          <Bookmark className="size-[45%]" />
        ) : kind === "group" && !name ? (
          <UsersRound className="size-[45%]" />
        ) : name ? (
          initials(name)
        ) : (
          <UserRound className="size-[45%]" />
        )}
      </span>
      {kind === "group" && name && (
        <span className="absolute -right-0.5 -bottom-0.5 inline-flex size-4 items-center justify-center rounded-full bg-background ring-2 ring-background">
          <UsersRound className="size-3 text-muted-foreground" />
        </span>
      )}
      {status && (
        <span className="absolute -top-0.5 -right-0.5 inline-flex rounded-full ring-2 ring-background">
          <StatusDot tone={status} pulse={pulse} />
        </span>
      )}
    </span>
  )
}
