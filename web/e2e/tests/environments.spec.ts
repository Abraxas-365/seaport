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
