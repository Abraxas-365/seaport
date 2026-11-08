// HTTP client of the admin API. The browser authenticates with the
// HttpOnly session cookie (POST /api/v1/session); no token lives in JS.

import type {
  Access,
  Channel,
  Conversation,
  CrewStatus,
  Customs,
  CustomsStatus,
  DefaultAccess,
  Dock,
  Effect,
  GroupMode,
  Health,
  InboxItem,
  Paginated,
  Rule,
  Session,
  Ship,
} from "./types"

export class ApiError extends Error {
  status: number
  code?: string
  requestId?: string

  constructor(status: number, message: string, code?: string, requestId?: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

/** Called on any 401 so the app can drop back to the login screen. */
let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok) {
    const e = data?.error
    if (res.status === 401 && !path.endsWith("/session")) onUnauthorized()
    throw new ApiError(res.status, e?.message ?? res.statusText, e?.code, e?.request_id)
  }
  return data as T
}

function qs(params: Record<string, string | number | undefined>) {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") s.set(k, String(v))
  const out = s.toString()
  return out ? `?${out}` : ""
}

const v1 = "/api/v1"

export const api = {
  health: () => request<Health>("GET", "/healthz"),

  session: () => request<Session>("GET", `${v1}/session`),
  login: (token: string) => request<Session>("POST", `${v1}/session`, { token }),
  logout: () => request<void>("DELETE", `${v1}/session`),

  docks: () => request<Paginated<Dock>>("GET", `${v1}/docks${qs({ limit: 100 })}`),
  dock: (id: string) => request<Dock>("GET", `${v1}/docks/${id}`),
  createDock: (channel: Channel, name: string) =>
    request<Dock>("POST", `${v1}/docks`, { channel, name }),
  renameDock: (id: string, name: string) => request<Dock>("PATCH", `${v1}/docks/${id}`, { name }),
  relinkDock: (id: string) => request<Dock>("POST", `${v1}/docks/${id}/relink`),
  unlinkDock: (id: string) => request<Dock>("POST", `${v1}/docks/${id}/unlink`),
  deleteDock: (id: string) => request<void>("DELETE", `${v1}/docks/${id}`),

  inbox: (f: { status?: CustomsStatus; dock_id?: string; limit?: number; offset?: number }) =>
    request<Paginated<InboxItem>>("GET", `${v1}/inbox${qs(f)}`),
  conversation: (id: string) => request<Conversation>("GET", `${v1}/conversations/${id}`),
  updateConversation: (
    id: string,
    patch: { access?: Access; group_mode?: GroupMode; title?: string },
  ) => request<Conversation>("PATCH", `${v1}/conversations/${id}`, patch),

  ships: (conversation: string, limit = 50, offset = 0) =>
    request<Paginated<Ship>>(
      "GET",
      `${v1}/conversations/${conversation}/ships${qs({ limit, offset })}`,
    ),
  send: (conversation: string, text: string) =>
    request<Ship>("POST", `${v1}/conversations/${conversation}/ships`, { text }),
  inject: (
    dock: string,
    msg: { chat_id: string; message_id: string; chat_title?: string; sender_id?: string; sender_name?: string; text: string },
  ) => request<Ship>("POST", `${v1}/docks/${dock}/ships`, msg),

  customs: (dock: string) => request<Customs>("GET", `${v1}/docks/${dock}/customs`),
  setPolicy: (dock: string, p: { default?: DefaultAccess; group_mode?: GroupMode }) =>
    request<Customs>("PATCH", `${v1}/docks/${dock}/customs/policy`, p),
  addRule: (dock: string, effect: Effect, pattern: string) =>
    request<Rule>("POST", `${v1}/docks/${dock}/customs/rules`, { effect, pattern }),
  removeRule: (id: string) => request<void>("DELETE", `${v1}/customs/rules/${id}`),

  crew: () => request<CrewStatus>("GET", `${v1}/crew`),
}
