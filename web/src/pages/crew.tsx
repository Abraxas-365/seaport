import { Bot, Terminal } from "lucide-react"
import { toast } from "sonner"
import { Copy } from "lucide-react"

import { flatten, useCrew, useInbox, useNow } from "@/lib/hooks"
import { conversationName } from "@/lib/format"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { AgentCard, EmptyState, ErrorState, PageHeader } from "@/components/puerto"

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
        description="Los agentes conectados al puerto y lo que están atendiendo ahora."
      >
        {agents.length > 0 && (
          <div className="flex gap-6 text-sm">
            <Stat label="Agentes" value={agents.length} />
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
          <div className="grid gap-4 md:grid-cols-2">
            {agents.map((a) => (
              <AgentCard key={a.session} agent={a} now={now} titleOf={(id) => titles.get(id)} />
            ))}
          </div>
        )}
        <ConnectCard />
      </div>
    </div>
  )
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
  const cmd = `PUERTO_CREW_TOKEN=… puerto connect -server ${window.location.origin} -- rness --acp`
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Terminal className="size-4" /> Conectar un agente
        </CardTitle>
        <CardDescription>
          Corre esto donde vive tu agente. Se conecta hacia afuera (no abre puertos) y se despierta con cada mensaje.
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
