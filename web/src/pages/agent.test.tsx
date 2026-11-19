import { screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { keys } from "@/lib/query"
import { newMachine } from "@/lib/machine"
import { Perm } from "@/lib/permissions.gen"
import type { AgentProfile, AgentSecret, CrewStatus, Dock, Hosted, HostingStatus, LibraryAgent } from "@/lib/types"
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
    expect(screen.getAllByText(/OPENAI_KEY/).length).toBeGreaterThan(0)
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
})
