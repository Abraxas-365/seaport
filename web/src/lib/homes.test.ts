import { describe, expect, it } from "vitest"

import { checkStaged, findSecret, homeDirAllowed, homePathProblem, type Staged } from "./homes"
import { homeRules } from "@/test/wire"

// The rules are the ones Go publishes (hosting.HomeRulesOf), via the wire fixtures.
const claude = homeRules("claude-code")
const rness = homeRules("rness")
const enc = new TextEncoder()
const staged = (files: Record<string, string | Uint8Array>): Staged =>
  new Map(Object.entries(files).map(([p, d]) => [p, { data: typeof d === "string" ? enc.encode(d) : d, exec: false }]))

describe("homePathProblem", () => {
  it.each([
    ["CLAUDE.md"],
    ["settings.json"],
    ["keybindings.json"],
    ["agents/reviewer.md"],
    ["skills/deploy/SKILL.md"],
    ["hooks/pre-commit.sh"],
    ["statusline.sh"],
    ["commands/a/b/c.md"],
  ])("claude-code accepts %s", (rel) => expect(homePathProblem(claude, "claude-code", rel)).toBe(""))

  it.each([
    ["", "ruta relativa"],
    ["/etc/passwd", "ruta relativa"],
    ["../settings.json", "ruta relativa"],
    ["agents//x.md", "ruta relativa"],
    ["agents\\x.md", "ruta relativa"],
    ["agents/./x.md", "ruta relativa"],
    ["agents/.git/config", "git"],
    [".credentials.json", "credenciales"],
    ["agents/credentials.json.old", "credenciales"],
    ["settings.local.json", "locales"],
    ["agents/x.md~", "copia"],
    ["agents/x.bak", "copia"],
    ["agents/.DS_Store", "copia"],
    ["plugins/x/plugin.json", "plugins"],
    ["projects/session.jsonl", "no es configuración"],
    ["history.jsonl", "no es configuración"],
    ["CLAUDE.md/inner.md", "es un archivo"],
    ["agents", "es una carpeta"],
  ])("claude-code refuses %j (%s)", (rel, why) => {
    expect(homePathProblem(claude, "claude-code", rel)).toContain(why)
  })

  it("refuses paths deeper than the rules allow", () => {
    const deep = "agents/" + Array.from({ length: claude.max_depth }, (_, i) => `d${i}`).join("/")
    expect(homePathProblem(claude, "claude-code", deep)).toContain(`${claude.max_depth} niveles`)
  })

  it("refuses paths longer than 255 characters", () => {
    expect(homePathProblem(claude, "claude-code", "agents/" + "a".repeat(260))).toContain("ruta relativa")
  })

  it("rness takes its own layout and keeps lua/seaport.lua for Seaport", () => {
    expect(homePathProblem(rness, "rness", "init.lua")).toBe("")
    expect(homePathProblem(rness, "rness", "hooks.json")).toBe("")
    expect(homePathProblem(rness, "rness", "lua/mine.lua")).toBe("")
    expect(homePathProblem(rness, "rness", "plugins/x/init.lua")).toBe("") // only Claude's plugins are refused
    expect(homePathProblem(rness, "rness", "lua/seaport.lua")).toBe("es de Seaport")
    expect(homePathProblem(rness, "rness", "CLAUDE.md")).toContain("no es configuración")
  })
})

describe("homeDirAllowed", () => {
  it("walks only the harness's config dirs", () => {
    expect(homeDirAllowed(claude, "agents")).toBe(true)
    expect(homeDirAllowed(claude, "skills/deploy")).toBe(true)
    expect(homeDirAllowed(claude, "projects")).toBe(false)
    expect(homeDirAllowed(claude, "agents/.git")).toBe(false)
  })
})

// Built at run time so this file holds no credential-shaped literal.
const anthropicKey = "sk-" + "ant-" + "A1b2C3d4E5f6G7h8I9j0K1l2"
const slackToken = "xo" + "xb-" + "0123456789-abcdef"
const pemHeader = "-----BEGIN " + "RSA PRIVATE " + "KEY-----"
const crewToken = "sp_" + "crew_" + "AbCdEfGhIjKlMnOp12"

describe("findSecret", () => {
  it("finds a secret and the line it is on, in Spanish", () => {
    const hit = findSecret(claude, "settings.json", `{\n  "env": {\n    "KEY": "${anthropicKey}"\n  }\n}`)
    expect(hit).toEqual({ path: "settings.json", line: 3, what: "una clave de Anthropic" })
  })

  it.each([
    [slackToken, "un token de Slack"],
    [pemHeader, "una clave privada"],
    [crewToken, "un token de tripulación de Seaport"],
  ])("recognizes %s", (text, what) => {
    expect(findSecret(claude, "x", `a\n${text}`)).toMatchObject({ line: 2, what })
  })

  it("is null for ordinary text and for look-alikes that are too short", () => {
    expect(findSecret(claude, "CLAUDE.md", "# Notes\nUse `sk-` prefixes for ids.\nsk-" + "ant-short")).toBeNull()
  })

  it("uses every pattern the server sends", () => {
    expect(claude.secrets.length).toBeGreaterThanOrEqual(10)
    for (const s of claude.secrets) expect(() => new RegExp(s.pattern), s.what).not.toThrow()
  })
})

describe("checkStaged", () => {
  it("passes a clean home", () => {
    const out = checkStaged(claude, "claude-code", staged({ "CLAUDE.md": "# hi", "agents/a.md": "x" }))
    expect(out).toEqual({ secrets: [], limits: [], notes: [] })
  })

  it("reports each file with a secret, skipping binary files", () => {
    const out = checkStaged(
      claude,
      "claude-code",
      staged({ "a.md": `key ${anthropicKey}`, "b.md": "fine", "img.png": new Uint8Array([0xff, 0xfe, 0x00]) }),
    )
    expect(out.secrets).toEqual([{ path: "a.md", line: 1, what: "una clave de Anthropic" }])
  })

  it("reports files over the size limit", () => {
    const out = checkStaged(claude, "claude-code", staged({ "big.bin": new Uint8Array(claude.max_file + 1) }))
    expect(out.limits).toEqual(["big.bin pasa de 5 MiB"])
  })

  it("reports too many files and too many bytes", () => {
    const many = Object.fromEntries(Array.from({ length: claude.max_files + 1 }, (_, i) => [`skills/f${i}`, ""]))
    expect(checkStaged(claude, "claude-code", staged(many)).limits).toEqual([`más de ${claude.max_files} archivos`])

    const chunk = new Uint8Array(claude.max_file)
    const total = Object.fromEntries(Array.from({ length: Math.floor(claude.max_bytes / claude.max_file) + 1 }, (_, i) => [`skills/c${i}`, chunk]))
    expect(checkStaged(claude, "claude-code", staged(total)).limits).toEqual(["más de 20 MiB en total"])
  })

  it("notes an rness init.lua that doesn't load the providers", () => {
    expect(checkStaged(rness, "rness", staged({ "init.lua": "print('hi')" })).notes).toHaveLength(1)
    expect(checkStaged(rness, "rness", staged({ "init.lua": 'require("providers")\n' })).notes).toEqual([])
    expect(checkStaged(rness, "rness", staged({ "init.lua": "require 'providers'" })).notes).toEqual([])
    expect(checkStaged(claude, "claude-code", staged({ "init.lua": "print('hi')" })).notes).toEqual([])
  })
})
