import { test, expect, unique } from "../fixtures"

test("an api dock made from the UI shows in the list and opens", async ({ pages, page }) => {
  const name = unique("form")
  await pages.docks.open()
  await pages.docks.create("API", name)
  // Creating an api dock opens it.
  await expect(pages.dock.title(name)).toBeVisible()
  await expect(page.getByText("Conectado", { exact: true }).first()).toBeVisible()
  await expect(page.getByText("Recibir por HTTP")).toBeVisible()

  await pages.docks.open()
  await expect(pages.docks.card(name)).toBeVisible()
  await pages.docks.card(name).click()
  await expect(pages.dock.title(name)).toBeVisible()
})

test("a webhook dock only triggers: its conversation composer is disabled", async ({ pages, page, api }) => {
  const name = unique("alerts")
  await pages.docks.open()
  await pages.docks.create("Webhook", name)
  await expect(pages.dock.title(name)).toBeVisible()
  await expect(page.getByText("Conectado", { exact: true }).first()).toBeVisible()

  // A webhook arrives (the dock's own proof: its secret as a bearer).
  const id = page.url().split("/docks/")[1]
  await api.allow(id)
  const rotated = await api.post<{ secret: string }>(`/api/v1/docks/${id}/webhook/secret`)
  const res = await page.request.post(`/hooks/${id}`, {
    headers: { Authorization: `Bearer ${rotated.secret}` },
    data: { event: "deploy", service: "web" },
  })
  expect(res.status()).toBe(202)

  await expect(pages.dock.conversationLink("webhook")).toBeVisible()
  await pages.dock.conversationLink("webhook").click()
  await expect(page).toHaveURL(/\/inbox\//)
  await expect(page.getByText("Este canal solo dispara: nada se publica aquí.")).toBeVisible()
  await expect(page.getByRole("button", { name: "Enviar" })).toHaveCount(0)
})
