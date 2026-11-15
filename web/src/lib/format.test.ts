import { describe, expect, it } from "vitest"

import {
  conversationHandle,
  conversationName,
  formatBytes,
  formatRelative,
  hueOf,
  initials,
  plural,
  prettyHandle,
  shipPreview,
} from "./format"
import type { Ship } from "./types"

describe("prettyHandle", () => {
  it.each([
    ["+34600000000", "+34600000000"],
    ["34600000001@s.whatsapp.net", "+34600000001"],
    ["34600000001:12@s.whatsapp.net", "+34600000001"],
    ["120363000000000000@g.us", "grupo 120363000000000000"],
    ["2314000000@" + "lid", "2314000000 (lid)"],
    ["ana@example.com", "ana"],
    ["C0EXAMPLE", "C0EXAMPLE"],
    ["owner/repo#7", "owner/repo#7"],
  ])("%s → %s", (handle, pretty) => expect(prettyHandle(handle)).toBe(pretty))
})

describe("conversationName", () => {
  it("prefers the title, then the pretty external id", () => {
    expect(conversationName({ title: "Ana Example", external_id: "x", kind: "direct" })).toBe("Ana Example")
    expect(conversationName({ title: "", external_id: "120363000000000000@g.us", kind: "group" })).toBe("grupo 120363000000000000")
  })

  it("names the owner's own chat", () => {
    expect(conversationName({ title: "", external_id: "x", kind: "self" })).toBe("Yo (chat propio)")
    expect(conversationName({ title: "Notes", external_id: "x", kind: "self" })).toBe("Notes")
  })
})

describe("conversationHandle", () => {
  it("shows the sender's address of an email thread, not its Message-ID", () => {
    expect(
      conversationHandle({ channel: "email", external_id: "a1@example.com", handles: ["a1@example.com", "ana@example.com"] }),
    ).toBe("ana@example.com")
  })

  it("falls back to the external id when an email thread has no other address", () => {
    expect(conversationHandle({ channel: "email", external_id: "a1@example.com", handles: ["a1@example.com"] })).toBe("a1")
  })

  it("is the pretty external id on the other channels", () => {
    expect(conversationHandle({ channel: "whatsapp", external_id: "34600000002@s.whatsapp.net", handles: ["x@example.com"] })).toBe(
      "+34600000002",
    )
    expect(conversationHandle({ channel: "slack", external_id: "C0EXAMPLE/1700000000.000100", handles: [] })).toBe(
      "C0EXAMPLE/1700000000.000100",
    )
  })
})

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1023, "1023 B"],
    [1024, "1 KB"],
    [1536, "1,5 KB"],
    [10 * 1024, "10 KB"],
    [5 * 1024 * 1024, "5 MB"],
    [3.5 * 1024 * 1024 * 1024, "3,5 GB"],
    [2048 * 1024 * 1024 * 1024, "2048 GB"],
  ])("%d → %s", (n, text) => expect(formatBytes(n)).toBe(text))
})

describe("plural", () => {
  it("counts with the right form", () => {
    expect(plural(0, "mensaje", "mensajes")).toBe("0 mensajes")
    expect(plural(1, "mensaje", "mensajes")).toBe("1 mensaje")
    expect(plural(2, "mensaje", "mensajes")).toBe("2 mensajes")
  })
})

describe("formatRelative", () => {
  const now = Date.parse("2030-01-02T12:00:00Z")
  const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()

  it("says now for under 45 seconds, either way", () => {
    expect(formatRelative(ago(10), now)).toBe("ahora")
    expect(formatRelative(ago(-30), now)).toBe("ahora")
  })

  it("uses minutes, hours and days, past and future", () => {
    expect(formatRelative(ago(5 * 60), now)).toBe("hace 5 minutos")
    expect(formatRelative(ago(3 * 3600), now)).toBe("hace 3 horas")
    expect(formatRelative(ago(2 * 86_400), now)).toBe("anteayer")
    expect(formatRelative(ago(-10 * 60), now)).toBe("dentro de 10 minutos")
    expect(formatRelative(ago(-5 * 86_400), now)).toBe("dentro de 5 días")
  })
})

describe("small helpers", () => {
  it("initials", () => {
    expect(initials("Ana Example")).toBe("AE")
    expect(initials("support")).toBe("SU")
    expect(initials("  ")).toBe("?")
  })

  it("hueOf is stable and within a circle", () => {
    expect(hueOf("abc")).toBe(hueOf("abc"))
    expect(hueOf("abc")).toBeGreaterThanOrEqual(0)
    expect(hueOf("abc")).toBeLessThan(360)
  })

  it("shipPreview marks the agent and the photos", () => {
    const ship = (over: Partial<Ship>) => ({ direction: "in", text: "hi\nthere", ...over }) as Ship
    expect(shipPreview(null)).toBe("")
    expect(shipPreview(ship({}))).toBe("hi there")
    expect(shipPreview(ship({ direction: "out" }))).toBe("Agente: hi there")
    expect(shipPreview(ship({ attachments: [{ id: "a", kind: "image", mime: "image/png", size: 1, sha256: "" }] }))).toBe(
      "📷 Foto · hi there",
    )
  })
})
