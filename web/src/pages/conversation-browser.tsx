import * as React from "react"
import { AppWindow, ExternalLink, Lock } from "lucide-react"

import { useAgents, useCan, useEndTakeover, useHosted, useNow, useProfile, useSlotAction } from "@/lib/hooks"
import { formatRelative } from "@/lib/format"
import { Perm } from "@/lib/permissions.gen"
import type { Conversation, LibraryAgent, Takeover } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Spinner } from "@/components/ui/spinner"
import { BrowserViewer, Mono, takeoverLabel } from "@/components/seaport"

/**
 * Whether a person can open an agent's managed browser from here: its
 * machine runs one, its profile uses it and the caller may drive it
 * (Perm.BrowserUse). `slot` is a running slot to open it on.
 */
export function useAgentBrowser(agentId: string) {
  const can = useCan()
  const hosted = useHosted(agentId)
  const profile = useProfile(agentId)
  const h = hosted.data
  const p = profile.data
  const ready = can(Perm.BrowserUse) && !!h && !!p?.machine?.browser?.engine && !!p.browser
  return {
    ready,
    slot: h?.slots.find((s) => s.state === "running"),
    starting: !!h?.slots.some((s) => s.state === "starting"),
    // What a start would wake: the lowest stopped slot, else a failed one.
    idle: h?.slots.find((s) => s.state === "stopped") ?? h?.slots.find((s) => s.state === "failed"),
  }
}

/**
 * The agent's browser in a dialog, opened at `url` when given (a takeover's
 * site). With no slot running it offers to start one and shows the viewer
 * once it runs (slot.updated keeps the hosting fresh).
 */
export function BrowserDialog({
  agent,
  url,
  open,
  onOpenChange,
}: {
  agent: LibraryAgent
  url?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const can = useCan()
  const b = useAgentBrowser(agent.id)
  const action = useSlotAction(agent.id)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent showCloseButton={!b.slot} className="max-h-[95vh] overflow-y-auto sm:max-w-5xl">
        <DialogHeader className={b.slot ? "sr-only" : undefined}>
          <DialogTitle>Navegador de @{agent.name}</DialogTitle>
          <DialogDescription>
            El Chromium de su instancia, con la sesión que comparte con el agente: lo que inicies aquí, el agente lo usa.
          </DialogDescription>
        </DialogHeader>
        {b.slot ? (
          <BrowserViewer
            key={`${b.slot.index}-${url ?? ""}`}
            agent={agent}
            slot={b.slot}
            startUrl={url}
            onClose={() => onOpenChange(false)}
          />
        ) : b.starting || action.isPending ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> Arrancando la instancia…
          </p>
        ) : (
          <div className="space-y-3 text-sm">
            <p className="text-muted-foreground">Ninguna instancia del agente está corriendo, así que su navegador está apagado.</p>
            {can(Perm.HostingWrite) && b.idle ? (
              <Button size="sm" onClick={() => action.mutate({ slot: b.idle!.index, action: "start" })}>
                Arrancar <Mono>{`${agent.name}-${b.idle.index}`}</Mono>
              </Button>
            ) : (
              <p className="text-muted-foreground">Arranca cuando le llegue un mensaje.</p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** A header button that opens the agent's browser, when it has one to open. */
export function BrowserButton({ agent }: { agent: LibraryAgent }) {
  const b = useAgentBrowser(agent.id)
  const [open, setOpen] = React.useState(false)
  if (!b.ready) return null
  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        aria-label={`Abrir el navegador de @${agent.name}`}
        title={`Navegador de @${agent.name}`}
        onClick={() => setOpen(true)}
      >
        <AppWindow />
      </Button>
      {open && <BrowserDialog agent={agent} open={open} onOpenChange={setOpen} />}
    </>
  )
}

/** The takeovers of agents about this conversation (any door of its thread). */
export function takeoversHere(agents: LibraryAgent[], doors: string[]): { agent: LibraryAgent; takeover: Takeover }[] {
  const here = new Set(doors)
  return agents.flatMap((agent) =>
    (agent.takeovers ?? [])
      .filter((t) => (t.conversation_id && here.has(t.conversation_id)) || (t.notified_in && here.has(t.notified_in)))
      .map((takeover) => ({ agent, takeover })),
  )
}

/**
 * A banner per site where an agent asked a person to take over its browser
 * for this conversation: open the browser there, or mark it done (the agent
 * is woken to go on).
 */
export function TakeoverBanners({ conversation, doors }: { conversation: Conversation; doors: Conversation[] }) {
  const agents = useAgents()
  const ids = doors.length > 0 ? doors.map((d) => d.id) : [conversation.id]
  const list = takeoversHere(agents.data?.items ?? [], ids)
  if (list.length === 0) return null
  return (
    <div className="divide-y border-b border-warning/40 bg-warning/10">
      {list.map(({ agent, takeover }) => (
        <TakeoverBanner key={`${agent.id}/${takeover.host}`} agent={agent} takeover={takeover} />
      ))}
    </div>
  )
}

function TakeoverBanner({ agent, takeover: t }: { agent: LibraryAgent; takeover: Takeover }) {
  const can = useCan()
  const b = useAgentBrowser(agent.id)
  const end = useEndTakeover(agent.id)
  const now = useNow()
  const [open, setOpen] = React.useState(false)
  const use = can(Perm.BrowserUse)
  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-2.5 text-sm">
      <Lock className="size-4 shrink-0 text-warning" />
      <span className="min-w-0 flex-1">
        <b>
          {t.by === "detector" ? `El navegador de @${agent.name} necesita que` : `@${agent.name} necesita que`}{" "}
          {takeoverLabel[t.kind] ?? takeoverLabel.other} <Mono>{t.host}</Mono>.
        </b>{" "}
        {t.by !== "detector" && t.note && <span className="text-muted-foreground">{t.note} · </span>}
        <span className="text-xs text-muted-foreground">{formatRelative(t.at, now)}</span>
      </span>
      {use && (
        <div className="flex gap-2">
          {b.ready && (
            <Button size="sm" onClick={() => setOpen(true)}>
              <ExternalLink /> Abrir navegador
            </Button>
          )}
          <Button size="sm" variant="outline" disabled={end.isPending} onClick={() => end.mutate(t.host)}>
            Listo
          </Button>
        </div>
      )}
      {open && <BrowserDialog agent={agent} url={`https://${t.host}`} open={open} onOpenChange={setOpen} />}
    </div>
  )
}
