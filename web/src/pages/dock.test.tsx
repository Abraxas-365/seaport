import { screen } from "@testing-library/react"
import { beforeEach, describe, expect, it } from "vitest"

import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type { DatadogInfo, Dock, InboxItem, WebhookInfo } from "@/lib/types"
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
