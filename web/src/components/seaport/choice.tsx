import * as React from "react"

import { cn } from "@/lib/utils"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { toneClasses, type Tone } from "./tone"

export interface ChoiceOption<T extends string> {
  value: T
  label: string
  description?: string
  icon?: React.ComponentType<{ className?: string }>
  tone?: Tone
}

/**
 * A segmented single choice (customs default, access override, group
 * mode). Always has a value; options can carry a tone and an icon.
 */
export function SegmentedChoice<T extends string>({
  value,
  onChange,
  options,
  disabled,
  size = "default",
  className,
  "aria-label": ariaLabel,
}: {
  value: T
  onChange: (v: T) => void
  options: ChoiceOption<T>[]
  disabled?: boolean
  size?: "sm" | "default"
  className?: string
  "aria-label"?: string
}) {
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size={size}
      value={value}
      onValueChange={(v) => v && onChange(v as T)}
      disabled={disabled}
      aria-label={ariaLabel}
      className={cn("w-full sm:w-auto", className)}
    >
      {options.map((o) => {
        const Icon = o.icon
        return (
          <ToggleGroupItem
            key={o.value}
            value={o.value}
            title={o.description}
            className={cn(
              "flex-1 gap-1.5 px-3 sm:flex-none",
              o.tone && toneClasses[o.tone].on,
              "data-[state=on]:font-semibold",
            )}
          >
            {Icon && <Icon className={cn("size-3.5", value === o.value && o.tone && toneClasses[o.tone].text)} />}
            {o.label}
          </ToggleGroupItem>
        )
      })}
    </ToggleGroup>
  )
}

/** Radio-card choice with descriptions, for settings that need explaining. */
export function ChoiceCards<T extends string>({
  value,
  onChange,
  options,
  disabled,
  className,
}: {
  value: T
  onChange: (v: T) => void
  options: ChoiceOption<T>[]
  disabled?: boolean
  className?: string
}) {
  return (
    <div role="radiogroup" className={cn("grid gap-2 sm:grid-cols-3", className)}>
      {options.map((o) => {
        const Icon = o.icon
        const active = o.value === value
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled}
            onClick={() => onChange(o.value)}
            className={cn(
              "flex flex-col items-start gap-1.5 rounded-xl border bg-card p-3 text-left text-sm transition-colors outline-none",
              "hover:bg-accent/50 focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50",
              active && "border-primary/50 bg-primary/5 ring-1 ring-primary/30 hover:bg-primary/5",
            )}
          >
            <span className="flex items-center gap-2 font-medium">
              {Icon && (
                <span
                  className={cn(
                    "inline-flex size-6 items-center justify-center rounded-md",
                    toneClasses[o.tone ?? "neutral"].soft,
                  )}
                >
                  <Icon className="size-3.5" />
                </span>
              )}
              {o.label}
            </span>
            {o.description && <span className="text-xs leading-relaxed text-muted-foreground">{o.description}</span>}
          </button>
        )
      })}
    </div>
  )
}
