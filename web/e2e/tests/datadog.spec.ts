import { test, expect, unique } from "../fixtures"

test("a Datadog dock mirrors its alerts into a Slack channel", async ({ ctx, pages, page, api }) => {
  // The mirror's dock: Slack, linked against the fake, with a destination.
  const slack = await api.post<{ id: string; name: string }>("/api/v1/docks", { channel: "slack", name: unique("oncall") })
  await api.post(`/api/v1/docks/${slack.id}/slack/connect`, { signing_secret: ctx.slack.signing_secret, bot_token: ctx.slack.bot_token })
  await api.assignAgent(slack.id)
  await api.allow(slack.id)
  const destination = "oncall"
  await api.put(`/api/v1/docks/${slack.id}/destinations/${destination}`, { target: "C0OPS", title: "On-call channel" })

  // The Datadog dock is made and wired from the UI.
  const name = unique("monitors")
  await pages.docks.open()
  await pages.docks.create("Datadog", name)
  await expect(pages.dock.title(name)).toBeVisible()
  await expect(page.getByText("Conectado", { exact: true }).first()).toBeVisible()
  const id = page.url().split("/docks/")[1]
  await api.assignAgent(id)
  await api.allow(id)

  await expect(page.getByText("Sin espejo: los mensajes solo despiertan al agente.")).toBeVisible()
  await page.getByRole("combobox", { name: "Muelle del espejo" }).click()
  await page.getByRole("option", { name: slack.name }).click()
  await page.getByRole("combobox", { name: "Destino del espejo" }).click()
  await page.getByRole("option", { name: new RegExp(`^${destination}`) }).click()
  await expect(page.getByText("Espejo guardado")).toBeVisible()
  await expect(page.getByText("Ahora:")).toContainText(destination)

  // Datadog fires: its proof is the dock's secret.
  const { secret } = await api.post<{ secret: string }>(`/api/v1/docks/${id}/datadog/secret`)
  const alertId = unique("alert").replace(/\D/g, "") || "1"
  const res = await page.request.post(`/datadog/webhook/${id}`, {
    headers: { "X-Seaport-Token": secret },
    data: {
      id: alertId, alert_id: "4242", transition: "Triggered", title: "[Triggered] CPU high on web", status: "CPU is above 90%",
      scope: "host:web-1", tags: "env:prod,service:web", message: "@webhook-seaport check the pods",
      link: "https://app.example.com/event?id=1", priority: "P2", date: "1700000000000", org: "acme",
    },
  })
  expect(res.status()).toBe(202)
  expect((await res.json()).mirrored).toBe(true)

  // The alert is a conversation of the Datadog dock…
  await page.reload()
  await page.getByRole("button", { name: /CPU high on web/ }).first().click()
  await expect(pages.conversation.bubble("CPU high on web", "in").first()).toBeVisible()

  // …linked to the mirrored thread in Slack, where the agent answers. There
  // the alert is the message Seaport opened the thread with (outbound).
  const chips = page.locator('[data-slot="thread-chips"]')
  await expect(chips).toBeVisible()
  const datadogDoor = page.url()
  await chips.getByRole("link").and(page.locator(':not([aria-current="page"])')).first().click()
  await expect(page).not.toHaveURL(datadogDoor)
  await expect(pages.conversation.bubble("CPU high on web", "out").first()).toBeVisible()
  await expect(pages.conversation.bubble(/echo: /, "out").first()).toBeVisible()
})
