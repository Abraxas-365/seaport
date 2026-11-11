import { QueryClient } from "@tanstack/react-query"

import { ApiError } from "./api"

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Live events keep data fresh; refetch on focus covers sleeps.
      staleTime: 30_000,
      // Retry server errors twice and rate limits (429) a few times after
      // Retry-After; other 4xx are final.
      retry: (count, err) => {
        if (err instanceof ApiError && err.status === 429) return count < 4
        return !(err instanceof ApiError && err.status < 500) && count < 2
      },
      retryDelay: (count, err) =>
        err instanceof ApiError && err.retryAfter
          ? err.retryAfter * 1000
          : Math.min(1000 * 2 ** count, 30_000),
    },
  },
})

export const keys = {
  config: ["config"] as const,
  session: ["session"] as const,
  health: ["health"] as const,
  docks: ["docks"] as const,
  dock: (id: string) => ["docks", id] as const,
  github: (dock: string) => ["github", dock] as const,
  jira: (dock: string) => ["jira", dock] as const,
  inboxAll: ["inbox"] as const,
  inbox: (f: object) => ["inbox", f] as const,
  conversation: (id: string) => ["conversation", id] as const,
  ships: (id: string) => ["ships", id] as const,
  turns: (id: string) => ["turns", id] as const,
  customs: (dock: string) => ["customs", dock] as const,
  crew: ["crew"] as const,
  crewTokens: ["crew-tokens"] as const,
  agents: ["agents"] as const,
  agent: (id: string) => ["agents", id] as const,
  profileAll: ["profile"] as const,
  profile: (id: string, version = 0) => ["profile", id, version] as const,
  profileVersions: (id: string) => ["profile", id, "versions"] as const,
  secrets: (id: string) => ["secrets", id] as const,
  teamAll: ["team"] as const,
  team: (dock: string) => ["team", dock] as const,
  environments: ["environments"] as const,
  environment: (id: string, version = 0) => ["environments", id, version] as const,
  environmentVersions: (id: string) => ["environments", id, "versions"] as const,
  buildAll: ["build"] as const,
  build: (id: string, version = 0) => ["build", id, version] as const,
  homeRules: ["home-rules"] as const,
  home: (digest: string) => ["home", digest] as const,
  homeFile: (digest: string, path: string) => ["home", digest, "file", path] as const,
  hosting: ["hosting"] as const,
  hosted: (agent: string) => ["hosting", agent] as const,
  slotLog: (agent: string, slot: number) => ["slot-log", agent, slot] as const,
  slotLogin: (agent: string, slot: number) => ["slot-login", agent, slot] as const,
  slotAccount: (agent: string, slot: number) => ["slot-account", agent, slot] as const,
  egress: (agent: string) => ["egress", agent] as const,
  usage: (agent: string, days: number) => ["usage", agent, days] as const,
}
