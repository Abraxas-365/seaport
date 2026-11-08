import * as React from "react"

import { cn } from "@/lib/utils"

/** The title row of a page: title, description and actions. */
export function PageHeader({
  title,
  description,
  actions,
  className,
  children,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  className?: string
  children?: React.ReactNode
}) {
  return (
    <div className={cn("flex flex-col gap-4 border-b px-6 py-5", className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h1 className="font-heading text-xl font-semibold tracking-tight">{title}</h1>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  )
}

/** A titled block inside a page (cards, settings groups). */
export function Section({
  title,
  description,
  actions,
  className,
  children,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  className?: string
  children: React.ReactNode
}) {
  return (
    <section className={cn("space-y-3", className)}>
      <div className="flex items-end justify-between gap-3">
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  )
}

/** Label/value rows for detail panels. */
export function DataList({ className, children }: { className?: string; children: React.ReactNode }) {
  return <dl className={cn("grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5 text-sm", className)}>{children}</dl>
}

export function DataItem({ label, children }: { label: React.ReactNode; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate">{children}</dd>
    </>
  )
}

/** Monospace identifier, truncated, selectable. */
export function Mono({ className, ...props }: React.ComponentProps<"code">) {
  return <code className={cn("font-mono text-[0.8em] text-muted-foreground", className)} {...props} />
}
