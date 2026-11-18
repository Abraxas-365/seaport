// A small Chrome DevTools Protocol client over the browser viewer's
// WebSocket (GET /api/v1/agents/{id}/hosting/slots/{n}/browser): the port
// relays CDP as it is to the slot's managed browser. The web drives one
// page (a flattened session), watches it as a screencast and forwards
// mouse and keyboard input; nothing of the stream is kept.

type Params = Record<string, unknown>

interface Pending {
  resolve: (result: Params) => void
  reject: (err: Error) => void
}

export interface CDPEvent {
  method: string
  params: Params
  sessionId?: string
}

/** A page of the browser (Target.TargetInfo, the ones of type page). */
export interface PageTarget {
  targetId: string
  title: string
  url: string
}

export class CDPClient {
  private ws: WebSocket
  private next = 1
  private pending = new Map<number, Pending>()
  private listeners = new Set<(ev: CDPEvent) => void>()
  readonly ready: Promise<void>
  readonly closed: Promise<string>

  constructor(url: string) {
    this.ws = new WebSocket(url)
    this.ready = new Promise((resolve, reject) => {
      this.ws.addEventListener("open", () => resolve(), { once: true })
      this.ws.addEventListener("error", () => reject(new Error("no se pudo abrir el navegador")), { once: true })
    })
    this.closed = new Promise((resolve) => {
      this.ws.addEventListener("close", (ev) => {
        const reason = ev.reason || (ev.code === 1000 ? "" : `conexión cerrada (${ev.code})`)
        for (const p of this.pending.values()) p.reject(new Error(reason || "la sesión del navegador terminó"))
        this.pending.clear()
        resolve(reason)
      })
    })
    this.ws.addEventListener("message", (ev) => this.receive(String(ev.data)))
  }

  private receive(data: string) {
    let msg: { id?: number; result?: Params; error?: { message: string }; method?: string; params?: Params; sessionId?: string }
    try {
      msg = JSON.parse(data)
    } catch {
      return
    }
    if (msg.id !== undefined) {
      const p = this.pending.get(msg.id)
      if (!p) return
      this.pending.delete(msg.id)
      if (msg.error) p.reject(new Error(msg.error.message))
      else p.resolve(msg.result ?? {})
      return
    }
    if (msg.method) for (const l of this.listeners) l({ method: msg.method, params: msg.params ?? {}, sessionId: msg.sessionId })
  }

  /** Sends a command (to the browser, or to a page's session). */
  send(method: string, params: Params = {}, sessionId?: string): Promise<Params> {
    if (this.ws.readyState !== WebSocket.OPEN) return Promise.reject(new Error("el navegador no está conectado"))
    const id = this.next++
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.ws.send(JSON.stringify({ id, method, params, sessionId }))
    })
  }

  on(listener: (ev: CDPEvent) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  close() {
    if (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING) this.ws.close(1000, "")
  }

  get open() {
    return this.ws.readyState === WebSocket.OPEN
  }

  /** The browser's pages. */
  async pages(): Promise<PageTarget[]> {
    const res = (await this.send("Target.getTargets")) as { targetInfos: (PageTarget & { type: string })[] }
    return res.targetInfos.filter((t) => t.type === "page").map(({ targetId, title, url }) => ({ targetId, title, url }))
  }

  /** Attaches to a page (flat mode): its session id. */
  async attach(targetId: string): Promise<string> {
    const res = (await this.send("Target.attachToTarget", { targetId, flatten: true })) as { sessionId: string }
    return res.sessionId
  }

  async newPage(url = "about:blank"): Promise<string> {
    const res = (await this.send("Target.createTarget", { url })) as { targetId: string }
    return res.targetId
  }
}

/**
 * The page a viewer opens on: the one the agent is working, i.e. the first
 * with a real address (the browser starts with an `about:blank` tab, which
 * the agent's DevTools MCP usually leaves behind when it opens its own),
 * else the first page; undefined when there is none.
 */
export function pickPage(pages: PageTarget[]): PageTarget | undefined {
  return pages.find((p) => /^https?:\/\//i.test(p.url)) ?? pages[0]
}

/**
 * Whether an address is a sign-in page that refuses automated browsers
 * (Google's "Couldn't sign you in · This browser or app may not be
 * secure"): nobody passes it in the agent's browser, the viewer included.
 * The same rule as REFUSED_PAGES in internal/connector/detect.js
 * (browser-detect.test.ts checks they agree).
 */
export function refusedSignIn(url: string): boolean {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return false
  }
  const host = u.hostname.toLowerCase()
  if (u.protocol !== "https:" && u.protocol !== "http:") return false
  return (host === "accounts.google.com" || host.endsWith(".accounts.google.com")) && u.pathname.includes("rejected")
}

/** A cookie as Storage.getCookies lists it (what a sign-in leaves). */
export interface Cookie {
  name: string
  domain: string
  path: string
  expires: number
  session: boolean
  httpOnly: boolean
  secure: boolean
}

/** The browser's cookies grouped by site (registrable-ish domain). */
export interface CookieSite {
  domain: string
  cookies: Cookie[]
  /** The latest expiry among its persistent cookies (0: session only). */
  expires: number
}

export function groupCookies(cookies: Cookie[]): CookieSite[] {
  const sites = new Map<string, CookieSite>()
  for (const c of cookies) {
    const domain = c.domain.replace(/^\./, "")
    const site = sites.get(domain) ?? { domain, cookies: [], expires: 0 }
    site.cookies.push(c)
    if (!c.session && c.expires > site.expires) site.expires = c.expires
    sites.set(domain, site)
  }
  return [...sites.values()].sort((a, b) => a.domain.localeCompare(b.domain))
}

/** Maps a DOM key event to Input.dispatchKeyEvent's parameters. */
export function keyParams(e: React.KeyboardEvent, type: "keyDown" | "keyUp"): Params {
  const text = e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey ? e.key : undefined
  const params: Params = {
    type: type === "keyDown" && text !== undefined ? "keyDown" : type === "keyDown" ? "rawKeyDown" : "keyUp",
    key: e.key,
    code: e.code,
    windowsVirtualKeyCode: e.keyCode,
    nativeVirtualKeyCode: e.keyCode,
    modifiers: modifiers(e),
    autoRepeat: e.repeat,
    location: e.location,
  }
  if (text !== undefined && type === "keyDown") {
    params.text = text
    params.unmodifiedText = text
  }
  // Keys Chromium needs the text of to act (Enter, Tab, Backspace…).
  if (type === "keyDown" && e.key === "Enter") params.text = "\r"
  return params
}

export function modifiers(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0)
}

export const mouseButtons = ["left", "middle", "right", "back", "forward"] as const
