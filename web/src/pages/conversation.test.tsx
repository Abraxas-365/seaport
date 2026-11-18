import { screen } from "@testing-library/react"
import { beforeEach, describe, expect, it } from "vitest"

import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type {
  AgentProfile,
  Approval,
  Conversation,
  CrewStatus,
  Dock,
  Environment,
  Hosted,
  LibraryAgent,
  Paginated,
  Ship,
  TeamMember,
  Turn,
} from "@/lib/types"
import { infinite, page } from "@/test/query"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { ConversationPage } from "./conversation"

beforeEach(stubNetwork)

function renderConversation(conv: Conversation, docks: Dock[], held = [Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead, Perm.ShipsWrite]) {
  const qc = seededClient(held)
  qc.setQueryData(keys.conversation(conv.id), conv)
  qc.setQueryData(keys.thread(conv.id), [conv])
  qc.setQueryData(keys.ships(conv.id), infinite<Ship>([]))
  qc.setQueryData(keys.turns(conv.id), page<Turn>([]))
  qc.setQueryData(keys.approvals(conv.id), page<Approval>([]))
  qc.setQueryData(keys.crew, { agents: [] } satisfies CrewStatus)
  qc.setQueryData(keys.docks, page<Dock>(docks) satisfies Paginated<Dock>)
  return renderAt(<ConversationPage />, qc, { pattern: "/inbox/:conversationId", path: `/inbox/${conv.id}` })
}

const conversation = (over: Partial<Conversation> = {}): Conversation => ({ ...apiData<Conversation>("conversation"), ...over })

describe("the composer", () => {
  it("is a form on a dock that replies, for an allowed chat", () => {
    const dock = apiData<Dock>("dock")
    renderConversation(conversation({ dock_id: dock.id }), [dock])
    expect(screen.getByRole("textbox")).toBeEnabled()
  })

  it("is replaced by the reason on a webhook dock (channels that only trigger)", () => {
    const dock = apiData<Dock>("dock_trigger")
    renderConversation(conversation({ dock_id: dock.id, channel: "webhook" }), [dock])
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
    expect(screen.getByText(/Este canal solo dispara: nada se publica aquí/)).toBeInTheDocument()
  })

  it("is replaced by the reason on a Datadog dock", () => {
    const dock = { ...apiData<Dock>("dock_trigger"), channel: "datadog" as const }
    renderConversation(conversation({ dock_id: dock.id, channel: "datadog" }), [dock])
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
    expect(screen.getByText(/Este canal solo dispara/)).toBeInTheDocument()
  })

  it("says replies are off when the owner switched them off on a channel that could", () => {
    const dock = { ...apiData<Dock>("dock"), replies: false }
    renderConversation(conversation({ dock_id: dock.id }), [dock])
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
    expect(screen.getByText("Este muelle solo dispara: las respuestas están apagadas.")).toBeInTheDocument()
  })

  it("asks to allow a pending chat", () => {
    const dock = apiData<Dock>("dock")
    renderConversation(conversation({ dock_id: dock.id, status: "pending" }), [dock])
    expect(screen.getByText("Permite este chat para poder responder.")).toBeInTheDocument()
  })

  it("is absent for who can't write ships", () => {
    const dock = apiData<Dock>("dock")
    renderConversation(conversation({ dock_id: dock.id }), [dock], [Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead])
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })
})

describe("a message held for routing", () => {
  const held = (over: Partial<Ship> = {}): Ship => ({
    ...apiData<Ship>("ship_held"),
    hold_reason: "no agent is assigned to this dock",
    created_at: new Date().toISOString(),
    ...over,
  })
  const agent = apiData<LibraryAgent>("agent")

  function renderHeld(ships: Ship[], team: TeamMember[], perms = [Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead, Perm.AgentsRead, Perm.AgentsWrite]) {
    const dock = apiData<Dock>("dock")
    const conv = conversation({ dock_id: dock.id })
    const qc = seededClient(perms)
    qc.setQueryData(keys.conversation(conv.id), conv)
    qc.setQueryData(keys.thread(conv.id), [conv])
    qc.setQueryData(keys.ships(conv.id), infinite<Ship>(ships))
    qc.setQueryData(keys.turns(conv.id), page<Turn>([]))
    qc.setQueryData(keys.approvals(conv.id), page<Approval>([]))
    qc.setQueryData(keys.crew, { agents: [] } satisfies CrewStatus)
    qc.setQueryData(keys.docks, page<Dock>([dock]))
    qc.setQueryData(keys.team(dock.id), { agents: team })
    qc.setQueryData(keys.agents, page<LibraryAgent>([{ ...agent, takeovers: [] }]))
    return renderAt(<ConversationPage />, qc, { pattern: "/inbox/:conversationId", path: `/inbox/${conv.id}` })
  }

  it("says why and offers to assign an agent, without resending", () => {
    renderHeld([held()], [])
    expect(screen.getByText(/Ningún agente atiende/)).toBeInTheDocument()
    expect(screen.getByText(/le llega lo retenido, sin reenviarlo/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Asignar" })).toBeDisabled()
  })

  it("asks for a default when the dock has a team but nobody was called", () => {
    renderHeld([held({ hold_reason: "no agent was called (mention one with @name)" })], [{ agent_id: agent.id, name: agent.name, default: false }])
    expect(screen.getByText(/no tiene uno por defecto/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Hacer por defecto" })).toBeInTheDocument()
  })

  it("tells to write again when the message is past the release window", () => {
    renderHeld([held({ created_at: new Date(Date.now() - 2 * 86_400_000).toISOString() })], [])
    expect(screen.getByText(/asignar un agente no lo entrega; escribe de nuevo/)).toBeInTheDocument()
  })

  it("links to the dock for who can't edit teams", () => {
    renderHeld([held()], [], [Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead, Perm.AgentsRead])
    expect(screen.getByRole("link", { name: "Ver el muelle" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Asignar" })).not.toBeInTheDocument()
  })

  it("is gone once a newer message went through", () => {
    renderHeld([apiData<Ship>("ship_out"), held()], [])
    expect(screen.queryByText(/Ningún agente atiende/)).not.toBeInTheDocument()
  })

  it("is not shown for customs holds (the access banner covers those)", () => {
    renderHeld([held({ hold_reason: "sender denied by a rule" })], [])
    expect(screen.queryByText(/sin reenviarlo/)).not.toBeInTheDocument()
  })
})

describe("browser takeovers", () => {
  it("shows the agent's request for this conversation with its browser and Listo", () => {
    const dock = apiData<Dock>("dock")
    const agent = apiData<LibraryAgent>("agent")
    const env = apiData<Environment>("environment")
    const conv = conversation({ dock_id: dock.id, id: agent.takeovers[0].conversation_id! })
    const qc = seededClient([Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead, Perm.AgentsRead, Perm.EnvironmentsRead, Perm.BrowserUse])
    qc.setQueryData(keys.conversation(conv.id), conv)
    qc.setQueryData(keys.thread(conv.id), [conv])
    qc.setQueryData(keys.ships(conv.id), infinite<Ship>([]))
    qc.setQueryData(keys.turns(conv.id), page<Turn>([]))
    qc.setQueryData(keys.approvals(conv.id), page<Approval>([]))
    qc.setQueryData(keys.crew, { agents: [] } satisfies CrewStatus)
    qc.setQueryData(keys.docks, page<Dock>([dock]))
    qc.setQueryData(keys.team(dock.id), { agents: [{ agent_id: agent.id, name: agent.name, default: true }] })
    qc.setQueryData(keys.agents, page<LibraryAgent>([agent]))
    qc.setQueryData(keys.hosted(agent.id), { ...apiData<Hosted>("hosted"), environment_id: env.id })
    qc.setQueryData(keys.environments, page<Environment>([env]))
    qc.setQueryData(keys.profile(agent.id, 0), { agent_id: agent.id, version: 3, browser: true } as AgentProfile)
    renderAt(<ConversationPage />, qc, { pattern: "/inbox/:conversationId", path: `/inbox/${conv.id}` })
    expect(screen.getByText(/necesita que/)).toBeInTheDocument()
    expect(screen.getByText("app.example.com")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Abrir navegador/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Listo" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: `Abrir el navegador de @${agent.name}` })).toBeInTheDocument()
  })

  it("is absent in other conversations", () => {
    const dock = apiData<Dock>("dock")
    const agent = apiData<LibraryAgent>("agent")
    const conv = conversation({ dock_id: dock.id, id: "00000000-0000-4000-8000-0000000000ff" })
    const qc = seededClient([Perm.ConversationsRead, Perm.DocksRead, Perm.ShipsRead, Perm.AgentsRead, Perm.BrowserUse])
    qc.setQueryData(keys.conversation(conv.id), conv)
    qc.setQueryData(keys.thread(conv.id), [conv])
    qc.setQueryData(keys.ships(conv.id), infinite<Ship>([]))
    qc.setQueryData(keys.turns(conv.id), page<Turn>([]))
    qc.setQueryData(keys.approvals(conv.id), page<Approval>([]))
    qc.setQueryData(keys.crew, { agents: [] } satisfies CrewStatus)
    qc.setQueryData(keys.docks, page<Dock>([dock]))
    qc.setQueryData(keys.agents, page<LibraryAgent>([agent]))
    renderAt(<ConversationPage />, qc, { pattern: "/inbox/:conversationId", path: `/inbox/${conv.id}` })
    expect(screen.queryByText(/necesita que/)).not.toBeInTheDocument()
  })
})
