import { describe, expect, it } from "vitest"

import { pickPage } from "./cdp"

describe("pickPage", () => {
  it("opens on the page the agent works, past the browser's blank first tab", () => {
    const pages = [
      { targetId: "a", title: "", url: "about:blank" },
      { targetId: "b", title: "GitHub", url: "https://github.com/login" },
    ]
    expect(pickPage(pages)?.targetId).toBe("b")
  })
  it("falls back to the first page, and to nothing", () => {
    expect(pickPage([{ targetId: "a", title: "", url: "about:blank" }])?.targetId).toBe("a")
    expect(pickPage([])).toBeUndefined()
  })
})
