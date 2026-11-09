import { Bot, Terminal } from "lucide-react"
import { toast } from "sonner"
import { Copy } from "lucide-react"

import { useQuery } from "@tanstack/react-query"

import { api } from "@/lib/api"
import { keys } from "@/lib/query"
import { flatten, useCrew, useInbox, useNow } from "@/lib/hooks"
import { conversationName } from "@/lib/format"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import type { Agent } from "@/lib/types"
import { AgentCard, EmptyState, ErrorState, PageHeader, Section } from "@/components/seaport"

export function CrewPage() {
  const crew = useCrew()
  const now = useNow(1000)
  const inbox = useInbox({})
  const titles = new Map(flatten(inbox.data).map((c) => [c.id, conversationName(c)]))
  const agents = crew.data?.agents ?? []
  const working = agents.reduce((n, a) => n + a.deliveries.length, 0)
  const capacity = agents.reduce((n, a) => n + a.concurrency, 0)

  return (
    <div className="min-h-0 flex-1 overflow-y-auto scrollbar-thin">
      <PageHeader
        title="Tripulación"
        description="Las conexiones de tus agentes y lo que están atendiendo ahora."
      >
        {agents.length > 0 && (
          <div className="flex gap-6 text-sm">
            <Stat label="Agentes" value={new Set(agents.map((a) => a.agent_id)).size} />
            <Stat label="Conexiones" value={agents.length} />
            <Stat label="En curso" value={working} />
            <Stat label="Capacidad" value={`${working}/${capacity}`} />
          </div>
        )}
      </PageHeader>
      <div className="mx-auto max-w-5xl space-y-6 p-6">
        {crew.isPending ? (
          <div className="grid gap-4 md:grid-cols-2">
            <Skeleton className="h-44 rounded-xl" />
            <Skeleton className="h-44 rounded-xl" />
          </div>
        ) : crew.isError ? (
          <ErrorState error={crew.error} onRetry={() => crew.refetch()} />
        ) : agents.length === 0 ? (
          <EmptyState
            icon={Bot}
            title="Nadie trabajando en el puerto"
            description="Conecta un agente desde tu máquina; los mensajes permitidos lo despertarán."
          />
        ) : (
          byAgent(agents).map(([name, conns]) => (
            <Section
              key={name}
              title={<span className="font-mono">@{name}</span>}
              description={conns.length === 1 ? "1 conexión" : `${conns.length} conexiones`}
            >
              <div className="grid gap-4 md:grid-cols-2">
                {conns.map((a) => (
                  <AgentCard key={a.session} agent={a} now={now} titleOf={(id) => titles.get(id)} />
                ))}
              </div>
            </Section>
          ))
        )}
        <ConnectCard />
      </div>
    </div>
  )
}

/** Connections grouped by the library agent they work as, by name. */
function byAgent(conns: Agent[]): [string, Agent[]][] {
  const groups = new Map<string, Agent[]>()
  for (const c of conns) groups.set(c.agent_name, [...(groups.get(c.agent_name) ?? []), c])
  return [...groups].sort(([a], [b]) => a.localeCompare(b))
}

function Stat({ label, value }: { label: string; value: number | string }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="font-heading text-lg font-semibold tabular-nums">{value}</div>
    </div>
  )
}

function ConnectCard() {
  const config = useQuery({ queryKey: keys.config, queryFn: api.config, staleTime: Infinity })
  // Open mode has no tokens: the connector names its agent.
  const cmd =
    config.data?.auth === "none"
      ? `seaport connect -server ${window.location.origin} -agent <nombre> -- rness --acp`
      : `SEAPORT_CREW_TOKEN=… seaport connect -server ${window.location.origin} -- rness --acp`
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Terminal className="size-4" /> Conectar un agente
        </CardTitle>
        <CardDescription>
          Corre esto donde vive tu agente. Trabaja como el agente de su token, en los muelles asignados a él; se
          conecta hacia afuera (no abre puertos) y se despierta con cada mensaje.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="relative">
          <pre className="scrollbar-thin overflow-x-auto rounded-lg bg-muted p-3 pr-12 font-mono text-xs">{cmd}</pre>
          <Button
            variant="ghost"
            size="icon-sm"
            className="absolute top-2 right-2"
            aria-label="Copiar"
            onClick={() => navigator.clipboard.writeText(cmd).then(() => toast.success("Copiado"))}
          >
            <Copy />
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
