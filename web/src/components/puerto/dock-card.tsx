import { Link } from "react-router"
import { ChevronRight } from "lucide-react"

import { formatRelative, prettyHandle } from "@/lib/format"
import type { Dock } from "@/lib/types"
import { Card, CardContent } from "@/components/ui/card"
import { ChannelIcon, channelLabel } from "./brand"
import { StatusBadge } from "./status"
import { dockLabel, dockTone } from "./tone"

/** A dock in the docks grid. */
export function DockCard({ dock, now }: { dock: Dock; now: number }) {
  return (
    <Link to={`/docks/${dock.id}`} className="group/dock rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50">
      <Card className="gap-0 py-0 transition-shadow group-hover/dock:shadow-md group-hover/dock:ring-primary/20">
        <CardContent className="flex items-center gap-3 p-4">
          <ChannelIcon channel={dock.channel} size="lg" />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{dock.name || channelLabel[dock.channel]}</span>
            </div>
            <div className="mt-0.5 truncate text-xs text-muted-foreground">
              {dock.address ? prettyHandle(dock.address) : channelLabel[dock.channel]} · desde{" "}
              {formatRelative(dock.created_at, now).replace(/^hace /, "hace ")}
            </div>
          </div>
          <StatusBadge tone={dockTone[dock.status]} pulse={dock.status === "linking"}>
            {dockLabel[dock.status]}
          </StatusBadge>
          <ChevronRight className="size-4 text-muted-foreground transition-transform group-hover/dock:translate-x-0.5" />
        </CardContent>
      </Card>
    </Link>
  )
}
