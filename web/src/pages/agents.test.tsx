import { screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type { Agent, AgentProfile, CrewStatus, Dock, Hosted, HostingStatus, LibraryAgent } from "@/lib/types"
import { page } from "@/test/query"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { AgentsPage, agentStatus } from "./agents"

const hostedAgent = (): LibraryAgent => apiData<LibraryAgent>("agent")
const remoteAgent = (): LibraryAgent => ({ ...apiData<LibraryAgent>("agent"), id: "a-remote", name: "laptop-bot", takeovers: [] })

function seeded(runtime = true, held?: Parameters<typeof seededClient>[0]) {
  const qc = seededClient(held)
  const hosted = apiData<Hosted>("hosted")
  qc.setQueryData(keys.agents, page<LibraryAgent>([hostedAgent(), remoteAgent()]))
  qc.setQueryData(keys.docks, page<Dock>([apiData<Dock>("dock")]))
  qc.setQueryData(keys.crew, apiData<CrewStatus>("crew_status"))
  qc.setQueryData(keys.hosting, { agents: [hosted], runtime } satisfies HostingStatus)
  return qc
}

/** Answers the API calls of a test by "METHOD path", recording what was sent. */
function answer(routes: Record<string, unknown>) {
  const calls: { method: string; path: string; body?: unknown }[] = []
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET"
    calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const route = routes[`${method} ${path.split("?")[0]}`]
    if (route === undefined) return new Response("{}", { status: 404 })
    return new Response(JSON.stringify(route), { status: 200, headers: { "Content-Type": "application/json" } })
  })
  return calls
}

describe("AgentsPage", () => {
  beforeEach(stubNetwork)
  afterEach(() => vi.unstubAllGlobals())

  it("shows where each agent runs and what it's doing", () => {
    renderAt(<AgentsPage />, seeded())
    expect(screen.getByText("Alojado · 1 instancia")).toBeInTheDocument()
    expect(screen.getByText("Remoto")).toBeInTheDocument()
    expect(screen.getByText("Trabajando")).toBeInTheDocument()
    expect(screen.getByText("Desconectado")).toBeInTheDocument()
  })

  it("has no environments, crew or tokens to send people to", () => {
    renderAt(<AgentsPage />, seeded())
    expect(screen.queryByText(/entorno/i)).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /tokens/i })).not.toBeInTheDocument()
  })

  it("creates a hosted agent with a key: agent, secret, config v1, instances", async () => {
    const created = { ...remoteAgent(), id: "a-new", name: "pm", docks: [], profile_version: 0 }
    const calls = answer({
      "POST /api/v1/agents": created,
      "PUT /api/v1/agents/a-new/secrets/ANTHROPIC_KEY": { name: "ANTHROPIC_KEY", env: false, updated_at: "2030-01-02T03:00:00Z" },
      "PUT /api/v1/agents/a-new/config": { ...apiData<AgentProfile>("config"), agent_id: "a-new", version: 1 },
      "PUT /api/v1/agents/a-new/hosting": { ...apiData<Hosted>("hosted"), agent_id: "a-new" },
    })
    const user = userEvent.setup()
    renderAt(<AgentsPage />, seeded())
    await user.click(screen.getByRole("button", { name: /Nuevo agente/ }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Nombre"), "pm")
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    expect(within(dialog).getByRole("radio", { name: /En el puerto/ })).toHaveAttribute("aria-checked", "true")
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    expect(within(dialog).queryByRole("radio", { name: /Lo elige quien conecta/ })).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole("radio", { name: /rness/ }))
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    await user.click(within(dialog).getByRole("radio", { name: /Clave de API/ }))
    await user.type(within(dialog).getByLabelText("Clave de Anthropic"), "k-123")
    await user.click(within(dialog).getByRole("button", { name: "Crear agente" }))

    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.path.endsWith("/hosting"))).toBe(true))
    const sent = calls.filter((c) => c.method !== "GET").map((c) => `${c.method} ${c.path}`)
    expect(sent).toEqual([
      "POST /api/v1/agents",
      "PUT /api/v1/agents/a-new/secrets/ANTHROPIC_KEY",
      "PUT /api/v1/agents/a-new/config",
      "PUT /api/v1/agents/a-new/hosting",
    ])
    const config = calls.find((c) => c.path.endsWith("/config"))?.body as Record<string, unknown>
    expect(config.base).toBe(0)
    expect(config.harness).toBe("rness")
    expect((config.machine as { models: Record<string, string> }).models).toEqual({ anthropic: "${ANTHROPIC_KEY}" })
    expect(calls.find((c) => c.path.endsWith("/hosting") && c.method === "PUT")?.body).toEqual({ slots: 1 })
  })

  it("creates a remote agent with no machine and no hosting", async () => {
    const created = { ...remoteAgent(), id: "a-new", name: "pm", docks: [], profile_version: 0 }
    const calls = answer({
      "POST /api/v1/agents": created,
      "PUT /api/v1/agents/a-new/config": { ...apiData<AgentProfile>("config"), agent_id: "a-new", version: 1 },
    })
    const user = userEvent.setup()
    renderAt(<AgentsPage />, seeded())
    await user.click(screen.getByRole("button", { name: /Nuevo agente/ }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Nombre"), "pm")
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    await user.click(within(dialog).getByRole("radio", { name: /Donde alguien lo conecte/ }))
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    await user.click(within(dialog).getByRole("radio", { name: /Claude Code/ }))
    await user.click(within(dialog).getByRole("button", { name: "Crear agente" }))

    await waitFor(() => expect(calls.some((c) => c.path.endsWith("/config"))).toBe(true))
    expect(calls.some((c) => c.method === "PUT" && c.path.endsWith("/hosting"))).toBe(false)
    const config = calls.find((c) => c.path.endsWith("/config"))?.body as Record<string, unknown>
    expect(config.harness).toBe("claude-code")
    expect(config.machine).toBeNull()
  })

  it("still hosts on a port whose runtime is off, saying so, but proposes remote first", async () => {
    const user = userEvent.setup()
    renderAt(<AgentsPage />, seeded(false))
    await user.click(screen.getByRole("button", { name: /Nuevo agente/ }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Nombre"), "pm")
    await user.click(within(dialog).getByRole("button", { name: "Siguiente" }))
    expect(within(dialog).getByRole("radio", { name: /Donde alguien lo conecte/ })).toHaveAttribute("aria-checked", "true")
    expect(within(dialog).getByText(/aún no corre instancias/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole("radio", { name: /En el puerto/ }))
    expect(within(dialog).getByRole("button", { name: "Siguiente" })).toBeEnabled()
  })

  it("hides creating to a role that only reads", () => {
    renderAt(<AgentsPage />, seeded(true, [Perm.AgentsRead, Perm.DocksRead, Perm.CrewRead, Perm.HostingRead]))
    expect(screen.queryByRole("button", { name: /Nuevo agente/ })).not.toBeInTheDocument()
  })
})

describe("agentStatus", () => {
  const conn = (working: boolean): Agent => ({
    ...apiData<CrewStatus>("crew_status").agents[0],
    deliveries: working ? apiData<CrewStatus>("crew_status").agents[0].deliveries : [],
  })
  const hosted = (states: string[]): Hosted => ({
    ...apiData<Hosted>("hosted"),
    slots: states.map((state, i) => ({ ...apiData<Hosted>("hosted").slots[0], index: i + 1, state: state as never })),
  })

  it("reads the connections first, then the instances", () => {
    expect(agentStatus([conn(true)], null).label).toBe("Trabajando")
    expect(agentStatus([conn(false)], null).label).toBe("Libre")
    expect(agentStatus([], null).label).toBe("Desconectado")
    expect(agentStatus([], hosted(["stopped", "starting"])).label).toBe("Iniciando")
    expect(agentStatus([], hosted(["failed"])).label).toBe("Con fallos")
    expect(agentStatus([], hosted(["stopped"])).label).toBe("En espera")
  })
})
