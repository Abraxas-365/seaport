// Wire types of the Seaport admin API (mirror internal/*/ Go JSON tags).

import type { Permission } from "./permissions.gen"

export type ID = string

export type Channel = "whatsapp" | "api" | "github" | "jira" | "slack"
export type DockStatus = "linking" | "linked" | "logged_out" | "failed"

export interface Dock {
  id: ID
  channel: Channel
  name: string
  address: string
  status: DockStatus
  created_at: string
  updated_at: string
  qr_code?: string
}

/** A GitHub dock's App and installations (GET /docks/{id}/github; no secret). */
export interface GitHubApp {
  app_id: number
  slug: string
  name: string
  owner: string
  owner_type: string
  html_url: string
  created_at: string
}

export interface GitHubInstallation {
  id: number
  account: string
  account_type: string
  suspended: boolean
  created_at: string
}

export interface GitHubInfo {
  app: GitHubApp | null
  installations: GitHubInstallation[]
  webhook_url: string
  /** false: GitHub can't reach this port (plain http or loopback). */
  reachable: boolean
  install_url?: string
}

/** A Jira dock's site and service account (GET /docks/{id}/jira; never the token). */
export interface JiraSite {
  site: string
  account_id: string
  display_name: string
  email: string
  created_at: string
}

export interface JiraInfo {
  site: JiraSite | null
  webhook_url: string
  /** false: Jira can't reach this port (plain http or loopback). */
  reachable: boolean
  /** The webhook secret: only in the answers that made it (link, rotate). */
  secret?: string
  /** Sites a dock may name (SEAPORT_JIRA_HOSTS). */
  hosts: string[]
}

/** A Slack dock's App (GET /docks/{id}/slack; no secret). */
export interface SlackApp {
  app_id: string
  name: string
  team_id: string
  team_name: string
  team_url: string
  bot_user_id: string
  /** false: created, not installed yet (or the token was revoked). */
  installed: boolean
  created_at: string
}

export interface SlackInfo {
  app: SlackApp | null
  /** The app manifest (JSON) to create the app by hand. */
  manifest: unknown
  events_url: string
  redirect_url: string
  /** false: Slack can't reach this port (plain http or loopback). */
  reachable: boolean
  /** Install (again) an app Seaport created. */
  install_url?: string
}

/** The form the web posts to GitHub to register the App. */
export interface GitHubManifest {
  action_url: string
  manifest: string
}

export type ConversationKind = "direct" | "group" | "self"
export type Access = "auto" | "allow" | "deny"
export type GroupMode = "mentions" | "all" | ""
export type CustomsStatus = "allowed" | "pending" | "denied"

export interface Conversation {
  id: ID
  dock_id: ID
  channel: Channel
  external_id: string
  kind: ConversationKind
  title: string
  access: Access
  group_mode?: GroupMode
  status: CustomsStatus
  handles: string[]
  last_activity_at: string
  created_at: string
  updated_at: string
}

export type Direction = "in" | "out"
export type ShipStatus =
  | "held"
  | "queued"
  | "claimed"
  | "handled"
  | "pending"
  | "sent"
  | "failed"

export interface Ship {
  id: ID
  conversation_id: ID
  direction: Direction
  external_id: string
  sender_id: string
  sender_name: string
  text: string
  status: ShipStatus
  /** Inbound: the agent it is (or was) for; outbound: the agent that sent it (none: a person). */
  agent_id?: ID
  /** Why a held ship was held (customs or routing). */
  hold_reason?: string
  delivery_id?: ID
  error?: string
  sent_at: string
  created_at: string
  /** Files that go with it (inbound images, files agents send), in order. */
  attachments?: Attachment[]
}

/** A file of a ship; its bytes are at attachmentURL(id). Documents are
 * downloads only (the server sends them as attachments). */
export interface Attachment {
  id: ID
  kind: "image" | "document"
  mime: string
  size: number
  name?: string
  sha256: string
}

export interface InboxItem extends Conversation {
  last_ship: Ship | null
  held: number
  queued: number
  in_flight: boolean
}

export type DefaultAccess = "ask" | "allow" | "deny"
export type Effect = "allow" | "deny"

export interface Policy {
  dock_id: ID
  default: DefaultAccess
  group_mode: Exclude<GroupMode, "">
}

export interface Rule {
  id: ID
  dock_id: ID
  effect: Effect
  pattern: string
  created_at: string
}

export interface Customs {
  policy: Policy
  rules: Rule[]
}

export interface AgentDelivery {
  id: ID
  conversation_id: ID
  ships: number
  since: string
}

/** One live connection (`seaport connect`) working as a library agent. */
export interface Agent {
  session: string
  agent_id: ID
  agent_name: string
  /** The connector's label. */
  name: string
  concurrency: number
  steer: boolean
  /** The agent profile version its harness runs (0: none). */
  profile_version: number
  /** The agent's current profile is another one: it restarts once idle, or
   * its connector predates profiles. */
  outdated: boolean
  remote: string
  connected_at: string
  deliveries: AgentDelivery[]
}

export interface CrewStatus {
  agents: Agent[]
}

/** An agent on a dock. */
export interface Assignment {
  dock_id: ID
  /** It takes the dock's messages that call nobody. */
  default: boolean
}

/** An agent of the organization's library (GET /api/v1/agents). */
export interface LibraryAgent {
  id: ID
  /** Unique in the organization; how people call it (@name). */
  name: string
  description: string
  created_by: string
  created_at: string
  updated_at: string
  /** Where it works; none: inactive. */
  docks: Assignment[]
  /** Its current profile version (0: none saved yet). */
  profile_version: number
}

/** A SKILL.md the agent can load (through the port's MCP `skill` tool). */
export interface Skill {
  name: string
  /** When to use it: what the harness sees before loading it. */
  description: string
  body: string
}

export interface McpHeader {
  name: string
  /** May name the agent's secrets as ${NAME}. */
  value: string
}

/** A streamable-HTTP MCP server added to the agent's sessions. */
export interface McpServer {
  name: string
  url: string
  headers: McpHeader[]
}

export type Approve = "" | "allow" | "reject"

/** One version of an agent's profile (GET /api/v1/agents/{id}/profile). */
export interface AgentProfile {
  agent_id: ID
  /** 0: none saved yet (the rest is empty). */
  version: number
  instructions: string
  skills: Skill[]
  mcp_servers: McpServer[]
  approve: Approve
  model: string
  effort: string
  created_by: string
  created_at: string
}

export type ProfileInput = Pick<AgentProfile, "instructions" | "skills" | "mcp_servers" | "approve" | "model" | "effort">

export interface ProfileVersion {
  version: number
  created_by: string
  created_at: string
}

/** What the web sees of a secret: never its value. */
export interface AgentSecret {
  name: string
  /** Also given to the harness as an environment variable. */
  env: boolean
  updated_at: string
}

/** One agent of a dock's team (GET /api/v1/docks/{id}/agents). */
export interface TeamMember {
  agent_id: ID
  name: string
  default: boolean
}

export interface Page {
  total: number
  limit: number
  offset: number
}

export interface Paginated<T> {
  items: T[]
  page: Page
}

/** GET /api/v1/config: how this port authenticates (SEAPORT_AUTH). */
export interface PortConfig {
  auth: "none" | "token" | "iam"
  version: string
}

/** GET /api/v1/session: who this browser is, in every mode. */
export interface Session {
  authenticated: boolean
  expires_at?: string
  subject?: string
  name?: string
  email?: string
  organization_id?: string
  organization_name?: string
  /** What the user may do (the whole catalog in none/token mode). */
  permissions?: Permission[]
}

/** A crew token (an agent credential); the value is shown only at creation. */
export interface CrewToken {
  id: ID
  /** The agent it connects as. */
  agent_id: ID
  agent_name: string
  name: string
  /** Start of the value ("sp_crew_AbCd"), to tell tokens apart. */
  hint: string
  created_by: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
}

export interface IssuedCrewToken extends CrewToken {
  value: string
}

export interface Health {
  status: string
  version: string
}

export type DeliveryState = "claimed" | "joined" | "acked" | "released" | "expired"

export interface DeliveryEvent {
  state: DeliveryState
  delivery_id?: ID
  conversation_id?: ID
  ship_ids?: ID[]
  count?: number
  owner?: string
}

/** ACP's tool call statuses ("": unchanged in an update). */
export type ToolStatus = "pending" | "in_progress" | "completed" | "failed"

/** One step of what an agent does: a chunk of its thinking or text, or a
 * tool call's latest state (title and status only, never its input or
 * output). */
export interface ActivityItem {
  kind: "thought" | "message" | "tool"
  text?: string
  tool?: string
  title?: string
  tool_kind?: string
  status?: ToolStatus | ""
}

/** crew.activity: live only (never stored, never replayed). */
export interface ActivityEvent {
  delivery_id: ID
  conversation_id: ID
  agent_id: ID
  agent_name: string
  at: string
  items: ActivityItem[]
}

/** How a delivery ended: finished, failed (ships requeued), or the
 * connection closed mid-turn. */
export type TurnOutcome = "acked" | "released" | "dropped"

/** One tool call of a stored turn (title redacted, never input/output). */
export interface TurnTool {
  title: string
  kind?: string
  status: ToolStatus
  started_at: string
  ended_at?: string
}

/** What an agent did on one delivery (ship.turn; GET …/turns). */
export interface Turn {
  delivery_id: ID
  conversation_id: ID
  agent_id: ID
  agent_name: string
  outcome: TurnOutcome
  started_at: string
  ended_at: string
  tools: TurnTool[]
  /** Tools past the server's cap, not kept. */
  omitted?: number
}

/** Server-Sent Event types of GET /api/v1/events. */
export interface EventMap {
  "dock.updated": Dock
  "dock.deleted": { id: ID }
  "conversation.updated": Conversation
  "customs.updated": { customs: Customs }
  "ship.created": Ship
  "ship.updated": Ship
  "delivery.changed": DeliveryEvent
  "ship.turn": Turn
  "crew.changed": CrewStatus
  "crew.activity": ActivityEvent
  "agent.updated": LibraryAgent
  "agent.deleted": { id: ID }
  "environment.updated": Environment
  "environment.deleted": { id: ID }
  "environment.build": Build
  "slot.updated": Hosted
}
export type EventType = keyof EventMap

// Hosting: agents the port runs itself (environments, slots, builds).

export type Harness = "rness" | "claude-code"
export type ResourceSize = "S" | "M" | "L"

export interface EnvRepo {
  /** https clone URL. */
  url: string
  /** Branch, tag or commit ("": the default branch). */
  ref: string
  /** Directory under /workspace ("": the repo's name). */
  path: string
  /** ${SECRET}: one of the hosted agent's secrets (a token). */
  credential: string
  /** Shell run in the clone on each start (deps). */
  on_start: string
}

/** An environment's spec: where hosted agents run (complete once stored). */
export interface EnvSpec {
  description: string
  harness: Harness
  /** Base image ("": the harness's Seaport base). */
  image: string
  /** Debian packages (name or name=version). */
  packages: string[]
  /** bash run as root while building the image. */
  setup: string
  repos: EnvRepo[]
  /** Whether the harness reads the repos' own config (AGENTS.md, .claude/…). */
  repo_config: "trust" | "ignore"
  /** Harness config baked into the image (sha256:…, `seaport env home push`); "" none. */
  home: string
  models: { anthropic: string }
  network: { egress: string[] }
  resources: { size?: ResourceSize; cpus: number; memory_mb: number }
  workspace: "persistent" | "ephemeral"
  /** Go duration ("30m", "2h"); "0" never stops. */
  idle_stop: string
}

export interface Environment {
  id: ID
  name: string
  version: number
  spec: EnvSpec
  created_by: string
  created_at: string
  updated_at: string
  /** Agents hosted on it. */
  agents: number
}

export interface HomeFile {
  /** Relative to the harness's root (~/.claude, ~/.rness). */
  path: string
  size: number
  sha256: string
  exec?: boolean
}

/** An uploaded harness config, named by digest. */
export interface Home {
  digest: string
  harness: Harness
  files: HomeFile[]
  size: number
  created_by: string
  created_at: string
}

/** What a harness's home may hold (GET /homes/rules). */
export interface HomeRules {
  root: string
  dirs: string[]
  files: string[]
  file_prefixes: string[]
  reserved: string[]
  secrets: { what: string; pattern: string }[]
  max_files: number
  max_file: number
  max_bytes: number
  max_upload: number
  max_depth: number
}

export interface EnvVersion {
  version: number
  created_by: string
  created_at: string
}

export type BuildState = "building" | "ready" | "failed"

/** An environment version's image build. */
export interface Build {
  environment_id: ID
  version: number
  state: BuildState
  base: string
  image: string
  detail: string
  /** Its output's tail (not in events). */
  log?: string
  started_at: string
  finished_at: string | null
  updated_at: string
}

export type SlotState = "stopped" | "starting" | "running" | "stopping" | "failed"

/** One stable instance of a hosted agent (<agent>-<index>). */
export interface Slot {
  index: number
  state: SlotState
  detail: string
  image: string
  /** 0: never started. */
  environment_version: number
  started_at: string | null
  active_at: string | null
  updated_at: string
}

/** A hosted agent: its environment and slots (none: unhosted). */
export interface Hosted {
  agent_id: ID
  environment_id: ID
  slots: Slot[]
  updated_by: string
  updated_at: string
}

export interface HostingStatus {
  agents: Hosted[]
  /** This port runs hosted agents (SEAPORT_RUNTIME isn't off). */
  runtime: boolean
}

export interface SlotLog {
  slot: number
  log: string
}

export type LoginState = "starting" | "waiting" | "verifying" | "done" | "failed"

/** A slot's latest subscription sign-in (experimental). */
export interface SlotLogin {
  id: string
  slot: number
  state: LoginState
  /** Where the person signs in (once waiting). */
  url: string
  detail: string
  created_by: string
  created_at: string
  updated_at: string
}

/** How a running slot's harness is signed in (asked of the harness). */
export interface SlotAccount {
  slot: number
  /** The harness can sign in from Seaport (Claude Code). */
  supported: boolean
  logged_in: boolean
  /** claude.ai (subscription), oauth_token, api_key, none. */
  method: string
  subscription?: string
}

export interface Denial {
  slot: number
  host: string
  count: number
  last_seen: string
}

export interface UsageDay {
  day: string
  provider: string
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  cache_write_tokens: number
  cache_read_tokens: number
}
