import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

import { context, test, expect, unique } from "../fixtures"

test("an environment made from the UI shows as YAML", async ({ pages, page }) => {
  const name = unique("backend")
  await pages.environments.open()
  await pages.environments.create(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  await page.getByRole("button", { name: "Ver como YAML" }).click()
  const dialog = page.getByRole("dialog", { name: `${name}.yaml` })
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText(`name: ${name}`)
  await expect(dialog).toContainText("harness: rness")
})

test("an environment keeps plain variables in its spec and secrets apart, and names a model key", async ({
  pages,
  page,
  api,
}) => {
  const name = unique("vars")
  await pages.environments.open()
  await pages.environments.create(name, "En blanco")
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  // A secret: only its name comes back, never the value; the spec names it.
  await page.getByRole("button", { name: "Secreto", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "Nuevo secreto del entorno" })
  await dialog.getByLabel("Nombre").fill("OPENAI_KEY")
  await dialog.getByLabel("Valor").fill("s3cr3t-model-key-0001")
  await dialog.getByRole("button", { name: "Guardar" }).click()
  await expect(dialog).toHaveCount(0)
  const list = page.getByRole("list", { name: "Secretos del entorno" })
  await expect(list.getByText("OPENAI_KEY", { exact: true })).toBeVisible()
  await expect(page.getByText("s3cr3t-model-key-0001")).toHaveCount(0)

  // Plain variables are an advanced knob: folded while everything there is at its default.
  const advanced = page.getByRole("button", { name: "Avanzado", exact: false })
  await expect(advanced).toHaveAttribute("aria-expanded", "false")
  await advanced.click()
  // Plain variables go in the spec; a credential-looking name is refused before saving.
  const vars = page.getByLabel("Variables")
  await vars.fill("TZ=UTC\nANTHROPIC_API_KEY=x")
  await expect(page.getByText("ANTHROPIC_API_KEY la pone el puerto en cada instancia.")).toBeVisible()
  await expect(page.getByRole("button", { name: /^Guardar v\d+$/ })).toBeDisabled()
  await vars.fill("TZ=UTC\nFEATURE=on")
  // A model key is picked among the environment's secrets, not typed.
  await page.getByRole("combobox", { name: "OpenAI" }).click()
  await page.getByRole("option", { name: "OPENAI_KEY" }).click()
  await page.getByRole("button", { name: /^Guardar v\d+$/ }).click()
  await expect(page.getByRole("region", { name: "Cambios sin guardar" })).toHaveCount(0)
  await expect(list.getByText("en uso")).toBeVisible()

  // The named secret can't be deleted; the YAML carries variables and models, never values.
  await expect(list.getByRole("button", { name: "Eliminar OPENAI_KEY" })).toBeDisabled()
  await page.getByRole("button", { name: "Ver como YAML" }).click()
  const yaml = page.getByRole("dialog", { name: `${name}.yaml` })
  await expect(yaml).toContainText("env:")
  await expect(yaml).toContainText("TZ: UTC")
  await expect(yaml).toContainText("openai: ${OPENAI_KEY}")
  await expect(yaml).not.toContainText("s3cr3t-model-key-0001")
  await yaml.getByRole("button", { name: "Close" }).click()
  // With variables set, Avanzado opens by itself next time.
  await page.reload()
  await expect(page.getByRole("button", { name: "Avanzado", exact: false })).toHaveAttribute("aria-expanded", "true")
  await expect(page.getByLabel("Variables")).toHaveValue(/TZ=UTC/)

  const envs = await api.get<{ items: { id: string; name: string }[] }>("/api/v1/environments")
  const env = envs.items.find((e) => e.name === name)!
  const stored = JSON.stringify(await api.get(`/api/v1/environments/${env.id}/secrets`))
  expect(stored).toContain("OPENAI_KEY")
  expect(stored).not.toContain("s3cr3t-model-key-0001")
})

test("the checklist adds the model key in one step and hosts an agent", async ({ pages, page, api }) => {
  const name = unique("setup")
  await pages.environments.open()
  await pages.environments.create(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  // A new environment says what's left; the model key (rness) is a step to do.
  const setup = page.getByTestId("env-setup")
  await expect(setup.getByText("Para empezar")).toBeVisible()
  const steps = setup.getByRole("list", { name: "Pasos" })
  await expect(steps.getByText("Clave del modelo")).toBeVisible()
  await expect(steps.getByText("Ninguno todavía", { exact: false })).toBeVisible()

  // "Añadir clave" saves the secret and names it in a new version: one step.
  await steps.getByRole("button", { name: "Añadir clave de Anthropic" }).click()
  const key = page.getByRole("dialog", { name: "Clave de Anthropic" })
  await expect(key.getByLabel("Nombre")).toHaveValue("ANTHROPIC_KEY")
  await key.getByLabel("Valor").fill("s3cr3t-anthropic-0002")
  await key.getByRole("button", { name: "Guardar" }).click()
  await expect(key).toHaveCount(0)
  await expect(page.getByRole("combobox", { name: "Anthropic" })).toHaveText("ANTHROPIC_KEY")
  await expect(steps.getByText("Anthropic: ANTHROPIC_KEY")).toBeVisible()
  await expect(page.getByRole("region", { name: "Cambios sin guardar" })).toHaveCount(0)
  await expect(page.getByText("Entorno v2 guardado")).toBeVisible()

  // Hosting from here: the agent shows as hosted and the checklist is done.
  await steps.getByRole("button", { name: "Alojar agente" }).click()
  const host = page.getByRole("dialog", { name: `Alojar un agente en ${name}` })
  await host.getByRole("combobox", { name: "Agente" }).click()
  await page.getByRole("option", { name: context().agent.name }).click()
  await host.getByRole("button", { name: "Alojar" }).click()
  await expect(host).toHaveCount(0)
  await expect(setup.getByText("Listo para trabajar")).toBeVisible()
  await expect(page.getByText("Alojados aquí:")).toBeVisible()
  await expect(page.getByText(`@${context().agent.name}`)).toBeVisible()

  const envs = await api.get<{ items: { id: string; name: string }[] }>("/api/v1/environments")
  const env = envs.items.find((e) => e.name === name)!
  const hosted = await api.get<{ environment_id: string; slots: unknown[] }>(
    `/api/v1/agents/${context().agent.id}/hosting`
  )
  expect(hosted.environment_id).toBe(env.id)
  // Leave the shared echo agent unhosted for the other specs.
  await api.delete(`/api/v1/agents/${context().agent.id}/hosting`)
})

test("packages and egress hosts are typed one at a time as chips", async ({ pages, page, api }) => {
  const name = unique("chips")
  await pages.environments.open()
  await pages.environments.create(name, "Python")
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  // The template's packages come as chips; Enter adds, × removes, duplicates are dropped.
  const packages = page.getByRole("textbox", { name: "Paquetes (apt)" })
  await expect(page.getByRole("button", { name: "Quitar python3-pip" })).toBeVisible()
  await packages.fill("make")
  await packages.press("Enter")
  await packages.fill("python3")
  await packages.press("Enter")
  await expect(page.getByRole("button", { name: "Quitar make" })).toBeVisible()
  await expect(page.getByRole("button", { name: "Quitar python3", exact: true })).toHaveCount(1)
  await page.getByRole("button", { name: "Quitar python3-venv" }).click()

  // Egress hosts live under Avanzado, open because the template set some.
  await expect(page.getByRole("button", { name: "Avanzado", exact: false })).toHaveAttribute("aria-expanded", "true")
  const hosts = page.getByRole("textbox", { name: "Hosts permitidos" })
  await hosts.fill("Github.com")
  await hosts.press("Enter")
  await page.getByRole("button", { name: /^Guardar v\d+$/ }).click()
  await expect(page.getByRole("region", { name: "Cambios sin guardar" })).toHaveCount(0)

  const envs = await api.get<{
    items: { id: string; name: string; spec: { packages: string[]; network: { egress: string[] } } }[]
  }>("/api/v1/environments")
  const env = envs.items.find((e) => e.name === name)!
  expect(env.spec.packages).toEqual(["python3", "python3-pip", "make"])
  expect(env.spec.network.egress).toEqual(["pypi.org", "files.pythonhosted.org", "github.com"])
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

test("the home upload refuses a file with a secret and takes a clean folder", async ({ pages, page }) => {
  const name = unique("homes")
  await pages.environments.open()
  await pages.environments.create(name)
  await expect(page.getByText("Configuración del harness")).toBeVisible()
  const input = page.locator("input[type=file][webkitdirectory]")

  // The browser checks the server's own rules (GET /api/v1/homes/rules) first.
  await input.setInputFiles(
    // AWS's documented example key: it looks like a credential and is not one.
    folder({ "init.lua": 'aws_key = "AKIAIOSFODNN7EXAMPLE"\n' })
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
})
