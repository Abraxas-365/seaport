import { Bot, User } from "lucide-react"

import type { Channel, Provenance, VaultMode } from "@/lib/types"
import { cn } from "@/lib/utils"
import { StatusBadge } from "./status"
import { channelLabel } from "./brand"
import { vaultModeLabel, vaultModeTone } from "./tone"

/** What a vault grant allows: Lectura (read) or Escritura (write, which includes read). */
export function VaultModeChip({ mode, className }: { mode: VaultMode; className?: string }) {
  return (
    <StatusBadge
      tone={vaultModeTone[mode]}
      dot={false}
      className={className}
      title={mode === "write" ? "Puede leer, guardar y borrar entradas" : "Solo puede leer y buscar"}
    >
      {vaultModeLabel[mode]}
    </StatusBadge>
  )
}

/**
 * Who wrote an entry: "agente pm desde WhatsApp" (an agent says the channel
 * of the dock it was working) or "persona Ana". `dock` is the provenance's
 * dock when the caller can resolve it.
 */
export function ProvenanceLine({
  provenance,
  dock,
  className,
}: {
  provenance: Provenance
  dock?: { name: string; channel: Channel }
  className?: string
}) {
  const agent = provenance.kind === "agent"
  const Icon = agent ? Bot : User
  return (
    <span data-slot="provenance" className={cn("inline-flex min-w-0 items-center gap-1 text-xs text-muted-foreground", className)}>
      <Icon className="size-3 shrink-0" />
      <span className="truncate">
        {agent ? "agente" : "persona"} <span className={cn("text-foreground/80", agent && "font-mono")}>{provenance.by || "—"}</span>
        {agent && dock && (
          <>
            {" "}
            desde <span title={dock.name}>{channelLabel[dock.channel]}</span>
          </>
        )}
        {agent && !dock && provenance.dock_id && " desde un muelle"}
      </span>
    </span>
  )
}
