import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

import { test, expect, unique } from "../fixtures"

type Config = {
  version: number
  harness: string
  machine: {
    packages: string[]
    models: Record<string, string>
    env: Record<string, string>
    network: { egress: string[] }
  } | null
}

const config = (api: { get<T>(path: string): Promise<T> }, id: string) => api.get<Config>(`/api/v1/agents/${id}/config`)

/** The id of the agent the wizard just opened (/agents/{id}#…). */
const agentId = (url: string) => new URL(url).pathname.split("/")[2]

test("a remote agent made from the wizard has only its harness; clone copies it", async ({ pages, page, api }) => {
  const name = unique("remote").slice(0, 32)
  await pages.agents.list()
  await pages.agents.create(name, { where: "remote", harness: "Claude Code" })
  const id = agentId(page.url())
  const saved = await config(api, id)
  expect(saved.version).toBe(1)
  expect(saved.harness).toBe("claude-code")
  expect(saved.machine).toBeNull()
  await expect(page.getByText("Remoto").first()).toBeVisible()

  // Clonar: a new agent with the same config, opened.
  await page.getByRole("button", { name: "Clonar" }).click()
  const dialog = page.getByRole("dialog", { name: `Clonar @${name}` })
  const copy = unique("copy")
  await dialog.getByLabel("Nombre").fill(copy)
  await dialog.getByRole("button", { name: "Clonar" }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByRole("heading", { level: 1, name: `@${copy}` })).toBeVisible()
  expect((await config(api, agentId(page.url()))).harness).toBe("claude-code")

  // Both on the list, as remote agents.
  await pages.agents.list()
  await expect(pages.agents.card(name).getByText("Remoto")).toBeVisible()
  await expect(pages.agents.card(copy).getByText("Remoto")).toBeVisible()
})

test("a hosted agent made from the wizard takes its model key, names it in its machine, and is hosted", async ({
  pages,
  page,
  api,
}) => {
  const name = unique("hosted").slice(0, 32)
  await pages.agents.list()
  await pages.agents.create(name, { where: "hosted", harness: "rness", key: "s3cr3t-model-key-0002" })
  const id = agentId(page.url())
  await expect(page.getByText("s3cr3t-model-key-0002")).toHaveCount(0)
  await expect(page.getByText(/^Alojado · 1 instancia/).first()).toBeVisible()

  const saved = await config(api, id)
  expect(saved.harness).toBe("rness")
  expect(saved.machine?.models).toEqual({ anthropic: "${ANTHROPIC_KEY}" })
  const secrets = JSON.stringify(await api.get(`/api/v1/agents/${id}/secrets`))
  expect(secrets).toContain("ANTHROPIC_KEY")
  expect(secrets).not.toContain("s3cr3t-model-key-0002")
  const hosted = await api.get<{ slots: unknown[] }>(`/api/v1/agents/${id}/hosting`)
  expect(hosted.slots).toHaveLength(1)

  // The key is in use: it can't be deleted while the machine names it.
  await page.getByRole("tab", { name: "Secretos" }).click()
  const list = page.getByRole("list", { name: "Secretos" })
  await expect(list.getByText("modelo anthropic")).toBeVisible()
  await expect(list.getByRole("button", { name: "Eliminar ANTHROPIC_KEY" })).toBeDisabled()

  // A hosted agent can't drop its machine: the editor offers no switch.
  await page.getByRole("tab", { name: "Configuración" }).click()
  const claude = page.getByRole("radiogroup", { name: "Acceso a Claude" })
  await expect(claude.getByRole("radio", { name: /Clave de API/ })).toHaveAttribute("aria-checked", "true")
  await expect(page.getByRole("combobox", { name: "Anthropic" })).toHaveText("ANTHROPIC_KEY")
  await expect(page.getByRole("switch", { name: "Máquina" })).toHaveCount(0)

  // Both: Claude through each instance's account, OpenAI with the key.
  await page.getByRole("combobox", { name: "OpenAI" }).click()
  await page.getByRole("option", { name: "ANTHROPIC_KEY" }).click()
  await claude.getByRole("radio", { name: /Cuenta de Claude/ }).click()
  await expect(page.getByRole("list", { name: "Cuentas de las instancias" })).toBeVisible()
  await page.getByRole("button", { name: /Guardar v/ }).click()
  await expect.poll(async () => (await config(api, id)).machine?.models).toEqual({
    anthropic: "subscription",
    openai: "${ANTHROPIC_KEY}",
  })

  // Back to remote (no slot is running here: the UI port has no runtime).
  await page.getByRole("tab", { name: "Dónde corre" }).click()
  await page.getByRole("button", { name: "Pasar a remoto" }).click()
  await page.getByRole("alertdialog").getByRole("button", { name: "Dejar de alojar" }).click()
  await expect(page.getByRole("button", { name: "Alojar", exact: true })).toBeVisible()
  await expect(page.getByText("Remoto").first()).toBeVisible()
})

test("a hosted agent signs in to a Claude account when made without a key", async ({ pages, page, api }) => {
  const name = unique("account").slice(0, 32)
  await pages.agents.list()
  await pages.agents.create(name, { where: "hosted", harness: "Claude Code" })
  const saved = await config(api, agentId(page.url()))
  expect(saved.harness).toBe("claude-code")
  expect(saved.machine?.models).toEqual({ anthropic: "subscription" })
  await page.getByRole("tab", { name: "Configuración" }).click()
  await expect(
    page.getByRole("radiogroup", { name: "Acceso a Claude" }).getByRole("radio", { name: /Cuenta de Claude/ }),
  ).toHaveAttribute("aria-checked", "true")
  await api.delete(`/api/v1/agents/${agentId(page.url())}/hosting`)
})

test("the machine keeps variables, packages and egress hosts; the YAML shows them, never a secret's value", async ({
  pages,
  page,
  api,
}) => {
  const name = unique("machine").slice(0, 32)
  await pages.agents.list()
  await pages.agents.create(name, { where: "hosted", harness: "rness", key: "s3cr3t-model-key-0001" })
  const id = agentId(page.url())
  await api.delete(`/api/v1/agents/${id}/hosting`)
  await page.getByRole("tab", { name: "Configuración" }).click()

  // Packages are chips: Enter adds, duplicates are dropped, × removes.
  const packages = page.getByRole("textbox", { name: "Paquetes (apt)" })
  for (const p of ["make", "git", "make"]) {
    await packages.fill(p)
    await packages.press("Enter")
  }
  await expect(page.getByRole("button", { name: "Quitar make" })).toHaveCount(1)
  await page.getByRole("button", { name: "Quitar git" }).click()

  // Advanced knobs are folded while at their defaults.
  const advanced = page.getByRole("button", { name: "Avanzado", exact: false })
  await expect(advanced).toHaveAttribute("aria-expanded", "false")
  await advanced.click()
  const hosts = page.getByRole("textbox", { name: "Hosts permitidos" })
  await hosts.fill("Github.com")
  await hosts.press("Enter")
  // A credential-looking variable is refused before saving.
  const vars = page.getByLabel("Variables", { exact: true })
  await vars.fill("TZ=UTC\nANTHROPIC_API_KEY=x")
  // Under the field and next to the save button.
  await expect(page.getByText("ANTHROPIC_API_KEY la pone el puerto en cada instancia.")).toHaveCount(2)
  await expect(pages.agents.save()).toBeDisabled()
  await vars.fill("TZ=UTC")
  await pages.agents.save().click()
  await expect(pages.agents.save()).toBeDisabled()

  const saved = await config(api, id)
  expect(saved.machine?.packages).toEqual(["make"])
  expect(saved.machine?.network.egress).toEqual(["github.com"])
  expect(saved.machine?.env).toEqual({ TZ: "UTC" })

  await page.getByRole("button", { name: "Ver como YAML" }).click()
  const yaml = page.getByRole("dialog", { name: `${name}.yaml` })
  await expect(yaml).toContainText("harness: rness")
  await expect(yaml).toContainText("TZ: UTC")
  await expect(yaml).toContainText("anthropic: ${ANTHROPIC_KEY}")
  await expect(yaml).not.toContainText("s3cr3t-model-key-0001")
  await yaml.getByRole("button", { name: "Close" }).click()

  // With something set there, Avanzado opens by itself next time.
  await page.reload()
  await expect(page.getByRole("button", { name: "Avanzado", exact: false })).toHaveAttribute("aria-expanded", "true")
  await expect(page.getByLabel("Variables", { exact: true })).toHaveValue(/TZ=UTC/)
})

/** A folder on disk: a directory input takes a path, not buffers. */
function folder(files: Record<string, string>): string {
  const dir = mkdtempSync(join(tmpdir(), "seaport-home-"))
  for (const [name, body] of Object.entries(files)) {
    mkdirSync(join(dir, name, ".."), { recursive: true })
    writeFileSync(join(dir, name), body)
  }
  return dir
}

test("the home upload refuses a file with a secret and takes a clean folder", async ({ pages, page, api }) => {
  const name = unique("homes").slice(0, 32)
  await pages.agents.list()
  await pages.agents.create(name, { where: "hosted", harness: "rness" })
  await api.delete(`/api/v1/agents/${agentId(page.url())}/hosting`)
  await page.getByRole("tab", { name: "Configuración" }).click()
  await expect(page.getByText("Archivos del harness")).toBeVisible()
  const input = page.locator("input[type=file][webkitdirectory]")

  // The browser checks the server's own rules (GET /api/v1/homes/rules) first.
  await input.setInputFiles(
    // AWS's documented example key: it looks like a credential and is not one.
    folder({ "init.lua": 'aws_key = "AKIAIOSFODNN7EXAMPLE"\n' }),
  )
  const refused = page.getByRole("dialog", { name: "Configuración de rness" })
  await expect(refused).toBeVisible()
  await expect(refused.getByText("Hay secretos: no se puede subir.")).toBeVisible()
  await expect(refused.getByText(/init\.lua.*línea 1 parece una clave de AWS/)).toBeVisible()
  await expect(refused.getByRole("button", { name: "Subir y usar" })).toBeDisabled()
  await refused.getByRole("button", { name: "Cancelar" }).click()
  await expect(refused).toHaveCount(0)

  await input.setInputFiles(folder({ "init.lua": 'require("seaport")\n', "hooks.json": "{}\n" }))
  const clean = page.getByRole("dialog", { name: "Configuración de rness" })
  await expect(clean).toBeVisible()
  await expect(clean.getByText("2 archivos")).toBeVisible()
  await expect(clean.getByText("Hay secretos: no se puede subir.")).toHaveCount(0)
  await clean.getByRole("button", { name: "Subir y usar" }).click()

  // The uploaded home is the draft's: listed, ready to save with the version.
  await expect(clean).toHaveCount(0)
  await expect(page.getByText("2 archivos · ")).toBeVisible()
  await expect(page.getByText("sha256:").first()).toBeVisible()
  await pages.agents.save().click()
  await expect(pages.agents.save()).toBeDisabled()
})
