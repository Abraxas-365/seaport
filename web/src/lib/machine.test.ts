import { describe, expect, it } from "vitest"

import {
  agentNamePattern,
  claudeAccess,
  cleanMachine,
  configSecrets,
  emptyConfig,
  fullMachine,
  machineProblem,
  modelsProblem,
  newMachine,
  parseEnv,
  envProblem,
  repoName,
  secretNameOk,
  secretRefName,
  signsIn,
} from "./machine"
import type { Machine } from "./types"

describe("secret references", () => {
  it("reads ${NAME} and refuses anything else", () => {
    expect(secretRefName("${GIT_TOKEN}")).toBe("GIT_TOKEN")
    expect(secretRefName("GIT_TOKEN")).toBe("")
    expect(secretRefName("Bearer ${GIT_TOKEN}")).toBe("")
  })

  it("keeps SEAPORT_ names for the port", () => {
    expect(secretNameOk("LINEAR_TOKEN")).toBe(true)
    expect(secretNameOk("SEAPORT_X")).toBe(false)
    expect(secretNameOk("lower")).toBe(false)
  })
})

describe("agent names", () => {
  it("follow agent.ValidName", () => {
    expect(agentNamePattern.test("pm")).toBe(true)
    expect(agentNamePattern.test("pm-copia_2")).toBe(true)
    expect(agentNamePattern.test("-pm")).toBe(false)
    expect(agentNamePattern.test("a".repeat(33))).toBe(false)
  })
})

describe("model access", () => {
  it("signs in like hosting.Normalize decides", () => {
    expect(signsIn("claude-code", {})).toBe(true)
    expect(signsIn("claude-code", { anthropic: "${ANTHROPIC_KEY}" })).toBe(false)
    expect(signsIn("claude-code", { openai: "${OPENAI_KEY}" })).toBe(true)
    expect(signsIn("rness", {})).toBe(true)
    expect(signsIn("rness", { openai: "${OPENAI_KEY}" })).toBe(false)
    expect(signsIn("rness", { anthropic: "subscription" })).toBe(true)
    expect(signsIn("rness", { anthropic: "subscription", groq: "${GROQ_KEY}" })).toBe(true)
  })

  it("reads how the slots reach Claude", () => {
    expect(claudeAccess("claude-code", {})).toBe("account")
    expect(claudeAccess("claude-code", { anthropic: "${ANTHROPIC_KEY}" })).toBe("key")
    expect(claudeAccess("rness", {})).toBe("account")
    expect(claudeAccess("rness", { openai: "${OPENAI_KEY}" })).toBe("none")
    expect(claudeAccess("rness", { anthropic: "subscription", openai: "${OPENAI_KEY}" })).toBe("account")
  })

  it("takes a Claude account with keys, and refuses literals", () => {
    expect(modelsProblem("rness", { anthropic: "subscription", openai: "${OPENAI_KEY}" })).toBe("")
    expect(modelsProblem("claude-code", { anthropic: "subscription", openai: "${OPENAI_KEY}" })).toBe("")
    expect(modelsProblem("rness", { openai: "sk-literal" })).toMatch(/secreto/)
    expect(modelsProblem("rness", { openai: "subscription" })).toMatch(/Solo Anthropic/)
  })
})

describe("machine", () => {
  it("fills what the server answers as null", () => {
    const m = fullMachine({ ...newMachine(), packages: null, repos: null, models: null } as unknown as Machine)
    expect(m.packages).toEqual([])
    expect(m.repos).toEqual([])
    expect(m.models).toEqual({})
  })

  it("is cleaned of blanks and duplicates before saving", () => {
    const m = cleanMachine(
      newMachine({
        packages: ["nodejs", " nodejs ", ""],
        models: { anthropic: "", openai: "${OPENAI_KEY}" },
        network: { egress: ["API.example.com", "api.example.com"] },
        idle_stop: " 2h ",
      }),
    )
    expect(m.packages).toEqual(["nodejs"])
    expect(m.models).toEqual({ openai: "${OPENAI_KEY}" })
    expect(m.network.egress).toEqual(["api.example.com"])
    expect(m.idle_stop).toBe("2h")
  })

  it("names its problems before saving", () => {
    expect(machineProblem("", newMachine())).toMatch(/harness/)
    expect(machineProblem("rness", newMachine({ idle_stop: "soon" }))).toMatch(/30m/)
    expect(
      machineProblem("rness", newMachine({ repos: [{ url: "git@example.com:a/b", ref: "", path: "", credential: "", on_start: "" }] })),
    ).toMatch(/https/)
    expect(machineProblem("rness", newMachine())).toBe("")
  })

  it("parses env lines and refuses the slot's own variables", () => {
    expect(parseEnv("A=1\n\nB = two\nC\n")).toEqual({ A: "1", B: "two", C: "" })
    expect(envProblem({ C: "" })).toMatch(/no tiene valor/)
    expect(envProblem({ HTTPS_PROXY: "x" })).not.toBe("")
    expect(envProblem({ LOG_LEVEL: "debug" })).toBe("")
  })

  it("names a repo's folder", () => {
    expect(repoName("https://github.com/acme/app.git")).toBe("app")
    expect(repoName("https://github.com/acme/app/")).toBe("app")
  })
})

describe("configSecrets", () => {
  it("lists what names each secret: headers, model keys, repo credentials", () => {
    const used = configSecrets({
      ...emptyConfig,
      mcp_servers: [{ name: "linear", url: "https://mcp.example.com", headers: [{ name: "Authorization", value: "Bearer ${LINEAR}" }] }],
      machine: newMachine({
        models: { openai: "${OPENAI_KEY}", anthropic: "subscription" },
        repos: [{ url: "https://github.com/acme/app", ref: "", path: "", credential: "${GIT}", on_start: "" }],
      }),
    })
    expect([...used.keys()].sort()).toEqual(["GIT", "LINEAR", "OPENAI_KEY"])
    expect(used.get("LINEAR")?.[0]).toMatch(/linear/)
  })
})
