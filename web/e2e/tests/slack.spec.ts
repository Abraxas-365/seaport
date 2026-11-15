import { test, expect, slackEvent, slackPosts, unique } from "../fixtures"

test("a Slack dock connected from the UI takes a mention and the reply reaches Slack", async ({ ctx, pages, page, api }) => {
  const name = unique("slack")
  await pages.docks.open()
  await pages.docks.create("Slack", name)
  await expect(pages.dock.title(name)).toBeVisible()
  await expect(page.getByText("Vinculando", { exact: true }).first()).toBeVisible()

  // Flow B: the app exists already; paste its signing secret and bot token.
  await page.getByRole("button", { name: "Ya tengo la App" }).click()
  await page.getByLabel("Signing Secret").fill(ctx.slack.signing_secret)
  await page.getByLabel("Bot token").fill(ctx.slack.bot_token)
  await page.getByRole("button", { name: "Conectar" }).click()
  await expect(page.getByText("Slack App conectada")).toBeVisible()
  await expect(page.getByText("Conectado", { exact: true }).first()).toBeVisible()
  await expect(page.getByText("Espacio de trabajo")).toBeVisible()
  // The secrets are never read back.
  await expect(page.getByText(ctx.slack.bot_token)).toHaveCount(0)

  const id = page.url().split("/docks/")[1]
  await api.assignAgent(id)
  await api.allow(id)

  const ts = `${Math.floor(Date.now() / 1000)}.000100`
  const status = await slackEvent(page.request, id, {
    type: "event_callback",
    team_id: "T0TEST",
    api_app_id: "A0TEST",
    event: { type: "app_mention", user: "U0ANA", text: "<@U0BOT> is the deploy done?", ts, channel: "C0SUP", channel_type: "channel" },
  })
  expect(status).toBe(200)

  // The dock lists the new conversation; opening it shows the mention and the reply.
  await page.reload()
  await expect(page.getByText("Conversaciones", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: /support|C0SUP/ }).first().click()
  await expect(pages.conversation.bubble("is the deploy done?", "in")).toBeVisible()
  await expect(pages.conversation.bubble(/echo: .*deploy done\?/, "out")).toBeVisible()

  // And the reply was posted to Slack, in the mention's thread.
  await expect
    .poll(async () => (await slackPosts()).some((p) => p.channel === "C0SUP" && p.thread_ts === ts && p.text.includes("echo:")))
    .toBe(true)
})
