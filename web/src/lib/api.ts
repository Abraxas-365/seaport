// HTTP client of the admin API. The browser authenticates with the
// HttpOnly session cookie (POST /api/v1/session); no token lives in JS.

import type {
  Access,
  Channel,
  Conversation,
  GitHubInfo,
  GitHubManifest,
  JiraInfo,
  SlackInfo,
  CrewStatus,
  CrewToken,
  Customs,
  CustomsStatus,
  DefaultAccess,
  Dock,
  Effect,
  GroupMode,
  Health,
  InboxItem,
  IssuedCrewToken,
  LibraryAgent,
  AgentProfile,
  AgentSecret,
  Build,
  Denial,
  EnvSpec,
  EnvVersion,
  Environment,
  Harness,
  Home,
  HomeRules,
  Hosted,
  HostingStatus,
  SlotAccount,
  SlotLog,
  SlotLogin,
  UsageDay,
  ProfileInput,
  ProfileVersion,
  Paginated,
  Rule,
  PortConfig,
  Session,
  Ship,
  Turn,
  TeamMember,
} from "./types"

export class ApiError extends Error {
  status: number
  code?: string
  requestId?: string
  /** Seconds to wait before retrying (429 Retry-After). */
  retryAfter?: number

  constructor(status: number, message: string, code?: string, requestId?: string, retryAfter?: number) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
    this.requestId = requestId
    this.retryAfter = retryAfter
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
    const retryAfter = Number(res.headers.get("Retry-After")) || undefined
    throw new ApiError(res.status, e?.message ?? res.statusText, e?.code, e?.request_id, retryAfter)
  }
  return data as T
}

/** Uploads a raw body (an archive); answers JSON like request. */
async function upload<T>(path: string, contentType: string, body: Uint8Array): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": contentType },
    body: body as BodyInit,
  })
  const text = await res.text()
  let data: { error?: { message?: string; code?: string; request_id?: string } } | undefined
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = undefined
  }
  if (!res.ok) {
    const e = data?.error
    if (res.status === 401) onUnauthorized()
    const retryAfter = Number(res.headers.get("Retry-After")) || undefined
    throw new ApiError(res.status, e?.message ?? res.statusText, e?.code, e?.request_id, retryAfter)
  }
  return data as T
}

/** Reads a binary body (a home's tar). */
async function requestBytes(path: string): Promise<Uint8Array> {
  const res = await requestRaw(path)
  return new Uint8Array(await res.arrayBuffer())
}

async function requestRaw(path: string): Promise<Response> {
  const res = await fetch(path, { credentials: "same-origin" })
  if (res.ok) return res
  const text = await res.text()
  let e: { message?: string; code?: string; request_id?: string } | undefined
  try {
    e = JSON.parse(text)?.error
  } catch {
    e = undefined
  }
  if (res.status === 401) onUnauthorized()
  throw new ApiError(res.status, e?.message ?? res.statusText, e?.code, e?.request_id)
}

function qs(params: Record<string, string | number | undefined>) {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") s.set(k, String(v))
  const out = s.toString()
  return out ? `?${out}` : ""
}

const v1 = "/api/v1"

/** Where an attachment's bytes are (the session cookie authenticates <img>). */
export function attachmentURL(id: string) {
  return `${v1}/attachments/${encodeURIComponent(id)}`
}

export const api = {
  health: () => request<Health>("GET", "/healthz"),

  config: () => request<PortConfig>("GET", `${v1}/config`),
  session: () => request<Session>("GET", `${v1}/session`),
  login: (token: string) => request<Session>("POST", `${v1}/session`, { token }),
  /** iam mode answers with IAMKit's end-session URL to finish there too. */
  logout: () => request<{ logout_url?: string } | undefined>("DELETE", `${v1}/session`),
  /** iam mode: a top-level navigation to IAMKit's sign-in (not fetch). */
  signInURL: (f: { next?: string; organization?: string; switchAccount?: boolean } = {}) =>
    `${v1}/session/login${qs({ next: f.next, organization: f.organization, switch: f.switchAccount ? 1 : undefined })}`,

  docks: () => request<Paginated<Dock>>("GET", `${v1}/docks${qs({ limit: 100 })}`),
  dock: (id: string) => request<Dock>("GET", `${v1}/docks/${id}`),
  createDock: (channel: Channel, name: string) =>
    request<Dock>("POST", `${v1}/docks`, { channel, name }),
  renameDock: (id: string, name: string) => request<Dock>("PATCH", `${v1}/docks/${id}`, { name }),
  relinkDock: (id: string) => request<Dock>("POST", `${v1}/docks/${id}/relink`),
  unlinkDock: (id: string) => request<Dock>("POST", `${v1}/docks/${id}/unlink`),
  deleteDock: (id: string) => request<void>("DELETE", `${v1}/docks/${id}`),
  github: (id: string) => request<GitHubInfo>("GET", `${v1}/docks/${id}/github`),
  githubManifest: (id: string, body: { organization?: string; name?: string }) =>
    request<GitHubManifest>("POST", `${v1}/docks/${id}/github/manifest`, body),
  jira: (id: string) => request<JiraInfo>("GET", `${v1}/docks/${id}/jira`),
  linkJira: (id: string, body: { site: string; email: string; api_token: string }) =>
    request<JiraInfo>("PUT", `${v1}/docks/${id}/jira`, body),
  rotateJiraSecret: (id: string) => request<JiraInfo>("POST", `${v1}/docks/${id}/jira/secret`),
  slack: (id: string) => request<SlackInfo>("GET", `${v1}/docks/${id}/slack`),
  slackCreate: (id: string, body: { config_token: string; name?: string }) =>
    request<{ install_url: string }>("POST", `${v1}/docks/${id}/slack/create`, body),
  slackInstall: (id: string) =>
    request<{ install_url: string }>("POST", `${v1}/docks/${id}/slack/install`),
  slackConnect: (id: string, body: { signing_secret: string; bot_token: string }) =>
    request<SlackInfo>("POST", `${v1}/docks/${id}/slack/connect`, body),

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
  turns: (conversation: string, limit = 100) =>
    request<Paginated<Turn>>("GET", `${v1}/conversations/${conversation}/turns${qs({ limit })}`),
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

  agents: () => request<Paginated<LibraryAgent>>("GET", `${v1}/agents${qs({ limit: 100 })}`),
  createAgent: (name: string, description: string) =>
    request<LibraryAgent>("POST", `${v1}/agents`, { name, description }),
  updateAgent: (id: string, patch: { name?: string; description?: string }) =>
    request<LibraryAgent>("PATCH", `${v1}/agents/${id}`, patch),
  deleteAgent: (id: string) => request<void>("DELETE", `${v1}/agents/${id}`),
  agent: (id: string) => request<LibraryAgent>("GET", `${v1}/agents/${id}`),
  profile: (id: string, version?: number) =>
    request<AgentProfile>("GET", `${v1}/agents/${id}/profile${version ? `/versions/${version}` : ""}`),
  saveProfile: (id: string, base: number, profile: ProfileInput) =>
    request<AgentProfile>("PUT", `${v1}/agents/${id}/profile`, { base, ...profile }),
  profileVersions: (id: string) => request<{ versions: ProfileVersion[] }>("GET", `${v1}/agents/${id}/profile/versions`),
  secrets: (id: string) => request<{ secrets: AgentSecret[] }>("GET", `${v1}/agents/${id}/secrets`),
  setSecret: (id: string, name: string, value: string, env: boolean) =>
    request<AgentSecret>("PUT", `${v1}/agents/${id}/secrets/${encodeURIComponent(name)}`, { value, env }),
  deleteSecret: (id: string, name: string) =>
    request<void>("DELETE", `${v1}/agents/${id}/secrets/${encodeURIComponent(name)}`),
  team: (dock: string) => request<{ agents: TeamMember[] }>("GET", `${v1}/docks/${dock}/agents`),
  assignAgent: (dock: string, agent: string, isDefault: boolean) =>
    request<LibraryAgent>("PUT", `${v1}/docks/${dock}/agents/${agent}`, { default: isDefault }),
  unassignAgent: (dock: string, agent: string) =>
    request<LibraryAgent>("DELETE", `${v1}/docks/${dock}/agents/${agent}`),

  crewTokens: () => request<Paginated<CrewToken>>("GET", `${v1}/crew/tokens${qs({ limit: 100 })}`),
  createCrewToken: (agent_id: string, name: string, expires_at?: string) =>
    request<IssuedCrewToken>("POST", `${v1}/crew/tokens`, { agent_id, name, expires_at }),
  revokeCrewToken: (id: string) => request<void>("DELETE", `${v1}/crew/tokens/${id}`),

  environments: () => request<Paginated<Environment>>("GET", `${v1}/environments${qs({ limit: 100 })}`),
  environment: (id: string, version?: number) =>
    request<Environment>("GET", `${v1}/environments/${id}${version ? `/versions/${version}` : ""}`),
  createEnvironment: (name: string, spec: Partial<EnvSpec>) =>
    request<Environment>("POST", `${v1}/environments`, { name, spec }),
  saveEnvironment: (id: string, base: number, spec: EnvSpec) =>
    request<Environment>("PUT", `${v1}/environments/${id}`, { base, spec }),
  deleteEnvironment: (id: string) => request<void>("DELETE", `${v1}/environments/${id}`),
  environmentVersions: (id: string) => request<{ versions: EnvVersion[] }>("GET", `${v1}/environments/${id}/versions`),
  build: (id: string, version?: number) =>
    request<Build>("GET", `${v1}/environments/${id}${version ? `/versions/${version}` : ""}/build`),
  startBuild: (id: string, force = false) => request<Build>("POST", `${v1}/environments/${id}/build`, { force }),

  homeRules: () => request<Record<Harness, HomeRules>>("GET", `${v1}/homes/rules`),
  home: (digest: string) => request<Home>("GET", `${v1}/homes/${digest}`),
  homeFile: (digest: string, path: string) =>
    requestRaw(`${v1}/homes/${digest}/files/${path.split("/").map(encodeURIComponent).join("/")}`).then((r) =>
      r.headers.get("Content-Type")?.startsWith("text/") ? r.text() : null,
    ),
  /** Where a home's tar is (the session cookie authenticates a download link). */
  homeArchiveURL: (digest: string) => `${v1}/homes/${digest}/archive`,
  homeArchive: (digest: string) => requestBytes(`${v1}/homes/${digest}/archive`),
  /** A tar.gz of the home's files (the web picks them with homeRules first). */
  uploadHome: (harness: Harness, tgz: Uint8Array) =>
    upload<Home>(`${v1}/homes${qs({ harness })}`, "application/gzip", tgz),

  hosting: () => request<HostingStatus>("GET", `${v1}/hosting`),
  hosted: (agent: string) => request<Hosted>("GET", `${v1}/agents/${agent}/hosting`),
  host: (agent: string, environment: string, slots: number) =>
    request<Hosted>("PUT", `${v1}/agents/${agent}/hosting`, { environment, slots }),
  unhost: (agent: string) => request<void>("DELETE", `${v1}/agents/${agent}/hosting`),
  slotAction: (agent: string, slot: number, action: "start" | "stop") =>
    request<Hosted>("POST", `${v1}/agents/${agent}/hosting/slots/${slot}/${action}`),
  slotLog: (agent: string, slot: number, lines = 300) =>
    request<SlotLog>("GET", `${v1}/agents/${agent}/hosting/slots/${slot}/logs${qs({ lines })}`),
  startLogin: (agent: string, slot: number) =>
    request<SlotLogin>("POST", `${v1}/agents/${agent}/hosting/slots/${slot}/login`),
  slotLogin: (agent: string, slot: number) => request<SlotLogin>("GET", `${v1}/agents/${agent}/hosting/slots/${slot}/login`),
  loginCode: (agent: string, slot: number, code: string) =>
    request<SlotLogin>("POST", `${v1}/agents/${agent}/hosting/slots/${slot}/login/code`, { code }),
  slotAccount: (agent: string, slot: number) =>
    request<SlotAccount>("GET", `${v1}/agents/${agent}/hosting/slots/${slot}/account`),
  egress: (agent: string) => request<{ denials: Denial[] }>("GET", `${v1}/agents/${agent}/hosting/egress`),
  usage: (agent: string, days = 30) => request<{ days: UsageDay[] }>("GET", `${v1}/agents/${agent}/usage${qs({ days })}`),
}
