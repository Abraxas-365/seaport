import { describe, expect, it } from "vitest"

import { configYaml } from "./config-yaml"
import { emptyConfig, newMachine } from "./machine"

describe("configYaml", () => {
  it("writes harness, profile and machine in the CLI's order, leaving out empty values", () => {
    const yaml = configYaml({
      ...emptyConfig,
      instructions: "Answer support.\nBe brief.",
      model: "claude-sonnet-5",
      browser: true,
      harness: "rness",
      machine: newMachine({
        packages: ["git"],
        models: { openai: "${OPENAI_KEY}" },
        env: { TZ: "UTC" },
        network: { egress: ["*.example.org"] },
        resources: { size: "M", cpus: 2, memory_mb: 4096 },
      }),
    })
    expect(yaml).toBe(`harness: rness
instructions: |-
  Answer support.
  Be brief.
model: claude-sonnet-5
browser: true
browser_detect: false
machine:
  packages:
    - git
  repo_config: trust
  models:
    openai: \${OPENAI_KEY}
  env:
    TZ: UTC
  network:
    egress:
      - "*.example.org"
  resources:
    size: M
  workspace: persistent
  idle_stop: 30m
`)
  })

  it("has no machine key for a remote agent", () => {
    expect(configYaml({ ...emptyConfig, harness: "claude-code" })).not.toContain("machine:")
  })
})
