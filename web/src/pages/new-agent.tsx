import * as React from "react"
import { useNavigate } from "react-router"
import { Cloud, KeyRound, Laptop, Plus, UserRound } from "lucide-react"

import { useCan, useHosting, useNewAgent } from "@/lib/hooks"
import { agentNamePattern, emptyConfig, keyNameFor, newMachine, providers, subscription } from "@/lib/machine"
import { Perm } from "@/lib/permissions.gen"
import type { Harness, ProfileInput } from "@/lib/types"
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { ChoiceCards, Mono, type ChoiceOption } from "@/components/seaport"
import { SlotCount } from "./agent-hosting"

type Where = "hosted" | "remote"
type Step = "name" | "where" | "harness" | "models"
/** How a hosted agent reaches Claude: a Claude account or an Anthropic key. */
type Access = "account" | "key"

const whereOptions: ChoiceOption<Where>[] = [
  { value: "hosted", label: "En el puerto", description: "Siempre disponible; el puerto le da instancias.", icon: Cloud },
  { value: "remote", label: "Donde alguien lo conecte", description: "Con seaport connect y un token.", icon: Laptop },
]

/**
 * "Nuevo agente": a name, where it runs, its harness and, when the port
 * hosts it, how it reaches its models. Creates the agent, its first config
 * (and the key as its secret) and, when hosted, its instances.
 */
export function NewAgentDialog() {
  const can = useCan()
  const hosting = useHosting()
  const create = useNewAgent()
  const navigate = useNavigate()
  const canHost = can(Perm.HostingWrite)
  // A port with its runtime off still takes hosting (as the agent page's
  // "Alojar"): the instances start once an operator turns it on.
  const runtimeOff = hosting.data?.runtime === false
  const [open, setOpen] = React.useState(false)
  const [step, setStep] = React.useState<Step>("name")
  const [name, setName] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [where, setWhere] = React.useState<Where>("remote")
  const [harness, setHarness] = React.useState<Harness | "">("")
  const [access, setAccess] = React.useState<Access>("account")
  const [key, setKey] = React.useState("")
  const [slots, setSlots] = React.useState(1)

  const n = name.trim().toLowerCase()
  const nameOk = agentNamePattern.test(n)
  const hosted = where === "hosted"
  const steps: Step[] = hosted ? ["name", "where", "harness", "models"] : ["name", "where", "harness"]
  const at = steps.indexOf(step)
  const last = at === steps.length - 1
  const accountOnly = access === "account"
  // Claude through a key; other providers' keys go in the YAML later.
  const p = providers[0]
  const ready = nameOk && !!harness && (accountOnly || key.trim() !== "")

  const reset = () => {
    setStep("name")
    setName("")
    setDescription("")
    const hostFirst = canHost && !runtimeOff
    setWhere(hostFirst ? "hosted" : "remote")
    setHarness(hostFirst ? "rness" : "")
    setAccess("account")
    setKey("")
    setSlots(1)
  }

  const submit = () => {
    let config: ProfileInput | null = null
    let secret: { name: string; value: string } | undefined
    if (hosted) {
      const models: Record<string, string> = {}
      if (accountOnly) models.anthropic = subscription
      else {
        secret = { name: keyNameFor(p), value: key.trim() }
        models[p.name] = `\${${secret.name}}`
      }
      config = { ...emptyConfig, harness, machine: newMachine({ models }) }
    } else if (harness) config = { ...emptyConfig, harness }
    create.mutate(
      { name: n, description: description.trim(), config, key: secret, slots: hosted ? slots : 0 },
      {
        onSuccess: ({ agent, error }) => {
          setOpen(false)
          navigate(`/agents/${agent.id}${error ? "#configuracion" : hosted ? "#donde" : ""}`)
        },
      },
    )
  }

  const next = () => {
    if (last) submit()
    else setStep(steps[at + 1])
  }

  const canNext =
    step === "name" ? nameOk : step === "harness" ? !hosted || !!harness : step === "models" ? ready : true

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (o) reset()
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus /> Nuevo agente
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (canNext && !create.isPending) next()
          }}
        >
          <DialogHeader>
            <DialogTitle>Nuevo agente</DialogTitle>
            <DialogDescription>
              Paso {at + 1} de {steps.length}:{" "}
              {step === "name"
                ? "cómo se llama"
                : step === "where"
                  ? "dónde corre"
                  : step === "harness"
                    ? "qué lo corre"
                    : "modelos y acceso"}
              .
            </DialogDescription>
          </DialogHeader>
          <div className="py-5">
            {step === "name" && (
              <FieldGroup>
                <Field data-invalid={!!n && !nameOk}>
                  <FieldLabel htmlFor="agent-name">Nombre</FieldLabel>
                  <Input
                    id="agent-name"
                    value={name}
                    placeholder="pm"
                    onChange={(e) => setName(e.target.value)}
                    className="font-mono"
                    autoComplete="off"
                    autoFocus
                  />
                  <FieldDescription>
                    {n && !nameOk
                      ? "Hasta 32 minúsculas, dígitos, - o _, empezando por letra o dígito."
                      : `Se le llama con @${n || "nombre"}.`}
                  </FieldDescription>
                </Field>
                <Field>
                  <FieldLabel htmlFor="agent-description">Descripción</FieldLabel>
                  <Textarea
                    id="agent-description"
                    value={description}
                    placeholder="Arma tickets a partir de lo que le cuentan."
                    onChange={(e) => setDescription(e.target.value)}
                    maxLength={1000}
                  />
                </Field>
              </FieldGroup>
            )}
            {step === "where" && (
              <div className="space-y-3">
                <ChoiceCards
                  value={where}
                  onChange={(w) => {
                    setWhere(w)
                    if (w === "hosted" && !harness) setHarness("rness")
                  }}
                  options={whereOptions.map((o) =>
                    o.value !== "hosted"
                      ? o
                      : !canHost
                        ? { ...o, description: "Tu rol no puede alojar agentes." }
                        : runtimeOff
                          ? { ...o, description: "Este puerto aún no corre instancias: arrancan cuando lo activen." }
                          : o,
                  )}
                  className="sm:grid-cols-2"
                />
                {!canHost && hosted && (
                  <p className="text-sm text-destructive">No puedes alojarlo aquí; elige la otra opción.</p>
                )}
                {hosted && canHost && (
                  <Field orientation="horizontal" className="max-w-xs">
                    <FieldLabel>Instancias</FieldLabel>
                    <SlotCount value={slots} onChange={setSlots} />
                  </Field>
                )}
              </div>
            )}
            {step === "harness" && (
              <ChoiceCards
                value={harness || "none"}
                onChange={(h) => setHarness(h === "none" ? "" : h)}
                options={[
                  { value: "rness", label: "rness", description: "Cualquier proveedor de modelos." },
                  { value: "claude-code", label: "Claude Code", description: "Modelos de Anthropic." },
                  ...(hosted
                    ? []
                    : [{ value: "none" as const, label: "Lo elige quien conecta", description: "Lo indica al conectarse." }]),
                ]}
                className={hosted ? "sm:grid-cols-2" : undefined}
              />
            )}
            {step === "models" && (
              <FieldGroup>
                <ChoiceCards
                  value={access}
                  onChange={setAccess}
                  options={[
                    {
                      value: "account",
                      label: "Cuenta de Claude",
                      description: "Cada instancia inicia sesión con una suscripción, desde Dónde corre.",
                      icon: UserRound,
                    },
                    {
                      value: "key",
                      label: "Clave de API",
                      description: "Se guarda cifrada como secreto del agente; solo la usa el gateway del puerto.",
                      icon: KeyRound,
                    },
                  ]}
                  className="sm:grid-cols-2"
                />
                {access === "key" && (
                  <Field>
                    <FieldLabel htmlFor="agent-key">Clave de {p.label}</FieldLabel>
                    <Input
                      id="agent-key"
                      type="password"
                      value={key}
                      onChange={(e) => setKey(e.target.value)}
                      className="font-mono"
                      autoComplete="new-password"
                      maxLength={16384}
                      autoFocus
                    />
                    <FieldDescription>
                      Queda como el secreto <Mono>{keyNameFor(p)}</Mono>. Repos y paquetes, luego en
                      Configuración.
                    </FieldDescription>
                  </Field>
                )}
              </FieldGroup>
            )}
          </div>
          <DialogFooter>
            {at > 0 ? (
              <Button type="button" variant="outline" onClick={() => setStep(steps[at - 1])}>
                Atrás
              </Button>
            ) : (
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancelar
              </Button>
            )}
            <Button type="submit" disabled={!canNext || (hosted && !canHost) || create.isPending}>
              {create.isPending && <Spinner />} {last ? "Crear agente" : "Siguiente"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
