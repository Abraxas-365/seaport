import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import type { UsageDay } from "@/lib/types"
import { UsageChart } from "./hosting"

const day = (over: Partial<UsageDay>): UsageDay => ({
  day: new Date().toISOString().slice(0, 10) + "T00:00:00Z",
  provider: "anthropic",
  model: "claude-example",
  requests: 2,
  input_tokens: 100,
  output_tokens: 20,
  cache_write_tokens: 0,
  cache_read_tokens: 0,
  web_search_requests: 0,
  web_fetch_requests: 0,
  ...over,
})

describe("UsageChart", () => {
  it("adds up the provider's per-call tools when there were any", () => {
    render(<UsageChart days={[day({ web_search_requests: 2 }), day({ model: "claude-other", web_search_requests: 1, web_fetch_requests: 1 })]} />)
    expect(screen.getByText(/3 búsquedas web · 1 lectura web/)).toBeInTheDocument()
    expect(screen.getByText("4")).toBeInTheDocument() // Llamadas
  })

  it("says nothing about tools when none were used", () => {
    render(<UsageChart days={[day({})]} />)
    expect(screen.queryByText(/Herramientas del proveedor/)).not.toBeInTheDocument()
  })
})
