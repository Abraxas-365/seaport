// @vitest-environment node
// (jsdom has no Blob.stream(), which the gzip helpers use; browsers and Node do.)

import { describe, expect, it } from "vitest"

import { gzip, readArchive, readTar, writeTar, type ArchiveFile } from "./archive"

const enc = new TextEncoder()
const file = (path: string, text: string, exec = false): ArchiveFile => ({ path, data: enc.encode(text), exec })
const text = (f: ArchiveFile) => new TextDecoder().decode(f.data)

describe("writeTar / readTar", () => {
  it("round-trips files, content and the exec bit, sorted by path", () => {
    const files = [file("settings.json", '{"a":1}'), file("hooks/run.sh", "#!/bin/sh\n", true), file("CLAUDE.md", "# hi\n")]
    const back = readTar(writeTar(files))
    expect(back.others).toEqual([])
    expect(back.files.map((f) => f.path)).toEqual(["CLAUDE.md", "hooks/run.sh", "settings.json"])
    expect(back.files.map(text)).toEqual(["# hi\n", "#!/bin/sh\n", '{"a":1}'])
    expect(back.files.map((f) => f.exec)).toEqual([false, true, false])
  })

  it("round-trips empty files and files that fill a block exactly", () => {
    const block = "x".repeat(512)
    const back = readTar(writeTar([file("empty", ""), file("block", block), file("more", block + "y")]))
    expect(back.files.map((f) => [f.path, f.data.length])).toEqual([
      ["block", 512],
      ["empty", 0],
      ["more", 513],
    ])
  })

  it("round-trips long paths through a PAX header", () => {
    const long = "skills/" + "deep/".repeat(30) + "SKILL.md"
    expect(long.length).toBeGreaterThan(100)
    const back = readTar(writeTar([file(long, "body")]))
    expect(back.files.map((f) => f.path)).toEqual([long])
    expect(text(back.files[0])).toBe("body")
  })

  it("round-trips non-ASCII names and bytes", () => {
    const f: ArchiveFile = { path: "skills/ñandú.md", data: new Uint8Array([0, 255, 128, 10]), exec: false }
    const back = readTar(writeTar([f]))
    expect(back.files[0].path).toBe(f.path)
    expect([...back.files[0].data]).toEqual([0, 255, 128, 10])
  })

  it("is deterministic (same files, same bytes)", () => {
    const a = writeTar([file("b", "2"), file("a", "1")])
    const b = writeTar([file("a", "1"), file("b", "2")])
    expect(a).toEqual(b)
  })

  it("refuses a truncated tar", () => {
    const tar = writeTar([file("a", "x".repeat(2000))])
    expect(() => readTar(tar.subarray(0, 700))).toThrow(/cortado/)
  })
})

describe("readArchive", () => {
  it("reads a .tar.gz by its magic bytes", async () => {
    const tar = writeTar([file("CLAUDE.md", "# hi\n")])
    const back = await readArchive(await gzip(tar))
    expect(back.files.map((f) => [f.path, text(f)])).toEqual([["CLAUDE.md", "# hi\n"]])
  })

  it("reads a plain .tar", async () => {
    const back = await readArchive(writeTar([file("a", "1")]))
    expect(back.files).toHaveLength(1)
  })

  it("reads a stored zip", async () => {
    const back = await readArchive(storedZip({ "settings.json": "{}", "agents/x.md": "x" }))
    expect(back.files.map((f) => f.path).sort()).toEqual(["agents/x.md", "settings.json"])
    expect(text(back.files.find((f) => f.path === "settings.json")!)).toBe("{}")
  })

  it("rejects what is not a zip", async () => {
    await expect(readArchive(new Uint8Array([0x50, 0x4b, 1, 2, 3]))).rejects.toThrow(/zip/)
  })
})

/** A minimal zip with stored (uncompressed) entries. */
function storedZip(entries: Record<string, string>): Uint8Array {
  const parts: Uint8Array[] = []
  const central: Uint8Array[] = []
  let offset = 0
  const u16 = (v: DataView, at: number, n: number) => v.setUint16(at, n, true)
  const u32 = (v: DataView, at: number, n: number) => v.setUint32(at, n, true)
  for (const [name, body] of Object.entries(entries)) {
    const n = enc.encode(name)
    const data = enc.encode(body)
    const local = new Uint8Array(30 + n.length)
    const lv = new DataView(local.buffer)
    u32(lv, 0, 0x04034b50)
    u32(lv, 22, data.length)
    u16(lv, 26, n.length)
    local.set(n, 30)
    const cen = new Uint8Array(46 + n.length)
    const cv = new DataView(cen.buffer)
    u32(cv, 0, 0x02014b50)
    u32(cv, 20, data.length)
    u32(cv, 24, data.length)
    u16(cv, 28, n.length)
    u32(cv, 38, 0o100644 << 16)
    u32(cv, 42, offset)
    cen.set(n, 46)
    parts.push(local, data)
    central.push(cen)
    offset += local.length + data.length
  }
  const dirSize = central.reduce((s, c) => s + c.length, 0)
  const eocd = new Uint8Array(22)
  const ev = new DataView(eocd.buffer)
  u32(ev, 0, 0x06054b50)
  u16(ev, 8, central.length)
  u16(ev, 10, central.length)
  u32(ev, 12, dirSize)
  u32(ev, 16, offset)
  const all = [...parts, ...central, eocd]
  const out = new Uint8Array(all.reduce((s, p) => s + p.length, 0))
  let at = 0
  for (const p of all) {
    out.set(p, at)
    at += p.length
  }
  return out
}
