// An agent's config as `seaport agents config show` writes it (and
// `seaport agents config apply -f` reads it): the harness, the profile's
// fields, then the machine, block YAML, empty values left out.

import type { ProfileInput } from "./types"

type Value = string | number | boolean | null | undefined | Value[] | { [k: string]: Value }

export function configYaml(c: ProfileInput): string {
  const machine = c.machine ? structuredClone(c.machine) : null
  if (machine?.resources.size) machine.resources = { ...machine.resources, cpus: 0, memory_mb: 0 } // the preset says it
  // The CLI's order (configFile: harness, profileFile's fields, machine).
  const doc = {
    harness: c.harness,
    instructions: c.instructions,
    skills: c.skills,
    mcp_servers: c.mcp_servers,
    approve: c.approve,
    model: c.model,
    effort: c.effort,
    browser: c.browser,
    browser_assist: c.browser_assist,
    browser_detect: c.browser_detect,
    browser_ignore_hosts: c.browser_ignore_hosts,
    machine,
  } as unknown as Record<string, Value>
  return lines(doc, 0).join("\n") + "\n"
}

/** What the CLI leaves out: empty strings, zeros, nulls and empty lists or maps (false stays). */
function empty(v: Value): boolean {
  if (v === undefined || v === null || v === "" || v === 0) return true
  if (Array.isArray(v)) return v.length === 0
  if (typeof v === "object") return Object.values(v).every(empty)
  return false
}

function lines(obj: Record<string, Value>, indent: number): string[] {
  const pad = " ".repeat(indent)
  const out: string[] = []
  for (const [k, v] of Object.entries(obj)) {
    if (empty(v)) continue
    if (Array.isArray(v)) {
      out.push(`${pad}${k}:`)
      for (const item of v) {
        if (item && typeof item === "object" && !Array.isArray(item)) {
          const inner = lines(item, indent + 4)
          if (inner.length) out.push(`${pad}  - ${inner[0].trimStart()}`, ...inner.slice(1))
        } else out.push(`${pad}  - ${scalar(item, indent + 4)}`)
      }
    } else if (typeof v === "object" && v !== null) {
      out.push(`${pad}${k}:`, ...lines(v, indent + 2))
    } else out.push(`${pad}${k}: ${scalar(v, indent + 2)}`)
  }
  return out
}

const plain = /^[A-Za-z0-9_$/][A-Za-z0-9_$/{}.@+=~:#* -]*$/
const reserved = /^(true|false|yes|no|on|off|null|~|y|n)$/i
const numeric = /^[-+]?(\d[\d_]*(\.\d*)?|\.\d+)([eE][-+]?\d+)?$/

function scalar(v: Value, indent: number): string {
  if (typeof v === "number" || typeof v === "boolean") return String(v)
  const s = String(v ?? "")
  if (s.includes("\n")) {
    const pad = " ".repeat(indent)
    const body = s.replace(/\n$/, "").split("\n")
    return `|${s.endsWith("\n") ? "" : "-"}\n` + body.map((l) => (l ? pad + l : "")).join("\n")
  }
  const safe = plain.test(s) && !s.endsWith(" ") && !s.endsWith(":") && !s.includes(": ") && !s.includes(" #")
  if (safe && !reserved.test(s) && !numeric.test(s)) return s
  return JSON.stringify(s)
}
