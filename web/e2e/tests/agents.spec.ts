import { writeFileSync } from "node:fs"

import { test, expect, unique } from "../fixtures"

test("the profile editor saves instructions and a secret, and the crew shows the connection outdated", async ({ ctx, pages, page, api }) => {
  await pages.agents.open(ctx.agent.id)
  await expect(page.getByRole("heading", { level: 1, name: `@${ctx.agent.name}` })).toBeVisible()

  const instructions = `You are the echo agent (${unique("persona")}).`
  const box = page.getByLabel("Instrucciones").first()
  await box.fill(instructions)
  await page.getByRole("button", { name: /^Guardar v\d+$/ }).click()
  await expect(page.getByRole("button", { name: /^Guardar v\d+$/ })).toBeDisabled()

  // A secret: only its name comes back, never the value.
  const secret = "REGION_TOKEN"
  await page.getByRole("button", { name: "Secreto" }).click()
  const dialog = page.getByRole("dialog", { name: "Nuevo secreto" })
  await dialog.getByLabel("Nombre").fill(secret)
  await dialog.getByLabel("Valor").fill("s3cr3t-value-0001")
  await dialog.getByRole("button", { name: "Guardar" }).click()
  await expect(page.getByText(secret, { exact: true })).toBeVisible()
  await expect(page.getByText("s3cr3t-value-0001")).toHaveCount(0)
  await expect(page.getByText("No se puede volver a ver.").first()).toBeHidden()

  // After a reload the instructions are the saved ones and the value is nowhere.
  await page.reload()
  await expect(page.getByLabel("Instrucciones").first()).toHaveValue(instructions)
  await expect(page.getByText("s3cr3t-value-0001")).toHaveCount(0)
  const stored = JSON.stringify(await api.get(`/api/v1/agents/${ctx.agent.id}/secrets`))
  expect(stored).toContain(secret)
  expect(stored).not.toContain("s3cr3t-value-0001")
})

test("the crew page shows the echo agent connected, outdated after a profile change until it restarts", async ({ ctx, pages, page, api }) => {
  await pages.crew.open()
  const card = page.getByTestId("agent-card").filter({ hasText: `@${ctx.agent.name}` })
  await expect(card).toBeVisible()
  await expect(card).toContainText("conectado")
  // Earlier specs changed the profile: wait for the restart they caused.
  await expect(card.getByText("desactualizado")).toHaveCount(0)

  // Keep the agent busy: a restart waits for the work in flight.
  const dock = await api.dock("api", unique("busy"), { allow: true })
  await api.inject(dock.id, "ana-8", "hold: keep the desk busy")
  await expect(card.getByText("Trabajando")).toBeVisible()

  // A profile change makes the connection outdated, and it stays so while busy.
  const current = await api.get<{ version: number; instructions: string }>(`/api/v1/agents/${ctx.agent.id}/profile`)
  await api.put(`/api/v1/agents/${ctx.agent.id}/profile`, { base: current.version, instructions: `${current.instructions} (changed)` })
  await expect(card.getByText(/perfil v\d+ · desactualizado/)).toBeVisible()

  // The turn ends, the connector restarts with the new profile.
  writeFileSync(ctx.gate, "")
  await expect(card.getByText("desactualizado")).toHaveCount(0)
  await expect(card).toContainText("conectado")
})
