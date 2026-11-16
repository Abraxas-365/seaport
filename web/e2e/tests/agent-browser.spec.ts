import { test, expect, unique } from "../fixtures"

// The agent page's browser parts: the takeover notice's link and where
// notices go. A fresh agent hosted on an environment with a browser (the
// runtime is off in TestUI: nothing starts, the page only reads the specs).
test("a takeover notice's link opens the agent's browser card, and notices go to a destination picked from its docks", async ({ page, api }) => {
  const ag = await api.post<{ id: string; name: string }>("/api/v1/agents", { name: unique("surf") })
  const env = await api.post<{ id: string }>("/api/v1/environments", {
    name: unique("web"),
    spec: { harness: "rness", browser: { engine: "chromium" } },
  })
  await api.put(`/api/v1/agents/${ag.id}/hosting`, { environment: env.id })
  const dock = await api.post<{ id: string }>("/api/v1/docks", { channel: "api", name: unique("ops") })
  await api.put(`/api/v1/docks/${dock.id}/agents/${ag.id}`, { default: true })
  await api.put(`/api/v1/docks/${dock.id}/destinations/oncall`, { target: "oncall-room", title: "On call" })
  await api.put(`/api/v1/agents/${ag.id}/profile`, { base: 0, browser: true, browser_detect: true })

  // Notices sent before the anchor link named /agents/{id}/hosting: it lands on the card.
  await page.goto(`/agents/${ag.id}/hosting`)
  await expect(page).toHaveURL(new RegExp(`/agents/${ag.id}#navegador$`))
  const card = page.locator("#navegador")
  await expect(card.getByText("Navegador", { exact: true })).toBeVisible()
  await expect(card.getByText(/Cada instancia corre un Chromium/)).toBeInViewport()

  // Where notices go: a select of nobody, the conversation and the docks' destinations.
  const assist = page.getByRole("combobox", { name: "Avisar a una persona" })
  await expect(assist).toHaveText("Nadie")
  await assist.click()
  await expect(page.getByRole("option", { name: "La conversación que trabaja" })).toBeVisible()
  await page.getByRole("option", { name: "Destino oncall" }).click()
  await expect(assist).toHaveText("Destino oncall")
  await page.getByRole("button", { name: /^Guardar v\d+$/ }).click()
  await expect(page.getByRole("button", { name: /^Guardar v\d+$/ })).toBeDisabled()
  const profile = await api.get<{ browser_assist: string }>(`/api/v1/agents/${ag.id}/profile`)
  expect(profile.browser_assist).toBe("oncall")

  // A destination no dock has any more stays shown, marked.
  await api.delete(`/api/v1/docks/${dock.id}/destinations/oncall`)
  await page.reload()
  await expect(page.getByRole("combobox", { name: "Avisar a una persona" })).toHaveText("Destino oncall (no está en sus muelles)")

  await api.delete(`/api/v1/agents/${ag.id}`)
})
