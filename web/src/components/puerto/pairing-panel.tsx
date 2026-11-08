import * as React from "react"
import { QRCodeSVG } from "qrcode.react"
import { RefreshCw, Smartphone } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"

/**
 * WhatsApp pairing: the QR (refreshed live by dock.updated events), the
 * steps on the phone, and a retry when pairing failed.
 */
export function PairingPanel({
  qr,
  failed,
  onRetry,
  retrying,
  className,
}: {
  qr?: string
  failed?: boolean
  onRetry?: () => void
  retrying?: boolean
  className?: string
}) {
  return (
    <div className={cn("grid items-center gap-6 sm:grid-cols-[auto_1fr]", className)}>
      <div className="relative mx-auto flex size-56 items-center justify-center rounded-2xl bg-white p-3 shadow-sm ring-1 ring-border">
        {qr ? (
          <QRCodeSVG value={qr} size={200} level="L" marginSize={0} className="size-full" />
        ) : failed ? (
          <div className="flex flex-col items-center gap-3 text-center text-sm text-neutral-600">
            <span>El código expiró</span>
            {onRetry && (
              <Button size="sm" variant="outline" onClick={onRetry} disabled={retrying}>
                {retrying ? <Spinner /> : <RefreshCw />} Generar otro
              </Button>
            )}
          </div>
        ) : (
          <>
            <Skeleton className="size-full rounded-lg bg-neutral-100" />
            <Spinner className="absolute size-6 text-neutral-400" />
          </>
        )}
      </div>
      <ol className="space-y-3 text-sm">
        <li className="flex items-center gap-2 font-medium">
          <Smartphone className="size-4 text-muted-foreground" /> En tu teléfono
        </li>
        <Step n={1}>
          Abre <b>WhatsApp</b> → <b>Ajustes</b>
        </Step>
        <Step n={2}>
          Toca <b>Dispositivos vinculados</b> → <b>Vincular un dispositivo</b>
        </Step>
        <Step n={3}>Escanea este código. Se renueva solo cada ~20 segundos.</Step>
      </ol>
    </div>
  )
}

function Step({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <li className="flex gap-3 text-muted-foreground">
      <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-secondary text-[0.7rem] font-semibold text-secondary-foreground tabular-nums">
        {n}
      </span>
      <span>{children}</span>
    </li>
  )
}
