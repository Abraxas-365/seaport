import { Check, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import type { Approval } from "@/lib/types"
import { cn } from "@/lib/utils"
import { formatFull } from "@/lib/format"
import { StatusBadge } from "./status"
import { approvalLabel, approvalTone } from "./tone"

/**
 * A question an agent asked people (ask_approval): its options as buttons
 * while pending (for who may write), then the choice and who made it.
 * Shown under the card message that carries the question.
 */
export function ApprovalCard({
  approval,
  canAnswer = false,
  busy = false,
  onAnswer,
  onCancel,
  className,
}: {
  approval: Approval
  canAnswer?: boolean
  busy?: boolean
  onAnswer?: (option: string) => void
  onCancel?: () => void
  className?: string
}) {
  const pending = approval.status === "pending"
  return (
    <div
      data-slot="approval-card"
      data-status={approval.status}
      className={cn("ml-auto mt-1 w-full max-w-md space-y-2 rounded-xl bg-background/80 p-3 text-xs ring-1 ring-border", className)}
    >
      <div className="flex items-center justify-between gap-2">
        <StatusBadge tone={approvalTone[approval.status]} pulse={pending}>
          {approvalLabel[approval.status]}
        </StatusBadge>
        {pending && <span className="text-muted-foreground">vence {formatFull(approval.expires_at)}</span>}
      </div>
      {pending && canAnswer && (
        <div className="flex flex-wrap gap-1.5">
          {approval.options.map((o) => (
            <Button key={o} size="sm" variant="outline" disabled={busy} onClick={() => onAnswer?.(o)}>
              {o}
            </Button>
          ))}
          <Button size="sm" variant="ghost" disabled={busy} onClick={onCancel} className="text-muted-foreground">
            <X className="size-3.5" /> Cancelar
          </Button>
        </div>
      )}
      {pending && !canAnswer && <p className="text-muted-foreground">Opciones: {approval.options.join(" · ")}</p>}
      {approval.status === "answered" && (
        <p className="flex items-center gap-1.5">
          <Check className="size-3.5 text-success" />
          <span>
            <strong>{approval.answer}</strong>
            {approval.answered_by && <span className="text-muted-foreground"> · {approval.answered_by}</span>}
          </span>
        </p>
      )}
    </div>
  )
}
