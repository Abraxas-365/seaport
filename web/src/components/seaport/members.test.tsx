import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { Invitation, Member, MemberApiKey } from "@/lib/types"
import { apiData } from "@/test/wire"
import { ApiKeyRow, CopyField, InvitationRow, MemberRow, roleLabel } from "./members"

const members = () => apiData<{ items: Member[] }>("members").items
const invitation = () => apiData<Invitation>("invitation")
const key = () => apiData<MemberApiKey>("member_api_key")
const now = Date.parse("2030-01-02T03:00:00Z")

describe("MemberRow", () => {
  it("shows the signed-in person read-only: no role picker, no remove", () => {
    const [me] = members()
    render(<MemberRow member={me} canAssign canRemove onChange={vi.fn()} onRemove={vi.fn()} />)
    expect(screen.getByText("Tú")).toBeInTheDocument()
    expect(screen.getByText("Administrador")).toBeInTheDocument()
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it("shows IAMKit's own roles but never lets Seaport change them", () => {
    const [me] = members()
    render(<MemberRow member={{ ...me, you: false }} />)
    expect(screen.getByText("Organization owner")).toBeInTheDocument()
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
  })

  it("offers role, companion switch and remove only to who may use them", async () => {
    const other = members()[1]
    const onChange = vi.fn()
    const onRemove = vi.fn().mockResolvedValue(undefined)
    render(<MemberRow member={other} canAssign canRemove onChange={onChange} onRemove={onRemove} />)
    expect(screen.getByRole("combobox", { name: /Rol de Luis Example/ })).toBeInTheDocument()
    await userEvent.click(screen.getByRole("switch", { name: /Administra miembros/ }))
    expect(onChange).toHaveBeenCalledWith({ role: "Viewer", manages_members: true })
    await userEvent.click(screen.getByRole("button", { name: /Quitar a Luis Example/ }))
    await userEvent.click(screen.getByRole("button", { name: "Quitar" }))
    expect(onRemove).toHaveBeenCalledOnce()
  })

  it("hides the controls from who may only read", () => {
    render(<MemberRow member={members()[1]} />)
    expect(screen.getByText("Lector")).toBeInTheDocument()
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it("says so when a person holds no Seaport role", () => {
    render(<MemberRow member={{ ...members()[1], role: "" }} />)
    expect(screen.getByText("Sin rol de Seaport")).toBeInTheDocument()
  })
})

describe("InvitationRow", () => {
  it("resends and revokes a pending invitation", async () => {
    const onResend = vi.fn()
    const onRevoke = vi.fn().mockResolvedValue(undefined)
    render(<InvitationRow invitation={invitation()} now={now} onResend={onResend} onRevoke={onRevoke} />)
    expect(screen.getByText("nueva@example.com")).toBeInTheDocument()
    expect(screen.getByText("Pendiente")).toBeInTheDocument()
    expect(screen.getByText("Operador")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: /Reenviar a nueva@example.com/ }))
    expect(onResend).toHaveBeenCalledOnce()
    await userEvent.click(screen.getByRole("button", { name: /Revocar la invitación/ }))
    await userEvent.click(screen.getByRole("button", { name: "Revocar" }))
    expect(onRevoke).toHaveBeenCalledOnce()
  })

  it("offers nothing on an invitation that is no longer pending", () => {
    render(<InvitationRow invitation={{ ...invitation(), status: "expired" }} now={now} onResend={vi.fn()} onRevoke={vi.fn()} />)
    expect(screen.getByText("Vencida")).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})

describe("ApiKeyRow", () => {
  it("shows the hint, never a value, and revokes an active key", async () => {
    const onRevoke = vi.fn().mockResolvedValue(undefined)
    render(<ApiKeyRow apiKey={key()} now={now} onRevoke={onRevoke} />)
    expect(screen.getByText("ik_key_AbCd…")).toBeInTheDocument()
    expect(screen.getByText("Lector")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: /Revocar la llave ci/ }))
    await userEvent.click(screen.getByRole("button", { name: "Revocar" }))
    expect(onRevoke).toHaveBeenCalledOnce()
  })

  it("has no revoke on a revoked key", () => {
    render(<ApiKeyRow apiKey={{ ...key(), state: "revoked" }} now={now} onRevoke={vi.fn()} />)
    expect(screen.getByText("Revocada")).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})

describe("CopyField", () => {
  it("copies the value", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } })
    render(<CopyField value="https://iam.example.com/invite?token=example" label="Enlace" />)
    await userEvent.click(screen.getByRole("button", { name: /Copiar/ }))
    expect(writeText).toHaveBeenCalledWith("https://iam.example.com/invite?token=example")
    expect(await screen.findByText("Copiado")).toBeInTheDocument()
    vi.unstubAllGlobals()
  })
})

describe("roleLabel", () => {
  it("translates Seaport's roles and leaves others as they are", () => {
    expect(roleLabel("Admin")).toBe("Administrador")
    expect(roleLabel("Auditor")).toBe("Auditor")
  })
})
