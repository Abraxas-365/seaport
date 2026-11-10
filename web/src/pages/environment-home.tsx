import * as React from "react"
import {
  Download,
  FilePlus,
  FolderCog,
  FolderOpen,
  Pencil,
  ShieldAlert,
  Trash2,
  TriangleAlert,
  Upload,
  X,
} from "lucide-react"
import { toast } from "sonner"

import { api } from "@/lib/api"
import { gzip, readArchive, readTar, writeTar, type ArchiveFile } from "@/lib/archive"
import { formatBytes, formatFull, plural } from "@/lib/format"
import {
  asText,
  changes as diffChanges,
  checkStaged,
  collect,
  diffLines,
  encodeText,
  findSecret,
  homePathProblem,
  sha256,
  shebang,
  walkDropped,
  type Incoming,
  type Problems,
  type Skip,
  type Staged,
} from "@/lib/homes"
import { useHome, useHomeFile, useHomeRules, useUploadHome } from "@/lib/hooks"
import type { Harness, Home, HomeRules } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import {
  ConfirmAction,
  DiffView,
  EmptyState,
  HomeTree,
  LogView,
  Mono,
  SegmentedChoice,
  StatusBadge,
  homeChangeLabel,
  homeChangeTone,
  toneClasses,
  type HomeChange,
} from "@/components/seaport"
import { harnessLabel } from "@/pages/environments"

/** Files the viewer fetches (bigger: download the archive). */
const maxView = 1 << 20
/** Files the editor opens. */
const maxEdit = 256 << 10
const archiveName = /\.(zip|tar|tgz|tar\.gz)$/i

/**
 * An environment's harness config (spec.home): what's in it, how it
 * differs from the version compared with, and (writable) uploading a
 * folder or archive, editing text files. Every change uploads a new home
 * and sets the draft's `home`; saving the spec makes the version.
 */
export function HomeSection({
  harness,
  home,
  compare,
  compareLabel,
  readOnly,
  onChange,
}: {
  harness: Harness
  /** The draft's (or viewed version's) home; "" none. */
  home: string
  /** The home to diff with (undefined: nothing to compare). */
  compare?: string
  /** "la v3 guardada", "v2". */
  compareLabel?: string
  readOnly: boolean
  onChange: (digest: string) => void
}) {
  const rules = useHomeRules()
  const current = useHome(home)
  const before = useHome(compare && compare !== home ? compare : "")
  const upload = useUploadHome()
  const [selected, setSelected] = React.useState("")
  const [staging, setStaging] = React.useState<Pending | null>(null)
  const [busy, setBusy] = React.useState("")
  const [editing, setEditing] = React.useState<{ path: string; text: string; isNew: boolean } | null>(null)
  const [dragging, setDragging] = React.useState(false)
  const r = rules.data?.[harness]

  const changes = React.useMemo(() => {
    if (compare === undefined || compare === home) return undefined
    const hashes = (h: Home | undefined) => new Map((h?.files ?? []).map((f) => [f.path, f.sha256]))
    if (compare && !before.data) return undefined
    if (home && !current.data) return undefined
    return diffChanges(hashes(before.data), hashes(current.data))
  }, [compare, home, before.data, current.data])

  // Offered files → what goes in the home, problems, and changes vs the current one.
  const stage = async (what: string, offered: () => Promise<{ items: Incoming[]; unwrap: boolean }>) => {
    if (!r) return
    setBusy(what)
    try {
      const { items, unwrap } = await offered()
      const { files, skipped } = await collect(r, harness, items, { unwrap })
      const hashes = new Map<string, string>()
      for (const [p, f] of files) hashes.set(p, await sha256(f.data))
      const now = new Map((current.data?.files ?? []).map((f) => [f.path, f.sha256]))
      setStaging({ files, skipped, problems: checkStaged(r, harness, files), changes: diffChanges(now, hashes) })
    } catch (e) {
      toast.error("No se pudo leer", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy("")
    }
  }

  const fromArchive = (file: File) =>
    stage(`Leyendo ${file.name}…`, async () => {
      if (r && file.size > r.max_upload * 4) throw new Error(`${file.name} pesa ${formatBytes(file.size)}: demasiado`)
      const read = await readArchive(new Uint8Array(await file.arrayBuffer()))
      return {
        items: [
          ...read.files.map((f) => ({ path: f.path, size: f.data.length, exec: f.exec, read: async () => f.data })),
          ...read.others.map((path) => ({ path, size: 0, exec: false, other: true, read: async () => new Uint8Array() })),
        ],
        unwrap: true,
      }
    })

  const fromFolder = (list: FileList) =>
    stage("Leyendo la carpeta…", async () => ({
      items: Array.from(list, (f) => ({
        path: f.webkitRelativePath || f.name,
        size: f.size,
        exec: false,
        read: async () => new Uint8Array(await f.arrayBuffer()),
      })),
      unwrap: true,
    }))

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    setDragging(false)
    if (readOnly || !r) return
    const entries = Array.from(e.dataTransfer.items)
      .map((i) => i.webkitGetAsEntry())
      .filter((x): x is FileSystemEntry => !!x)
    const files = Array.from(e.dataTransfer.files)
    if (entries.length === 1 && entries[0].isFile && archiveName.test(entries[0].name) && files[0]) return fromArchive(files[0])
    if (entries.length === 0) return
    stage("Leyendo la carpeta…", async () => ({ items: await walkDropped(r, entries), unwrap: false }))
  }

  /** Uploads files as the new home and puts it in the draft. */
  const put = async (files: ArchiveFile[]) => {
    if (files.length === 0) {
      onChange("")
      return
    }
    const tgz = await gzip(writeTar(files))
    if (r && tgz.length > r.max_upload) throw new Error(`comprimido pesa ${formatBytes(tgz.length)} (máximo ${formatBytes(r.max_upload)})`)
    const h = await upload.mutateAsync({ harness, tgz })
    onChange(h.digest)
    return h
  }

  /** Rewrites the current home's files (an edit, a new file, a removal). */
  const rewrite = async (what: string, change: (files: Map<string, ArchiveFile>) => void) => {
    setBusy(what)
    try {
      const files = new Map<string, ArchiveFile>()
      if (home) for (const f of readTar(await api.homeArchive(home)).files) files.set(f.path, f)
      change(files)
      await put([...files.values()])
      return true
    } catch (e) {
      if (!upload.isError) toast.error("No se pudo cambiar la configuración", { description: e instanceof Error ? e.message : String(e) })
      return false
    } finally {
      setBusy("")
    }
  }

  const folderInput = React.useRef<HTMLInputElement>(null)
  const archiveInput = React.useRef<HTMLInputElement>(null)
  const sel = current.data?.files.find((f) => f.path === selected)
  const selChange = selected ? changes?.get(selected) : undefined
  const counts = changes && countChanges(changes)

  if (rules.isPending || (home && current.isPending)) return <Skeleton className="h-40 rounded-xl" />
  if (!r) return <p className="text-sm text-destructive">No se pudieron leer las reglas de la configuración.</p>

  return (
    <div
      className={cn("space-y-3 rounded-xl", dragging && "ring-2 ring-primary/40 ring-offset-2 ring-offset-background")}
      onDragOver={(e) => {
        if (readOnly) return
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
      }}
      onDrop={onDrop}
    >
      <input
        ref={(el) => {
          folderInput.current = el
          el?.setAttribute("webkitdirectory", "")
        }}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          if (e.target.files?.length) fromFolder(e.target.files)
          e.target.value = ""
        }}
      />
      <input
        ref={archiveInput}
        type="file"
        accept=".zip,.tar,.tgz,.gz,application/zip,application/gzip,application/x-tar"
        hidden
        onChange={(e) => {
          const f = e.target.files?.[0]
          if (f) fromArchive(f)
          e.target.value = ""
        }}
      />

      {current.data && current.data.harness !== harness && (
        <Notice tone="danger" icon={TriangleAlert}>
          Esta configuración es de {harnessLabel[current.data.harness]} y el entorno usa {harnessLabel[harness]}: el build
          fallará. Sube una para {harnessLabel[harness]} o quítala.
        </Notice>
      )}
      {current.isError && <p className="text-sm text-destructive">No se pudo leer la configuración: {current.error.message}</p>}

      {!home ? (
        <div className="rounded-xl border border-dashed">
          <EmptyState
            icon={FolderCog}
            title={compare ? "Sin configuración (al guardar se quita)" : "Sin configuración propia"}
            description={
              readOnly ? (
                <>Las instancias usan la configuración de la base de {harnessLabel[harness]}.</>
              ) : (
                <>
                  Suelta aquí tu <Mono>~/{r.root}</Mono> (o un .zip / .tar.gz). Solo se sube la configuración:{" "}
                  {[...r.files, ...r.dirs.map((d) => d + "/")].join(", ")}. Sesiones, historial y credenciales quedan
                  fuera.
                </>
              )
            }
            action={
              !readOnly && (
                <Actions
                  busy={busy}
                  onFolder={() => folderInput.current?.click()}
                  onArchive={() => archiveInput.current?.click()}
                  onNew={() => setEditing({ path: "", text: "", isNew: true })}
                />
              )
            }
          />
        </div>
      ) : (
        current.data && (
          <>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <StatusBadge tone="brand" dot={false}>
                ~/{r.root}
              </StatusBadge>
              <span className="text-muted-foreground">
                {plural(current.data.files.length, "archivo", "archivos")} · {formatBytes(current.data.size)} · subida por{" "}
                {current.data.created_by || "—"} {formatFull(current.data.created_at)}
              </span>
              <Mono className="truncate" title={current.data.digest}>
                {current.data.digest.slice(0, 19)}
              </Mono>
              <div className="ml-auto flex items-center gap-1">
                <Button variant="ghost" size="sm" asChild>
                  <a href={api.homeArchiveURL(home)} download={`${r.root.replace(/^\./, "")}-home.tar`}>
                    <Download /> Descargar
                  </a>
                </Button>
                {!readOnly && (
                  <Button variant="ghost" size="sm" onClick={() => onChange("")}>
                    <X /> Quitar
                  </Button>
                )}
              </div>
            </div>
            {compare !== undefined && compareLabel && (
              <p className="text-xs text-muted-foreground">
                {compare === home
                  ? `Igual que ${compareLabel}.`
                  : counts
                    ? `Respecto a ${compareLabel}: ${counts}.`
                    : `Comparando con ${compareLabel}…`}
              </p>
            )}
            <div className="grid min-h-64 gap-3 md:grid-cols-[minmax(14rem,2fr)_3fr]">
              <div className="max-h-[28rem] overflow-y-auto rounded-lg border p-1.5 scrollbar-thin">
                <HomeTree files={current.data.files} changes={changes} selected={selected} onSelect={setSelected} />
              </div>
              <div className="min-w-0">
                {selected ? (
                  <FileView
                    key={`${selected}:${selChange ?? ""}`}
                    home={home}
                    compare={compare}
                    path={selected}
                    size={sel?.size ?? 0}
                    exec={!!sel?.exec}
                    change={selChange}
                    busy={busy}
                    readOnly={readOnly}
                    onEdit={(text) => setEditing({ path: selected, text, isNew: false })}
                    onRemove={() => rewrite(`Quitando ${selected}…`, (m) => void m.delete(selected)).then(() => setSelected(""))}
                  />
                ) : (
                  <div className="flex h-full min-h-40 items-center justify-center rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground">
                    Elige un archivo para verlo{changes?.size ? " o ver sus cambios" : ""}.
                  </div>
                )}
              </div>
            </div>
            {!readOnly && (
              <div className="flex flex-wrap items-center gap-2 border-t pt-3">
                <span className="mr-auto text-xs text-muted-foreground">
                  Reemplázala soltando otra carpeta o archivo aquí.
                </span>
                <Actions
                  busy={busy}
                  replace
                  onFolder={() => folderInput.current?.click()}
                  onArchive={() => archiveInput.current?.click()}
                  onNew={() => setEditing({ path: "", text: "", isNew: true })}
                />
              </div>
            )}
          </>
        )
      )}

      {staging && (
        <StageDialog
          rules={r}
          harness={harness}
          pending={staging}
          replacing={!!home}
          busy={upload.isPending}
          error={upload.error?.message}
          onCancel={() => {
            setStaging(null)
            upload.reset()
          }}
          onUpload={async () => {
            try {
              const h = await put([...staging.files].map(([path, f]) => ({ path, data: f.data, exec: f.exec })))
              setStaging(null)
              setSelected("")
              if (h) toast.success("Configuración lista", { description: "Guarda la versión para usarla." })
            } catch (e) {
              if (!(e instanceof Error) || upload.isError) return
              toast.error("No se pudo subir", { description: e.message })
            }
          }}
        />
      )}
      {editing && (
        <EditDialog
          rules={r}
          harness={harness}
          initial={editing}
          taken={new Set(current.data?.files.map((f) => f.path))}
          busy={!!busy}
          onClose={() => setEditing(null)}
          onApply={async (path, text) => {
            const prev = editing.isNew ? undefined : current.data?.files.find((f) => f.path === path)
            const data = encodeText(text)
            const ok = await rewrite(`Guardando ${path}…`, (m) =>
              void m.set(path, { path, data, exec: prev?.exec ?? shebang(data) }),
            )
            if (ok) {
              setEditing(null)
              setSelected(path)
              toast.success(`${path} actualizado`, { description: "Guarda la versión para usarlo." })
            }
          }}
        />
      )}
    </div>
  )
}

interface Pending {
  files: Staged
  skipped: Skip[]
  problems: Problems
  /** Against the current home. */
  changes: Map<string, HomeChange>
}

function countChanges(c: Map<string, HomeChange>) {
  if (c.size === 0) return "sin cambios"
  const n = { added: 0, changed: 0, removed: 0 }
  for (const v of c.values()) n[v]++
  return [
    n.added && plural(n.added, "nuevo", "nuevos"),
    n.changed && plural(n.changed, "cambiado", "cambiados"),
    n.removed && plural(n.removed, "quitado", "quitados"),
  ]
    .filter(Boolean)
    .join(", ")
}

function Actions({
  busy,
  replace,
  onFolder,
  onArchive,
  onNew,
}: {
  busy: string
  replace?: boolean
  onFolder: () => void
  onArchive: () => void
  onNew: () => void
}) {
  return (
    <div className="flex flex-wrap items-center justify-center gap-2">
      {busy && (
        <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <Spinner /> {busy}
        </span>
      )}
      <Button size="sm" variant={replace ? "outline" : "default"} disabled={!!busy} onClick={onFolder}>
        <FolderOpen /> {replace ? "Reemplazar con carpeta" : "Subir carpeta"}
      </Button>
      <Button size="sm" variant="outline" disabled={!!busy} onClick={onArchive}>
        <Upload /> .zip / .tar.gz
      </Button>
      <Button size="sm" variant="ghost" disabled={!!busy} onClick={onNew}>
        <FilePlus /> Nuevo archivo
      </Button>
    </div>
  )
}

function Notice({
  tone,
  icon: Icon,
  children,
}: {
  tone: "danger" | "warning"
  icon: React.ComponentType<{ className?: string }>
  children: React.ReactNode
}) {
  return (
    <div className={cn("flex gap-2 rounded-lg px-3 py-2 text-sm", toneClasses[tone].soft)}>
      <Icon className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 space-y-1">{children}</div>
    </div>
  )
}

/** One file: its text, or its diff against the compared home. */
function FileView({
  home,
  compare,
  path,
  size,
  exec,
  change,
  busy,
  readOnly,
  onEdit,
  onRemove,
}: {
  home: string
  compare?: string
  path: string
  size: number
  exec: boolean
  change?: HomeChange
  busy: string
  readOnly: boolean
  onEdit: (text: string) => void
  onRemove: () => void
}) {
  const removed = change === "removed"
  const [mode, setMode] = React.useState<"diff" | "file">(change === "changed" ? "diff" : "file")
  const now = useHomeFile(home, path, !removed && size <= maxView)
  const old = useHomeFile(compare ?? "", path, !!compare && (removed || (change === "changed" && mode === "diff")))
  const text = removed ? old.data : now.data
  const diff = React.useMemo(
    () => (mode === "diff" && change === "changed" && typeof now.data === "string" && typeof old.data === "string"
      ? diffLines(old.data, now.data)
      : undefined),
    [mode, change, now.data, old.data],
  )
  const loading = (removed ? old : now).isPending || (mode === "diff" && change === "changed" && old.isPending)
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Mono className="min-w-0 truncate text-sm text-foreground">{path}</Mono>
        {change && (
          <StatusBadge tone={homeChangeTone[change]} dot={false}>
            {homeChangeLabel[change]}
          </StatusBadge>
        )}
        {exec && (
          <StatusBadge tone="neutral" dot={false} title="Se instala ejecutable (0755)">
            ejecutable
          </StatusBadge>
        )}
        {!removed && <span className="text-xs text-muted-foreground">{formatBytes(size)}</span>}
        <div className="ml-auto flex items-center gap-1">
          {change === "changed" && (
            <SegmentedChoice
              aria-label="Vista"
              size="sm"
              value={mode}
              onChange={(v) => setMode(v as "diff" | "file")}
              options={[
                { value: "diff", label: "Cambios" },
                { value: "file", label: "Archivo" },
              ]}
            />
          )}
          {!readOnly && !removed && typeof now.data === "string" && size <= maxEdit && (
            <Button variant="ghost" size="icon-sm" aria-label={`Editar ${path}`} disabled={!!busy} onClick={() => onEdit(now.data as string)}>
              <Pencil />
            </Button>
          )}
          {!readOnly && !removed && (
            <ConfirmAction
              trigger={
                <Button variant="ghost" size="icon-sm" aria-label={`Quitar ${path}`} disabled={!!busy}>
                  <Trash2 />
                </Button>
              }
              title={`¿Quitar ${path}?`}
              description={<p>Se sube la configuración sin él; guarda la versión para aplicarlo.</p>}
              actionLabel="Quitar"
              onConfirm={async () => onRemove()}
            />
          )}
        </div>
      </div>
      {!removed && size > maxView ? (
        <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
          {formatBytes(size)}: demasiado grande para verlo aquí. Descarga la configuración para revisarlo.
        </p>
      ) : loading ? (
        <Skeleton className="h-48 rounded-lg" />
      ) : diff === null ? (
        <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
          Demasiados cambios para compararlos aquí.
        </p>
      ) : diff ? (
        <DiffView lines={diff} className="max-h-[28rem]" />
      ) : text === null ? (
        <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
          Archivo binario: se copia tal cual.
        </p>
      ) : (
        <LogView
          text={text}
          empty="(vacío)"
          className={cn("max-h-[28rem]", removed && "line-through opacity-60")}
        />
      )}
    </div>
  )
}

/** What a dropped folder or archive becomes, before uploading. */
function StageDialog({
  rules,
  harness,
  pending,
  replacing,
  busy,
  error,
  onCancel,
  onUpload,
}: {
  rules: HomeRules
  harness: Harness
  pending: Pending
  replacing: boolean
  busy: boolean
  error?: string
  onCancel: () => void
  onUpload: () => void
}) {
  const { files, skipped, problems } = pending
  const list = React.useMemo(() => [...files].map(([path, f]) => ({ path, size: f.data.length })), [files])
  const total = list.reduce((n, f) => n + f.size, 0)
  const blocked = files.size === 0 || problems.secrets.length > 0 || problems.limits.length > 0
  const skippedFiles = skipped.reduce((n, s) => n + (s.count ?? 1), 0)
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onCancel()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Configuración de {harnessLabel[harness]}</DialogTitle>
          <DialogDescription>
            {files.size > 0 ? (
              <>
                {plural(files.size, "archivo", "archivos")} · {formatBytes(total)} para <Mono>~/{rules.root}</Mono>
                {replacing && <> · respecto a la actual: {countChanges(pending.changes)}</>}.
              </>
            ) : (
              <>Nada de lo que elegiste es configuración de {harnessLabel[harness]}.</>
            )}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] space-y-3 overflow-y-auto">
          {problems.secrets.length > 0 && (
            <Notice tone="danger" icon={ShieldAlert}>
              <p className="font-medium">Hay secretos: no se puede subir.</p>
              <ul className="list-disc pl-4">
                {problems.secrets.map((s) => (
                  <li key={s.path}>
                    <Mono className="text-inherit">{s.path}</Mono> línea {s.line} parece {s.what}
                  </li>
                ))}
              </ul>
              <p>
                La configuración se hornea en la imagen y la lee quien vea el entorno. Dale el secreto al agente (secreto
                env) y léelo del entorno, como <Mono className="text-inherit">{"\"${GITHUB_TOKEN}\""}</Mono>.
              </p>
            </Notice>
          )}
          {problems.limits.length > 0 && (
            <Notice tone="danger" icon={TriangleAlert}>
              <p className="font-medium">Pasa los límites:</p>
              <ul className="list-disc pl-4">
                {problems.limits.map((l) => (
                  <li key={l}>{l}</li>
                ))}
              </ul>
            </Notice>
          )}
          {problems.notes.map((n) => (
            <Notice key={n} tone="warning" icon={TriangleAlert}>
              {n}
            </Notice>
          ))}
          {files.size > 0 && (
            <div className="max-h-72 overflow-y-auto rounded-lg border p-1.5 scrollbar-thin">
              <HomeTree files={list} changes={replacing ? pending.changes : undefined} />
            </div>
          )}
          {skipped.length > 0 && (
            <details className="rounded-lg border px-3 py-2 text-sm">
              <summary className="cursor-pointer text-muted-foreground">
                Se deja fuera: {plural(skippedFiles, "elemento", "elementos")}
              </summary>
              <ul className="mt-2 space-y-1">
                {skipped.map((s) => (
                  <li key={s.path} className="flex flex-wrap gap-x-2">
                    <Mono className="text-foreground">{s.path}</Mono>
                    {s.count && s.count > 1 && <span className="text-xs text-muted-foreground">({s.count})</span>}
                    <span className="text-xs text-muted-foreground">{s.reason}</span>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancelar
          </Button>
          <Button onClick={onUpload} disabled={blocked || busy}>
            {busy ? <Spinner /> : <Upload />} Subir y usar
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Edits (or creates) one text file of the home. */
function EditDialog({
  rules,
  harness,
  initial,
  taken,
  busy,
  onClose,
  onApply,
}: {
  rules: HomeRules
  harness: Harness
  initial: { path: string; text: string; isNew: boolean }
  taken: Set<string>
  busy: boolean
  onClose: () => void
  onApply: (path: string, text: string) => void
}) {
  const [path, setPath] = React.useState(initial.path)
  const [text, setText] = React.useState(initial.text)
  const p = path.trim()
  const pathProblem = initial.isNew && p ? homePathProblem(rules, harness, p) || (taken.has(p) ? "ya existe" : "") : ""
  const secret = React.useMemo(() => findSecret(rules, p, text), [rules, p, text])
  const tooBig = encodeText(text).length > rules.max_file
  const unchanged = !initial.isNew && text === initial.text
  const ok = !!p && !pathProblem && !secret && !tooBig && !unchanged && asText(encodeText(text)) !== null
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (ok) onApply(p, text)
          }}
        >
          <DialogHeader>
            <DialogTitle>{initial.isNew ? "Nuevo archivo" : initial.path}</DialogTitle>
            <DialogDescription>
              Se sube una configuración nueva con este cambio; guarda la versión del entorno para aplicarla.
            </DialogDescription>
          </DialogHeader>
          {initial.isNew && (
            <Field data-invalid={!!pathProblem}>
              <FieldLabel htmlFor="home-path">Ruta en ~/{rules.root}</FieldLabel>
              <Input
                id="home-path"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                placeholder={harness === "rness" ? "skills/revisar/SKILL.md" : "CLAUDE.md"}
                className="font-mono"
                autoFocus
              />
              <FieldDescription>
                {pathProblem || `Arriba: ${[...rules.files, ...rules.dirs.map((d) => d + "/…")].join(", ")}.`}
              </FieldDescription>
            </Field>
          )}
          <Field data-invalid={!!secret || tooBig}>
            <Textarea
              aria-label="Contenido"
              value={text}
              onChange={(e) => setText(e.target.value)}
              className="min-h-[50vh] font-mono text-xs"
              spellCheck={false}
              autoFocus={!initial.isNew}
            />
            {secret && (
              <FieldDescription>
                Línea {secret.line} parece {secret.what}: los secretos no van aquí. Dalo como secreto env del agente y
                léelo del entorno.
              </FieldDescription>
            )}
            {tooBig && <FieldDescription>Pasa de {formatBytes(rules.max_file)}.</FieldDescription>}
          </Field>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
              Cancelar
            </Button>
            <Button type="submit" disabled={!ok || busy}>
              {busy && <Spinner />} Aplicar
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
