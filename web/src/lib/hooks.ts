// Data hooks: one place for query keys, fetchers and mutations.

import { useEffect, useState } from "react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query"
import { toast } from "sonner"

import { api } from "./api"
import { keys } from "./query"
import type {
  Access,
  Channel,
  CustomsStatus,
  DefaultAccess,
  Effect,
  GroupMode,
  InboxItem,
  Paginated,
  Ship,
} from "./types"

export type InboxFilter = { status?: CustomsStatus; dock_id?: string }

const PAGE = 50

function nextOffset<T>(last: Paginated<T>) {
  const next = last.page.offset + last.page.limit
  return next < last.page.total ? next : undefined
}

export function flatten<T>(data: InfiniteData<Paginated<T>> | undefined): T[] {
  if (!data) return []
  const seen = new Set<string>()
  const out: T[] = []
  for (const p of data.pages)
    for (const it of p.items) {
      const id = (it as { id: string }).id
      if (!seen.has(id)) {
        seen.add(id)
        out.push(it)
      }
    }
  return out
}

export function useInbox(f: InboxFilter) {
  return useInfiniteQuery({
    queryKey: keys.inbox(f),
    queryFn: ({ pageParam }) => api.inbox({ ...f, limit: PAGE, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset<InboxItem>,
  })
}

export function useShips(conversation: string) {
  return useInfiniteQuery({
    queryKey: keys.ships(conversation),
    queryFn: ({ pageParam }) => api.ships(conversation, PAGE, pageParam),
    initialPageParam: 0,
    getNextPageParam: nextOffset<Ship>,
  })
}

export function useConversation(id: string) {
  return useQuery({ queryKey: keys.conversation(id), queryFn: () => api.conversation(id) })
}

export function useDocks() {
  return useQuery({ queryKey: keys.docks, queryFn: api.docks })
}

export function useDock(id: string) {
  return useQuery({ queryKey: keys.dock(id), queryFn: () => api.dock(id) })
}

export function useCustoms(dock: string | undefined) {
  return useQuery({
    queryKey: keys.customs(dock ?? ""),
    queryFn: () => api.customs(dock!),
    enabled: !!dock,
  })
}

export function useCrew() {
  return useQuery({ queryKey: keys.crew, queryFn: api.crew })
}

function fail(what: string) {
  return (err: Error) => toast.error(what, { description: err.message })
}

export function useSetAccess() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, access }: { id: string; access: Access }) => api.updateConversation(id, { access }),
    onSuccess: (c) => {
      qc.setQueryData(keys.conversation(c.id), c)
      qc.invalidateQueries({ queryKey: keys.inboxAll })
    },
    onError: fail("No se pudo cambiar el acceso"),
  })
}

export function useSetGroupMode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, mode }: { id: string; mode: GroupMode }) =>
      api.updateConversation(id, { group_mode: mode }),
    onSuccess: (c) => qc.setQueryData(keys.conversation(c.id), c),
    onError: fail("No se pudo cambiar el modo del grupo"),
  })
}

export function useSend(conversation: string) {
  return useMutation({
    mutationFn: (text: string) => api.send(conversation, text),
    onError: fail("No se pudo enviar"),
  })
}

export function useSetPolicy(dock: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (p: { default?: DefaultAccess; group_mode?: GroupMode }) => api.setPolicy(dock, p),
    onSuccess: (c) => {
      qc.setQueryData(keys.customs(dock), c)
      toast.success("Aduana actualizada")
    },
    onError: fail("No se pudo guardar la política"),
  })
}

export function useAddRule(dock: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ effect, pattern }: { effect: Effect; pattern: string }) => api.addRule(dock, effect, pattern),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.customs(dock) }),
    onError: fail("No se pudo agregar la regla"),
  })
}

export function useRemoveRule(dock: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.removeRule(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.customs(dock) }),
    onError: fail("No se pudo quitar la regla"),
  })
}

export function useCreateDock() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ channel, name }: { channel: Channel; name: string }) => api.createDock(channel, name),
    onSuccess: (d) => {
      qc.setQueryData(keys.dock(d.id), d)
      qc.invalidateQueries({ queryKey: keys.docks, exact: true })
    },
    onError: fail("No se pudo crear el muelle"),
  })
}

export function useDockAction() {
  const qc = useQueryClient()
  const onDock = (d: Awaited<ReturnType<typeof api.dock>>) => {
    qc.setQueryData(keys.dock(d.id), d)
    qc.invalidateQueries({ queryKey: keys.docks, exact: true })
  }
  return {
    rename: useMutation({
      mutationFn: ({ id, name }: { id: string; name: string }) => api.renameDock(id, name),
      onSuccess: onDock,
      onError: fail("No se pudo renombrar"),
    }),
    relink: useMutation({
      mutationFn: (id: string) => api.relinkDock(id),
      onSuccess: onDock,
      onError: fail("No se pudo iniciar la vinculación"),
    }),
    unlink: useMutation({
      mutationFn: (id: string) => api.unlinkDock(id),
      onSuccess: (d) => {
        onDock(d)
        toast.success("Muelle desvinculado", { description: "El historial se conserva." })
      },
      onError: fail("No se pudo desvincular"),
    }),
    remove: useMutation({
      mutationFn: (id: string) => api.deleteDock(id),
      onSuccess: (_, id) => {
        qc.removeQueries({ queryKey: keys.dock(id) })
        qc.removeQueries({ queryKey: keys.customs(id) })
        qc.invalidateQueries({ queryKey: keys.docks, exact: true })
        qc.invalidateQueries({ queryKey: keys.inboxAll })
        toast.success("Muelle eliminado")
      },
      onError: fail("No se pudo eliminar"),
    }),
  }
}

/** Ticks every `ms` so relative times and durations stay current. */
export function useNow(ms = 30_000) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms])
  return now
}
