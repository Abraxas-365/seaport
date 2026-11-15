import { test, expect } from "../fixtures"

// These start signed out: the worker's admin session is not used here.
test.use({ storageState: { cookies: [], origins: [] } })

test("a wrong token is refused with a message", async ({ page }) => {
  await page.goto("/")
  await page.getByLabel("Token de administrador").fill("not-the-admin-token")
  await page.getByRole("button", { name: "Entrar" }).click()
  await expect(page.getByText("Ese token no es el del puerto.")).toBeVisible()
  await expect(page.getByRole("link", { name: "Muelles" })).toHaveCount(0)
})

test("the right token lands on the docks", async ({ page, ctx }) => {
  await page.goto("/")
  await page.getByLabel("Token de administrador").fill(ctx.token)
  await page.getByRole("button", { name: "Entrar" }).click()
  await expect(page.getByRole("link", { name: "Muelles" })).toBeVisible()
  // The session survives a reload: a cookie, not page state.
  await page.reload()
  await expect(page.getByRole("link", { name: "Muelles" })).toBeVisible()
  await expect(page.getByLabel("Token de administrador")).toHaveCount(0)
})
