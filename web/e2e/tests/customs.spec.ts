import { test, expect, unique } from "../fixtures"

test("a new chat waits in customs; allowing it releases and answers the held message", async ({ api, pages, page }) => {
  // The dock default is "ask": new chats are pending.
  const dock = await api.dock("api", unique("gate"))
  const m = await api.inject(dock.id, "ana-4", "let me in")

  await pages.customs.open()
  await expect(page.getByText("Esperando decisión")).toBeVisible()
  await expect(page.getByText("Chat ana-4").first()).toBeVisible()

  await pages.conversation.open(m.conversation)
  await expect(page.getByText("En aduana.")).toBeVisible()
  await expect(pages.conversation.bubble("let me in", "in")).toBeVisible()
  await expect(page.getByText("Permite este chat para poder responder.")).toBeVisible()

  await page.getByRole("button", { name: "Permitir" }).click()
  await expect(pages.conversation.bubble("echo: let me in", "out")).toBeVisible()
  await expect(page.getByText("En aduana.")).toHaveCount(0)
})

test("denying a pending chat blocks it", async ({ api, pages, page }) => {
  const dock = await api.dock("api", unique("gate"))
  const m = await api.inject(dock.id, "ana-5", "spam spam")
  await pages.conversation.open(m.conversation)
  await expect(page.getByText("En aduana.")).toBeVisible()

  await page.getByRole("button", { name: "Bloquear" }).click()
  await expect(page.getByText("Este chat está bloqueado; no se le puede escribir.")).toBeVisible()
  await expect(page.getByText("En aduana.")).toHaveCount(0)
  await expect(pages.conversation.bubble("echo: spam spam", "out")).toHaveCount(0)
})
