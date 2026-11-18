import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { ThemeProvider } from "@/components/theme-provider"
import type { LibraryAgent, Takeover } from "@/lib/types"
import { renderAt, seededClient, stubNetwork } from "@/test/render"
import { apiData } from "@/test/wire"
import { KeySink, RefusedSignIn, Takeovers } from "./browser-viewer"

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

  it("says Seaport detected the page, with its path, for the detector's", () => {
    stubNetwork()
    renderAt(
      <Takeovers
        agent={agentWith([
          takeover({ host: "sso.example.com", kind: "login", by: "detector", path: "/signin", note: "the connector's page detector saw a page" }),
        ])}
        canClear={false}
      />,
      seededClient(),
    )
    const line = screen.getByText(/Seaport detectó que el navegador necesita que inicies sesión en/)
    expect(line).toHaveTextContent("sso.example.com/signin")
    expect(line).not.toHaveTextContent("page detector")
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

describe("RefusedSignIn", () => {
  it("says the refusal can't be passed here and what to use instead", () => {
    stubNetwork()
    renderAt(<RefusedSignIn />, seededClient())
    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("Google no deja iniciar sesión en este navegador")
    expect(alert).toHaveTextContent("reintentar no sirve")
    expect(alert).toHaveTextContent("API del servicio")
  })
})

describe("KeySink", () => {
  const setup = () => {
    const onKey = vi.fn()
    const onText = vi.fn()
    const outside = vi.fn()
    window.addEventListener("keydown", outside)
    render(
      <ThemeProvider>
        <KeySink inputRef={null} onKey={onKey} onText={onText} />
      </ThemeProvider>,
    )
    const field = screen.getByLabelText("Teclado del navegador")
    field.focus()
    return { field, onKey, onText, outside, done: () => window.removeEventListener("keydown", outside) }
  }

  it("sends every key to the page and none to the web's shortcuts", async () => {
    const { onKey, outside, done } = setup()
    const theme = document.documentElement.className
    await userEvent.keyboard("dD")
    expect(onKey.mock.calls.filter(([type]) => type === "keyDown").map(([, e]) => e.key)).toEqual(["d", "D"])
    expect(outside).not.toHaveBeenCalled()
    expect(document.documentElement.className).toBe(theme) // "d" didn't toggle the theme
    done()
  })

  it("leaves dead keys and compositions to the system and sends the composed text", () => {
    const { field, onKey, onText, done } = setup()
    fireEvent.keyDown(field, { key: "Dead", code: "Quote" })
    expect(onKey).not.toHaveBeenCalled()
    fireEvent.compositionStart(field)
    ;(field as HTMLTextAreaElement).value = "é"
    fireEvent.input(field, { isComposing: true })
    expect(onText).not.toHaveBeenCalled()
    fireEvent.compositionEnd(field)
    expect(onText).toHaveBeenCalledWith("é")
    expect((field as HTMLTextAreaElement).value).toBe("")
    done()
  })

  it("pastes the person's clipboard as text", () => {
    const { field, onKey, onText, done } = setup()
    fireEvent.keyDown(field, { key: "v", metaKey: true })
    expect(onKey).not.toHaveBeenCalled()
    fireEvent.paste(field, { clipboardData: { getData: () => "secreto" } })
    expect(onText).toHaveBeenCalledWith("secreto")
    done()
  })
})
