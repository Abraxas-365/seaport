import { test, expect, unique, context } from "../fixtures"

test("an inbound message appears and the echo agent answers", async ({ api, pages }) => {
  const dock = await api.dock("api", unique("support"), { allow: true })
  const m = await api.inject(dock.id, "ana-1", "hello harbour")
  await pages.conversation.open(m.conversation)

  await expect(pages.conversation.bubble("hello harbour", "in")).toBeVisible()
  await expect(pages.conversation.bubble("echo: hello harbour", "out")).toBeVisible()
})

test("the tools an agent ran show as a turn summary", async ({ api, pages }) => {
  const dock = await api.dock("api", unique("tools"), { allow: true })
  const m = await api.inject(dock.id, "ana-2", "please tool: run it")
  await pages.conversation.open(m.conversation)

  await expect(pages.conversation.bubble("echo: please tool: run it", "out")).toBeVisible()
  const summary = pages.conversation.turnSummary()
  await expect(summary).toBeVisible()
  await expect(summary).toContainText("1 herramienta")
  await summary.getByRole("button").click()
  await expect(summary.getByText("Run echo tool")).toBeVisible()
})

test("a message written in the web composer goes out in the conversation", async ({ api, pages }) => {
  const dock = await api.dock("api", unique("composer"), { allow: true })
  const m = await api.inject(dock.id, "ana-3", "first")
  await pages.conversation.open(m.conversation)
  await expect(pages.conversation.bubble("echo: first", "out")).toBeVisible()

  await pages.conversation.send("a note from the desk")
  await expect(pages.conversation.bubble("a note from the desk", "out")).toBeVisible()
  await expect(pages.conversation.composer()).toHaveValue("")

  // The agent still answers what people write next.
  await api.inject(dock.id, "ana-3", "and then second")
  await expect(pages.conversation.bubble("echo: and then second", "out")).toBeVisible()
})

test("a message held because nobody works the dock is delivered once an agent is assigned from the conversation", async ({ api, page, pages }) => {
  const d = await api.post<{ id: string; name: string }>("/api/v1/docks", { channel: "api", name: unique("orphan") })
  await api.allow(d.id)
  const m = await api.inject(d.id, "ana-4", "anyone there?")
  await pages.conversation.open(m.conversation)

  await expect(pages.conversation.bubble("anyone there?", "in")).toBeVisible()
  await expect(page.getByText("ningún agente atiende este muelle")).toBeVisible()
  await expect(page.getByText(/le llega lo retenido, sin reenviarlo/)).toBeVisible()

  await page.getByRole("combobox", { name: "Agente para el muelle" }).click()
  await page.getByRole("option", { name: `@${context().agent.name}` }).click()
  await page.getByRole("button", { name: "Asignar" }).click()

  // No resend: the held message reaches the agent and the banner goes.
  await expect(pages.conversation.bubble("echo: anyone there?", "out")).toBeVisible()
  await expect(page.getByText(/sin reenviarlo/)).toHaveCount(0)
})
