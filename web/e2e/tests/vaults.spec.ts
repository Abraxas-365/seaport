import { test, expect, unique } from "../fixtures"

test("a vault is created, gets an entry with a second version, and is granted to the echo agent", async ({ ctx, pages, page }) => {
  await pages.vaults.open()
  const name = unique("company")
  await pages.vaults.create(name, "Company")
  // Creating one opens its page.
  await expect(page.getByRole("heading", { level: 1, name: "Company" })).toBeVisible()
  await expect(page.getByText(name, { exact: true })).toBeVisible()

  // The first entry; it opens selected, at v1.
  await pages.vaults.createEntry("support-hours", "# Support hours\n\nMonday to Friday, 9 to 18.")
  await expect(pages.vaults.entry("support-hours")).toBeVisible()
  await expect(page.getByText("v1", { exact: true })).toBeVisible()

  // Editing saves a new version; the list of versions shows both.
  await page.getByRole("button", { name: "Editar support-hours" }).click()
  const box = page.getByLabel("Contenido de support-hours")
  await box.fill("# Support hours\n\nMonday to Friday, 9 to 17.")
  await page.getByRole("button", { name: "Guardar" }).click()
  await expect(page.getByText("v2", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Versiones" }).click()
  await expect(page.getByText("actual")).toBeVisible()
  await expect(page.getByRole("button", { name: "Revertir a esta versión" })).toBeVisible()

  // Granting the vault to the echo agent, in write mode.
  await page.getByRole("tab", { name: "Accesos" }).click()
  await expect(page.getByText("Ningún agente la tiene concedida directamente.")).toBeVisible()
  await page.getByRole("button", { name: "Dar acceso" }).click()
  const dialog = page.getByRole("dialog", { name: "Dar acceso a una bóveda" })
  await dialog.getByRole("combobox").click()
  await page.getByRole("option", { name: new RegExp(ctx.agent.name) }).click()
  await dialog.getByRole("radio", { name: "Escritura" }).click()
  await dialog.getByRole("button", { name: "Dar acceso" }).click()
  const row = page.getByRole("listitem").filter({ hasText: ctx.agent.name })
  await expect(row).toBeVisible()
  await expect(row.getByText("Escritura")).toBeVisible()

  // Granting again changes the mode.
  await row.getByRole("button", { name: "Pasar a lectura" }).click()
  await expect(row.getByText("Lectura")).toBeVisible()

  // The list counts its entry.
  await pages.vaults.open()
  await expect(pages.vaults.card(name)).toContainText("1 entrada")
})
