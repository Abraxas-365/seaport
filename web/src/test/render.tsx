import type { ReactElement } from "react"
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query"
import { render } from "@testing-library/react"
import { MemoryRouter, Route, Routes } from "react-router"
import { vi } from "vitest"

import { TooltipProvider } from "@/components/ui/tooltip"
import { keys } from "@/lib/query"
import { permissions } from "@/lib/permissions.gen"
import type { Permission } from "@/lib/permissions.gen"
import { newClient } from "./query"

/** Queries nobody seeded never answer (no network in tests): they stay loading. */
export function stubNetwork() {
  vi.stubGlobal("fetch", () => new Promise<Response>(() => {}))
}

/** A client whose session grants `held` (everything by default), with seeded data kept fresh. */
export function seededClient(held: readonly Permission[] = permissions): QueryClient {
  const qc = newClient()
  qc.setDefaultOptions({ queries: { retry: false, gcTime: Infinity, staleTime: Infinity } })
  qc.setQueryData(keys.session, { authenticated: true, permissions: [...held] })
  return qc
}

/** Renders `ui` at `path` of a route `pattern` ("/docks/:dockId"), with query client, tooltips and router. */
export function renderAt(ui: ReactElement, qc: QueryClient, { pattern = "/", path = "/" } = {}) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path={pattern} element={ui} />
          </Routes>
        </MemoryRouter>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}
