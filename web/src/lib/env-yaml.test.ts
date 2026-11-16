import { describe, expect, it } from "vitest"

import { environmentYaml } from "./env-yaml"
import type { Environment } from "./types"
import { apiData } from "@/test/wire"

/**
 * The fixture's keys are sorted; the API sends the spec in Go's field order,
 * which is the order `env export` writes: rebuild it.
 */
const env = () => {
  const e = apiData<Environment>("environment")
  const s = e.spec
  e.spec = {
    description: s.description,
    harness: s.harness,
    image: s.image,
    packages: s.packages,
    setup: s.setup,
    repos: s.repos.map((r) => ({ url: r.url, ref: r.ref, path: r.path, credential: r.credential, on_start: r.on_start })),
    repo_config: s.repo_config,
    home: s.home,
    models: s.models,
    env: s.env,
    network: s.network,
    resources: s.resources,
    workspace: s.workspace,
    idle_stop: s.idle_stop,
    browser: s.browser,
  }
  return e
}

describe("environmentYaml", () => {
  it("writes the fixture environment as `seaport env export` does", () => {
    const e = env()
    expect(environmentYaml(e.name, e.spec)).toBe(`name: default
description: Go toolchain
harness: claude-code
packages:
  - git
  - make
setup: echo ready
repos:
  - url: https://example.com/org/repo.git
    ref: main
    path: repo
    credential: \${GIT_TOKEN}
    on_start: make deps
repo_config: ignore
models:
  anthropic: \${ANTHROPIC_KEY}
env:
  TZ: UTC
network:
  egress:
    - example.com
    - "*.example.org"
resources:
  size: M
workspace: persistent
idle_stop: 30m
browser:
  engine: chromium
`)
  })

  it("leaves empty values out, and custom resources spelled out", () => {
    const e = env()
    e.spec.description = ""
    e.spec.packages = []
    e.spec.repos = []
    e.spec.setup = ""
    e.spec.browser = { engine: "" }
    e.spec.resources = { cpus: 4, memory_mb: 8192 }
    const yaml = environmentYaml("small", e.spec)
    expect(yaml).not.toContain("description")
    expect(yaml).not.toContain("packages")
    expect(yaml).not.toContain("browser")
    expect(yaml).toContain("resources:\n  cpus: 4\n  memory_mb: 8192\n")
    expect(yaml.startsWith("name: small\n")).toBe(true)
  })

  it("writes multi-line setup as a block scalar and keeps its trailing newline rule", () => {
    const e = env()
    e.spec.setup = "set -e\napt-get update\n"
    expect(environmentYaml("x", e.spec)).toContain("setup: |\n  set -e\n  apt-get update\n")
    e.spec.setup = "set -e\nmake"
    expect(environmentYaml("x", e.spec)).toContain("setup: |-\n  set -e\n  make\n")
  })

  it("quotes what YAML would read as another type", () => {
    const e = env()
    e.spec.packages = ["yes", "123", "a: b", "tail:", "x #y", "1.5", "plain-name=1.2"]
    const yaml = environmentYaml("x", e.spec)
    for (const quoted of ['"yes"', '"123"', '"a: b"', '"tail:"', '"x #y"', '"1.5"'])
      expect(yaml).toContain(`  - ${quoted}\n`)
    expect(yaml).toContain("  - plain-name=1.2\n")
  })

  it("quotes egress wildcards, which YAML would read as aliases", () => {
    const e = env()
    e.spec.network = { egress: ["*", "*.example.com"] }
    const yaml = environmentYaml("x", e.spec)
    expect(yaml).toContain('    - "*"\n')
    expect(yaml).toContain('    - "*.example.com"\n')
  })

  it("does not mutate the spec it is given", () => {
    const e = env()
    const before = structuredClone(e.spec)
    environmentYaml(e.name, e.spec)
    expect(e.spec).toEqual(before)
  })
})
