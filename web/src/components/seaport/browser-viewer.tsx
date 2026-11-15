import * as React from "react"
import {
  ArrowLeft,
  ArrowRight,
  AppWindow,
  Cookie as CookieIcon,
  ExternalLink,
  Hand,
  Lock,
  Plus,
  RotateCw,
  Trash2,
  X,
} from "lucide-react"

import { CDPClient, groupCookies, keyParams, modifiers, mouseButtons, type Cookie, type CookieSite, type PageTarget } from "@/lib/cdp"
import { api } from "@/lib/api"
import { formatRelative } from "@/lib/format"
import { useCrew, useEndTakeover, useNow } from "@/lib/hooks"
import type { LibraryAgent, Slot, TakeoverKind } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Mono } from "./layout"
import { StatusBadge } from "./status"

/** The screencast's largest size (the browser's window is 1280×800). */
const width = 1280
const height = 800

type Phase = "connecting" | "live" | "closed"

/**
 * The browser viewer: watches a slot's managed browser live (a CDP
 * screencast) and lets the person drive it (mouse, keyboard, address bar,
 * tabs) to sign in to sites for the agent. While the agent works a
 * delivery, the view is read-only until the person takes the control.
 */
export function BrowserViewer({
  agent,
  slot,
  startUrl,
  onClose,
  className,
}: {
  agent: LibraryAgent
  slot: Slot
  /** Opened in a new tab once connected (a login request's site). */
  startUrl?: string
  onClose: () => void
  className?: string
}) {
  const clientRef = React.useRef<CDPClient | null>(null)
  const client = clientRef.current
  const [phase, setPhase] = React.useState<Phase>("connecting")
  const [reason, setReason] = React.useState("")
  const [pages, setPages] = React.useState<PageTarget[]>([])
  const [page, setPage] = React.useState<{ targetId: string; sessionId: string } | null>(null)
  const [frame, setFrame] = React.useState<string>("")
  // The page's CSS size the frames show (Page.screencastFrame metadata):
  // input coordinates map to it, whatever the frame's pixels.
  const [size, setSize] = React.useState({ w: width, h: height })
  const [url, setUrl] = React.useState("")
  const [typing, setTyping] = React.useState<string | null>(null)
  const [taken, setTaken] = React.useState(false)
  // null: panel closed; "loading": open, cookies on their way; else the sites.
  const [sites, setSites] = React.useState<CookieSite[] | "loading" | null>(null)
  const canvasRef = React.useRef<HTMLDivElement>(null)
  const crew = useCrew()
  const working = (crew.data?.agents ?? []).some((c) => c.agent_id === agent.id && c.deliveries.length > 0)
  const readOnly = working && !taken

  // Connect once; the client lives as long as the viewer.
  React.useEffect(() => {
    const c = new CDPClient(api.browserSocket(agent.id, slot.index))
    clientRef.current = c
    let cancelled = false
    c.ready
      .then(async () => {
        if (cancelled) return
        setPhase("live") // re-renders with clientRef.current set
        const list = await c.pages()
        if (cancelled) return
        setPages(list)
        const first = startUrl ? await c.newPage(startUrl) : (list[0]?.targetId ?? (await c.newPage()))
        await attach(c, first)
      })
      .catch((err: Error) => {
        setReason(err.message)
        setPhase("closed")
      })
    c.closed.then((r) => {
      if (cancelled) return
      setReason(r)
      setPhase("closed")
    })
    return () => {
      cancelled = true
      c.close()
      clientRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agent.id, slot.index])

  async function attach(c: CDPClient, targetId: string) {
    const sessionId = await c.attach(targetId)
    await c.send("Page.enable", {}, sessionId)
    await c.send("Page.startScreencast", { format: "jpeg", quality: 70, maxWidth: width, maxHeight: height, everyNthFrame: 1 }, sessionId)
    setPage({ targetId, sessionId })
    const info = (await c.send("Target.getTargetInfo", { targetId })) as { targetInfo: PageTarget }
    setUrl(info.targetInfo.url === "about:blank" ? "" : info.targetInfo.url)
  }

  // Frames, navigations and target changes.
  React.useEffect(() => {
    if (!client || !page) return
    return client.on((ev) => {
      if (ev.method === "Page.screencastFrame" && ev.sessionId === page.sessionId) {
        setFrame(`data:image/jpeg;base64,${ev.params.data as string}`)
        const m = ev.params.metadata as { deviceWidth: number; deviceHeight: number } | undefined
        if (m && m.deviceWidth > 0 && m.deviceHeight > 0)
          setSize((s) => (s.w === m.deviceWidth && s.h === m.deviceHeight ? s : { w: m.deviceWidth, h: m.deviceHeight }))
        client.send("Page.screencastFrameAck", { sessionId: ev.params.sessionId }, page.sessionId).catch(() => {})
      } else if (ev.method === "Page.frameNavigated" && ev.sessionId === page.sessionId) {
        const f = ev.params.frame as { parentId?: string; url: string }
        if (!f.parentId) setUrl(f.url === "about:blank" ? "" : f.url)
      } else if (ev.method === "Target.targetCreated" || ev.method === "Target.targetDestroyed" || ev.method === "Target.targetInfoChanged") {
        client.pages().then(setPages).catch(() => {})
      }
    })
  }, [client, page])
  React.useEffect(() => {
    if (!client || phase !== "live") return
    client.send("Target.setDiscoverTargets", { discover: true }).catch(() => {})
  }, [client, phase])

  const switchPage = async (targetId: string) => {
    if (!client || !page) return
    await client.send("Page.stopScreencast", {}, page.sessionId).catch(() => {})
    await client.send("Target.detachFromTarget", { sessionId: page.sessionId }).catch(() => {})
    await client.send("Target.activateTarget", { targetId }).catch(() => {})
    await attach(client, targetId)
  }
  const navigate = async (to: string) => {
    if (!client || !page) return
    const target = /^[a-z]+:\/\//i.test(to) ? to : `https://${to}`
    setTyping(null)
    await client.send("Page.navigate", { url: target }, page.sessionId).catch(() => {})
  }
  const point = (e: React.MouseEvent) => {
    const r = canvasRef.current!.getBoundingClientRect()
    return { x: Math.round(((e.clientX - r.left) / r.width) * size.w), y: Math.round(((e.clientY - r.top) / r.height) * size.h) }
  }
  const mouse = (type: "mousePressed" | "mouseReleased" | "mouseMoved", e: React.MouseEvent, clicks = 0) => {
    if (!client || !page || readOnly) return
    const { x, y } = point(e)
    client
      .send("Input.dispatchMouseEvent", {
        type,
        x,
        y,
        button: type === "mouseMoved" ? "none" : mouseButtons[e.button] ?? "left",
        buttons: e.buttons,
        clickCount: clicks,
        modifiers: modifiers(e),
      }, page.sessionId)
      .catch(() => {})
  }
  const wheel = (e: React.WheelEvent) => {
    if (!client || !page || readOnly) return
    const { x, y } = point(e)
    client
      .send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: e.deltaX, deltaY: e.deltaY, modifiers: modifiers(e) }, page.sessionId)
      .catch(() => {})
  }
  const key = (type: "keyDown" | "keyUp", e: React.KeyboardEvent) => {
    if (!client || !page || readOnly) return
    e.preventDefault()
    client.send("Input.dispatchKeyEvent", keyParams(e, type), page.sessionId).catch(() => {})
  }
  const loadSites = async () => {
    if (!client) return
    const res = (await client.send("Storage.getCookies")) as { cookies: Cookie[] }
    setSites(groupCookies(res.cookies))
  }
  // The browser target has no deleteCookies; Network.deleteCookies on any
  // page's session deletes from the profile's shared jar.
  const forget = async (site: CookieSite) => {
    if (!client || !page) return
    await Promise.all(
      site.cookies.map((c) =>
        client.send("Network.deleteCookies", { name: c.name, domain: c.domain, path: c.path }, page.sessionId).catch(() => {}),
      ),
    )
    await loadSites()
  }

  const name = `${agent.name}-${slot.index}`
  return (
    <div className={cn("flex flex-col overflow-hidden rounded-xl border bg-card", className)}>
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <AppWindow className="size-4 text-muted-foreground" />
        <span className="text-sm font-medium">
          Navegador de <Mono>{name}</Mono>
        </span>
        {phase === "connecting" && <StatusBadge tone="info">Conectando…</StatusBadge>}
        {phase === "live" && <StatusBadge tone={readOnly ? "warning" : "success"}>{readOnly ? "El agente trabaja" : "En vivo"}</StatusBadge>}
        {phase === "closed" && <StatusBadge tone="neutral">Cerrado</StatusBadge>}
        <span className="ml-auto flex items-center gap-1">
          {phase === "live" && readOnly && (
            <Button size="sm" variant="outline" onClick={() => setTaken(true)}>
              <Hand /> Tomar el control
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={phase !== "live"}
            onClick={() => {
              if (sites) setSites(null)
              else {
                setSites("loading")
                void loadSites()
              }
            }}
          >
            <CookieIcon /> Sesiones
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="Cerrar el navegador" onClick={onClose}>
            <X />
          </Button>
        </span>
      </div>

      {phase === "live" && (
        <div className="flex items-center gap-1 border-b px-2 py-1.5">
          <Button size="icon-sm" variant="ghost" aria-label="Atrás" disabled={readOnly} onClick={() => history(client, page, -1)}>
            <ArrowLeft />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="Adelante" disabled={readOnly} onClick={() => history(client, page, 1)}>
            <ArrowRight />
          </Button>
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label="Recargar"
            disabled={readOnly}
            onClick={() => page && client?.send("Page.reload", {}, page.sessionId).catch(() => {})}
          >
            <RotateCw />
          </Button>
          <form
            className="flex min-w-0 flex-1 items-center gap-1"
            onSubmit={(e) => {
              e.preventDefault()
              if (typing !== null && typing.trim()) navigate(typing.trim())
            }}
          >
            {url.startsWith("https://") && <Lock className="size-3.5 shrink-0 text-success" />}
            <Input
              aria-label="Dirección"
              value={typing ?? url}
              readOnly={readOnly}
              onChange={(e) => setTyping(e.target.value)}
              onBlur={() => setTyping(null)}
              placeholder="Escribe una dirección y pulsa Enter"
              className="h-7 font-mono text-xs"
            />
          </form>
          <Button size="icon-sm" variant="ghost" aria-label="Pestaña nueva" disabled={readOnly} onClick={() => client?.newPage().then((t) => switchPage(t))}>
            <Plus />
          </Button>
        </div>
      )}

      {phase === "live" && pages.length > 1 && (
        <div className="flex gap-1 overflow-x-auto border-b px-2 py-1 scrollbar-thin">
          {pages.map((p) => (
            <button
              key={p.targetId}
              type="button"
              className={cn(
                "max-w-48 truncate rounded-md px-2 py-0.5 text-xs",
                p.targetId === page?.targetId ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent/50",
              )}
              title={p.url}
              onClick={() => switchPage(p.targetId)}
            >
              {p.title || p.url || "Nueva pestaña"}
            </button>
          ))}
        </div>
      )}

      <div className="flex min-h-0">
        <div
          ref={canvasRef}
          tabIndex={0}
          role="application"
          aria-label="Página del navegador"
          style={{ aspectRatio: `${size.w} / ${size.h}` }}
          className={cn(
            "relative min-w-0 flex-1 select-none bg-muted outline-none focus-visible:ring-2 focus-visible:ring-ring",
            readOnly ? "cursor-not-allowed" : "cursor-default",
          )}
          onMouseDown={(e) => {
            canvasRef.current?.focus()
            mouse("mousePressed", e, e.detail || 1)
          }}
          onMouseUp={(e) => mouse("mouseReleased", e, e.detail || 1)}
          onMouseMove={(e) => mouse("mouseMoved", e)}
          onWheel={wheel}
          onKeyDown={(e) => key("keyDown", e)}
          onKeyUp={(e) => key("keyUp", e)}
          onContextMenu={(e) => e.preventDefault()}
        >
          {frame ? (
            <img src={frame} alt="" draggable={false} className="size-full object-fill" />
          ) : (
            <div className="flex size-full items-center justify-center text-sm text-muted-foreground">
              {phase === "closed" ? reason || "La sesión del navegador terminó." : <Spinner />}
            </div>
          )}
          {phase === "closed" && frame && (
            <div className="absolute inset-0 flex items-center justify-center bg-background/70 text-sm">
              {reason || "La sesión del navegador terminó."}
            </div>
          )}
        </div>
        {sites && (
          <SessionsPanel sites={sites} onRefresh={loadSites} onForget={readOnly ? undefined : forget} />
        )}
      </div>
      <p className="border-t px-3 py-1.5 text-xs text-muted-foreground">
        Lo que inicies sesión aquí queda en el perfil del navegador de la instancia: el agente lo usa después. Nada del
        vídeo se guarda; el puerto registra quién abrió y cerró.
      </p>
    </div>
  )
}

async function history(client: CDPClient | null, page: { sessionId: string } | null, delta: number) {
  if (!client || !page) return
  const h = (await client.send("Page.getNavigationHistory", {}, page.sessionId).catch(() => null)) as {
    currentIndex: number
    entries: { id: number }[]
  } | null
  const entry = h?.entries[h.currentIndex + delta]
  if (entry) await client.send("Page.navigateToHistoryEntry", { entryId: entry.id }, page.sessionId).catch(() => {})
}

/** The sites the browser holds cookies of (its sessions), with forget. */
function SessionsPanel({
  sites,
  onRefresh,
  onForget,
}: {
  sites: CookieSite[] | "loading"
  onRefresh: () => void
  onForget?: (site: CookieSite) => void
}) {
  const now = useNow(60_000)
  return (
    <div className="flex w-64 shrink-0 flex-col border-l">
      <div className="flex items-center justify-between px-3 py-2 text-xs font-medium">
        Sesiones
        <Button size="icon-sm" variant="ghost" aria-label="Actualizar" onClick={onRefresh}>
          <RotateCw />
        </Button>
      </div>
      <ul className="min-h-0 flex-1 divide-y overflow-y-auto text-xs scrollbar-thin">
        {sites === "loading" && <li className="px-3 py-4 text-center text-muted-foreground">Leyendo…</li>}
        {sites !== "loading" && sites.length === 0 && <li className="px-3 py-4 text-center text-muted-foreground">Sin cookies todavía.</li>}
        {sites !== "loading" && sites.map((s) => (
          <li key={s.domain} className="flex items-center gap-2 px-3 py-2">
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono">{s.domain}</div>
              <div className="text-muted-foreground">
                {s.cookies.length} {s.cookies.length === 1 ? "cookie" : "cookies"}
                {s.expires > 0 && <> · expira {formatRelative(new Date(s.expires * 1000).toISOString(), now)}</>}
              </div>
            </div>
            {onForget && (
              <Button size="icon-sm" variant="ghost" aria-label={`Olvidar ${s.domain}`} title="Olvidar" onClick={() => onForget(s)}>
                <Trash2 />
              </Button>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}

/** What a person must do in the browser, per takeover kind. */
export const takeoverLabel: Record<TakeoverKind, string> = {
  login: "inicies sesión en",
  captcha: "resuelvas un captcha en",
  "2fa": "introduzcas el código 2FA en",
  other: "hagas algo en",
}

/** The sites where the agent asked a person to take over its browser (ask_takeover). */
export function Takeovers({ agent, canClear, onOpen }: { agent: LibraryAgent; canClear: boolean; onOpen?: (host: string) => void }) {
  const end = useEndTakeover(agent.id)
  const now = useNow()
  const list = agent.takeovers ?? []
  if (list.length === 0) return null
  return (
    <ul className="divide-y rounded-lg border border-warning/40 bg-warning/5 text-sm">
      {list.map((r) => (
        <li key={r.host} className="flex flex-wrap items-center gap-2 px-3 py-2">
          <Lock className="size-3.5 text-warning" />
          <span className="min-w-0 flex-1">
            El agente necesita que {takeoverLabel[r.kind] ?? takeoverLabel.other} <Mono>{r.host}</Mono>
            {r.note && <span className="text-muted-foreground"> · {r.note}</span>}
            <span className="text-xs text-muted-foreground"> · {formatRelative(r.at, now)}</span>
          </span>
          {onOpen && (
            <Button size="sm" variant="outline" onClick={() => onOpen(r.host)}>
              <ExternalLink /> Abrir
            </Button>
          )}
          {canClear && (
            <Button size="sm" variant="ghost" disabled={end.isPending} onClick={() => end.mutate(r.host)}>
              Listo
            </Button>
          )}
        </li>
      ))}
    </ul>
  )
}
