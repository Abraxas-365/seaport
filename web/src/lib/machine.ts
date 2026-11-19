/**
 * Pure helpers for an agent's config: its harness, its machine (where a
 * hosted agent runs) and the `${NAME}` references to the agent's secrets.
 * The server is the judge (hosting.Normalize, Spec.Validate); these keep the
 * form honest before saving.
 */
import type { Harness, Machine, MachineRepo, ProfileInput } from "@/lib/types"

/** The model providers the port's gateway serves (hosting.Providers), in
 * the order shown; the label is the provider's own name. */
export const providers: { name: string; label: string; keyEnv: string }[] = [
  { name: "anthropic", label: "Anthropic", keyEnv: "ANTHROPIC_API_KEY" },
  { name: "openai", label: "OpenAI", keyEnv: "OPENAI_API_KEY" },
  { name: "openrouter", label: "OpenRouter", keyEnv: "OPENROUTER_API_KEY" },
  { name: "groq", label: "Groq", keyEnv: "GROQ_API_KEY" },
  { name: "deepseek", label: "DeepSeek", keyEnv: "DEEPSEEK_API_KEY" },
]

export type Provider = (typeof providers)[number]

/** The literal `models.anthropic` value that signs in to a Claude account. */
export const subscription = "subscription"

export const harnessLabel: Record<Harness, string> = { rness: "rness", "claude-code": "Claude Code" }

/** The agent name rule (agent.ValidName): also its @mention. */
export const agentNamePattern = /^[a-z0-9][a-z0-9_-]{0,31}$/

/** The suggested secret name for a provider's key (`ANTHROPIC_KEY`). */
export const keyNameFor = (p: Provider) => p.keyEnv.replace(/_API_KEY$/, "_KEY")

export const secretNamePattern = /^[A-Z_][A-Z0-9_]{0,63}$/
export const secretRefPattern = /^\$\{[A-Z_][A-Z0-9_]{0,63}\}$/
const secretRef = /^\$\{([A-Z_][A-Z0-9_]{0,63})\}$/
const secretRefs = /\$\{([A-Z_][A-Z0-9_]{0,63})\}/g

/** Whether a name can be a secret's (and not the port's own SEAPORT_*). */
export const secretNameOk = (name: string) => secretNamePattern.test(name) && !name.startsWith("SEAPORT_")

/** The name a `${NAME}` reference points at, or "" when it isn't one. */
export function secretRefName(ref: string): string {
  return secretRef.exec(ref)?.[1] ?? ""
}

/** A secret's name as a `${NAME}` reference. */
export const asSecretRef = (name: string) => `\${${name}}`

/** Idle stop: minutes and/or hours, or 0/never. */
export const idleStopPattern = /^(0|never|(\d+h)?(\d+m)?)$/

/** A repo URL the machine takes: https, a host and a path. */
export const repoURLPattern = /^https:\/\/[^\s/@?#]+\/[^\s?#]+$/

/** The folder a repo clones into by default (its last path part). */
export function repoName(url: string): string {
  return (url.replace(/\/+$/, "").split("/").pop() ?? "").replace(/\.git$/, "")
}

/**
 * The agent's secrets its config names, with what names each: MCP headers
 * (`header linear`), model keys (`modelo openai`) and repo credentials
 * (`repo <url>`). A secret named here can't be deleted.
 */
export function configSecrets(p: Pick<ProfileInput, "mcp_servers" | "machine">): Map<string, string[]> {
  const out = new Map<string, string[]>()
  const add = (name: string, use: string) => {
    const uses = out.get(name) ?? []
    if (!uses.includes(use)) out.set(name, [...uses, use])
  }
  for (const s of p.mcp_servers ?? [])
    for (const h of s.headers ?? []) for (const m of h.value.matchAll(secretRefs)) add(m[1], `header ${s.name}`)
  const m = p.machine
  if (m) {
    for (const [prov, v] of Object.entries(m.models ?? {})) {
      const name = secretRefName(v)
      if (name) add(name, `modelo ${prov}`)
    }
    for (const r of m.repos ?? []) {
      const name = secretRefName(r.credential)
      if (name) add(name, `repo ${r.url}`)
    }
  }
  return out
}

/**
 * Whether the machine's slots sign in to a Claude account instead of using
 * a model key (hosting Machine.SignsIn after Normalize): `anthropic:
 * subscription`, or no Anthropic key on Claude Code, or no key at all on
 * rness.
 */
export function signsIn(harness: Harness | "", models: Record<string, string> | undefined): boolean {
  const named = Object.entries(models ?? {}).filter(([, v]) => v)
  if (named.some(([p, v]) => p === "anthropic" && v === subscription)) return true
  if (harness === "claude-code") return !named.some(([p]) => p === "anthropic")
  if (harness === "rness") return named.length === 0
  return false
}

/**
 * What's wrong with the model access, or "" when nothing: every value is a
 * `${SECRET}` or Anthropic's `subscription`, and rness can't mix a Claude
 * account with other keys (hosting.Normalize refuses it).
 */
export function modelsProblem(harness: Harness | "", models: Record<string, string> | undefined): string {
  const named = Object.entries(models ?? {}).filter(([, v]) => v)
  for (const [p, v] of named) {
    if (v === subscription) {
      if (p !== "anthropic") return "Solo Anthropic se conecta con una cuenta."
    } else if (!secretRefPattern.test(v)) return "Cada clave nombra un secreto del agente, como ${NOMBRE}."
  }
  if (harness === "rness" && named.some(([, v]) => v === subscription) && named.length > 1)
    return "Con rness, la cuenta de Claude no se combina con claves de otros proveedores."
  return ""
}

/** The slot's own variables: the machine's env may not set them. */
const slotVars = new Set([
  "SEAPORT_GATEWAY",
  "HTTPS_PROXY",
  "HTTP_PROXY",
  "https_proxy",
  "http_proxy",
  "NO_PROXY",
  "no_proxy",
  ...providers.flatMap((p) => [p.keyEnv, p.keyEnv.replace(/_API_KEY$/, "_BASE_URL")]),
])

/** `env` as the textarea shows it, NAME=value per line. */
export function envText(env: Record<string, string>): string {
  return Object.entries(env)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n")
}

/** The textarea back to `env`; a line without `=` is a name with an empty
 * value (the port refuses it, so the field shows the problem). */
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split("\n")) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf("=")
    const k = (i < 0 ? line : line.slice(0, i)).trim()
    out[k] = i < 0 ? "" : line.slice(i + 1).trim()
  }
  return out
}

/** What's wrong with an `env` name, or "" when nothing. */
export function envProblem(env: Record<string, string>): string {
  for (const [k, v] of Object.entries(env)) {
    if (!secretNameOk(k)) return `${k}: mayúsculas, dígitos y _; no puede empezar con SEAPORT_.`
    if (slotVars.has(k)) return `${k} la pone el puerto en cada instancia.`
    if (!v) return `${k} no tiene valor.`
  }
  if (Object.keys(env).length > 50) return "Como mucho 50 variables."
  return ""
}

/** A config with nothing set (a new agent's, before its first version). */
export const emptyConfig: ProfileInput = {
  instructions: "",
  skills: [],
  mcp_servers: [],
  approve: "",
  model: "",
  effort: "",
  browser: false,
  browser_assist: "",
  browser_detect: false,
  browser_ignore_hosts: [],
  harness: "",
  machine: null,
}

/** A machine with the defaults the server would fill (hosting.Normalize). */
export function newMachine(over: Partial<Machine> = {}): Machine {
  return {
    image: "",
    packages: [],
    setup: "",
    repos: [],
    repo_config: "trust",
    home: "",
    models: {},
    env: {},
    network: { egress: [] },
    browser: { engine: "" },
    resources: { size: "S", cpus: 0, memory_mb: 0 },
    workspace: "persistent",
    idle_stop: "30m",
    ...over,
  }
}

/** A machine as the server answered it, with every list and map present
 * (Go's nil slices and maps arrive as null). */
export function fullMachine(m: Machine): Machine {
  const d = newMachine()
  return {
    ...d,
    ...m,
    packages: m.packages ?? [],
    repos: (m.repos ?? []).map((r) => ({ ...blankRepo, ...r })),
    models: m.models ?? {},
    env: m.env ?? {},
    network: { egress: m.network?.egress ?? [] },
    browser: { engine: m.browser?.engine ?? "" },
    resources: m.resources ?? d.resources,
  }
}

export const blankRepo: MachineRepo = { url: "", ref: "", path: "", credential: "", on_start: "" }

/** The machine as it is saved: blank entries and duplicates out. */
export function cleanMachine(m: Machine): Machine {
  const list = (xs: string[]) => [...new Set(xs.map((x) => x.trim()).filter(Boolean))]
  return {
    ...m,
    env: Object.fromEntries(Object.entries(m.env ?? {}).filter(([k]) => k)),
    models: Object.fromEntries(Object.entries(m.models ?? {}).filter(([, v]) => v)),
    packages: list(m.packages ?? []),
    network: { egress: list((m.network?.egress ?? []).map((h) => h.toLowerCase())) },
    resources: m.resources?.size
      ? { size: m.resources.size, cpus: 0, memory_mb: 0 }
      : { cpus: m.resources?.cpus ?? 0, memory_mb: m.resources?.memory_mb ?? 0 },
    idle_stop: m.idle_stop.trim(),
    image: m.image.trim(),
  }
}

/** What's wrong with a machine before saving, or "" when nothing. */
export function machineProblem(harness: Harness | "", m: Machine): string {
  if (!harness) return "Un agente alojado necesita un harness."
  const models = modelsProblem(harness, m.models)
  if (models) return models
  const env = envProblem(m.env ?? {})
  if (env) return env
  if (!idleStopPattern.test(m.idle_stop.trim())) return "Detener sin trabajo: como 30m o 2h, o 0."
  for (const r of m.repos ?? []) {
    if (!repoURLPattern.test(r.url.trim())) return `Repo ${r.url || "sin URL"}: debe ser https.`
    if (r.credential && !secretRefPattern.test(r.credential)) return `Repo ${r.url}: la credencial nombra un secreto.`
  }
  return ""
}
