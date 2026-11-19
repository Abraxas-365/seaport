import { writeFileSync } from "node:fs"

import { test, expect, unique } from "../fixtures"

test("the config editor saves instructions and a secret, never showing its value back", async ({ ctx, pages, page, api }) => {
  await pages.agents.open(ctx.agent.id, "configuracion")
  await expect(page.getByRole("heading", { level: 1, name: `@${ctx.agent.name}` })).toBeVisible()

  const instructions = `You are the echo agent (${unique("persona")}).`
  const box = page.getByLabel("Instrucciones").first()
  await box.fill(instructions)
  await pages.agents.save().click()
  await expect(pages.agents.save()).toBeDisabled()

  // A secret: only its name comes back, never the value.
  const secret = "REGION_TOKEN"
  await page.getByRole("tab", { name: "Secretos" }).click()
  await page.getByRole("button", { name: "Secreto", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "Nuevo secreto" })
  await dialog.getByLabel("Nombre").fill(secret)
  await dialog.getByLabel("Valor").fill("s3cr3t-value-0001")
  await dialog.getByRole("button", { name: "Guardar" }).click()
  await expect(dialog).toHaveCount(0)
  const list = page.getByRole("list", { name: "Secretos" })
  await expect(list.getByText(secret, { exact: true })).toBeVisible()
  await expect(page.getByText("s3cr3t-value-0001")).toHaveCount(0)

  // After a reload the instructions are the saved ones and the value is nowhere.
  await pages.agents.open(ctx.agent.id, "configuracion")
  await page.reload()
  await expect(page.getByLabel("Instrucciones").first()).toHaveValue(instructions)
  await expect(page.getByText("s3cr3t-value-0001")).toHaveCount(0)
  const stored = JSON.stringify(await api.get(`/api/v1/agents/${ctx.agent.id}/secrets`))
  expect(stored).toContain(secret)
  expect(stored).not.toContain("s3cr3t-value-0001")
})

test("the agent shows its connection, outdated after a config change until it restarts", async ({ ctx, pages, page, api }) => {
  await pages.agents.open(ctx.agent.id)
  const card = page.getByTestId("agent-card")
  await expect(card).toBeVisible()
  await expect(card).toContainText("conectado")
  // Earlier specs changed the config: wait for the restart they caused.
  await expect(card.getByText("desactualizada")).toHaveCount(0)

  // Keep the agent busy: a restart waits for the work in flight.
  const dock = await api.dock("api", unique("busy"), { allow: true })
  await api.inject(dock.id, "ana-8", "hold: keep the desk busy")
  await expect(card.getByText("Trabajando")).toBeVisible()
  // The list says so too.
  await pages.agents.list()
  await expect(pages.agents.card(ctx.agent.name).getByText("Trabajando")).toBeVisible()
  await expect(pages.agents.card(ctx.agent.name).getByText("Remoto")).toBeVisible()

  // A config change makes the connection outdated, and it stays so while busy.
  await pages.agents.open(ctx.agent.id)
  const current = await api.get<{ version: number; instructions: string }>(`/api/v1/agents/${ctx.agent.id}/config`)
  await api.put(`/api/v1/agents/${ctx.agent.id}/config`, {
    base: current.version,
    instructions: `${current.instructions} (changed)`,
  })
  await expect(card.getByText(/config v\d+ · desactualizada/)).toBeVisible()

  // The turn ends, the connector restarts with the new config.
  writeFileSync(ctx.gate, "")
  await expect(card.getByText("desactualizada")).toHaveCount(0)
  await expect(card).toContainText("conectado")
})
