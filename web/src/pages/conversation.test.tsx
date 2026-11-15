import { screen } from "@testing-library/react"
import { beforeEach, describe, expect, it } from "vitest"

import { keys } from "@/lib/query"
import { Perm } from "@/lib/permissions.gen"
import type { Approval, Conversation, CrewStatus, Dock, Paginated, Ship, Turn } from "@/lib/types"
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
