import { describe, expect, it } from "vitest"

import { envProblem, envText, parseEnv, specSecrets } from "./environment-secrets"
import type { EnvSpec } from "@/lib/types"

describe("parseEnv / envText", () => {
  it("reads NAME=value per line, trimming and skipping blanks", () => {
    expect(parseEnv("TZ=UTC\n\n  NODE_ENV = production \nX=a=b")).toEqual({
      TZ: "UTC",
      NODE_ENV: "production",
      X: "a=b",
    })
  })

  it("keeps a name without = as an empty value, so the field reports it", () => {
    expect(parseEnv("FLAG")).toEqual({ FLAG: "" })
    expect(envProblem({ FLAG: "" })).toMatch(/FLAG no tiene valor/)
  })

  it("round-trips", () => {
    const env = { TZ: "UTC", FEATURE: "on" }
    expect(parseEnv(envText(env))).toEqual(env)
  })
})

describe("envProblem", () => {
  it("accepts plain variables", () => {
    expect(envProblem({ TZ: "UTC", NODE_ENV: "production" })).toBe("")
  })

  it("refuses bad names, the port's own variables and SEAPORT_*", () => {
    expect(envProblem({ lower: "x" })).toMatch(/lower/)
    expect(envProblem({ SEAPORT_X: "x" })).toMatch(/SEAPORT_/)
    expect(envProblem({ ANTHROPIC_API_KEY: "x" })).toMatch(/la pone el puerto/)
    expect(envProblem({ HTTPS_PROXY: "x" })).toMatch(/la pone el puerto/)
  })
})

describe("specSecrets", () => {
  it("names the model keys and repo credentials the spec refers to", () => {
    const spec = {
      models: { anthropic: "${ANTHROPIC_KEY}", openai: "${OPENAI_KEY}" },
      repos: [{ url: "https://example.com/org/repo.git", credential: "${GIT_TOKEN}" }, { url: "https://example.com/pub.git", credential: "" }],
    } as unknown as EnvSpec
    const named = specSecrets(spec)
    expect([...named.keys()].sort()).toEqual(["ANTHROPIC_KEY", "GIT_TOKEN", "OPENAI_KEY"])
    expect(named.get("GIT_TOKEN")).toEqual(["repo https://example.com/org/repo.git"])
    expect(named.get("OPENAI_KEY")).toEqual(["modelo openai"])
  })
})
