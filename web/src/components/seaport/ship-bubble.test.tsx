import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import type { Ship } from "@/lib/types"
import { renderAt, seededClient } from "@/test/render"
import { apiData } from "@/test/wire"
import { ShipBubble } from "./ship-bubble"

const bubble = (ship: Ship, props = {}) => renderAt(<ShipBubble ship={ship} {...props} />, seededClient())

/** The bubble's footer (time + status icon) carries the tooltip. */
async function hoverFooter(text: string) {
  const user = userEvent.setup()
  await user.hover(screen.getByText(text).closest("div")!)
}

describe("ShipBubble", () => {
  it("marks a held message and says why in Spanish", async () => {
    bubble(apiData<Ship>("ship_held"))
    expect(screen.getByText("retenido")).toBeInTheDocument()
    expect(screen.getByText("Hello, can you look at this?")).toBeInTheDocument()
    await hoverFooter("retenido")
    expect((await screen.findAllByText(/Retenido · .* · esperando tu decisión en la aduana/)).length).toBeGreaterThan(0)
  })

  it.each([
    ["conversation denied", "conversación bloqueada"],
    ["sender denied by a rule", "remitente bloqueado por una regla"],
    ["group message without a mention", "mensaje de grupo sin mención"],
    ["no agent is assigned to this dock", "ningún agente atiende este muelle"],
    ["no agent was called (mention one with @name)", "no se llamó a ningún agente (@nombre)"],
    ["its agent left the dock", "su agente dejó el muelle"],
  ])("translates the hold reason %j", async (reason, label) => {
    bubble({ ...apiData<Ship>("ship_held"), hold_reason: reason })
    await hoverFooter("retenido")
    expect((await screen.findAllByText(new RegExp(`· ${label.replace(/[()@]/g, "\\$&")}`))).length).toBeGreaterThan(0)
  })

  it("shows an unknown hold reason as the server sent it", async () => {
    bubble({ ...apiData<Ship>("ship_held"), hold_reason: "a reason from the future" })
    await hoverFooter("retenido")
    expect((await screen.findAllByText(/· a reason from the future/)).length).toBeGreaterThan(0)
  })

  it("does not call a delivered message held", () => {
    bubble(apiData<Ship>("ship_in"))
    expect(screen.queryByText("retenido")).not.toBeInTheDocument()
  })

  it("names the agent on the bubble only when several work the chat", () => {
    const { unmount } = bubble(apiData<Ship>("ship_out"), { agentName: "support-bot" })
    expect(screen.queryByText("@support-bot")).not.toBeInTheDocument()
    unmount()
    bubble(apiData<Ship>("ship_out"), { agentName: "support-bot", multiAgent: true })
    expect(screen.getByText("@support-bot")).toBeInTheDocument()
  })

  it("flags a failed send", () => {
    bubble({ ...apiData<Ship>("ship_out"), status: "failed", error: "channel down" })
    expect(screen.getByText("no se envió")).toBeInTheDocument()
  })
})
