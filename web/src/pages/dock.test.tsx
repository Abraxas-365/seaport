import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type { DatadogInfo, Dock, InboxItem, WebhookInfo, WhatsAppSettings } from "@/lib/types"
import { infinite } from "@/test/query"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { DockPage } from "./dock"

beforeEach(stubNetwork)

const readOnly = [Perm.DocksRead, Perm.ConversationsRead]

function renderDock(dock: Dock, seed: (qc: ReturnType<typeof seededClient>) => void = () => {}) {
  const qc = seededClient(readOnly)
  qc.setQueryData(keys.dock(dock.id), dock)
  qc.setQueryData(keys.inbox({ dock_id: dock.id }), infinite<InboxItem>([]))
  seed(qc)
  return renderAt(<DockPage />, qc, { pattern: "/docks/:dockId", path: `/docks/${dock.id}` })
}

const webhookInfo = (): WebhookInfo => ({
  url: "https://seaport.example.com/hooks/abc",
  reachable: true,
  template: {},
  has_secret: true,
})

const datadogInfo = (): DatadogInfo => ({
  url: "https://seaport.example.com/datadog/webhook/abc",
  reachable: true,
  payload: "{}",
  has_secret: true,
  mirror: null,
})

describe("a webhook dock", () => {
  it("says no conversation arrived yet and how to try one", () => {
    const dock = apiData<Dock>("dock_trigger")
    renderDock(dock, (qc) => qc.setQueryData(keys.webhook(dock.id), webhookInfo()))
    expect(screen.getByText("Sin conversaciones")).toBeInTheDocument()
    expect(screen.getByText("Todavía no llega ningún barco a este muelle.")).toBeInTheDocument()
    expect(screen.getByText("Envía un mensaje de prueba con el formulario de arriba.")).toBeInTheDocument()
  })

  it("explains it only triggers: no replies card, no destinations", () => {
    const dock = apiData<Dock>("dock_trigger")
    renderDock(dock, (qc) => qc.setQueryData(keys.webhook(dock.id), webhookInfo()))
    expect(screen.getByText(/Este canal solo dispara/)).toBeInTheDocument()
    expect(screen.queryByText("Destinos")).not.toBeInTheDocument()
  })

  it("shows its URL while linked", () => {
    const dock = apiData<Dock>("dock_trigger")
    renderDock(dock, (qc) => qc.setQueryData(keys.webhook(dock.id), webhookInfo()))
    expect(screen.getByText("https://seaport.example.com/hooks/abc")).toBeInTheDocument()
  })

  it("says the webhook is closed once the dock is logged out", () => {
    const dock = { ...apiData<Dock>("dock_trigger"), status: "logged_out" as const }
    renderDock(dock)
    expect(screen.getByText("Webhook cerrado")).toBeInTheDocument()
    expect(screen.getByText(/La dirección no responde/)).toBeInTheDocument()
    // Read-only callers get no reconnect button.
    expect(screen.queryByRole("button", { name: /Reconectar/ })).not.toBeInTheDocument()
  })
})

describe("a Datadog dock", () => {
  const datadog = (): Dock => ({ ...apiData<Dock>("dock_trigger"), channel: "datadog", name: "Monitors" })

  it("tells how to send the first alert", () => {
    const dock = datadog()
    renderDock(dock, (qc) => qc.setQueryData(keys.datadog(dock.id), datadogInfo()))
    expect(screen.getByText("Sin conversaciones")).toBeInTheDocument()
    expect(screen.getByText(/Menciona el webhook en un monitor de Datadog/)).toBeInTheDocument()
  })

  it("says the webhook is closed when the dock isn't linked", () => {
    const dock = { ...datadog(), status: "failed" as const }
    renderDock(dock)
    expect(screen.getByText("Webhook de Datadog cerrado")).toBeInTheDocument()
  })
})

describe("the mirror card", () => {
  const chat = (): Dock => ({ ...apiData<Dock>("dock"), id: "00000000-0000-4000-8000-000000000001" as Dock["id"], name: "Support" })

  it("shows where a trigger dock's threads are opened", () => {
    const dock = apiData<Dock>("dock_trigger")
    renderDock(dock, (qc) => {
      qc.setQueryData(keys.webhook(dock.id), webhookInfo())
      qc.setQueryData(keys.docks, { items: [chat()], page: 1 })
    })
    expect(screen.getByText("Espejo")).toBeInTheDocument()
    expect(screen.getByText("Support")).toBeInTheDocument()
    expect(screen.getByText("oncall")).toBeInTheDocument()
  })

  it("says there is none, on a Datadog dock too", () => {
    const dock: Dock = { ...apiData<Dock>("dock_trigger"), channel: "datadog", mirror: undefined }
    renderDock(dock, (qc) => qc.setQueryData(keys.datadog(dock.id), datadogInfo()))
    expect(screen.getByText(/Sin espejo/)).toBeInTheDocument()
  })

  it("is not on a dock that replies", () => {
    const dock = { ...apiData<Dock>("dock"), channel: "api" as const, address: "" }
    renderDock(dock, (qc) => qc.setQueryData(keys.destinations(dock.id), []))
    expect(screen.queryByText("Espejo")).not.toBeInTheDocument()
  })

  it("keeps the replies switch off while the dock has a mirror", () => {
    const dock: Dock = { ...apiData<Dock>("dock"), channel: "slack", replies: false, mirror: { dock_id: chat().id, destination: "oncall" } }
    renderDock(dock)
    expect(screen.getByRole("switch", { name: "Permitir respuestas" })).toBeDisabled()
    expect(screen.getByText(/quítalo antes de activar las respuestas/)).toBeInTheDocument()
  })
})

describe("a dock that replies", () => {
  it("has the replies card instead of the trigger-only notice", () => {
    const dock = { ...apiData<Dock>("dock"), channel: "api" as const, address: "" }
    renderDock(dock, (qc) => qc.setQueryData(keys.destinations(dock.id), []))
    expect(screen.queryByText(/Este canal solo dispara/)).not.toBeInTheDocument()
    expect(screen.getByText("Sin conversaciones")).toBeInTheDocument()
    expect(screen.getByText("Envía un mensaje de prueba con el formulario de arriba.")).toBeInTheDocument()
  })
})

describe("the agent tools card", () => {
  it("shows each switch as the dock has it, read-only without docks:write", () => {
    // The trigger fixture keeps only the conversations list on.
    const dock = apiData<Dock>("dock_trigger")
    renderDock(dock, (qc) => qc.setQueryData(keys.webhook(dock.id), webhookInfo()))
    expect(screen.getByText("Herramientas del agente")).toBeInTheDocument()
    const history = screen.getByRole("switch", { name: "Leer el historial" })
    const list = screen.getByRole("switch", { name: "Listar las conversaciones" })
    const files = screen.getByRole("switch", { name: "Abrir archivos" })
    expect(history).not.toBeChecked()
    expect(list).toBeChecked()
    expect(files).not.toBeChecked()
    expect(history).toBeDisabled()
    expect(screen.getByText("El agente sabe que llegó un archivo, pero no puede abrirlo.")).toBeInTheDocument()
  })
})

describe("the seen-by-the-agent card", () => {
  afterEach(() => vi.unstubAllGlobals())
  const whatsapp = (): Dock => ({ ...apiData<Dock>("dock"), channel: "whatsapp", address: "+34600000000" })
  const seen = apiData<WhatsAppSettings>("whatsapp")

  it("shows the settings, read-only without docks:write", () => {
    const dock = whatsapp()
    renderDock(dock, (qc) => qc.setQueryData(keys.whatsapp(dock.id), seen))
    expect(screen.getByText("Visto por el agente")).toBeInTheDocument()
    const react = screen.getByRole("switch", { name: "Reaccionar" })
    const read = screen.getByRole("switch", { name: "Marcar como leído" })
    expect(react).toBeChecked()
    expect(read).not.toBeChecked()
    expect(react).toBeDisabled()
    expect(screen.getByText("Reacciona con 👀 al mensaje.")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Reaccionar con 👀" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.queryByRole("textbox", { name: "Otro emoji" })).not.toBeInTheDocument()
  })

  it("saves only what was changed", async () => {
    const user = userEvent.setup()
    const dock = whatsapp()
    const calls: { method: string; path: string; body?: unknown }[] = []
    let stored = seen
    vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
      if (!path.endsWith("/whatsapp")) return new Promise<Response>(() => {})
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ method: init?.method ?? "GET", path, body })
      stored = { ...stored, ...body }
      return new Response(JSON.stringify(stored), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    })
    const qc = seededClient([...readOnly, Perm.DocksWrite])
    qc.setQueryData(keys.dock(dock.id), dock)
    qc.setQueryData(keys.inbox({ dock_id: dock.id }), infinite<InboxItem>([]))
    qc.setQueryData(keys.whatsapp(dock.id), seen)
    renderAt(<DockPage />, qc, { pattern: "/docks/:dockId", path: `/docks/${dock.id}` })

    await user.click(screen.getByRole("switch", { name: "Marcar como leído" }))
    await user.type(screen.getByRole("textbox", { name: "Otro emoji" }), "🫶")
    await user.click(screen.getByRole("button", { name: "Usar" }))

    const puts = calls.filter((c) => c.method === "PUT")
    expect(puts.map((c) => c.path)).toEqual([`/api/v1/docks/${dock.id}/whatsapp`, `/api/v1/docks/${dock.id}/whatsapp`])
    expect(puts.map((c) => c.body)).toEqual([{ mark_read: true }, { emoji: "🫶" }])
    expect(await screen.findByText("Reacciona con 🫶 al mensaje.")).toBeInTheDocument()
    expect(screen.getByRole("switch", { name: "Marcar como leído" })).toBeChecked()
  })

  it("is not on other channels", () => {
    const dock = apiData<Dock>("dock")
    renderDock(dock)
    expect(screen.queryByText("Visto por el agente")).not.toBeInTheDocument()
  })
})
