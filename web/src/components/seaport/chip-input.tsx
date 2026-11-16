import * as React from "react"
import { X } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * A list of short values as chips: Enter, comma, space or a paste adds
 * (split on whitespace and commas), Backspace on an empty field removes the
 * last, × removes one. Duplicates are dropped. For apt packages, egress
 * hosts: things typed one at a time, read at a glance.
 */
export function ChipInput({
  id,
  value,
  onChange,
  placeholder,
  readOnly,
  mono = true,
  "aria-label": ariaLabel,
  "aria-describedby": describedBy,
  className,
}: {
  id?: string
  value: string[]
  onChange: (next: string[]) => void
  placeholder?: string
  readOnly?: boolean
  mono?: boolean
  "aria-label"?: string
  "aria-describedby"?: string
  className?: string
}) {
  const [text, setText] = React.useState("")
  const inputRef = React.useRef<HTMLInputElement>(null)
  const chips = value.map((v) => v.trim()).filter(Boolean)
  const add = (raw: string) => {
    const parts = raw
      .split(/[\s,]+/)
      .map((p) => p.trim())
      .filter(Boolean)
    if (parts.length === 0) return
    const next = [...chips]
    for (const p of parts) if (!next.includes(p)) next.push(p)
    if (next.length !== chips.length) onChange(next)
  }
  const remove = (i: number) => onChange(chips.filter((_, j) => j !== i))
  const commit = () => {
    if (text.trim()) add(text)
    setText("")
  }
  return (
    <div
      data-slot="chip-input"
      className={cn(
        "flex min-h-8 flex-wrap items-center gap-1.5 rounded-lg border border-input px-2 py-1 text-sm dark:bg-input/30",
        !readOnly && "cursor-text focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50",
        className
      )}
      onClick={() => inputRef.current?.focus()}
    >
      {chips.map((chip, i) => (
        <span
          key={chip}
          className={cn(
            "inline-flex h-6 items-center gap-1 rounded-md bg-muted px-2 text-xs text-foreground",
            mono && "font-mono"
          )}
        >
          {chip}
          {!readOnly && (
            <button
              type="button"
              aria-label={`Quitar ${chip}`}
              className="-mr-1 rounded-sm p-0.5 text-muted-foreground hover:bg-foreground/10 hover:text-foreground"
              onClick={(e) => {
                e.stopPropagation()
                remove(i)
              }}
            >
              <X className="size-3" />
            </button>
          )}
        </span>
      ))}
      {!readOnly && (
        <input
          ref={inputRef}
          id={id}
          aria-label={ariaLabel}
          aria-describedby={describedBy}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === "," || e.key === " ") {
              e.preventDefault()
              commit()
            } else if (e.key === "Backspace" && text === "" && chips.length > 0) {
              e.preventDefault()
              remove(chips.length - 1)
            }
          }}
          onBlur={commit}
          onPaste={(e) => {
            const pasted = e.clipboardData.getData("text")
            if (/[\s,]/.test(pasted)) {
              e.preventDefault()
              add(text + pasted)
              setText("")
            }
          }}
          placeholder={chips.length === 0 ? placeholder : undefined}
          autoComplete="off"
          spellCheck={false}
          className={cn(
            "h-6 min-w-24 flex-1 bg-transparent outline-none placeholder:text-muted-foreground/50",
            mono && "font-mono"
          )}
        />
      )}
      {readOnly && chips.length === 0 && <span className="text-muted-foreground">—</span>}
    </div>
  )
}
