import * as React from "react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { buttonVariants } from "@/components/ui/button"

/**
 * Confirmation for consequential actions. `confirmText` makes the user
 * type a word (e.g. the dock name) before a destructive action.
 */
export function ConfirmAction({
  trigger,
  title,
  description,
  actionLabel,
  destructive,
  confirmText,
  onConfirm,
}: {
  trigger: React.ReactNode
  title: string
  description: React.ReactNode
  actionLabel: string
  destructive?: boolean
  confirmText?: string
  onConfirm: () => Promise<unknown>
}) {
  const [open, setOpen] = React.useState(false)
  const [typed, setTyped] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const ok = !confirmText || typed.trim() === confirmText

  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setTyped("")
      }}
    >
      <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <div className="space-y-2">{description}</div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        {confirmText && (
          <div className="space-y-1.5 text-sm">
            <label htmlFor="confirm-text" className="text-muted-foreground">
              Escribe <b className="text-foreground">{confirmText}</b> para confirmar
            </label>
            <Input id="confirm-text" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            disabled={!ok || busy}
            className={cn(destructive && buttonVariants({ variant: "destructive" }))}
            onClick={async (e) => {
              e.preventDefault()
              setBusy(true)
              try {
                await onConfirm()
                setOpen(false)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy && <Spinner />}
            {actionLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
