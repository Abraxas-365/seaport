import * as React from "react"
import { Inbox, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { ApiError } from "@/lib/api"

/** Empty state with Puerto's voice. */
export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <Empty className={cn("border-0", className)}>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description && <EmptyDescription>{description}</EmptyDescription>}
      </EmptyHeader>
      {action && <EmptyContent>{action}</EmptyContent>}
    </Empty>
  )
}

export function LoadingState({ label = "Cargando…", className }: { label?: string; className?: string }) {
  return (
    <div className={cn("flex items-center justify-center gap-2 p-10 text-sm text-muted-foreground", className)}>
      <Spinner /> {label}
    </div>
  )
}

export function ErrorState({
  error,
  onRetry,
  className,
}: {
  error: unknown
  onRetry?: () => void
  className?: string
}) {
  const msg = error instanceof Error ? error.message : "Algo salió mal"
  const status = error instanceof ApiError ? error.status : undefined
  return (
    <EmptyState
      className={className}
      title={status === 404 ? "No encontrado" : "No se pudo cargar"}
      description={msg}
      action={
        onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            Reintentar
          </Button>
        )
      }
    />
  )
}
