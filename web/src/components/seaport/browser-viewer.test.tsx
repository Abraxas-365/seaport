import { screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { LibraryAgent, Takeover } from "@/lib/types"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { Takeovers } from "./browser-viewer"

const takeover = (over: Partial<Takeover>): Takeover => ({ host: "app.example.com", kind: "login", note: "", at: new Date().toISOString(), ...over })

const agentWith = (takeovers: Takeover[]): LibraryAgent => ({ ...apiData<LibraryAgent>("agent"), takeovers })

describe("Takeovers", () => {
  it("renders nothing without takeovers", () => {
    stubNetwork()
    const { container } = renderAt(<Takeovers agent={agentWith([])} canClear />, seededClient())
    expect(container).toBeEmptyDOMElement()
  })

  it("says what the person must do per kind, with the note", () => {
    stubNetwork()
    renderAt(
      <Takeovers
        agent={agentWith([
          takeover({ host: "app.example.com", kind: "login", note: "revisar el tablero" }),
          takeover({ host: "shop.example.com", kind: "captcha" }),
          takeover({ host: "bank.example.com", kind: "2fa" }),
          takeover({ host: "pay.example.com", kind: "other", note: "pagar la factura" }),
        ])}
        canClear={false}
      />,
      seededClient(),
    )
    expect(screen.getByText(/inicies sesión en/)).toHaveTextContent("app.example.com · revisar el tablero")
    expect(screen.getByText(/resuelvas un captcha en/)).toHaveTextContent("shop.example.com")
    expect(screen.getByText(/introduzcas el código 2FA en/)).toHaveTextContent("bank.example.com")
    expect(screen.getByText(/hagas algo en/)).toHaveTextContent("pay.example.com · pagar la factura")
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it("opens the viewer on the host and offers Listo to who may use the browser", async () => {
    stubNetwork()
    const onOpen = vi.fn()
    renderAt(<Takeovers agent={agentWith([takeover({})])} canClear onOpen={onOpen} />, seededClient())
    await userEvent.click(screen.getByRole("button", { name: /Abrir/ }))
    expect(onOpen).toHaveBeenCalledWith("app.example.com")
    expect(screen.getByRole("button", { name: "Listo" })).toBeEnabled()
  })
})
