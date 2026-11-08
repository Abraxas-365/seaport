// Tone: the one place status → color meaning lives (see index.css).

import type { CustomsStatus, DockStatus, ShipStatus } from "@/lib/types"

export type Tone = "neutral" | "brand" | "success" | "warning" | "danger" | "info"

/** Classes per tone, by surface. Use these instead of raw colors. */
export const toneClasses: Record<Tone, { dot: string; soft: string; text: string; ring: string; on: string }> = {
  neutral: {
    dot: "bg-muted-foreground/60",
    soft: "bg-muted text-muted-foreground",
    text: "text-muted-foreground",
    ring: "ring-border",
    on: "data-[state=on]:bg-muted data-[state=on]:text-foreground",
  },
  brand: {
    dot: "bg-primary",
    soft: "bg-primary/10 text-primary",
    text: "text-primary",
    ring: "ring-primary/30",
    on: "data-[state=on]:bg-primary/10 data-[state=on]:text-primary",
  },
  success: {
    dot: "bg-success",
    soft: "bg-success/12 text-success dark:bg-success/15",
    text: "text-success",
    ring: "ring-success/30",
    on: "data-[state=on]:bg-success/12 data-[state=on]:text-success",
  },
  warning: {
    dot: "bg-warning",
    soft: "bg-warning/15 text-[color-mix(in_oklch,var(--warning),var(--foreground)_35%)] dark:text-warning",
    text: "text-[color-mix(in_oklch,var(--warning),var(--foreground)_35%)] dark:text-warning",
    ring: "ring-warning/35",
    on: "data-[state=on]:bg-warning/15 data-[state=on]:text-[color-mix(in_oklch,var(--warning),var(--foreground)_35%)] dark:data-[state=on]:text-warning",
  },
  danger: {
    dot: "bg-destructive",
    soft: "bg-destructive/10 text-destructive dark:bg-destructive/15",
    text: "text-destructive",
    ring: "ring-destructive/30",
    on: "data-[state=on]:bg-destructive/10 data-[state=on]:text-destructive",
  },
  info: {
    dot: "bg-info",
    soft: "bg-info/12 text-info dark:bg-info/15",
    text: "text-info",
    ring: "ring-info/30",
    on: "data-[state=on]:bg-info/12 data-[state=on]:text-info",
  },
}

export const customsTone: Record<CustomsStatus, Tone> = {
  allowed: "success",
  pending: "warning",
  denied: "danger",
}

export const customsLabel: Record<CustomsStatus, string> = {
  allowed: "Permitido",
  pending: "En aduana",
  denied: "Bloqueado",
}

export const dockTone: Record<DockStatus, Tone> = {
  linked: "success",
  linking: "warning",
  logged_out: "neutral",
  failed: "danger",
}

export const dockLabel: Record<DockStatus, string> = {
  linked: "Conectado",
  linking: "Vinculando",
  logged_out: "Desvinculado",
  failed: "Falló",
}

export const shipTone: Record<ShipStatus, Tone> = {
  held: "warning",
  queued: "neutral",
  claimed: "info",
  handled: "success",
  pending: "neutral",
  sent: "success",
  failed: "danger",
}

export const shipLabel: Record<ShipStatus, string> = {
  held: "Retenido",
  queued: "En cola",
  claimed: "Con el agente",
  handled: "Atendido",
  pending: "Enviando",
  sent: "Enviado",
  failed: "Falló",
}
