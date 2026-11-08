import * as React from "react"
import { Ban, Check, CircleHelp, Moon, Sun } from "lucide-react"

import type { Agent, Dock, InboxItem, Ship } from "@/lib/types"
import { useTheme } from "@/components/theme-provider"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  AgentCard,
  ChannelIcon,
  ChatAvatar,
  ChoiceCards,
  Composer,
  ConfirmAction,
  ConversationRow,
  CountBadge,
  DayDivider,
  DockCard,
  EmptyState,
  PairingPanel,
  PuertoLogo,
  PuertoMark,
  SegmentedChoice,
  ShipBubble,
  StatusBadge,
  StatusDot,
  customsLabel,
  customsTone,
  dockLabel,
  dockTone,
  shipLabel,
  shipTone,
  type Tone,
} from "@/components/puerto"

/**
 * The living catalog of Puerto's design system: tokens, primitives and
 * domain components with fixture data. Reachable at /design.
 */
export function DesignSystemPage() {
  const { theme, setTheme } = useTheme()
  const dark = theme === "dark" || (theme === "system" && document.documentElement.classList.contains("dark"))
  return (
    <>
      <div className="min-h-svh bg-canvas">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b bg-background/85 px-6 backdrop-blur">
          <PuertoLogo />
          <span className="text-sm text-muted-foreground">Design system</span>
          <div className="ml-auto flex items-center gap-2 text-sm">
            <Sun className="size-4" />
            <Switch checked={dark} onCheckedChange={(v) => setTheme(v ? "dark" : "light")} aria-label="Tema oscuro" />
            <Moon className="size-4" />
          </div>
        </header>
        <div className="mx-auto max-w-6xl space-y-16 px-6 py-10">
          <Intro />
          <Colors />
          <Typography />
          <Signals />
          <Primitives />
          <Domain />
          <Conversation />
          <Feedback />
        </div>
      </div>
    </>
  )
}


function Block({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <section className="space-y-5">
      <div className="space-y-1">
        <h2 className="font-heading text-lg font-semibold tracking-tight">{title}</h2>
        {description && <p className="max-w-2xl text-sm text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  )
}

function Specimen({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className="space-y-2">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className={className}>{children}</div>
    </div>
  )
}

function Intro() {
  return (
    <div className="flex flex-col gap-6 rounded-3xl border bg-card p-8 shadow-xs md:flex-row md:items-center">
      <PuertoMark className="size-16" />
      <div className="space-y-2">
        <h1 className="font-heading text-3xl font-semibold tracking-tight">Puerto</h1>
        <p className="max-w-2xl text-muted-foreground">
          Calmo, legible, operativo. Azul puerto para la marca; las señales (verde, ámbar, rojo, cian) solo significan
          estado. Construido sobre shadcn/ui (radix, estilo nova) y Tailwind v4, con Geist.
        </p>
      </div>
    </div>
  )
}

const swatches: { name: string; varName: string; fg?: string }[] = [
  { name: "background", varName: "--background", fg: "--foreground" },
  { name: "canvas", varName: "--canvas", fg: "--foreground" },
  { name: "card", varName: "--card", fg: "--card-foreground" },
  { name: "primary", varName: "--primary", fg: "--primary-foreground" },
  { name: "secondary", varName: "--secondary", fg: "--secondary-foreground" },
  { name: "muted", varName: "--muted", fg: "--muted-foreground" },
  { name: "accent", varName: "--accent", fg: "--accent-foreground" },
  { name: "border", varName: "--border", fg: "--foreground" },
  { name: "success", varName: "--success", fg: "--success-foreground" },
  { name: "warning", varName: "--warning", fg: "--warning-foreground" },
  { name: "destructive", varName: "--destructive", fg: "--destructive-foreground" },
  { name: "info", varName: "--info", fg: "--info-foreground" },
]

function Colors() {
  return (
    <Block title="Color" description="Tokens semánticos en OKLCH (src/index.css). Nunca uses colores crudos para estado.">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6">
        {swatches.map((s) => (
          <div key={s.name} className="overflow-hidden rounded-xl border bg-card">
            <div
              className="flex h-16 items-end p-2 text-xs font-medium"
              style={{ background: `var(${s.varName})`, color: s.fg ? `var(${s.fg})` : undefined }}
            >
              Aa
            </div>
            <div className="px-2 py-1.5 font-mono text-[0.7rem] text-muted-foreground">{s.name}</div>
          </div>
        ))}
      </div>
    </Block>
  )
}

function Typography() {
  return (
    <Block title="Tipografía" description="Geist para todo, Geist Mono para IDs, números de teléfono y código.">
      <Card className="py-0">
        <CardContent className="space-y-4 p-6">
          <div className="font-heading text-3xl font-semibold tracking-tight">Los barcos llegan al puerto</div>
          <div className="font-heading text-xl font-semibold tracking-tight">Título de página · text-xl</div>
          <div className="text-sm font-semibold">Título de sección · text-sm semibold</div>
          <p className="max-w-2xl text-sm">
            Cuerpo · text-sm. Cada mensaje es un barco; la aduana decide si entra, y la tripulación (tus agentes) lo
            atiende.
          </p>
          <p className="text-sm text-muted-foreground">Secundario · muted-foreground</p>
          <p className="text-xs text-muted-foreground">Meta · text-xs, horas con tabular-nums: 09:41 · 12:07</p>
          <code className="font-mono text-sm">51999123436@s.whatsapp.net · 01JD8M…</code>
          <div className="flex items-center gap-1 text-sm">
            Atajos <Kbd>d</Kbd> tema · <Kbd>⌘</Kbd>
            <Kbd>B</Kbd> barra lateral
          </div>
        </CardContent>
      </Card>
    </Block>
  )
}

const tones: Tone[] = ["neutral", "brand", "success", "warning", "danger", "info"]

function Signals() {
  return (
    <Block
      title="Señales"
      description="Un tono por significado: success = entra/listo, warning = esperando, danger = cerrado/falló, info = el agente trabajando."
    >
      <div className="grid gap-6 md:grid-cols-2">
        <Specimen label="Tonos" className="flex flex-wrap items-center gap-3">
          {tones.map((t) => (
            <StatusBadge key={t} tone={t}>
              {t}
            </StatusBadge>
          ))}
        </Specimen>
        <Specimen label="Luces (pulse = actividad en vivo)" className="flex items-center gap-5">
          {tones.map((t) => (
            <StatusDot key={t} tone={t} pulse={t === "info" || t === "warning"} />
          ))}
        </Specimen>
        <Specimen label="Aduana" className="flex flex-wrap gap-2">
          {(["allowed", "pending", "denied"] as const).map((s) => (
            <StatusBadge key={s} tone={customsTone[s]}>
              {customsLabel[s]}
            </StatusBadge>
          ))}
        </Specimen>
        <Specimen label="Muelle" className="flex flex-wrap gap-2">
          {(["linked", "linking", "logged_out", "failed"] as const).map((s) => (
            <StatusBadge key={s} tone={dockTone[s]} pulse={s === "linking"}>
              {dockLabel[s]}
            </StatusBadge>
          ))}
        </Specimen>
        <Specimen label="Barco" className="flex flex-wrap gap-2">
          {(["held", "queued", "claimed", "handled", "pending", "sent", "failed"] as const).map((s) => (
            <StatusBadge key={s} tone={shipTone[s]} pulse={s === "claimed"}>
              {shipLabel[s]}
            </StatusBadge>
          ))}
        </Specimen>
        <Specimen label="Contadores" className="flex items-center gap-2">
          <CountBadge tone="warning" count={3} />
          <CountBadge tone="brand" count={12} />
          <CountBadge tone="danger" count={140} />
        </Specimen>
      </div>
    </Block>
  )
}

function Primitives() {
  const [access, setAccess] = React.useState<"auto" | "allow" | "deny">("auto")
  const [def, setDef] = React.useState<"ask" | "allow" | "deny">("ask")
  return (
    <Block title="Primitivas" description="shadcn/ui tal cual, con los tokens de Puerto.">
      <div className="grid gap-8 md:grid-cols-2">
        <Specimen label="Botones" className="flex flex-wrap gap-2">
          <Button>Permitir</Button>
          <Button variant="secondary">Agregar</Button>
          <Button variant="outline">Bloquear</Button>
          <Button variant="ghost">Cancelar</Button>
          <Button variant="destructive">Eliminar</Button>
          <Button variant="link">Ver más</Button>
        </Specimen>
        <Specimen label="Entrada" className="space-y-2">
          <Input placeholder="+51999123436, +51*" className="font-mono" />
          <Skeleton className="h-8 w-2/3" />
        </Specimen>
        <Specimen label="SegmentedChoice">
          <SegmentedChoice
            value={access}
            onChange={setAccess}
            options={[
              { value: "auto", label: "Reglas", tone: "brand" },
              { value: "allow", label: "Permitir", icon: Check, tone: "success" },
              { value: "deny", label: "Bloquear", icon: Ban, tone: "danger" },
            ]}
          />
        </Specimen>
        <Specimen label="Marcas" className="flex items-center gap-3">
          <ChannelIcon channel="whatsapp" size="sm" />
          <ChannelIcon channel="whatsapp" />
          <ChannelIcon channel="whatsapp" size="lg" />
          <ChannelIcon channel="api" size="lg" />
          <Separator orientation="vertical" className="h-8" />
          <PuertoMark />
          <PuertoMark className="size-10" />
        </Specimen>
        <Specimen label="ChoiceCards" className="md:col-span-2">
          <ChoiceCards
            value={def}
            onChange={setDef}
            options={[
              { value: "ask", label: "Preguntar", icon: CircleHelp, tone: "warning", description: "Los chats nuevos esperan en aduana." },
              { value: "allow", label: "Permitir", icon: Check, tone: "success", description: "Cualquiera despierta al agente." },
              { value: "deny", label: "Bloquear", icon: Ban, tone: "danger", description: "Solo lo que permitan tus reglas." },
            ]}
          />
        </Specimen>
      </div>
    </Block>
  )
}

const now = Date.now()
const ago = (min: number) => new Date(now - min * 60_000).toISOString()

const sampleShip = (over: Partial<Ship>): Ship => ({
  id: Math.random().toString(36).slice(2),
  conversation_id: "c1",
  direction: "in",
  external_id: "x",
  sender_id: "+51999123436",
  sender_name: "Ana Torres",
  text: "",
  status: "handled",
  sent_at: ago(3),
  created_at: ago(3),
  ...over,
})

const inboxItems: InboxItem[] = [
  {
    id: "c-ana",
    dock_id: "d1",
    channel: "whatsapp",
    external_id: "51999123436@s.whatsapp.net",
    kind: "direct",
    title: "Ana Torres",
    access: "auto",
    status: "allowed",
    handles: ["+51999123436"],
    last_activity_at: ago(1),
    created_at: ago(5000),
    updated_at: ago(1),
    last_ship: sampleShip({ text: "¿Me confirmas la cotización de mañana?" }),
    held: 0,
    queued: 0,
    in_flight: true,
  },
  {
    id: "c-ventas",
    dock_id: "d1",
    channel: "whatsapp",
    external_id: "120363@g.us",
    kind: "group",
    title: "Ventas Lima",
    access: "auto",
    status: "allowed",
    handles: [],
    last_activity_at: ago(42),
    created_at: ago(9000),
    updated_at: ago(42),
    last_ship: sampleShip({ direction: "out", status: "sent", text: "Listo, ya envié el reporte semanal." }),
    held: 0,
    queued: 2,
    in_flight: false,
  },
  {
    id: "c-nuevo",
    dock_id: "d1",
    channel: "whatsapp",
    external_id: "51911222333@s.whatsapp.net",
    kind: "direct",
    title: "",
    access: "auto",
    status: "pending",
    handles: ["+51911222333"],
    last_activity_at: ago(60 * 26),
    created_at: ago(60 * 26),
    updated_at: ago(60 * 26),
    last_ship: sampleShip({ text: "Hola, vi su anuncio", status: "held" }),
    held: 3,
    queued: 0,
    in_flight: false,
  },
  {
    id: "c-spam",
    dock_id: "d1",
    channel: "whatsapp",
    external_id: "51900000000@s.whatsapp.net",
    kind: "direct",
    title: "Promo Spam",
    access: "deny",
    status: "denied",
    handles: [],
    last_activity_at: ago(60 * 24 * 9),
    created_at: ago(60 * 24 * 9),
    updated_at: ago(60 * 24 * 9),
    last_ship: sampleShip({ text: "GANASTE un premio!!!", status: "held" }),
    held: 1,
    queued: 0,
    in_flight: false,
  },
]

const sampleDocks: Dock[] = [
  { id: "d1", channel: "whatsapp", name: "WhatsApp oficina", address: "51999123436:12@s.whatsapp.net", status: "linked", created_at: ago(60 * 24 * 30), updated_at: ago(5) },
  { id: "d2", channel: "whatsapp", name: "Soporte", address: "", status: "linking", created_at: ago(2), updated_at: ago(1) },
  { id: "d3", channel: "api", name: "Formulario web", address: "", status: "linked", created_at: ago(60 * 24 * 3), updated_at: ago(60) },
]

const sampleAgent: Agent = {
  session: "s-7f3a",
  name: "rness",
  concurrency: 3,
  steer: true,
  remote: "190.237.12.4:51022",
  connected_at: ago(95),
  deliveries: [
    { id: "dl1", conversation_id: "c-ana", ships: 2, since: new Date(now - 47_000).toISOString() },
  ],
}

function Domain() {
  const [active, setActive] = React.useState("c-ana")
  return (
    <Block title="Componentes de dominio" description="src/components/puerto: lo que se arma en las pantallas.">
      <div className="grid gap-8 lg:grid-cols-2">
        <Specimen label="ChatAvatar (tinte estable por id, luz de aduana)" className="flex items-center gap-4">
          <ChatAvatar name="Ana Torres" seed="c-ana" status="success" />
          <ChatAvatar name="Ventas Lima" seed="c-ventas" kind="group" />
          <ChatAvatar name="" seed="c-nuevo" status="warning" />
          <ChatAvatar name="" seed="self" kind="self" />
          <ChatAvatar name="Luis" seed="c-luis" size="lg" status="info" pulse />
          <ChatAvatar name="María P" seed="c-maria" size="sm" />
        </Specimen>
        <Specimen label="ConversationRow">
          <Card className="gap-0 p-1.5">
            {inboxItems.map((c) => (
              <ConversationRow key={c.id} item={c} active={active === c.id} onSelect={() => setActive(c.id)} />
            ))}
          </Card>
        </Specimen>
        <Specimen label="DockCard" className="space-y-3">
          {sampleDocks.map((d) => (
            <DockCard key={d.id} dock={d} now={now} />
          ))}
        </Specimen>
        <Specimen label="AgentCard">
          <AgentCard agent={sampleAgent} now={now} titleOf={() => "Ana Torres"} />
        </Specimen>
        <Specimen label="PairingPanel" className="lg:col-span-2">
          <Card className="py-0">
            <CardContent className="p-6">
              <PairingPanel qr="2@puerto-design-system-sample,qr,code" />
            </CardContent>
          </Card>
        </Specimen>
      </div>
    </Block>
  )
}

function Conversation() {
  return (
    <Block title="Conversación" description="Burbujas: entrantes a la izquierda, el agente a la derecha; retenidos punteados.">
      <Card className="gap-0 overflow-hidden py-0">
        <div className="bg-canvas px-4 pb-4 md:px-10">
          <DayDivider label="Hoy" />
          <ShipBubble ship={sampleShip({ text: "Hola! ¿Tienen stock del modelo X200?", sent_at: ago(14) })} showSender />
          <ShipBubble ship={sampleShip({ text: "Y si sí, ¿cuánto demora el envío a Arequipa?", sent_at: ago(13) })} grouped />
          <ShipBubble
            ship={sampleShip({ direction: "out", status: "sent", text: "¡Hola Ana! Sí, quedan 4 unidades. A Arequipa demora 2 días hábiles.", sent_at: ago(12) })}
          />
          <ShipBubble ship={sampleShip({ direction: "out", status: "pending", text: "¿Te reservo una?", sent_at: ago(12) })} grouped />
          <ShipBubble ship={sampleShip({ text: "Mensaje en grupo sin mención", status: "held", sender_name: "Pedro", sent_at: ago(6) })} showSender />
          <ShipBubble ship={sampleShip({ direction: "out", status: "failed", error: "not connected", text: "Esto no salió", sent_at: ago(2) })} />
          <ShipBubble ship={sampleShip({ text: "Perfecto, resérvame una 🙌", status: "claimed", sent_at: ago(1) })} />
        </div>
        <Composer onSend={() => new Promise((r) => setTimeout(r, 600))} />
      </Card>
    </Block>
  )
}

function Feedback() {
  return (
    <Block title="Estados y confirmaciones">
      <div className="grid gap-6 md:grid-cols-2">
        <Card className="py-0">
          <EmptyState title="La aduana está vacía" description="Los chats nuevos esperan aquí hasta que decidas." />
        </Card>
        <Card className="py-0">
          <CardContent className="flex h-full flex-col items-start justify-center gap-3 p-6">
            <p className="text-sm text-muted-foreground">
              Las acciones destructivas piden escribir el nombre antes de confirmar.
            </p>
            <ConfirmAction
              trigger={<Button variant="destructive">Eliminar muelle</Button>}
              title="¿Eliminar Soporte?"
              description={<p>Se borran sus conversaciones, mensajes y reglas.</p>}
              destructive
              confirmText="Soporte"
              actionLabel="Eliminar"
              onConfirm={() => new Promise((r) => setTimeout(r, 500))}
            />
          </CardContent>
        </Card>
      </div>
    </Block>
  )
}
