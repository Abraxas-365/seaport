import { describe, expect, it } from "vitest"

import type { Build, Environment } from "@/lib/types"
import { apiData } from "@/test/wire"
import { readiness } from "./environments"

const base = apiData<Environment>("environment")
const env = (over: Partial<Environment> = {}, spec: Partial<Environment["spec"]> = {}): Environment => ({
  ...base,
  agents: 0,
  ...over,
  spec: { ...base.spec, harness: "rness", models: { anthropic: "${ANTHROPIC_KEY}" }, ...spec },
})
const build = (state: Build["state"]): Build => ({ ...apiData<Build>("build"), state })

describe("readiness", () => {
  it("asks for the model key first", () => {
    expect(readiness(env({}, { models: {} }), [], null, true).label).toBe("Falta la clave del modelo")
    expect(readiness(env({}, { models: {} }), [], null, true).tone).toBe("warning")
  })

  it("lets a Claude Code environment sign in instead", () => {
    expect(readiness(env({}, { harness: "claude-code", models: {} }), [], null, true).label).toBe("Sin agentes")
  })

  it("names a secret the spec references but the environment lacks", () => {
    expect(readiness(env(), [], null, true).label).toBe("Falta el secreto ANTHROPIC_KEY")
    expect(readiness(env(), undefined, null, true).label).toBe("Sin agentes")
  })

  it("then counts agents, with the image's state when the runtime builds", () => {
    const secrets = ["ANTHROPIC_KEY"]
    expect(readiness(env(), secrets, null, true).label).toBe("Sin agentes")
    expect(readiness(env({ agents: 2 }), secrets, null, false)).toEqual({ tone: "success", label: "2 agentes" })
    expect(readiness(env({ agents: 1 }), secrets, null, true).label).toBe("Sin construir")
    expect(readiness(env({ agents: 1 }), secrets, build("failed"), true).tone).toBe("danger")
    expect(readiness(env({ agents: 1 }), secrets, build("ready"), true).label).toBe("1 agente")
  })
})
