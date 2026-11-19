import * as React from "react"
import { Pencil, Plus } from "lucide-react"

import { useSetSecret } from "@/lib/hooks"
import { asSecretRef, secretNameOk, secretRefName } from "@/lib/machine"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldContent, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Mono } from "./layout"

/**
 * Sets one of an agent's secrets: a new one, or a new value for `name`.
 * With `trigger` it opens itself; without, the caller drives `open`.
 * `onSaved` gets the name, so a picker can select what was just made.
 */
export function SecretDialog({
  agentId,
  agentName,
  name: fixed,
  env: wasEnv,
  suggested,
  trigger,
  open: controlled,
  onOpenChange,
  onSaved,
}: {
  agentId: string
  agentName?: string
  /** Changes this secret's value (the name is fixed). */
  name?: string
  /** Its current "variable de entorno" switch. */
  env?: boolean
  /** A name to start a new secret with (`ANTHROPIC_KEY`). */
  suggested?: string
  /** "default": a "Secreto" button; "edit": a pencil; a node: that. */
  trigger?: "default" | "edit" | React.ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
  onSaved?: (name: string) => void
}) {
  const setSecret = useSetSecret(agentId)
  const [own, setOwn] = React.useState(false)
  const open = controlled ?? own
  const [name, setName] = React.useState(fixed ?? suggested ?? "")
  const [value, setValue] = React.useState("")
  const [env, setEnv] = React.useState(wasEnv ?? false)
  // Reset on opening (state adjusted during render, like the profile editor).
  const [wasOpen, setWasOpen] = React.useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (open) {
      setName(fixed ?? suggested ?? "")
      setEnv(wasEnv ?? false)
    }
    setValue("")
  }
  const setOpen = (o: boolean) => {
    if (controlled === undefined) setOwn(o)
    onOpenChange?.(o)
  }
  const nameOk = secretNameOk(name)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {trigger !== undefined && (
        <DialogTrigger asChild>
          {trigger === "edit" ? (
            <Button variant="ghost" size="icon-sm" aria-label={`Cambiar ${fixed}`}>
              <Pencil />
            </Button>
          ) : trigger === "default" ? (
            <Button variant="outline" size="sm">
              <Plus /> Secreto
            </Button>
          ) : (
            trigger
          )}
        </DialogTrigger>
      )}
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            e.stopPropagation()
            setSecret.mutate(
              { name, value, env },
              {
                onSuccess: () => {
                  setOpen(false)
                  onSaved?.(name)
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>{fixed ? `Cambiar ${fixed}` : "Nuevo secreto"}</DialogTitle>
            <DialogDescription>
              {fixed
                ? "El valor anterior se reemplaza; no se puede ver."
                : `${agentName ? `Para @${agentName}. ` : ""}Se guarda cifrado y no se puede volver a ver.`}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="py-5">
            <Field data-invalid={!!name && !nameOk}>
              <FieldLabel htmlFor="secret-name">Nombre</FieldLabel>
              <Input
                id="secret-name"
                value={name}
                readOnly={!!fixed}
                onChange={(e) => setName(e.target.value.trim().toUpperCase())}
                className="font-mono"
                placeholder="LINEAR_TOKEN"
                autoComplete="off"
              />
              <FieldDescription>
                {!!name && !nameOk
                  ? "Mayúsculas, dígitos y _; no puede empezar con SEAPORT_."
                  : `Se nombra como \${${name || "NOMBRE"}}.`}
              </FieldDescription>
            </Field>
            <Field>
              <FieldLabel htmlFor="secret-value">Valor</FieldLabel>
              <Input
                id="secret-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                className="font-mono"
                autoComplete="new-password"
                maxLength={16384}
                autoFocus={!!fixed || !!suggested}
              />
            </Field>
            <Field orientation="horizontal">
              <FieldContent>
                <FieldLabel htmlFor="secret-env">Variable de entorno</FieldLabel>
                <FieldDescription>
                  El harness lo recibe como variable: en sus instancias alojadas, y en <Mono>seaport connect</Mono> solo
                  con <Mono>-env</Mono>.
                </FieldDescription>
              </FieldContent>
              <Switch id="secret-env" checked={env} onCheckedChange={setEnv} />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!nameOk || !value || setSecret.isPending}>
              {setSecret.isPending && <Spinner />} Guardar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

const newValue = "__new__"
const noneValue = "__none__"

/**
 * Picks one of the agent's secrets for a `${NAME}` field (a model key, a
 * repo credential), or makes one: "Nuevo secreto…" opens SecretDialog and
 * selects what it saved. A value the agent doesn't have (typed in YAML)
 * stays selectable and is marked. `options` are other literal values
 * offered first (Anthropic's "Cuenta de Claude").
 */
export function SecretPicker({
  id,
  label,
  value,
  onChange,
  agentId,
  agentName,
  secrets,
  readOnly,
  none = "Ninguno",
  suggested,
  options = [],
  canCreate = true,
}: {
  id: string
  label: string
  /** `${NAME}`, one of `options`, or "". */
  value: string
  onChange: (value: string) => void
  agentId: string
  agentName?: string
  /** The agent's secret names. */
  secrets: string[]
  readOnly?: boolean
  none?: string
  /** The name a new secret starts with. */
  suggested?: string
  options?: { value: string; label: string }[]
  /** Offer "Nuevo secreto…" (needs agents:write). */
  canCreate?: boolean
}) {
  const [creating, setCreating] = React.useState(false)
  const name = secretRefName(value)
  const literal = options.some((o) => o.value === value)
  const known = !value || literal || secrets.includes(name)
  const bad = !!value && !name && !literal
  return (
    <>
      <Select
        value={value || noneValue}
        onValueChange={(v) => {
          if (v === newValue) setCreating(true)
          else onChange(v === noneValue ? "" : v)
        }}
        disabled={readOnly}
      >
        <SelectTrigger id={id} aria-label={label} aria-invalid={bad || undefined} className="w-full font-mono">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={noneValue} className="font-sans text-muted-foreground">
            {none}
          </SelectItem>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value} className="font-sans">
              {o.label}
            </SelectItem>
          ))}
          {secrets.map((n) => (
            <SelectItem key={n} value={asSecretRef(n)} className="font-mono">
              {n}
            </SelectItem>
          ))}
          {!known && (
            <SelectItem value={value} className="font-mono">
              {name || value} <span className="font-sans text-xs text-muted-foreground">(el agente no lo tiene)</span>
            </SelectItem>
          )}
          {canCreate && !readOnly && (
            <>
              <SelectSeparator />
              <SelectItem value={newValue} className="font-sans">
                <Plus /> Nuevo secreto…
              </SelectItem>
            </>
          )}
        </SelectContent>
      </Select>
      {canCreate && !readOnly && (
        <SecretDialog
          agentId={agentId}
          agentName={agentName}
          suggested={suggested}
          open={creating}
          onOpenChange={setCreating}
          onSaved={(n) => onChange(asSecretRef(n))}
        />
      )}
    </>
  )
}
