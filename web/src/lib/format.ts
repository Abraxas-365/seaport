// Display helpers: names, times, handles.

import type { Conversation, Ship } from "./types"

const rtf = new Intl.RelativeTimeFormat("es", { numeric: "auto" })
const timeFmt = new Intl.DateTimeFormat("es", { hour: "2-digit", minute: "2-digit" })
const dayFmt = new Intl.DateTimeFormat("es", { day: "numeric", month: "short" })
const fullFmt = new Intl.DateTimeFormat("es", { dateStyle: "medium", timeStyle: "short" })

export function formatTime(iso: string) {
  return timeFmt.format(new Date(iso))
}

export function formatFull(iso: string) {
  return fullFmt.format(new Date(iso))
}

/** Inbox-style stamp: time today, "ayer", weekday this week, else date. */
export function formatStamp(iso: string, now = new Date()) {
  const d = new Date(iso)
  const days = Math.floor((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (days <= 0) return timeFmt.format(d)
  if (days === 1) return "ayer"
  if (days < 7) return new Intl.DateTimeFormat("es", { weekday: "short" }).format(d)
  return dayFmt.format(d)
}

export function formatRelative(iso: string, now = Date.now()) {
  const s = Math.round((new Date(iso).getTime() - now) / 1000)
  const abs = Math.abs(s)
  if (abs < 45) return "ahora"
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute")
  if (abs < 86_400) return rtf.format(Math.round(s / 3600), "hour")
  return rtf.format(Math.round(s / 86_400), "day")
}

export function formatDuration(fromIso: string, now = Date.now()) {
  const s = Math.max(0, Math.round((now - new Date(fromIso).getTime()) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function dayLabel(iso: string, now = new Date()) {
  const d = new Date(iso)
  const days = Math.floor((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (days === 0) return "Hoy"
  if (days === 1) return "Ayer"
  return new Intl.DateTimeFormat("es", { weekday: "long", day: "numeric", month: "long" }).format(d)
}

function startOfDay(d: Date) {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

/** A handle for humans: "+51999123436" stays, JIDs lose their server. */
export function prettyHandle(h: string) {
  if (h.startsWith("+")) return h
  const at = h.indexOf("@")
  if (at > 0) {
    const user = h.slice(0, at).split(":")[0]
    const server = h.slice(at + 1)
    if (server === "s.whatsapp.net") return "+" + user
    if (server === "g.us") return "grupo " + user
    if (server === "lid") return user + " (lid)"
    return user
  }
  return h
}

export function conversationName(c: Pick<Conversation, "title" | "external_id" | "kind">) {
  if (c.kind === "self") return c.title || "Yo (chat propio)"
  return c.title || prettyHandle(c.external_id)
}

export function initials(name: string) {
  const words = name
    .replace(/[^\p{L}\p{N}\s]/gu, " ")
    .trim()
    .split(/\s+/)
    .filter(Boolean)
  if (words.length === 0) return "?"
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
  return (words[0][0] + words[1][0]).toUpperCase()
}

/** Stable hue (0-359) per string, for avatar tints. */
export function hueOf(s: string) {
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  return h % 360
}

export function shipPreview(s: Ship | null) {
  if (!s) return ""
  const who = s.direction === "out" ? "Agente: " : ""
  return who + s.text.replace(/\s+/g, " ")
}

export function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`
}
