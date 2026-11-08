import * as React from "react"
import { SendHorizontal } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import { Spinner } from "@/components/ui/spinner"

/**
 * Message box at the bottom of a conversation. Enter sends, Shift+Enter
 * breaks the line; grows up to ~8 lines.
 */
export function Composer({
  onSend,
  disabled,
  disabledReason,
  placeholder = "Escribe como el agente…",
  className,
}: {
  onSend: (text: string) => Promise<unknown>
  disabled?: boolean
  disabledReason?: React.ReactNode
  placeholder?: string
  className?: string
}) {
  const [text, setText] = React.useState("")
  const [sending, setSending] = React.useState(false)
  const ref = React.useRef<HTMLTextAreaElement>(null)

  React.useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = "auto"
    el.style.height = Math.min(el.scrollHeight, 200) + "px"
  }, [text])

  async function submit() {
    const t = text.trim()
    if (!t || sending || disabled) return
    setSending(true)
    try {
      await onSend(t)
      setText("")
    } finally {
      setSending(false)
      ref.current?.focus()
    }
  }

  if (disabled && disabledReason) {
    return (
      <div className={cn("border-t bg-background px-4 py-3 text-center text-sm text-muted-foreground", className)}>
        {disabledReason}
      </div>
    )
  }

  return (
    <form
      className={cn("border-t bg-background p-3", className)}
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <div className="flex items-end gap-2 rounded-2xl border bg-card p-1.5 pl-3 shadow-xs focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/30">
        <textarea
          ref={ref}
          rows={1}
          value={text}
          disabled={disabled || sending}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault()
              void submit()
            }
          }}
          className="scrollbar-thin max-h-[200px] min-h-8 flex-1 resize-none bg-transparent py-1.5 text-sm outline-none placeholder:text-muted-foreground"
        />
        <Button type="submit" size="icon" className="rounded-xl" disabled={!text.trim() || sending || disabled} aria-label="Enviar">
          {sending ? <Spinner /> : <SendHorizontal />}
        </Button>
      </div>
      <div className="mt-1.5 hidden px-2 text-[0.68rem] text-muted-foreground sm:block">
        <Kbd>Enter</Kbd> envía · <Kbd>Shift</Kbd>+<Kbd>Enter</Kbd> nueva línea · se envía por el canal del dock como si
        respondiera el agente
      </div>
    </form>
  )
}
