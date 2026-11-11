import * as React from "react"
import { MessageCircle, Webhook } from "lucide-react"

import type { Channel } from "@/lib/types"
import { cn } from "@/lib/utils"

/** Seaport's mark: an anchor ring, drawn so it reads at 16px. */
export function SeaportMark({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <svg viewBox="0 0 32 32" fill="none" aria-hidden className={cn("size-6", className)} {...props}>
      <rect width="32" height="32" rx="9" className="fill-primary" />
      <g
        className="stroke-primary-foreground"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <circle cx="16" cy="9.5" r="2.6" />
        <path d="M16 12.1V25" />
        <path d="M11.5 16h9" />
        <path d="M8.5 19.5c.6 3.4 3.7 5.5 7.5 5.5s6.9-2.1 7.5-5.5" />
      </g>
    </svg>
  )
}

export function SeaportLogo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <SeaportMark />
      <span className="font-heading text-[0.95rem] font-semibold tracking-tight">Seaport</span>
    </span>
  )
}

export const channelLabel: Record<Channel, string> = {
  whatsapp: "WhatsApp",
  api: "API",
  github: "GitHub",
}

/** GitHub's mark (lucide has no brand icons). */
export function GitHubMark({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden className={className} {...props}>
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  )
}

/** Channel glyph on a channel-colored tile. */
export function ChannelIcon({
  channel,
  size = "md",
  className,
}: {
  channel: Channel
  size?: "sm" | "md" | "lg"
  className?: string
}) {
  const Icon = channel === "whatsapp" ? MessageCircle : channel === "github" ? GitHubMark : Webhook
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-lg",
        channel === "whatsapp"
          ? "bg-[oklch(0.94_0.06_155)] text-[oklch(0.45_0.13_155)] dark:bg-[oklch(0.3_0.06_155)] dark:text-[oklch(0.85_0.12_155)]"
          : channel === "github"
            ? "bg-foreground text-background"
            : "bg-secondary text-secondary-foreground",
        size === "sm" && "size-6 [&_svg]:size-3.5",
        size === "md" && "size-8 [&_svg]:size-4",
        size === "lg" && "size-11 rounded-xl [&_svg]:size-5",
        className,
      )}
      aria-label={channelLabel[channel]}
    >
      <Icon />
    </span>
  )
}
