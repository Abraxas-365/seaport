import { Download, FileText, ImageOff } from "lucide-react"
import { useState } from "react"

import { attachmentURL } from "@/lib/api"
import { plural } from "@/lib/format"
import type { Attachment } from "@/lib/types"
import { cn } from "@/lib/utils"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"

function sizeLabel(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** One image of a ship: a thumbnail that opens it full size. */
function ImageThumb({ attachment, single }: { attachment: Attachment; single: boolean }) {
  const [open, setOpen] = useState(false)
  const [broken, setBroken] = useState(false)
  const src = attachmentURL(attachment.id)
  const label = attachment.name || "Imagen"
  if (broken) {
    return (
      <div
        className={cn(
          "flex items-center justify-center gap-1.5 rounded-lg bg-muted text-xs text-muted-foreground",
          single ? "h-24 w-48" : "aspect-square",
        )}
      >
        <ImageOff className="size-4" /> no disponible
      </div>
    )
  }
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="block overflow-hidden rounded-lg focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        aria-label={`Ver ${label}`}
      >
        <img
          src={src}
          alt={label}
          loading="lazy"
          onError={() => setBroken(true)}
          className={cn("bg-muted object-cover", single ? "max-h-72 w-full min-w-32" : "aspect-square w-full")}
        />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-[min(92vw,64rem)] gap-2 p-2 sm:max-w-[min(92vw,64rem)]">
          <DialogTitle className="sr-only">{label}</DialogTitle>
          <DialogDescription className="px-1 text-xs">
            {label} · {attachment.mime} · {sizeLabel(attachment.size)}
          </DialogDescription>
          <img src={src} alt={label} className="max-h-[80vh] w-full rounded-md object-contain" />
        </DialogContent>
      </Dialog>
    </>
  )
}

/** A document of a ship: a chip that downloads it (never rendered). */
function DocumentChip({ attachment }: { attachment: Attachment }) {
  const label = attachment.name || "Archivo"
  return (
    <a
      href={attachmentURL(attachment.id)}
      download={label}
      className="flex min-w-0 items-center gap-2 rounded-lg border bg-background/60 px-2.5 py-1.5 text-xs hover:bg-background focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      aria-label={`Descargar ${label}`}
    >
      <FileText className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate font-medium">{label}</span>
      <span className="shrink-0 text-muted-foreground">{sizeLabel(attachment.size)}</span>
      <Download className="size-3.5 shrink-0 text-muted-foreground" />
    </a>
  )
}

/** The files of a ship, above its caption: images as thumbnails,
 * documents as download chips. */
export function ShipAttachments({ attachments }: { attachments: Attachment[] }) {
  const images = attachments.filter((a) => a.kind === "image")
  const documents = attachments.filter((a) => a.kind === "document")
  if (images.length === 0 && documents.length === 0) return null
  const single = images.length === 1
  return (
    <div data-slot="ship-attachments" className="mt-0.5 mb-1 flex flex-col gap-1">
      {images.length > 0 && (
        <div
          aria-label={plural(images.length, "imagen", "imágenes")}
          className={cn("-mx-1.5", single ? "" : "grid grid-cols-2 gap-1")}
        >
          {images.map((a) => (
            <ImageThumb key={a.id} attachment={a} single={single} />
          ))}
        </div>
      )}
      {documents.length > 0 && (
        <div aria-label={plural(documents.length, "archivo", "archivos")} className="flex flex-col gap-1">
          {documents.map((a) => (
            <DocumentChip key={a.id} attachment={a} />
          ))}
        </div>
      )}
    </div>
  )
}
