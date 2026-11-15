import * as React from "react"
import { Check, Copy, KeyRound, MailPlus, RefreshCw, Trash2, UserRound } from "lucide-react"

import { formatFull, formatRelative } from "@/lib/format"
import type { Invitation, Member, MemberApiKey, MemberRole } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ConfirmAction } from "./confirm"
import { Mono } from "./layout"
import { StatusBadge } from "./status"
import type { Tone } from "./tone"

/** Seaport's roles, in the order they are offered, with what each can do. */
export const memberRoles: { value: MemberRole; label: string; hint: string }[] = [
  { value: "Admin", label: "Administrador", hint: "Todo, incluidos agentes, entornos y tokens." },
  { value: "Operator", label: "Operador", hint: "Trabaja con muelles, conversaciones y agentes; no cambia la configuración." },
  { value: "Viewer", label: "Lector", hint: "Solo consulta." },
]

/** The Spanish name of a role; any other IAMKit role shows as it is. */
export function roleLabel(role: string): string {
  return memberRoles.find((r) => r.value === role)?.label ?? role
}

/** The role picker of the invite, access and key forms. */
export function RoleSelect({
  value,
  onChange,
  disabled,
  allowed,
  className,
  "aria-label": label = "Rol",
}: {
  value: string
  onChange: (role: MemberRole) => void
  disabled?: boolean
  /** The roles the caller may hand out (never more than they hold); all by default. */
  allowed?: readonly string[]
  className?: string
  "aria-label"?: string
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as MemberRole)} disabled={disabled}>
      <SelectTrigger aria-label={label} className={className}>
        <SelectValue placeholder="Sin rol de Seaport" />
      </SelectTrigger>
      <SelectContent>
        {memberRoles.map((r) => (
          <SelectItem key={r.value} value={r.value} disabled={allowed ? !allowed.includes(r.value) : false}>
            {r.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** A read-only value (an invitation link, an API key) with a copy button. */
export function CopyField({ value, label, testId }: { value: string; label: string; testId?: string }) {
  const [copied, setCopied] = React.useState(false)
  const copy = async () => {
    await navigator.clipboard.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <div className="flex items-center gap-2 rounded-lg border bg-muted/40 p-2">
      <Mono className="min-w-0 flex-1 px-1 text-sm break-all select-all" aria-label={label} data-testid={testId}>
        {value}
      </Mono>
      <Button type="button" size="sm" variant="outline" className="shrink-0" onClick={copy}>
        {copied ? <Check /> : <Copy />} {copied ? "Copiado" : "Copiar"}
      </Button>
    </div>
  )
}

const rowClass = "flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3"

/**
 * One person of the organization: their Seaport role (changed in place) and
 * whether they manage members, with remove. The signed-in person's own row
 * is read-only (nobody locks themselves out). The controls are only given
 * to those who may use them.
 */
export function MemberRow({
  member: m,
  allowedRoles,
  canAssign,
  canRemove,
  busy,
  onChange,
  onRemove,
}: {
  member: Member
  allowedRoles?: readonly string[]
  canAssign?: boolean
  canRemove?: boolean
  busy?: boolean
  onChange?: (change: { role: string; manages_members: boolean }) => void
  onRemove?: () => Promise<unknown>
}) {
  const editable = canAssign && !m.you && m.active
  return (
    <li className={rowClass}>
      <UserRound className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{m.name || m.email}</span>
          {m.you && <StatusBadge tone="brand" dot={false}>Tú</StatusBadge>}
          {!m.active && <StatusBadge tone="neutral">Inactivo</StatusBadge>}
          {m.other_roles.map((r) => (
            <StatusBadge key={r} tone="neutral" dot={false} title="Rol de IAMKit: no se cambia desde Seaport">
              {r}
            </StatusBadge>
          ))}
        </div>
        {m.name && <div className="truncate text-xs text-muted-foreground">{m.email}</div>}
      </div>
      {editable ? (
        <>
          <RoleSelect
            aria-label={`Rol de ${m.name || m.email}`}
            className="w-44"
            value={m.role}
            allowed={allowedRoles}
            disabled={busy}
            onChange={(role) => onChange?.({ role, manages_members: m.manages_members })}
          />
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <Switch
              aria-label={`Administra miembros: ${m.name || m.email}`}
              checked={m.manages_members}
              disabled={busy || !m.role}
              onCheckedChange={(manages_members) => onChange?.({ role: m.role, manages_members })}
            />
            Administra miembros
          </label>
        </>
      ) : (
        <>
          <StatusBadge tone={m.role ? "info" : "neutral"} dot={false}>
            {m.role ? roleLabel(m.role) : "Sin rol de Seaport"}
          </StatusBadge>
          {m.manages_members && <StatusBadge tone="neutral" dot={false}>Administra miembros</StatusBadge>}
        </>
      )}
      {canRemove && !m.you && onRemove && (
        <ConfirmAction
          destructive
          title={`¿Quitar a ${m.name || m.email}?`}
          description={<p>Pierde el acceso a la organización y su sesión deja de funcionar al instante.</p>}
          actionLabel="Quitar"
          onConfirm={onRemove}
          trigger={
            <Button variant="ghost" size="sm" className="text-destructive" aria-label={`Quitar a ${m.name || m.email}`}>
              <Trash2 /> Quitar
            </Button>
          }
        />
      )}
    </li>
  )
}

const invitationTone: Record<string, { tone: Tone; label: string }> = {
  pending: { tone: "warning", label: "Pendiente" },
  accepted: { tone: "success", label: "Aceptada" },
  expired: { tone: "danger", label: "Vencida" },
  revoked: { tone: "neutral", label: "Revocada" },
}

/** A pending invitation: who, with which role, until when; resend and revoke. */
export function InvitationRow({
  invitation: i,
  now,
  busy,
  onResend,
  onRevoke,
}: {
  invitation: Invitation
  now: number
  busy?: boolean
  onResend?: () => void
  onRevoke?: () => Promise<unknown>
}) {
  const status = invitationTone[i.status] ?? { tone: "neutral" as Tone, label: i.status }
  return (
    <li className={rowClass}>
      <MailPlus className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{i.email}</span>
          <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
          <StatusBadge tone="info" dot={false}>
            {i.role ? roleLabel(i.role) : "Sin rol de Seaport"}
          </StatusBadge>
          {i.manages_members && <StatusBadge tone="neutral" dot={false}>Administra miembros</StatusBadge>}
        </div>
        {i.expires_at && (
          <div className="truncate text-xs text-muted-foreground" title={formatFull(i.expires_at)}>
            {i.status === "pending" ? "Vence" : "Venció"} {formatRelative(i.expires_at, now)}
          </div>
        )}
      </div>
      {i.status === "pending" && onResend && (
        <Button variant="ghost" size="sm" disabled={busy} onClick={onResend} aria-label={`Reenviar a ${i.email}`}>
          <RefreshCw /> Reenviar
        </Button>
      )}
      {i.status === "pending" && onRevoke && (
        <ConfirmAction
          destructive
          title={`¿Revocar la invitación a ${i.email}?`}
          description={<p>El enlace que ya recibió deja de funcionar.</p>}
          actionLabel="Revocar"
          onConfirm={onRevoke}
          trigger={
            <Button variant="ghost" size="sm" className="text-destructive" aria-label={`Revocar la invitación a ${i.email}`}>
              <Trash2 /> Revocar
            </Button>
          }
        />
      )}
    </li>
  )
}

const keyTone: Record<string, { tone: Tone; label: string }> = {
  active: { tone: "success", label: "Activa" },
  expired: { tone: "danger", label: "Vencida" },
  revoked: { tone: "neutral", label: "Revocada" },
}

/** An organization API key (for the CLI): name, role, hint, expiry and last use. */
export function ApiKeyRow({
  apiKey: k,
  now,
  onRevoke,
}: {
  apiKey: MemberApiKey
  now: number
  onRevoke?: () => Promise<unknown>
}) {
  const state = keyTone[k.state] ?? { tone: "neutral" as Tone, label: k.state }
  return (
    <li className={rowClass}>
      <KeyRound className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{k.name}</span>
          <Mono className="text-xs text-muted-foreground">{k.hint}…</Mono>
          <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
          <StatusBadge tone={k.role ? "info" : "neutral"} dot={false}>
            {k.role ? roleLabel(k.role) : "Permisos a medida"}
          </StatusBadge>
        </div>
        <div className={cn("truncate text-xs text-muted-foreground")}>
          <span title={formatFull(k.created_at)}>Creada {formatRelative(k.created_at, now)}</span>
          {" · "}
          {k.last_used_at ? (
            <span title={formatFull(k.last_used_at)}>usada {formatRelative(k.last_used_at, now)}</span>
          ) : (
            "sin usar"
          )}
          {" · "}
          {k.expires_at ? (
            <span title={formatFull(k.expires_at)}>
              {k.state === "expired" ? "venció" : "vence"} {formatRelative(k.expires_at, now)}
            </span>
          ) : (
            "sin vencimiento"
          )}
        </div>
      </div>
      {k.state !== "revoked" && k.state !== "expired" && onRevoke && (
        <ConfirmAction
          destructive
          title={`¿Revocar la llave «${k.name}»?`}
          description={<p>Quien la use deja de poder entrar al instante.</p>}
          actionLabel="Revocar"
          onConfirm={onRevoke}
          trigger={
            <Button variant="ghost" size="sm" className="text-destructive" aria-label={`Revocar la llave ${k.name}`}>
              <Trash2 /> Revocar
            </Button>
          }
        />
      )}
    </li>
  )
}
