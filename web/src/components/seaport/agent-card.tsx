import { Link } from "react-router"
import { Bot, Zap } from "lucide-react"

import { formatDuration, formatRelative } from "@/lib/format"
import type { Agent } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Card, CardContent } from "@/components/ui/card"
import { Mono } from "./layout"
import { StatusBadge, StatusDot } from "./status"

/** A connected connector: name, capacity meter, current work. */
export function AgentCard({
  agent,
  titleOf,
  now,
}: {
  agent: Agent
  titleOf?: (conversationId: string) => string | undefined
  now: number
}) {
  const busy = agent.deliveries.length
  return (
    <Card className="gap-0 py-0">
      <CardContent className="space-y-4 p-4">
        <div className="flex items-start gap-3">
          <span className="relative inline-flex size-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
            <Bot className="size-5" />
            <span className="absolute -top-0.5 -right-0.5 rounded-full ring-2 ring-card">
              <StatusDot tone={busy ? "info" : "success"} pulse={busy > 0} />
            </span>
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{agent.name || "agente"}</span>
              {agent.steer && (
                <StatusBadge tone="brand" dot={false} title="Recibe mensajes nuevos a mitad de turno">
                  <Zap className="size-3" /> steer
                </StatusBadge>
              )}
            </div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              conectado {formatRelative(agent.connected_at, now)} · <Mono>{agent.remote}</Mono>
            </div>
          </div>
          <StatusBadge tone={busy ? "info" : "success"}>{busy ? "Trabajando" : "Libre"}</StatusBadge>
        </div>

        <div className="space-y-1.5">
          <div className="flex justify-between text-xs text-muted-foreground">
            <span>Capacidad</span>
            <span className="tabular-nums">
              {busy} / {agent.concurrency} conversaciones
            </span>
          </div>
          <div className="flex gap-1">
            {Array.from({ length: agent.concurrency }, (_, i) => (
              <span
                key={i}
                className={cn(
                  "h-1.5 flex-1 rounded-full",
                  i < busy ? "bg-info" : "bg-muted",
                )}
              />
            ))}
          </div>
        </div>

        {busy > 0 && (
          <ul className="space-y-1">
            {agent.deliveries.map((d) => (
              <li key={d.id}>
                <Link
                  to={`/inbox/${d.conversation_id}`}
                  className="flex items-center gap-2 rounded-lg bg-muted/60 px-2.5 py-1.5 text-sm transition-colors hover:bg-muted"
                >
                  <Bot className="size-3.5 text-info motion-safe:animate-pulse" />
                  <span className="min-w-0 flex-1 truncate">{titleOf?.(d.conversation_id) ?? "Conversación"}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {d.ships} msj · {formatDuration(d.since, now)}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
        <div className="text-[0.68rem] text-muted-foreground">
          sesión <Mono>{agent.session}</Mono>
        </div>
      </CardContent>
    </Card>
  )
}
