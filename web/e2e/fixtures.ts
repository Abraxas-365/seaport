import { createHmac } from "node:crypto"
import { mkdtempSync, readFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { test as base, expect, request, type APIRequestContext, type Page } from "@playwright/test"

import { Pages } from "./pages"

/** What `TestUI` (internal/bootstrap/ui_e2e_test.go) tells the specs. */
export interface Context {
  url: string
  token: string
  agent: { id: string; name: string }
  slack: { url: string; posts_url: string; signing_secret: string; bot_token: string }
  /** Creating this file lets a "hold:" turn of the echo agent finish. */
  gate: string
}

let cached: Context | undefined
export function context(): Context {
  if (!cached) {
    const file = process.env.SEAPORT_UI_CONTEXT
    if (!file) throw new Error("SEAPORT_UI_CONTEXT is not set: run these through `SEAPORT_TEST_UI=1 go test -run TestUI ./internal/bootstrap/`")
    cached = JSON.parse(readFileSync(file, "utf8")) as Context
  }
  return cached
}

let counter = 0
/** A name no other spec uses: the specs share one port and one database. */
export function unique(prefix: string): string {
  counter += 1
  return `${prefix}-${Date.now().toString(36)}${counter}`
}

/** The port's HTTP API as the admin, for what a spec sets up around the UI. */
export class Api {
  constructor(private readonly http: APIRequestContext) {}

  private async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.http.fetch(path, { method, data: body })
    if (!res.ok()) throw new Error(`${method} ${path}: ${res.status()} ${await res.text()}`)
    const text = await res.text()
    return (text ? JSON.parse(text) : undefined) as T
  }

  get<T = unknown>(path: string) {
    return this.call<T>("GET", path)
  }
  post<T = unknown>(path: string, body?: unknown) {
    return this.call<T>("POST", path, body)
  }
  put<T = unknown>(path: string, body?: unknown) {
    return this.call<T>("PUT", path, body)
  }
  patch<T = unknown>(path: string, body?: unknown) {
    return this.call<T>("PATCH", path, body)
  }

  /** A dock worked by the echo agent; `allow` lets every chat in (customs). */
  async dock(channel: string, name: string, opts: { allow?: boolean } = {}): Promise<{ id: string; name: string }> {
    const d = await this.post<{ id: string; name: string }>("/api/v1/docks", { channel, name })
    await this.assignAgent(d.id)
    if (opts.allow) await this.allow(d.id)
    return d
  }
  assignAgent(dock: string) {
    return this.put(`/api/v1/docks/${dock}/agents/${context().agent.id}`, { default: true })
  }
  allow(dock: string) {
    return this.patch(`/api/v1/docks/${dock}/customs/policy`, { default: "allow" })
  }
  /** A message into an `api` dock; answers the conversation it opened. */
  async inject(dock: string, chat: string, text: string): Promise<{ conversation: string; ship: string }> {
    const s = await this.post<{ id: string; conversation_id: string }>(`/api/v1/docks/${dock}/ships`, {
      chat_id: chat,
      chat_title: `Chat ${chat}`,
      message_id: unique("m"),
      sender_id: chat,
      sender_name: "Ana",
      text,
    })
    return { conversation: s.conversation_id, ship: s.id }
  }
  /** The conversations of a dock, newest first. */
  async conversations(dock: string): Promise<{ id: string; title: string; external_id: string }[]> {
    const r = await this.get<{ items: { id: string; title: string; external_id: string }[] }>(`/api/v1/inbox?dock_id=${dock}&limit=50`)
    return r.items
  }
}

/** A signed Slack Events API callback to a Slack dock, as Slack would send it. */
export async function slackEvent(http: APIRequestContext, dock: string, payload: unknown): Promise<number> {
  const raw = JSON.stringify(payload)
  const stamp = Math.floor(Date.now() / 1000).toString()
  const sig = "v0=" + createHmac("sha256", context().slack.signing_secret).update(`v0:${stamp}:${raw}`).digest("hex")
  const res = await http.post(`/slack/events/${dock}`, {
    headers: { "X-Slack-Request-Timestamp": stamp, "X-Slack-Signature": sig, "Content-Type": "application/json" },
    data: raw,
  })
  return res.status()
}

/** What the port posted to the fake Slack (the fake's own debug route, not the port's). */
export async function slackPosts(): Promise<{ channel: string; text: string; thread_ts?: string }[]> {
  const res = await fetch(context().slack.posts_url)
  return (await res.json()) as { channel: string; text: string; thread_ts?: string }[]
}

interface Fixtures {
  api: Api
  pages: Pages
  ctx: Context
}

interface WorkerFixtures {
  loggedIn: string
  adminApi: Api
}

export const test = base.extend<Fixtures, WorkerFixtures>({
  // The admin logs in once per worker through the real login page; every
  // test of the worker starts from that session.
  loggedIn: [
    async ({ browser }, use, workerInfo) => {
      const ctx = context()
      const page = await browser.newPage({ baseURL: workerInfo.project.use.baseURL })
      await page.goto("/")
      await page.getByLabel("Token de administrador").fill(ctx.token)
      await page.getByRole("button", { name: "Entrar" }).click()
      await expect(page.getByRole("link", { name: "Muelles" })).toBeVisible()
      const file = join(mkdtempSync(join(tmpdir(), "seaport-ui-")), "admin-session.json")
      await page.context().storageState({ path: file })
      await page.close()
      await use(file)
    },
    { scope: "worker" },
  ],
  storageState: async ({ loggedIn }, use) => {
    await use(loggedIn)
  },
  adminApi: [
    async ({}, use, workerInfo) => {
      const ctx = context()
      const http = await request.newContext({
        baseURL: workerInfo.project.use.baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${ctx.token}` },
      })
      await use(new Api(http))
      await http.dispose()
    },
    { scope: "worker" },
  ],
  api: async ({ adminApi }, use) => {
    await use(adminApi)
  },
  pages: async ({ page }, use) => {
    await use(new Pages(page))
  },
  ctx: async ({}, use) => {
    await use(context())
  },
})

export { expect }
export type { Page }
