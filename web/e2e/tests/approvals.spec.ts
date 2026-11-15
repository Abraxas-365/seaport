import { test, expect, unique } from "../fixtures"

test("an agent's question renders as a card; answering it wakes the agent", async ({ api, pages }) => {
  const dock = await api.dock("api", unique("approve"), { allow: true })
  const m = await api.inject(dock.id, "ana-6", "ask: ¿Desplegar?|Sí|No")
  await pages.conversation.open(m.conversation)

  const card = pages.conversation.approvalCard()
  await expect(card).toBeVisible()
  await expect(card.getByText("Esperando respuesta")).toBeVisible()
  await expect(card.getByRole("button", { name: "Sí" })).toBeVisible()
  await expect(card.getByRole("button", { name: "No" })).toBeVisible()

  await card.getByRole("button", { name: "Sí" }).click()
  await expect(card.getByText("Respondida")).toBeVisible()
  await expect(card.getByText("Sí", { exact: true })).toBeVisible()

  // The port tells the agent (a system message); the echo agent answers it.
  await expect(pages.conversation.bubble(/answered "Sí"/, "in").first()).toBeVisible()
  await expect(pages.conversation.bubble(/echo: .*answered "Sí"/, "out")).toBeVisible()
})
