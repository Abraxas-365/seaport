import * as React from "react"
import { MessageCircle, Webhook } from "lucide-react"

import type { Channel } from "@/lib/types"
import { cn } from "@/lib/utils"

/** Puerto's mark: an anchor ring, drawn so it reads at 16px. */
export function PuertoMark({ className, ...props }: React.ComponentProps<"svg">) {
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

export function PuertoLogo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <PuertoMark />
      <span className="font-heading text-[0.95rem] font-semibold tracking-tight">Puerto</span>
    </span>
  )
}

export const channelLabel: Record<Channel, string> = {
  whatsapp: "WhatsApp",
  api: "API",
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
  const Icon = channel === "whatsapp" ? MessageCircle : Webhook
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-lg",
        channel === "whatsapp"
          ? "bg-[oklch(0.94_0.06_155)] text-[oklch(0.45_0.13_155)] dark:bg-[oklch(0.3_0.06_155)] dark:text-[oklch(0.85_0.12_155)]"
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
