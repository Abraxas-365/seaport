import * as React from "react"
import { Check } from "lucide-react"

import { cn } from "@/lib/utils"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { toneClasses, type Tone } from "./tone"

export interface ChoiceOption<T extends string> {
  value: T
  label: string
  description?: string
  icon?: React.ComponentType<{ className?: string }>
  tone?: Tone
  /** Can't be chosen now (the description says why). */
  disabled?: boolean
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
            disabled={o.disabled}
            className={cn(
              "flex-1 gap-1.5 px-3 text-muted-foreground sm:flex-none",
              // The chosen one stands out: filled, ringed and bold (a
              // grey fill alone reads as hover).
              "data-[state=on]:font-semibold data-[state=on]:ring-1 data-[state=on]:ring-inset",
              o.tone && o.tone !== "neutral"
                ? [toneClasses[o.tone].on, toneClasses[o.tone].ring]
                : "data-[state=on]:border-primary/60 data-[state=on]:bg-primary/10 data-[state=on]:text-primary data-[state=on]:ring-primary/40 data-[state=on]:hover:bg-primary/15 data-[state=on]:hover:text-primary",
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
  "aria-label": ariaLabel,
}: {
  value: T
  onChange: (v: T) => void
  options: ChoiceOption<T>[]
  disabled?: boolean
  className?: string
  "aria-label"?: string
}) {
  return (
    <div role="radiogroup" aria-label={ariaLabel} className={cn("grid gap-2 sm:grid-cols-3", className)}>
      {options.map((o) => {
        const Icon = o.icon
        const active = o.value === value
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled || o.disabled}
            onClick={() => onChange(o.value)}
            className={cn(
              "relative flex flex-col items-start gap-1.5 rounded-xl border bg-card p-3 text-left text-sm transition-colors outline-none",
              "hover:bg-accent/50 focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50",
              active && "border-primary bg-primary/10 ring-1 ring-primary hover:bg-primary/10",
            )}
          >
            {active && (
              <span className="absolute top-2.5 right-2.5 inline-flex size-4 items-center justify-center rounded-full bg-primary text-primary-foreground">
                <Check className="size-3" strokeWidth={3} />
              </span>
            )}
            <span className="flex items-center gap-2 pr-5 font-medium">
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
