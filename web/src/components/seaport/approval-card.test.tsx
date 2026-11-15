import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { Approval } from "@/lib/types"
import { apiData } from "@/test/wire"
import { ApprovalCard } from "./approval-card"

const pending = (): Approval => ({ ...apiData<Approval>("approval"), status: "pending", answer: undefined, answered_by: undefined })

describe("ApprovalCard", () => {
  it("offers each option and a cancel to who can answer, and reports the choice", async () => {
    const onAnswer = vi.fn()
    const onCancel = vi.fn()
    render(<ApprovalCard approval={pending()} canAnswer onAnswer={onAnswer} onCancel={onCancel} />)
    expect(screen.getByText("Esperando respuesta")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: "No" }))
    expect(onAnswer).toHaveBeenCalledWith("No")
    await userEvent.click(screen.getByRole("button", { name: /Cancelar/ }))
    expect(onCancel).toHaveBeenCalledOnce()
  })

  it("disables the buttons while an answer is in flight", () => {
    render(<ApprovalCard approval={pending()} canAnswer busy />)
    for (const b of screen.getAllByRole("button")) expect(b).toBeDisabled()
  })

  it("lists the options as text for who can't answer", () => {
    render(<ApprovalCard approval={pending()} />)
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
    expect(screen.getByText("Opciones: Yes · No")).toBeInTheDocument()
  })

  it("shows the answer and who gave it, with no buttons", () => {
    render(<ApprovalCard approval={apiData<Approval>("approval")} canAnswer />)
    expect(screen.getByText("Respondida")).toBeInTheDocument()
    expect(screen.getByText("Yes")).toBeInTheDocument()
    expect(screen.getByText(/\+34600000001 \(Ana Example\)/)).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it.each([
    ["cancelled", "Cancelada"],
    ["expired", "Venció"],
  ] as const)("shows a %s approval as closed", (status, label) => {
    render(<ApprovalCard approval={{ ...pending(), status }} canAnswer />)
    expect(screen.getByText(label)).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})
