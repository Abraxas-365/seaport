import * as React from "react"

import type { Build, Slot, SlotAccount, UsageDay } from "@/lib/types"
import { formatDuration, formatFull, formatRelative } from "@/lib/format"
import { cn } from "@/lib/utils"
import { Mono } from "./layout"
import { StatusBadge } from "./status"
import { buildLabel, buildTone, slotLabel, slotTone } from "./tone"

/**
 * One instance of a hosted agent (<agent>-<n>): its state, since when,
 * what it runs and why it failed. `actions` go on the right (start/stop,
 * logs).
 */
export function SlotRow({
  name,
  slot,
  current,
  now,
  actions,
  className,
}: {
  name: string
  slot: Slot
  /** The environment's current version: an older one restarts once idle. */
  current?: number
  /** useNow() of the page. */
  now: number
  actions?: React.ReactNode
  className?: string
}) {
  const busy = slot.state === "starting" || slot.state === "stopping"
  const outdated =
    slot.state === "running" && current !== undefined && slot.environment_version > 0 && slot.environment_version < current
  return (
    <div data-slot="slot-row" className={cn("flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5", className)}>
      <span className="font-mono text-sm font-medium">{name}</span>
      <StatusBadge tone={slotTone[slot.state]} pulse={busy}>
        {slotLabel[slot.state]}
      </StatusBadge>
      {outdated && (
        <StatusBadge tone="info" dot={false} title="Reinicia con la versión actual del entorno al quedar libre">
          v{slot.environment_version} → v{current}
        </StatusBadge>
      )}
      <span className="text-xs text-muted-foreground">
        {slot.state === "running" && slot.started_at
          ? `hace ${formatDuration(slot.started_at, now)}${slot.environment_version ? ` · v${slot.environment_version}` : ""}`
          : slot.state === "running"
            ? ""
            : formatRelative(slot.updated_at, now)}
        {slot.state === "running" && slot.active_at && ` · trabajó ${formatRelative(slot.active_at, now)}`}
      </span>
      {actions && <div className="ml-auto flex items-center gap-1">{actions}</div>}
      {slot.detail && slot.state !== "running" && (
        <p
          className={cn(
            "basis-full text-xs",
            slot.state === "failed" ? "text-destructive" : "text-muted-foreground",
          )}
        >
          {slot.detail}
        </p>
      )}
    </div>
  )
}

const accountMethod: Record<string, string> = {
  "claude.ai": "Claude",
  oauth_token: "token OAuth",
  api_key: "clave API",
}

/**
 * How a slot's harness is signed in (subscription slots, experimental):
 * undefined while it's being asked.
 */
export function AccountBadge({ account, className }: { account: SlotAccount | undefined; className?: string }) {
  if (!account)
    return (
      <StatusBadge className={className} pulse>
        Cuenta…
      </StatusBadge>
    )
  if (!account.logged_in)
    return (
      <StatusBadge className={className} tone="warning" title="No toma mensajes hasta que se conecte a una cuenta">
        Sin cuenta
      </StatusBadge>
    )
  const how = accountMethod[account.method] ?? account.method
  return (
    <StatusBadge className={className} tone="success" title={`Conectada con ${how}`}>
      {how}
      {account.subscription ? ` · ${account.subscription}` : ""}
    </StatusBadge>
  )
}

/** An image build's state, as a badge (the log goes in a LogView). */
export function BuildBadge({ build, className }: { build: Build | null | undefined; className?: string }) {
  if (!build)
    return (
      <StatusBadge className={className} title="Esta versión aún no tiene imagen">
        Sin construir
      </StatusBadge>
    )
  return (
    <StatusBadge
      className={className}
      tone={buildTone[build.state]}
      pulse={build.state === "building"}
      title={build.finished_at ? `Terminó ${formatFull(build.finished_at)}` : `Empezó ${formatFull(build.started_at)}`}
    >
      {buildLabel[build.state]} · v{build.version}
    </StatusBadge>
  )
}

/**
 * Terminal output (build or slot logs), following the end while the
 * reader is there; scrolling up stops following.
 */
export function LogView({
  text,
  empty = "Sin salida todavía.",
  className,
}: {
  text: string | undefined
  empty?: string
  className?: string
}) {
  const ref = React.useRef<HTMLPreElement>(null)
  const follow = React.useRef(true)
  React.useLayoutEffect(() => {
    const el = ref.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [text])
  return (
    <pre
      ref={ref}
      data-slot="log-view"
      onScroll={(e) => {
        const el = e.currentTarget
        follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
      }}
      className={cn(
        "max-h-96 min-h-24 overflow-auto rounded-lg border bg-muted/40 p-3 font-mono text-[0.72rem] leading-relaxed whitespace-pre-wrap break-all",
        !text && "flex items-center justify-center text-muted-foreground",
        className,
      )}
    >
      {text || empty}
    </pre>
  )
}

const compact = new Intl.NumberFormat("es", { notation: "compact", maximumFractionDigits: 1 })

/** Tokens in a short form (12,3 mil). */
export function formatTokens(n: number) {
  return compact.format(n)
}

/**
 * Model use per day through the gateway: one bar per day (input + output +
 * cache tokens), newest on the right, with totals.
 */
export function UsageChart({ days, span = 30, className }: { days: UsageDay[]; span?: number; className?: string }) {
  const byDay = new Map<string, { tokens: number; requests: number }>()
  for (const d of days) {
    const k = d.day.slice(0, 10)
    const cur = byDay.get(k) ?? { tokens: 0, requests: 0 }
    cur.tokens += d.input_tokens + d.output_tokens + d.cache_write_tokens + d.cache_read_tokens
    cur.requests += d.requests
    byDay.set(k, cur)
  }
  const today = new Date()
  const series = Array.from({ length: span }, (_, i) => {
    const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - (span - 1 - i)))
    const k = d.toISOString().slice(0, 10)
    return { day: k, ...(byDay.get(k) ?? { tokens: 0, requests: 0 }) }
  })
  const max = Math.max(1, ...series.map((s) => s.tokens))
  const totals = days.reduce(
    (t, d) => ({
      requests: t.requests + d.requests,
      input: t.input + d.input_tokens,
      output: t.output + d.output_tokens,
      cache: t.cache + d.cache_read_tokens + d.cache_write_tokens,
    }),
    { requests: 0, input: 0, output: 0, cache: 0 },
  )
  const models = [...new Set(days.map((d) => d.model))]
  return (
    <div data-slot="usage-chart" className={cn("space-y-3", className)}>
      <div className="flex h-20 items-end gap-px" role="img" aria-label={`Tokens por día, últimos ${span} días`}>
        {series.map((s) => (
          <div
            key={s.day}
            className="flex-1 rounded-t-sm bg-primary/70 transition-colors hover:bg-primary"
            style={{ height: `${s.tokens ? Math.max(4, (s.tokens / max) * 100) : 0}%` }}
            title={`${s.day}: ${formatTokens(s.tokens)} tokens · ${s.requests} llamadas`}
          />
        ))}
      </div>
      <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
        {[
          ["Llamadas", totals.requests.toLocaleString("es")],
          ["Entrada", formatTokens(totals.input)],
          ["Salida", formatTokens(totals.output)],
          ["Caché", formatTokens(totals.cache)],
        ].map(([k, v]) => (
          <div key={k}>
            <dt className="text-xs text-muted-foreground">{k}</dt>
            <dd className="font-medium tabular-nums">{v}</dd>
          </div>
        ))}
      </dl>
      {models.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Modelos:{" "}
          {models.map((m, i) => (
            <React.Fragment key={m}>
              {i > 0 && ", "}
              <Mono>{m}</Mono>
            </React.Fragment>
          ))}
        </p>
      )}
    </div>
  )
}
