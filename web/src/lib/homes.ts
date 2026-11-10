// Harness homes in the browser: which files of a dropped folder are
// config (the port's rules, GET /homes/rules), secret-looking strings,
// a tree of paths, and line diffs. The port checks everything again.

import type { HomeRules } from "./types"

export interface StagedFile {
  data: Uint8Array
  exec: boolean
}

/** Files by path (relative to the home's root). */
export type Staged = Map<string, StagedFile>

export interface Skip {
  path: string
  reason: string
  /** Files under path when it's a dir left out whole. */
  count?: number
}

/** A file offered for the home (a dropped folder's, an archive's), read lazily. */
export interface Incoming {
  path: string
  size: number
  exec: boolean
  read: () => Promise<Uint8Array>
  /** A dir left out without walking it (path ends in /). */
  skippedDir?: boolean
  /** Not a regular file (a link, a device): listed as left out. */
  other?: boolean
}

/**
 * The home's files among what was offered: the dropped folder's own name
 * (or a .claude/.rness inside it) is the root; what isn't config, or is
 * over a file's limit, is left out and listed (dirs once, with a count).
 */
export async function collect(
  rules: HomeRules,
  harness: string,
  offered: Incoming[],
  { unwrap = true }: { unwrap?: boolean } = {},
): Promise<{ files: Staged; skipped: Skip[] }> {
  let items = offered.map((f) => ({ ...f, path: f.path.replace(/^(\.\/)+/, "") })).filter((f) => f.path)
  const top = (p: string) => p.split("/")[0]
  const known = (name: string) => rules.dirs.includes(name) || rules.files.includes(name)
  // One wrapping folder (a zip of .claude/, a picked folder): its contents are the home.
  for (let level = 0; unwrap && level < 2; level++) {
    const first = new Set(items.map((f) => top(f.path)))
    const [only] = first
    if (first.size !== 1 || known(only) || !items.every((f) => f.path.includes("/"))) break
    items = items.map((f) => ({ ...f, path: f.path.slice(only.length + 1) }))
  }
  // A home dir holding the harness's root (~ with .claude/ inside).
  const skipped = new Map<string, Skip>()
  const skip = (path: string, reason: string) => {
    const s = skipped.get(path)
    if (s) s.count = (s.count ?? 1) + 1
    else skipped.set(path, { path, reason })
  }
  if (!items.some((f) => known(top(f.path))) && items.some((f) => f.path.startsWith(rules.root + "/"))) {
    items = items.filter((f) => {
      if (f.path.startsWith(rules.root + "/")) return true
      skip(f.path.includes("/") ? top(f.path) + "/" : f.path, `fuera de ${rules.root}`)
      return false
    })
    items = items.map((f) => ({ ...f, path: f.path.slice(rules.root.length + 1) }))
  }
  const files: Staged = new Map()
  for (const f of items) {
    if (f.other) {
      skip(f.path, "enlace o dispositivo: solo van archivos")
      continue
    }
    if (f.skippedDir) {
      const dir = f.path.replace(/\/$/, "")
      skip(dir + "/", homePathProblem(rules, harness, dir + "/x") || "no es configuración del harness")
      continue
    }
    const why = homePathProblem(rules, harness, f.path)
    if (why) {
      const parts = f.path.split("/")
      if (parts.length > 1 && !rules.dirs.includes(parts[0])) skip(parts[0] + "/", why)
      else skip(f.path, why)
      continue
    }
    if (f.size > rules.max_file) {
      skip(f.path, `pasa de ${formatBytesShort(rules.max_file)} por archivo`)
      continue
    }
    const data = await f.read()
    files.set(f.path, { data, exec: f.exec || shebang(data) })
  }
  return { files, skipped: [...skipped.values()] }
}

/** The offered files of a dropped folder (drag and drop), walking only the
 * dirs a home may hold: a ~ with sessions can be gigabytes. */
export async function walkDropped(rules: HomeRules, entries: FileSystemEntry[]): Promise<Incoming[]> {
  const list = (d: FileSystemDirectoryEntry) =>
    new Promise<FileSystemEntry[]>((resolve, reject) => {
      const reader = d.createReader()
      const out: FileSystemEntry[] = []
      const next = () =>
        reader.readEntries((batch) => {
          if (batch.length === 0) resolve(out)
          else {
            out.push(...batch)
            next()
          }
        }, reject)
      next()
    })
  const fileOf = (f: FileSystemFileEntry) => new Promise<File>((resolve, reject) => f.file(resolve, reject))
  const known = (name: string) => rules.dirs.includes(name) || rules.files.includes(name)
  // One dropped folder is the home; a ~ holding .claude/.rness, that one.
  let roots = entries
  if (roots.length === 1 && roots[0].isDirectory) {
    let inner = await list(roots[0] as FileSystemDirectoryEntry)
    if (!inner.some((e) => known(e.name))) {
      const home = inner.find((e) => e.isDirectory && e.name === rules.root)
      if (home) inner = await list(home as FileSystemDirectoryEntry)
    }
    roots = inner
  }
  const out: Incoming[] = []
  const walk = async (items: FileSystemEntry[], prefix: string) => {
    for (const e of items) {
      const rel = prefix + e.name
      if (e.isFile) {
        const file = await fileOf(e as FileSystemFileEntry)
        out.push({ path: rel, size: file.size, exec: false, read: async () => new Uint8Array(await file.arrayBuffer()) })
      } else if (e.isDirectory) {
        if (!homeDirAllowed(rules, rel)) out.push({ path: rel + "/", size: 0, exec: false, skippedDir: true, read: async () => new Uint8Array() })
        else await walk(await list(e as FileSystemDirectoryEntry), rel + "/")
      }
    }
  }
  await walk(roots, "")
  return out
}

/** Scripts run directly (hooks, status lines) need the exec bit, which a browser can't see. */
export const shebang = (data: Uint8Array) => data[0] === 0x23 && data[1] === 0x21

/** Why rel can't go in the home ("" when it can). Mirrors hosting.HomePath. */
export function homePathProblem(rules: HomeRules, harness: string, rel: string): string {
  const parts = rel.split("/")
  if (!rel || rel.length > 255 || rel.includes("\\") || rel.startsWith("/") || parts.some((p) => p === "" || p === "." || p === ".."))
    return "no es una ruta relativa válida"
  if (parts.length > rules.max_depth) return `más de ${rules.max_depth} niveles`
  if (parts.includes(".git")) return "metadatos de git"
  const base = parts[parts.length - 1]
  if (base === ".credentials.json" || base.startsWith("credentials.json")) return "credenciales: nunca van en la configuración"
  if (base === "settings.local.json") return "ajustes locales de una máquina"
  if (base === ".DS_Store" || base.endsWith("~") || base.endsWith(".bak") || base.includes(".backup"))
    return "copia de seguridad o resto del editor"
  if (harness === "claude-code" && parts[0] === "plugins")
    return "los plugins no se suben: actívalos en settings.json (enabledPlugins) y el build los instala"
  const dir = rules.dirs.includes(parts[0])
  const file = rules.files.includes(parts[0]) || (parts.length === 1 && rules.file_prefixes.some((p) => parts[0].startsWith(p)))
  if (!dir && !file) return "no es configuración del harness (sesiones, historial, cachés…)"
  if (file && parts.length > 1) return `${parts[0]} es un archivo`
  if (dir && parts.length === 1) return `${parts[0]} es una carpeta`
  if (rules.reserved.includes(rel)) return "es de Seaport"
  return ""
}

/** Whether a dir (relative to the root) may hold config: walks skip the rest. */
export function homeDirAllowed(rules: HomeRules, rel: string) {
  const parts = rel.split("/")
  return !parts.includes(".git") && rules.dirs.includes(parts[0])
}

export interface SecretHit {
  path: string
  line: number
  what: string
}

const secretWhat: Record<string, string> = {
  "an Anthropic API key": "una clave de Anthropic",
  "an OpenAI API key": "una clave de OpenAI",
  "a GitHub token": "un token de GitHub",
  "a GitLab token": "un token de GitLab",
  "a Slack token": "un token de Slack",
  "an AWS access key": "una clave de AWS",
  "a Google API key": "una clave de Google",
  "a private key": "una clave privada",
  "a Seaport crew token": "un token de tripulación de Seaport",
  "an IAMKit API key": "una clave de IAMKit",
}

/** The first secret-looking string of a text file (as ScanSecrets). */
export function findSecret(rules: HomeRules, path: string, text: string): SecretHit | null {
  for (const s of rules.secrets) {
    const m = new RegExp(s.pattern).exec(text)
    if (m) return { path, line: text.slice(0, m.index).split("\n").length, what: secretWhat[s.what] ?? s.what }
  }
  return null
}

const strict = new TextDecoder("utf-8", { fatal: true })

/** The file as text, or null when it isn't UTF-8 (images…). */
export function asText(data: Uint8Array): string | null {
  try {
    return strict.decode(data)
  } catch {
    return null
  }
}

export const encodeText = (s: string) => new TextEncoder().encode(s)

/** Lowercase hex SHA-256, as the manifest's. */
export async function sha256(data: Uint8Array) {
  const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", data as BufferSource))
  return Array.from(sum, (b) => b.toString(16).padStart(2, "0")).join("")
}

export interface Problems {
  secrets: SecretHit[]
  /** Over a limit: uploading would be refused. */
  limits: string[]
  /** Soft: the home works, maybe not as meant. */
  notes: string[]
}

export function checkStaged(rules: HomeRules, harness: string, staged: Staged): Problems {
  const out: Problems = { secrets: [], limits: [], notes: [] }
  let total = 0
  for (const [path, f] of staged) {
    total += f.data.length
    if (f.data.length > rules.max_file) out.limits.push(`${path} pasa de ${formatBytesShort(rules.max_file)}`)
    const text = asText(f.data)
    if (text !== null) {
      const hit = findSecret(rules, path, text)
      if (hit) out.secrets.push(hit)
    }
  }
  if (staged.size > rules.max_files) out.limits.push(`más de ${rules.max_files} archivos`)
  if (total > rules.max_bytes) out.limits.push(`más de ${formatBytesShort(rules.max_bytes)} en total`)
  if (harness === "rness") {
    const init = staged.get("init.lua")
    const text = init && asText(init.data)
    if (text != null && !/require\s*\(?\s*["']providers["']/.test(text))
      out.notes.push(
        'init.lua reemplaza al de la base entero y no carga require("providers"): las instancias no tendrán proveedores de modelos.',
      )
  }
  return out
}

function formatBytesShort(n: number) {
  return `${Math.round(n / (1 << 20))} MiB`
}

export interface TreeNode {
  name: string
  path: string
  dir: boolean
  size: number
  children: TreeNode[]
}

/** A tree of paths (dirs first, then by name), with each dir's total size. */
export function buildTree(files: { path: string; size: number }[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", dir: true, size: 0, children: [] }
  for (const f of files) {
    let node = root
    const parts = f.path.split("/")
    parts.forEach((name, i) => {
      node.size += f.size
      const path = parts.slice(0, i + 1).join("/")
      const last = i === parts.length - 1
      let next = node.children.find((c) => c.name === name && c.dir === !last)
      if (!next) {
        next = { name, path, dir: !last, size: 0, children: [] }
        node.children.push(next)
      }
      if (last) next.size = f.size
      node = next
    })
  }
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name)))
    n.children.forEach(sort)
  }
  sort(root)
  return root.children
}

export type Change = "added" | "changed" | "removed"

/** Per path, how `next` differs from `prev` (by content hash). */
export function changes(prev: Map<string, string>, next: Map<string, string>): Map<string, Change> {
  const out = new Map<string, Change>()
  for (const [p, h] of next) {
    const before = prev.get(p)
    if (before === undefined) out.set(p, "added")
    else if (before !== h) out.set(p, "changed")
  }
  for (const p of prev.keys()) if (!next.has(p)) out.set(p, "removed")
  return out
}

export interface DiffLine {
  kind: "same" | "add" | "del"
  text: string
  /** Line numbers in the old and new text (0: absent). */
  a: number
  b: number
}

/** Max lines × lines a diff compares (beyond, it reports null). */
const maxDiffCells = 4_000_000

/** A line diff (LCS over what differs between the common prefix and suffix). */
export function diffLines(oldText: string, newText: string): DiffLine[] | null {
  const a = oldText === "" ? [] : oldText.replace(/\n$/, "").split("\n")
  const b = newText === "" ? [] : newText.replace(/\n$/, "").split("\n")
  let pre = 0
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++
  let suf = 0
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++
  const x = a.slice(pre, a.length - suf)
  const y = b.slice(pre, b.length - suf)
  if (x.length * y.length > maxDiffCells) return null
  // lcs[i][j]: LCS length of x[i:] and y[j:], flattened.
  const w = y.length + 1
  const lcs = new Uint32Array((x.length + 1) * w)
  for (let i = x.length - 1; i >= 0; i--)
    for (let j = y.length - 1; j >= 0; j--)
      lcs[i * w + j] = x[i] === y[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1])
  const out: DiffLine[] = []
  for (let k = 0; k < pre; k++) out.push({ kind: "same", text: a[k], a: k + 1, b: k + 1 })
  let i = 0
  let j = 0
  while (i < x.length || j < y.length) {
    if (i < x.length && j < y.length && x[i] === y[j]) {
      out.push({ kind: "same", text: x[i], a: pre + i + 1, b: pre + j + 1 })
      i++
      j++
    } else if (j < y.length && (i === x.length || lcs[i * w + j + 1] >= lcs[(i + 1) * w + j])) {
      out.push({ kind: "add", text: y[j], a: 0, b: pre + j + 1 })
      j++
    } else {
      out.push({ kind: "del", text: x[i], a: pre + i + 1, b: 0 })
      i++
    }
  }
  for (let k = 0; k < suf; k++)
    out.push({ kind: "same", text: a[a.length - suf + k], a: a.length - suf + k + 1, b: b.length - suf + k + 1 })
  return out
}
