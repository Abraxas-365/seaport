import { QueryClient } from "@tanstack/react-query"

import { ApiError } from "./api"

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Live events keep data fresh; refetch on focus covers sleeps.
      staleTime: 30_000,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
})

export const keys = {
  config: ["config"] as const,
  session: ["session"] as const,
  health: ["health"] as const,
  docks: ["docks"] as const,
  dock: (id: string) => ["docks", id] as const,
  inboxAll: ["inbox"] as const,
  inbox: (f: object) => ["inbox", f] as const,
  conversation: (id: string) => ["conversation", id] as const,
  ships: (id: string) => ["ships", id] as const,
  customs: (dock: string) => ["customs", dock] as const,
  crew: ["crew"] as const,
}
