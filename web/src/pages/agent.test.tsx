import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { keys } from "@/lib/query"
import { newMachine } from "@/lib/machine"
import { Perm } from "@/lib/permissions.gen"
import type {
  AgentProfile,
  AgentSecret,
  CrewStatus,
  Destination,
  Dock,
  Hosted,
  HostingStatus,
  LibraryAgent,
} from "@/lib/types"
import { page } from "@/test/query"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { AgentPage } from "./agent"

const agent = (): LibraryAgent => apiData<LibraryAgent>("agent")

function seeded({
  hosted = true,
  profile = {},
  held,
}: { hosted?: boolean; profile?: Partial<AgentProfile>; held?: Parameters<typeof seededClient>[0] } = {}) {
  const a = agent()
  const qc = seededClient(held)
  const config: AgentProfile = {
    ...apiData<AgentProfile>("config"),
    agent_id: a.id,
    version: a.profile_version,
    harness: "rness",
    machine: newMachine({ models: { openai: "${OPENAI_KEY}" } }),
    ...profile,
  }
  qc.setQueryData(keys.agent(a.id), a)
  qc.setQueryData(keys.agents, page<LibraryAgent>([a]))
  qc.setQueryData(keys.docks, page<Dock>([apiData<Dock>("dock")]))
  qc.setQueryData(keys.crew, apiData<CrewStatus>("crew_status"))
  qc.setQueryData(keys.profile(a.id, 0), config)
  qc.setQueryData(keys.secrets(a.id), {
    secrets: [
      { name: "OPENAI_KEY", env: false, updated_at: "2030-01-02T03:00:00Z" },
      { name: "SPARE", env: true, updated_at: "2030-01-02T03:00:00Z" },
    ] satisfies AgentSecret[],
  })
  qc.setQueryData(keys.hosted(a.id), hosted ? apiData<Hosted>("hosted") : null)
  qc.setQueryData(keys.hosting, { agents: hosted ? [apiData<Hosted>("hosted")] : [], runtime: true } satisfies HostingStatus)
  return { qc, a }
}

const at = (id: string, hash = "") => ({ pattern: "/agents/:agentId", path: `/agents/${id}${hash}` })

describe("AgentPage", () => {
  beforeEach(stubNetwork)
  afterEach(() => vi.unstubAllGlobals())

  it("has the agent-centric tabs and says where it runs", () => {
    const { qc, a } = seeded()
    renderAt(<AgentPage />, qc, at(a.id))
    for (const name of ["Resumen", "Configuración", "Secretos", "Dónde corre"])
      expect(screen.getByRole("tab", { name })).toBeInTheDocument()
    expect(screen.getByText("Alojado · 1 instancia")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Clonar/ })).toBeInTheDocument()
  })

  it("marks the secrets the config names and won't delete them", () => {
    const { qc, a } = seeded()
    renderAt(<AgentPage />, qc, at(a.id, "#secretos"))
    const list = screen.getByRole("list", { name: "Secretos" })
    expect(within(list).getByText("modelo openai")).toBeInTheDocument()
    expect(within(list).getByRole("button", { name: "Eliminar OPENAI_KEY" })).toBeDisabled()
    expect(within(list).getByRole("button", { name: "Eliminar SPARE" })).toBeEnabled()
  })

  it("edits the harness and the model access in Configuración", async () => {
    const { qc, a } = seeded()
    renderAt(<AgentPage />, qc, at(a.id, "#configuracion"))
    expect(screen.getByRole("radio", { name: /rness/ })).toBeInTheDocument()
    // A hosted agent can't drop its harness.
    expect(screen.queryByRole("radio", { name: /Lo elige quien conecta/ })).not.toBeInTheDocument()
    expect(screen.getByTestId("model-access")).toBeInTheDocument()
    expect(screen.getByText(/OpenAI con clave, desde el YAML/)).toBeInTheDocument()
  })

  it("sets the AI access in one place: Claude through an account or a key, other keys stay", async () => {
    const { qc, a } = seeded()
    renderAt(<AgentPage />, qc, at(a.id, "#configuracion"))
    const claude = screen.getByRole("radiogroup", { name: "Acceso a Claude" })
    // Claude only: the other providers aren't offered, a YAML key is named and kept.
    expect(within(claude).getAllByRole("radio")).toHaveLength(2)
    expect(screen.queryByText("Otros proveedores")).not.toBeInTheDocument()
    expect(screen.queryByRole("combobox", { name: "OpenAI" })).not.toBeInTheDocument()
    for (const r of within(claude).getAllByRole("radio")) expect(r).toHaveAttribute("aria-checked", "false")
    const user = userEvent.setup()
    await user.click(within(claude).getByRole("radio", { name: /Cuenta de Claude/ }))
    expect(within(claude).getByRole("radio", { name: /Cuenta de Claude/ })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("list", { name: "Cuentas de las instancias" })).toBeInTheDocument()
    expect(screen.getByText(/También OpenAI con clave, desde el YAML/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Guardar v/ })).toBeEnabled()
  })

  it("won't save a Claude key before its secret is picked", async () => {
    const { qc, a } = seeded()
    renderAt(<AgentPage />, qc, at(a.id, "#configuracion"))
    const user = userEvent.setup()
    await user.click(screen.getByRole("radio", { name: /Clave de API/ }))
    expect(screen.getByRole("radio", { name: /Clave de API/ })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByRole("combobox", { name: "Anthropic" })).toHaveTextContent("Elige un secreto…")
    expect(screen.getByRole("button", { name: /Guardar v/ })).toBeDisabled()
    expect(screen.getAllByText(/Elige el secreto con la clave de Anthropic/).length).toBeGreaterThan(0)
    // Discarding goes back to what the config says.
    await user.click(screen.getByRole("button", { name: "Descartar" }))
    expect(screen.getByRole("radio", { name: /Clave de API/ })).toHaveAttribute("aria-checked", "false")
    expect(screen.queryByRole("combobox", { name: "Anthropic" })).not.toBeInTheDocument()
  })

  it("leaves a Claude account for a key: the account goes, the key waits", async () => {
    const { qc, a } = seeded({ profile: { machine: newMachine({ models: { anthropic: "subscription" } }) } })
    renderAt(<AgentPage />, qc, at(a.id, "#configuracion"))
    const user = userEvent.setup()
    expect(screen.getByRole("radio", { name: /Cuenta de Claude/ })).toHaveAttribute("aria-checked", "true")
    await user.click(screen.getByRole("radio", { name: /Clave de API/ }))
    expect(screen.getByRole("radio", { name: /Clave de API/ })).toHaveAttribute("aria-checked", "true")
    expect(screen.queryByRole("list", { name: "Cuentas de las instancias" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Guardar v/ })).toBeDisabled()
  })

  it("lets a remote agent go without a harness or machine", async () => {
    const { qc, a } = seeded({ hosted: false, profile: { harness: "", machine: null } })
    renderAt(<AgentPage />, qc, at(a.id, "#configuracion"))
    expect(screen.getByRole("radio", { name: /Lo elige quien conecta/ })).toBeInTheDocument()
    expect(screen.queryByTestId("model-access")).not.toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole("radio", { name: /Claude Code/ }))
    await user.click(screen.getByRole("switch", { name: "Máquina" }))
    expect(screen.getByTestId("model-access")).toBeInTheDocument()
  })

  it("shows no configuration controls to a role that only reads", () => {
    const { qc, a } = seeded({ held: [Perm.AgentsRead, Perm.HostingRead, Perm.CrewRead, Perm.DocksRead] })
    renderAt(<AgentPage />, qc, at(a.id, "#secretos"))
    expect(screen.queryByRole("button", { name: /Clonar/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Eliminar/ })).not.toBeInTheDocument()
  })

  describe("alerts", () => {
    const dock = apiData<Dock>("dock")
    const destinations: Destination[] = [
      { dock_id: dock.id, name: "ops", target: "C0123456789", title: "#ops", created_at: "2030-01-02T03:00:00Z" },
    ]

    /** Answers PATCH /hosting with the hosted agent the body makes, recording the bodies. */
    function patches(a: LibraryAgent) {
      const bodies: unknown[] = []
      let stored = apiData<Hosted>("hosted")
      vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
        if (init?.method !== "PATCH" || path !== `/api/v1/agents/${a.id}/hosting`)
          return new Promise<Response>(() => {})
        const body = JSON.parse(String(init.body)) as { alerts?: Hosted["alerts"] }
        bodies.push(body)
        stored = { ...stored, alerts: body.alerts ? { ...body.alerts, error: "" } : null }
        return new Response(JSON.stringify(stored), { status: 200, headers: { "Content-Type": "application/json" } })
      })
      return bodies
    }

    it("says where they go and saves a new wait", async () => {
      const { qc, a } = seeded()
      qc.setQueryData(keys.destinations(dock.id), destinations)
      const bodies = patches(a)
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      expect(screen.getByTestId("alerts-now")).toHaveTextContent("Ahora: Support (Slack) · ops")
      expect(screen.getByTestId("alerts-now")).toHaveTextContent("más de 10 minutos")
      const save = screen.getByRole("button", { name: /Guardar avisos/ })
      expect(save).toBeDisabled()

      const user = userEvent.setup()
      const wait = screen.getByLabelText(/Avisar si una conversación espera/)
      await user.clear(wait)
      await user.type(wait, "0")
      expect(screen.getByText(/Entre 1 y 1440 minutos/)).toBeInTheDocument()
      expect(save).toBeDisabled()
      await user.clear(wait)
      await user.type(wait, "90")
      expect(screen.getByText("1 h 30 min")).toBeInTheDocument()
      await user.click(save)
      expect(bodies).toEqual([{ alerts: { dock_id: dock.id, destination: "ops", wait: "90m" } }])
      expect(await screen.findByText(/más de 1 h 30 min/)).toBeInTheDocument()
    })

    it("turns them off", async () => {
      const { qc, a } = seeded()
      qc.setQueryData(keys.destinations(dock.id), destinations)
      const bodies = patches(a)
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      await userEvent.click(screen.getByRole("button", { name: /Apagar avisos/ }))
      expect(bodies).toEqual([{ alerts: null }])
      expect(await screen.findByText(/Sin avisos/)).toBeInTheDocument()
      expect(screen.queryByRole("button", { name: /Apagar avisos/ })).not.toBeInTheDocument()
    })

    it("shows why the last notice failed", () => {
      const { qc, a } = seeded()
      const hosted = apiData<Hosted>("hosted")
      qc.setQueryData(keys.hosted(a.id), { ...hosted, alerts: { ...hosted.alerts!, error: "the dock is unlinked" } })
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      expect(screen.getByRole("alert")).toHaveTextContent("No se pudo enviar el último aviso: the dock is unlinked")
    })

    it("offers only docks that open conversations", () => {
      const { qc, a } = seeded()
      qc.setQueryData(keys.docks, page<Dock>([{ ...dock, channel: "email" }]))
      qc.setQueryData(keys.hosted(a.id), { ...apiData<Hosted>("hosted"), alerts: null })
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      expect(screen.getByText(/Ningún muelle puede abrir conversaciones/)).toBeInTheDocument()
    })

    it("is read-only without hosting:write", () => {
      const held = [Perm.AgentsRead, Perm.HostingRead, Perm.CrewRead, Perm.DocksRead]
      const { qc, a } = seeded({ held })
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      expect(screen.getByTestId("alerts-now")).toHaveTextContent("ops")
      expect(screen.queryByRole("button", { name: /Guardar avisos/ })).not.toBeInTheDocument()
    })

    it("is hidden from a reader when they are off", () => {
      const { qc, a } = seeded({ held: [Perm.AgentsRead, Perm.HostingRead, Perm.CrewRead, Perm.DocksRead] })
      qc.setQueryData(keys.hosted(a.id), { ...apiData<Hosted>("hosted"), alerts: null })
      renderAt(<AgentPage />, qc, at(a.id, "#donde"))
      expect(screen.queryByTestId("alerts-now")).not.toBeInTheDocument()
    })
  })
})
