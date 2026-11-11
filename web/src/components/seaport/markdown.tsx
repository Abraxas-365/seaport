import * as React from "react"
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown"
import remarkGfm from "remark-gfm"

import { cn } from "@/lib/utils"

/** Only absolute web and mail links are followed; anything else (relative
 * paths, which would resolve against Seaport, or odd schemes) stays text. */
function safeHref(href?: string) {
  if (!href) return undefined
  return /^(https?:|mailto:)/i.test(href) ? href : undefined
}

/** A fenced block's language, from react-markdown's `language-x` class. */
function language(className?: string) {
  return /language-([\w-]+)/.exec(className ?? "")?.[1]
}

/** Fenced blocks holding prose rather than code. */
const prose = new Set(["suggestion", "md", "markdown", "text", "txt"])

const components: Components = {
  p: ({ children }) => <p className="my-1.5 first:mt-0 last:mb-0">{children}</p>,
  a: ({ href, children }) => {
    const url = safeHref(href)
    if (!url) return <span>{children}</span>
    return (
      <a href={url} target="_blank" rel="noopener noreferrer nofollow" className="underline underline-offset-2 hover:opacity-80">
        {children}
      </a>
    )
  },
  // Remote images would leak who reads the chat (and CSP blocks them):
  // show their text as a link instead.
  img: ({ src, alt }) => {
    const url = safeHref(typeof src === "string" ? src : undefined)
    const label = alt || "imagen"
    return url ? (
      <a href={url} target="_blank" rel="noopener noreferrer nofollow" className="underline underline-offset-2">
        🖼 {label}
      </a>
    ) : (
      <span>🖼 {label}</span>
    )
  },
  ul: ({ children }) => <ul className="my-1.5 list-disc space-y-0.5 pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1.5 list-decimal space-y-0.5 pl-5">{children}</ol>,
  li: ({ children }) => <li className="[&>p]:my-0">{children}</li>,
  h1: ({ children }) => <p className="mt-2 mb-1 text-base font-semibold first:mt-0">{children}</p>,
  h2: ({ children }) => <p className="mt-2 mb-1 text-[0.95rem] font-semibold first:mt-0">{children}</p>,
  h3: ({ children }) => <p className="mt-2 mb-1 font-semibold first:mt-0">{children}</p>,
  h4: ({ children }) => <p className="mt-2 mb-1 font-semibold first:mt-0">{children}</p>,
  h5: ({ children }) => <p className="mt-2 mb-1 font-semibold first:mt-0">{children}</p>,
  h6: ({ children }) => <p className="mt-2 mb-1 font-semibold first:mt-0">{children}</p>,
  blockquote: ({ children }) => (
    <blockquote className="my-1.5 border-l-2 border-current/30 pl-2.5 opacity-90">{children}</blockquote>
  ),
  hr: () => <hr className="my-2 border-current/20" />,
  table: ({ children }) => (
    <div className="my-1.5 overflow-x-auto">
      <table className="w-full border-collapse text-xs">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border border-current/20 px-2 py-1 text-left font-semibold">{children}</th>,
  td: ({ children }) => <td className="border border-current/20 px-2 py-1 align-top">{children}</td>,
  input: ({ checked }) => <input type="checkbox" checked={!!checked} disabled className="mr-1 align-middle" />,
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const text = String(children ?? "")
    const lang = language(className)
    // Inline code has no language and no line break.
    if (!lang && !text.includes("\n")) {
      return <code className="rounded bg-current/10 px-1 py-px font-mono text-[0.85em]">{children}</code>
    }
    return (
      <div className="my-1.5 overflow-hidden rounded-lg bg-current/10">
        {lang && (
          <div className="border-b border-current/15 px-2.5 py-1 font-mono text-[0.7rem] opacity-70">
            {lang === "suggestion" ? "Sugerencia de cambio" : lang}
          </div>
        )}
        <pre
          className={cn(
            "scrollbar-thin overflow-x-auto px-2.5 py-2 font-mono text-xs leading-relaxed",
            // Prose (a suggested doc change, plain text) wraps; code scrolls.
            lang && prose.has(lang) ? "whitespace-pre-wrap" : "whitespace-pre",
          )}
        >
          <code>{text.replace(/\n$/, "")}</code>
        </pre>
      </div>
    )
  },
}

/**
 * Message text written in Markdown (GitHub-flavoured: tables, task lists,
 * strikethrough, autolinks), styled to sit inside a chat bubble in either
 * colour. Raw HTML is never rendered, only absolute http(s)/mailto links
 * are followed (in a new tab) and remote images become links.
 */
export const Markdown = React.memo(function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div data-slot="markdown" className={cn("break-words [overflow-wrap:anywhere]", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml urlTransform={defaultUrlTransform} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  )
})
