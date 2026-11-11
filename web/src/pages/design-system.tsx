import * as React from "react"
import { Ban, Check, CircleHelp, Moon, Play, Square, Sun, TerminalSquare } from "lucide-react"

import type { Agent, Build, Dock, InboxItem, Ship, Slot, Turn, UsageDay } from "@/lib/types"
import type { Activity } from "@/lib/activity"
import { useTheme } from "@/components/theme-provider"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Separator } from "@/components/ui/separator"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  AgentActivity,
  TurnSummary,
  AgentCard,
  AgentChip,
  AccountBadge,
  BuildBadge,
  ChannelIcon,
  ChatAvatar,
  ChoiceCards,
  Composer,
  ConfirmAction,
  ConversationRow,
  CountBadge,
  DayDivider,
  DockCard,
  DiffView,
  EmptyState,
  HomeTree,
  LogView,
  NoAccess,
  PairingPanel,
  SeaportLogo,
  SeaportMark,
  SegmentedChoice,
  ShipBubble,
  SlotRow,
  StatusBadge,
  StatusDot,
  UsageChart,
  customsLabel,
  customsTone,
  dockLabel,
  dockTone,
  shipLabel,
  shipTone,
  type HomeChange,
  type Tone,
} from "@/components/seaport"
import { diffLines } from "@/lib/homes"

/**
 * The living catalog of Seaport's design system: tokens, primitives and
 * domain components with fixture data. Reachable at /design.
 */
export function DesignSystemPage() {
  const { theme, setTheme } = useTheme()
  const dark = theme === "dark" || (theme === "system" && document.documentElement.classList.contains("dark"))
  return (
    <>
      <div className="min-h-svh bg-canvas">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b bg-background/85 px-6 backdrop-blur">
          <SeaportLogo />
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
          <MarkdownMessages />
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
      <SeaportMark className="size-16" />
      <div className="space-y-2">
        <h1 className="font-heading text-3xl font-semibold tracking-tight">Seaport</h1>
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
    <Block title="Primitivas" description="shadcn/ui tal cual, con los tokens de Seaport.">
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
          <ChannelIcon channel="github" size="lg" />
          <Separator orientation="vertical" className="h-8" />
          <SeaportMark />
          <SeaportMark className="size-10" />
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

const sec = (s: number) => now - s * 1000
const sampleActivity: Activity = {
  delivery: "d-sample",
  agentName: "ventas",
  startedAt: sec(42),
  steps: [
    { kind: "thought", text: "Ana quiere reservar una X200. Reviso el stock antes de confirmar.", at: sec(40) },
    { kind: "tool", id: "t1", title: "inventory.lookup X200", toolKind: "fetch", status: "completed", at: sec(38), endedAt: sec(36) },
    { kind: "tool", id: "t2", title: "Read pedidos/reservas.csv", toolKind: "read", status: "completed", at: sec(35), endedAt: sec(35) },
    { kind: "tool", id: "t3", title: "crm.reserve --sku X200 --qty 1", toolKind: "execute", status: "failed", at: sec(30), endedAt: sec(26) },
    { kind: "thought", text: "Falló por sesión expirada; reintento con un token nuevo.", at: sec(25) },
    { kind: "tool", id: "t4", title: "crm.reserve --sku X200 --qty 1", toolKind: "execute", status: "in_progress", at: sec(12) },
  ],
}

const iso = (s: number) => new Date(sec(s)).toISOString()
const sampleTurn: Turn = {
  delivery_id: "d-done",
  conversation_id: "c-ana",
  agent_id: "a1",
  agent_name: "ventas",
  outcome: "acked",
  started_at: iso(130),
  ended_at: iso(118),
  tools: [
    { title: "inventory.lookup X200", kind: "fetch", status: "completed", started_at: iso(128), ended_at: iso(126) },
    { title: "Read envios/tarifas.csv", kind: "read", status: "completed", started_at: iso(125), ended_at: iso(125) },
    { title: "Bash: curl -H 'Authorization: [redacted]' …/quote", kind: "execute", status: "failed", started_at: iso(124), ended_at: iso(121) },
  ],
}

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
  agent_id: "a-pm",
  agent_name: "pm",
  name: "rness en la laptop",
  concurrency: 3,
  steer: true,
  profile_version: 2,
  outdated: true,
  remote: "190.237.12.4:51022",
  connected_at: ago(95),
  deliveries: [
    { id: "dl1", conversation_id: "c-ana", ships: 2, since: new Date(now - 47_000).toISOString() },
  ],
}

const sampleSlot = (over: Partial<Slot>): Slot => ({
  index: 1,
  state: "stopped",
  detail: "",
  image: "seaport-env-backend:v3-1a2b3c",
  environment_version: 3,
  started_at: null,
  active_at: null,
  updated_at: ago(42),
  ...over,
})

const sampleSlots: Slot[] = [
  sampleSlot({ index: 1, state: "running", started_at: ago(75), active_at: ago(3) }),
  sampleSlot({ index: 2, state: "running", environment_version: 2, started_at: ago(300), active_at: ago(20) }),
  sampleSlot({ index: 3, state: "starting", updated_at: ago(0) }),
  sampleSlot({ index: 4, state: "failed", detail: "clone https://github.com/acme/app.git: authentication failed" }),
  sampleSlot({ index: 5 }),
]

const sampleBuild = (over: Partial<Build>): Build => ({
  environment_id: "e1",
  version: 3,
  state: "ready",
  base: "ghcr.io/abraxas-365/seaport-rness:latest",
  image: "seaport-env-backend:v3-1a2b3c",
  detail: "",
  started_at: ago(10),
  finished_at: ago(8),
  updated_at: ago(8),
  ...over,
})

const sampleHomeFiles = [
  { path: "CLAUDE.md", size: 1840 },
  { path: "settings.json", size: 612 },
  { path: "statusline.sh", size: 210 },
  { path: "agents/reviewer.md", size: 940 },
  { path: "skills/release/SKILL.md", size: 2310 },
  { path: "skills/release/checklist.md", size: 480 },
]

const sampleHomeChanges = new Map<string, HomeChange>([
  ["CLAUDE.md", "changed"],
  ["agents/reviewer.md", "added"],
  ["commands/deploy.md", "removed"],
])

const sampleDiff = diffLines(
  "# Proyecto\n\nResponde en español.\nUsa pnpm.\nNo toques migrations/.\nCorre los tests antes de terminar.\n",
  "# Proyecto\n\nResponde en español, breve.\nUsa pnpm.\nNo toques migrations/.\nCorre los tests antes de terminar.\nAbre un PR en borrador.\n",
) ?? []

const sampleUsage: UsageDay[] = Array.from({ length: 18 }, (_, i) => ({
  day: new Date(now - (i * 1.6 + (i % 3)) * 86_400_000).toISOString().slice(0, 10) + "T00:00:00Z",
  provider: "anthropic",
  model: i % 4 === 0 ? "claude-haiku-4-5" : "claude-sonnet-4-5",
  requests: 12 + ((i * 7) % 30),
  input_tokens: 40_000 + ((i * 9_301) % 90_000),
  output_tokens: 6_000 + ((i * 1_733) % 12_000),
  cache_write_tokens: 8_000,
  cache_read_tokens: 120_000 + ((i * 17_011) % 200_000),
}))

function Domain() {
  const [active, setActive] = React.useState("c-ana")
  return (
    <Block title="Componentes de dominio" description="src/components/seaport: lo que se arma en las pantallas.">
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
        <Specimen label="AgentCard (una conexión, como el agente de la biblioteca)">
          <AgentCard agent={sampleAgent} now={now} titleOf={() => "Ana Torres"} />
        </Specimen>
        <Specimen label="AgentChip (@nombre; punto = conectado, estrella = por defecto)" className="flex flex-wrap items-center gap-2">
          <AgentChip name="pm" online isDefault />
          <AgentChip name="reviewer" online={false} />
          <AgentChip name="soporte" />
        </Specimen>
        <Specimen label="SlotRow (instancias de un agente alojado; v2 → v3 = reinicia al quedar libre)" className="lg:col-span-2">
          <ul className="divide-y rounded-lg border bg-card">
            {sampleSlots.map((s) => (
              <li key={s.index}>
                <SlotRow
                  name={`pm-${s.index}`}
                  slot={s}
                  current={3}
                  now={now}
                  actions={
                    <>
                      <Button variant="ghost" size="icon-sm" aria-label="Registro">
                        <TerminalSquare />
                      </Button>
                      <Button variant="ghost" size="icon-sm" aria-label="Arrancar o detener">
                        {s.state === "running" || s.state === "starting" ? <Square /> : <Play />}
                      </Button>
                    </>
                  }
                />
              </li>
            ))}
          </ul>
        </Specimen>
        <Specimen label="AccountBadge (cuenta de una instancia con suscripción; experimental)" className="flex flex-wrap items-center gap-2">
          <AccountBadge account={undefined} />
          <AccountBadge account={{ slot: 1, supported: true, logged_in: false, method: "none" }} />
          <AccountBadge account={{ slot: 1, supported: true, logged_in: true, method: "claude.ai", subscription: "max" }} />
          <AccountBadge account={{ slot: 1, supported: true, logged_in: true, method: "oauth_token" }} />
        </Specimen>
        <Specimen label="BuildBadge (imagen de una versión del entorno)" className="flex flex-wrap items-center gap-2">
          <BuildBadge build={null} />
          <BuildBadge build={sampleBuild({ state: "building", finished_at: null })} />
          <BuildBadge build={sampleBuild({})} />
          <BuildBadge build={sampleBuild({ state: "failed", version: 2 })} />
        </Specimen>
        <Specimen label="LogView (build o instancia; sigue el final)">
          <LogView
            className="max-h-40"
            text={"#5 [2/3] RUN apt-get install -y nodejs npm\n#5 DONE 21.4s\n#6 [3/3] RUN bash /opt/seaport/setup.sh\n#6 DONE 3.1s\nnaming to seaport-env-backend:v3-1a2b3c done"}
          />
        </Specimen>
        <Specimen label="HomeTree (configuración del harness; cambios vs otra versión)">
          <div className="rounded-lg border p-1.5">
            <HomeTree files={sampleHomeFiles} changes={sampleHomeChanges} selected="CLAUDE.md" />
          </div>
        </Specimen>
        <Specimen label="DiffView (líneas; lo que no cambia se pliega)">
          <DiffView lines={sampleDiff} context={1} />
        </Specimen>
        <Specimen label="UsageChart (tokens por día por el gateway)" className="lg:col-span-2">
          <UsageChart days={sampleUsage} />
        </Specimen>
        <Specimen label="PairingPanel" className="lg:col-span-2">
          <Card className="py-0">
            <CardContent className="p-6">
              <PairingPanel qr="2@seaport-design-system-sample,qr,code" />
            </CardContent>
          </Card>
        </Specimen>
      </div>
    </Block>
  )
}

function Conversation() {
  return (
    <Block title="Conversación" description="Burbujas: entrantes a la izquierda, el agente a la derecha; retenidos punteados; bajo cada respuesta, el resumen guardado de herramientas; y lo que el agente hace en vivo.">
      <Card className="gap-0 overflow-hidden py-0">
        <div className="bg-canvas px-4 pb-4 md:px-10">
          <DayDivider label="Hoy" />
          <ShipBubble ship={sampleShip({ text: "Hola! ¿Tienen stock del modelo X200?", sent_at: ago(14) })} showSender />
          <ShipBubble ship={sampleShip({ text: "Y si sí, ¿cuánto demora el envío a Arequipa?", sent_at: ago(13) })} grouped />
          <ShipBubble
            ship={sampleShip({ direction: "out", status: "sent", text: "¡Hola Ana! Sí, quedan 4 unidades. A Arequipa demora 2 días hábiles.", sent_at: ago(12) })}
          />
          <ShipBubble ship={sampleShip({ direction: "out", status: "pending", text: "¿Te reservo una?", sent_at: ago(12) })} grouped />
          {/* What the agent did for that answer, stored when the turn ended (tools only, folded). */}
          <TurnSummary turn={sampleTurn} className="mt-1" />
          <ShipBubble ship={sampleShip({ text: "Mensaje en grupo sin mención", status: "held", sender_name: "Pedro", sent_at: ago(6) })} showSender />
          <ShipBubble ship={sampleShip({ direction: "out", status: "failed", error: "not connected", text: "Esto no salió", sent_at: ago(2) })} />
          <ShipBubble ship={sampleShip({ text: "Perfecto, resérvame una 🙌", status: "claimed", sent_at: ago(1) })} />
          {/* What the agent is doing on that message, live (never stored). */}
          <AgentActivity activity={sampleActivity} className="mt-3" />
          {/* An image attachment (no bytes behind it here: it shows the unavailable state). */}
          <ShipBubble
            ship={sampleShip({
              text: "Así llegó la caja",
              status: "handled",
              sent_at: ago(1),
              attachments: [{ id: "00000000-0000-0000-0000-000000000000", kind: "image", mime: "image/jpeg", size: 245_000, sha256: "" }],
            })}
          />
          {/* A document an agent sent with its reply: a download chip. */}
          <ShipBubble
            ship={sampleShip({
              direction: "out",
              status: "sent",
              text: "Revisión QA lista.",
              sent_at: ago(1),
              attachments: [
                { id: "00000000-0000-0000-0000-000000000001", kind: "document", mime: "text/plain; charset=utf-8", size: 4_200, name: "qa-report.md", sha256: "" },
              ],
            })}
          />
        </div>
        <Composer onSend={() => new Promise((r) => setTimeout(r, 600))} />
      </Card>
    </Block>
  )
}

const sampleMarkdown = [
  "(on docs/guides/github.md:161) **A corregir.** Los enlaces apuntan al commit ([files.go#L100](https://github.com/acme/web/blob/main/files.go#L100)), no a la rama `seaport-files`.",
  "",
  "```suggestion",
  "- **File links give `404`.** The reader has no access to the repository.",
  "```",
  "",
  "| Check | Estado |",
  "| :--- | :--- |",
  "| Permisos | ✅ |",
  "| Enlaces | ❌ |",
  "",
  "- [x] Leer el diff",
  "- [ ] Probar en local",
  "",
  "> Raw <b>HTML</b> no se muestra; [rutas relativas](../x.md) quedan como texto.",
].join("\n")

function MarkdownMessages() {
  return (
    <Block title="Mensajes en Markdown" description="GitHub y los muelles API escriben Markdown (negritas, código, sugerencias, tablas, listas de tareas); WhatsApp se muestra tal cual. Sin HTML crudo; solo enlaces http(s) absolutos, en otra pestaña.">
      <Card className="gap-0 overflow-hidden py-0">
        <div className="bg-canvas px-4 pb-4 md:px-10">
          <ShipBubble ship={sampleShip({ text: "@qa revisa el PR por favor, sobre todo `files.go`", sent_at: ago(5) })} markdown />
          <ShipBubble ship={sampleShip({ direction: "out", status: "sent", text: sampleMarkdown, sent_at: ago(4) })} markdown />
        </div>
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
          <NoAccess what="los tokens de agentes" />
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
