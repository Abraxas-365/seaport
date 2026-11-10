// Archives in the browser, no library: read tar (PAX/GNU long names),
// tar.gz and zip (stored or deflated); write a plain tar (PAX for long
// names) and gzip it. Enough for harness homes: regular files only, the
// port normalizes the rest.

export interface ArchiveFile {
  path: string
  data: Uint8Array
  exec: boolean
}

export interface ArchiveRead {
  files: ArchiveFile[]
  /** Entries that aren't regular files (links, devices). */
  others: string[]
}

const utf8 = new TextDecoder()
const encoder = new TextEncoder()

async function pipe(data: Uint8Array, stream: CompressionStream | DecompressionStream): Promise<Uint8Array> {
  const out = await new Response(new Blob([data as BlobPart]).stream().pipeThrough(stream)).arrayBuffer()
  return new Uint8Array(out)
}

export const gzip = (data: Uint8Array) => pipe(data, new CompressionStream("gzip"))
const gunzip = (data: Uint8Array) => pipe(data, new DecompressionStream("gzip"))
const inflateRaw = (data: Uint8Array) => pipe(data, new DecompressionStream("deflate-raw"))

/** Reads a .tar, .tar.gz/.tgz or .zip by its first bytes. */
export async function readArchive(data: Uint8Array): Promise<ArchiveRead> {
  if (data[0] === 0x1f && data[1] === 0x8b) return readTar(await gunzip(data))
  if (data[0] === 0x50 && data[1] === 0x4b) return readZip(data)
  return readTar(data)
}

function cstr(b: Uint8Array, at: number, len: number) {
  const s = b.subarray(at, at + len)
  const end = s.indexOf(0)
  return utf8.decode(end < 0 ? s : s.subarray(0, end))
}

function octal(b: Uint8Array, at: number, len: number) {
  const s = cstr(b, at, len).trim()
  return s ? parseInt(s, 8) : 0
}

/** PAX records ("<len> key=value\n"). */
function pax(data: Uint8Array) {
  const out: Record<string, string> = {}
  for (const rec of utf8.decode(data).split("\n")) {
    const sp = rec.indexOf(" ")
    const eq = rec.indexOf("=")
    if (sp > 0 && eq > sp) out[rec.slice(sp + 1, eq)] = rec.slice(eq + 1)
  }
  return out
}

export function readTar(data: Uint8Array): ArchiveRead {
  const out: ArchiveRead = { files: [], others: [] }
  let at = 0
  let longName = ""
  let paxPath = ""
  let paxSize = -1
  while (at + 512 <= data.length) {
    const h = data.subarray(at, at + 512)
    if (h.every((b) => b === 0)) break
    const size = paxSize >= 0 ? paxSize : octal(h, 124, 12)
    paxSize = -1
    const type = String.fromCharCode(h[156] || 48)
    const ustar = cstr(h, 257, 6).startsWith("ustar")
    const prefix = ustar ? cstr(h, 345, 155) : ""
    const body = data.subarray(at + 512, at + 512 + size)
    at += 512 + Math.ceil(size / 512) * 512
    if (body.length < size) throw new Error("el tar está cortado")
    switch (type) {
      case "x": {
        // Applies to the next entry.
        const p = pax(body)
        paxPath = p.path ?? ""
        if (p.size) paxSize = Number(p.size)
        continue
      }
      case "g":
        continue
      case "L":
        longName = cstr(body, 0, body.length)
        continue
      case "K":
        continue
    }
    let name = paxPath || longName || (prefix ? `${prefix}/${cstr(h, 0, 100)}` : cstr(h, 0, 100))
    paxPath = ""
    longName = ""
    name = name.replace(/^(\.\/)+/, "")
    if (type === "5" || name.endsWith("/")) continue
    if (type === "0" || type === "7") {
      out.files.push({ path: name, data: body.slice(), exec: (octal(h, 100, 8) & 0o111) !== 0 })
    } else {
      out.others.push(name)
    }
  }
  return out
}

export async function readZip(data: Uint8Array): Promise<ArchiveRead> {
  const v = new DataView(data.buffer, data.byteOffset, data.byteLength)
  let eocd = -1
  for (let i = data.length - 22; i >= Math.max(0, data.length - 65_557); i--) {
    if (v.getUint32(i, true) === 0x06054b50) {
      eocd = i
      break
    }
  }
  if (eocd < 0) throw new Error("no es un zip válido")
  const count = v.getUint16(eocd + 10, true)
  let at = v.getUint32(eocd + 16, true)
  if (at === 0xffffffff) throw new Error("los zip64 no se leen aquí: usa un .tar.gz")
  const out: ArchiveRead = { files: [], others: [] }
  for (let i = 0; i < count; i++) {
    if (v.getUint32(at, true) !== 0x02014b50) throw new Error("el zip está dañado")
    const flags = v.getUint16(at + 8, true)
    const method = v.getUint16(at + 10, true)
    const csize = v.getUint32(at + 20, true)
    const nlen = v.getUint16(at + 28, true)
    const elen = v.getUint16(at + 30, true)
    const clen = v.getUint16(at + 32, true)
    const mode = v.getUint32(at + 38, true) >>> 16
    const local = v.getUint32(at + 42, true)
    const name = utf8.decode(data.subarray(at + 46, at + 46 + nlen)).replace(/^(\.\/)+/, "")
    at += 46 + nlen + elen + clen
    if (name.endsWith("/")) continue
    if ((mode & 0o170000) === 0o120000 || (flags & 1) !== 0) {
      out.others.push(name)
      continue
    }
    const start = local + 30 + v.getUint16(local + 26, true) + v.getUint16(local + 28, true)
    const raw = data.subarray(start, start + csize)
    let body: Uint8Array
    if (method === 0) body = raw.slice()
    else if (method === 8) body = await inflateRaw(raw)
    else throw new Error(`${name}: compresión ${method} no soportada (usa deflate o un .tar.gz)`)
    out.files.push({ path: name, data: body, exec: (mode & 0o111) !== 0 })
  }
  return out
}

function header(name: Uint8Array, size: number, mode: number, type: string) {
  const h = new Uint8Array(512)
  const put = (s: string, at: number) => h.set(encoder.encode(s), at)
  const num = (n: number, at: number, len: number) => put(n.toString(8).padStart(len - 1, "0") + "\0", at)
  h.set(name.subarray(0, 100), 0)
  num(mode, 100, 8)
  num(1000, 108, 8)
  num(1000, 116, 8)
  num(size, 124, 12)
  num(0, 136, 12)
  put("        ", 148)
  put(type, 156)
  put("ustar\x0000", 257)
  const sum = h.reduce((a, b) => a + b, 0)
  put(sum.toString(8).padStart(6, "0") + "\0 ", 148)
  return h
}

/** A PAX record "<len> path=<path>\n", where len counts itself. */
function paxRecord(key: string, value: string) {
  const body = ` ${key}=${value}\n`
  const n = encoder.encode(body).length
  let len = n + String(n).length
  if (String(len).length !== String(n).length) len = n + String(len).length
  return encoder.encode(`${len}${body}`)
}

/** A tar of files (sorted, mode 0644/0755, uid 1000, mtime 0). */
export function writeTar(files: ArchiveFile[]): Uint8Array {
  const parts: Uint8Array[] = []
  const pad = (n: number) => parts.push(new Uint8Array((512 - (n % 512)) % 512))
  for (const f of [...files].sort((a, b) => (a.path < b.path ? -1 : 1))) {
    let name = encoder.encode(f.path)
    if (name.length > 100) {
      const rec = paxRecord("path", f.path)
      parts.push(header(encoder.encode("././@PaxHeader"), rec.length, 0o644, "x"), rec)
      pad(rec.length)
      name = encoder.encode("seaport-long-name")
    }
    parts.push(header(name, f.data.length, f.exec ? 0o755 : 0o644, "0"), f.data)
    pad(f.data.length)
  }
  parts.push(new Uint8Array(1024))
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let at = 0
  for (const p of parts) {
    out.set(p, at)
    at += p.length
  }
  return out
}
