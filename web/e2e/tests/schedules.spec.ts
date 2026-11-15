import { test, expect, unique } from "../fixtures"

test("a schedule made from the conversation shows a cron preview, is listed and can be disabled", async ({ api, pages, page }) => {
  const dock = await api.dock("api", unique("sched"), { allow: true })
  const m = await api.inject(dock.id, "ana-7", "hi")
  await pages.conversation.open(m.conversation)
  await expect(pages.conversation.bubble("echo: hi", "out")).toBeVisible()

  const details = await pages.conversation.openDetails()
  await expect(details.getByText("Programaciones")).toBeVisible()
  await details.getByRole("button", { name: "Nueva" }).click()

  const dialog = page.getByRole("dialog", { name: "Nueva programación" })
  const name = unique("standup")
  await dialog.getByLabel("Nombre").fill(name)
  await dialog.getByLabel("Cuándo").fill("*/10 * * * *")
  // The server computes the next three times; they are listed under the field.
  await expect(dialog.locator("ul li")).toHaveCount(3)
  await dialog.getByLabel("Mensaje").fill("Daily summary please")
  await dialog.getByRole("button", { name: "Crear" }).click()

  const row = page.locator('[data-slot="schedule-row"]').filter({ hasText: name })
  await expect(row).toBeVisible()
  await expect(row.getByText("Próxima")).toBeVisible()

  await row.getByRole("switch", { name: "Desactivar programación" }).click()
  await expect(row.getByText("Desactivada")).toBeVisible()
})
