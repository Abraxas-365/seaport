import { screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { keys } from "@/lib/query"
import type { Invitation, IssuedMemberApiKey, Member, MemberAccess, Paginated } from "@/lib/types"
import { page } from "@/test/query"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { MembersPage } from "./members"

const access = (over: Partial<MemberAccess> = {}): MemberAccess => ({ ...apiData<MemberAccess>("members_access"), ...over })

function seeded(a: MemberAccess) {
  const qc = seededClient()
  qc.setQueryData(keys.config, { auth: "iam", version: "test" })
  qc.setQueryData(keys.memberAccess, a)
  qc.setQueryData(keys.members, apiData<Paginated<Member>>("members"))
  qc.setQueryData(keys.memberInvitations, page<Invitation>([]))
  qc.setQueryData(keys.memberApiKeys, page([]))
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

describe("MembersPage", () => {
  beforeEach(stubNetwork)
  afterEach(() => vi.unstubAllGlobals())

  it("shows no page to a person who can't administer", () => {
    renderAt(<MembersPage />, seeded(access({ members: false, invite: false, assign_roles: false, remove: false, keys_read: false, keys_write: false })))
    expect(screen.getByText("Sin permiso")).toBeInTheDocument()
    expect(screen.queryByRole("tab", { name: "Miembros" })).not.toBeInTheDocument()
  })

  it("lists the members under the three tabs", () => {
    renderAt(<MembersPage />, seeded(access()))
    expect(screen.getByRole("heading", { name: "Miembros y accesos" })).toBeInTheDocument()
    expect(screen.getByRole("tab", { name: "Invitaciones" })).toBeInTheDocument()
    expect(screen.getByRole("tab", { name: "Llaves de API" })).toBeInTheDocument()
    expect(screen.getByText("Ana Example")).toBeInTheDocument()
    expect(screen.getByText("Luis Example")).toBeInTheDocument()
  })

  it("hides the controls the person's permissions don't include", () => {
    renderAt(<MembersPage />, seeded(access({ assign_roles: false, remove: false })))
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Quitar a/ })).not.toBeInTheDocument()
  })

  it("changes a member's role with the rest of their access", async () => {
    const calls = answer({ "PUT /api/v1/members/user-2": { ...apiData<{ items: Member[] }>("members").items[1], role: "Operator" } })
    renderAt(<MembersPage />, seeded(access()))
    await userEvent.click(screen.getByRole("switch", { name: /Administra miembros: Luis Example/ }))
    await waitFor(() => expect(calls).toContainEqual({ method: "PUT", path: "/api/v1/members/user-2", body: { role: "Viewer", manages_members: true } }))
  })

  it("shows the invitation link when IAMKit didn't send the email", async () => {
    const issued: Invitation = { ...apiData<Invitation>("invitation"), link: "https://iam.example.com/invite?token=abc" }
    const calls = answer({ "POST /api/v1/members/invitations": issued })
    renderAt(<MembersPage />, seeded(access()))
    await userEvent.click(screen.getByRole("tab", { name: "Invitaciones" }))
    await userEvent.click(screen.getAllByRole("button", { name: /Invitar/ })[0])
    await userEvent.type(screen.getByLabelText("Correo"), "nueva@example.com")
    await userEvent.click(screen.getByRole("button", { name: "Invitar" }))
    expect(await screen.findByTestId("invitation-link")).toHaveTextContent("https://iam.example.com/invite?token=abc")
    expect(calls).toContainEqual({
      method: "POST",
      path: "/api/v1/members/invitations",
      body: { email: "nueva@example.com", role: "Viewer", manages_members: false },
    })
  })

  it("says the email went out when there is no link", async () => {
    answer({ "POST /api/v1/members/invitations": { ...apiData<Invitation>("invitation"), link: undefined } })
    renderAt(<MembersPage />, seeded(access()))
    await userEvent.click(screen.getByRole("tab", { name: "Invitaciones" }))
    await userEvent.click(screen.getAllByRole("button", { name: /Invitar/ })[0])
    await userEvent.type(screen.getByLabelText("Correo"), "nueva@example.com")
    await userEvent.click(screen.getByRole("button", { name: "Invitar" }))
    expect(await screen.findByText("Invitación enviada")).toBeInTheDocument()
    expect(screen.queryByTestId("invitation-link")).not.toBeInTheDocument()
  })

  it("shows a new API key's value once, and not after closing", async () => {
    const issued = apiData<IssuedMemberApiKey>("member_api_key_issued")
    answer({ "POST /api/v1/members/api-keys": issued })
    renderAt(<MembersPage />, seeded(access()))
    await userEvent.click(screen.getByRole("tab", { name: "Llaves de API" }))
    await userEvent.click(screen.getAllByRole("button", { name: /Nueva llave/ })[0])
    await userEvent.type(screen.getByLabelText("Nombre"), "ci")
    await userEvent.click(screen.getByRole("button", { name: "Crear llave" }))
    expect(await screen.findByTestId("issued-api-key")).toHaveTextContent(issued.value)
    await userEvent.click(screen.getByRole("button", { name: "Listo" }))
    expect(screen.queryByTestId("issued-api-key")).not.toBeInTheDocument()
  })

  it("won't create keys for who can only read them", async () => {
    renderAt(<MembersPage />, seeded(access({ keys_write: false })))
    await userEvent.click(screen.getByRole("tab", { name: "Llaves de API" }))
    expect(screen.queryByRole("button", { name: /Nueva llave/ })).not.toBeInTheDocument()
  })
})
