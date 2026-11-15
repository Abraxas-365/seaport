import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

import { test, expect, unique } from "../fixtures"

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

test("an environment keeps plain variables in its spec and secrets apart, and names a model key", async ({ pages, page, api }) => {
  const name = unique("vars")
  await pages.environments.open()
  await pages.environments.create(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()

  // A secret: only its name comes back, never the value; the spec names it.
  await page.getByRole("button", { name: "Secreto" }).click()
  const dialog = page.getByRole("dialog", { name: "Nuevo secreto del entorno" })
  await dialog.getByLabel("Nombre").fill("OPENAI_KEY")
  await dialog.getByLabel("Valor").fill("s3cr3t-model-key-0001")
  await dialog.getByRole("button", { name: "Guardar" }).click()
  await expect(dialog).toHaveCount(0)
  const list = page.getByRole("list", { name: "Secretos del entorno" })
  await expect(list.getByText("OPENAI_KEY", { exact: true })).toBeVisible()
  await expect(page.getByText("s3cr3t-model-key-0001")).toHaveCount(0)

  // Plain variables go in the spec; a credential-looking name is refused before saving.
  const vars = page.getByLabel("Variables")
  await vars.fill("TZ=UTC\nANTHROPIC_API_KEY=x")
  await expect(page.getByText("ANTHROPIC_API_KEY la pone el puerto en cada instancia.")).toBeVisible()
  await expect(page.getByRole("button", { name: /^Guardar v\d+$/ })).toBeDisabled()
  await vars.fill("TZ=UTC\nFEATURE=on")
  await page.getByRole("textbox", { name: "OpenAI" }).fill("${OPENAI_KEY}")
  await page.getByRole("button", { name: /^Guardar v\d+$/ }).click()
  await expect(page.getByRole("button", { name: /^Guardar v\d+$/ })).toBeDisabled()
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

  const envs = await api.get<{ items: { id: string; name: string }[] }>("/api/v1/environments")
  const env = envs.items.find((e) => e.name === name)!
  const stored = JSON.stringify(await api.get(`/api/v1/environments/${env.id}/secrets`))
  expect(stored).toContain("OPENAI_KEY")
  expect(stored).not.toContain("s3cr3t-model-key-0001")
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
})
