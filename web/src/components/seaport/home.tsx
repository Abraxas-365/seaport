import * as React from "react"
import { ChevronRight, FileText, Folder, FolderOpen } from "lucide-react"

import { formatBytes } from "@/lib/format"
import { buildTree, type DiffLine, type TreeNode } from "@/lib/homes"
import { cn } from "@/lib/utils"
import { homeChangeLabel, homeChangeTone, toneClasses, type HomeChange } from "./tone"

/**
 * The files of a harness home as a tree: sizes, and how each differs from
 * the version it's compared with (removed files show struck through).
 */
export function HomeTree({
  files,
  changes,
  selected,
  onSelect,
  className,
}: {
  files: { path: string; size: number }[]
  /** Per path; removed paths may be absent from files. */
  changes?: Map<string, HomeChange>
  selected?: string
  onSelect?: (path: string) => void
  className?: string
}) {
  const all = React.useMemo(() => {
    const have = new Set(files.map((f) => f.path))
    const gone = [...(changes ?? [])].filter(([p, c]) => c === "removed" && !have.has(p)).map(([path]) => ({ path, size: 0 }))
    return buildTree([...files, ...gone])
  }, [files, changes])
  // Big homes open collapsed.
  const [closed, setClosed] = React.useState<Set<string>>(() =>
    files.length > 60 ? new Set(all.filter((n) => n.dir).map((n) => n.path)) : new Set(),
  )
  const toggle = (path: string) =>
    setClosed((s) => {
      const next = new Set(s)
      if (!next.delete(path)) next.add(path)
      return next
    })
  const changedUnder = (n: TreeNode): boolean =>
    n.dir ? n.children.some(changedUnder) : !!changes?.get(n.path)
  const allRemoved = (n: TreeNode): boolean =>
    n.dir ? n.children.every(allRemoved) : changes?.get(n.path) === "removed"
  const row = (n: TreeNode, depth: number): React.ReactNode => {
    const change = n.dir ? undefined : changes?.get(n.path)
    const gone = n.dir ? allRemoved(n) : change === "removed"
    const open = !closed.has(n.path)
    const Icon = n.dir ? (open ? FolderOpen : Folder) : FileText
    return (
      <li key={(n.dir ? "d:" : "f:") + n.path}>
        <button
          type="button"
          onClick={() => (n.dir ? toggle(n.path) : onSelect?.(n.path))}
          aria-expanded={n.dir ? open : undefined}
          aria-current={!n.dir && selected === n.path ? "true" : undefined}
          className={cn(
            "flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left text-sm hover:bg-accent",
            !n.dir && selected === n.path && "bg-accent font-medium",
          )}
          style={{ paddingLeft: `${0.5 + depth * 0.9}rem` }}
        >
          {n.dir ? (
            <ChevronRight className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
          ) : (
            <span className="w-3.5 shrink-0" />
          )}
          <Icon className="size-3.5 shrink-0 text-muted-foreground" />
          <span
            className={cn(
              "min-w-0 truncate font-mono text-[0.8rem]",
              gone && "text-muted-foreground line-through",
            )}
          >
            {n.name}
          </span>
          {n.dir && !open && changedUnder(n) && (
            <span className={cn("size-1.5 shrink-0 rounded-full", toneClasses.warning.dot)} />
          )}
          {change && (
            <span
              className={cn("shrink-0 rounded px-1 text-[0.65rem] font-medium uppercase", toneClasses[homeChangeTone[change]].soft)}
              title={homeChangeLabel[change]}
            >
              {homeChangeLabel[change]}
            </span>
          )}
          {!gone && (
            <span className="ml-auto shrink-0 pl-2 text-xs text-muted-foreground tabular-nums">{formatBytes(n.size)}</span>
          )}
        </button>
        {n.dir && open && <ul>{n.children.map((c) => row(c, depth + 1))}</ul>}
      </li>
    )
  }
  return (
    <ul data-slot="home-tree" className={cn("space-y-px", className)}>
      {all.map((n) => row(n, 0))}
    </ul>
  )
}

/** A line diff (old → new), with line numbers; long unchanged runs fold. */
export function DiffView({ lines, context = 3, className }: { lines: DiffLine[]; context?: number; className?: string }) {
  const [unfolded, setUnfolded] = React.useState<Set<number>>(new Set())
  const near = React.useMemo(() => {
    const keep = new Array<boolean>(lines.length).fill(false)
    lines.forEach((l, i) => {
      if (l.kind === "same") return
      for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true
    })
    return keep
  }, [lines, context])
  if (!lines.some((l) => l.kind !== "same"))
    return <p className={cn("p-3 text-sm text-muted-foreground", className)}>Sin cambios.</p>
  const out: React.ReactNode[] = []
  for (let i = 0; i < lines.length; ) {
    if (!near[i] && !unfolded.has(i)) {
      let j = i
      while (j < lines.length && !near[j]) j++
      const from = i
      out.push(
        <tr key={`fold-${from}`}>
          <td colSpan={3} className="bg-muted/50 px-3 py-0.5">
            <button
              type="button"
              className="text-xs text-muted-foreground hover:text-foreground"
              onClick={() =>
                setUnfolded((s) => {
                  const next = new Set(s)
                  for (let k = from; k < j; k++) next.add(k)
                  return next
                })
              }
            >
              ⋯ {j - from} líneas sin cambios
            </button>
          </td>
        </tr>,
      )
      i = j
      continue
    }
    const l = lines[i]
    out.push(
      <tr
        key={i}
        className={cn(l.kind === "add" && toneClasses.success.soft, l.kind === "del" && toneClasses.danger.soft)}
      >
        <td className="w-10 px-2 text-right text-muted-foreground/70 select-none">{l.a || ""}</td>
        <td className="w-10 px-2 text-right text-muted-foreground/70 select-none">{l.b || ""}</td>
        <td className="px-2 whitespace-pre-wrap break-all text-foreground">
          <span className="select-none text-muted-foreground">{l.kind === "add" ? "+" : l.kind === "del" ? "−" : " "} </span>
          {l.text}
        </td>
      </tr>,
    )
    i++
  }
  return (
    <div data-slot="diff-view" className={cn("overflow-auto rounded-lg border font-mono text-xs leading-5", className)}>
      <table className="w-full border-collapse">
        <tbody>{out}</tbody>
      </table>
    </div>
  )
}
