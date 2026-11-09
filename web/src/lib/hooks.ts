// Data hooks: one place for query keys, fetchers and mutations.

import { useCallback, useEffect, useState } from "react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query"
import { toast } from "sonner"

import { api } from "./api"
import { Perm, type Permission } from "./permissions.gen"
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
  ProfileInput,
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
  const can = useCan()
  return useInfiniteQuery({
    enabled: can(Perm.ConversationsRead),
    queryKey: keys.inbox(f),
    queryFn: ({ pageParam }) => api.inbox({ ...f, limit: PAGE, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset<InboxItem>,
  })
}

export function useShips(conversation: string) {
  const can = useCan()
  return useInfiniteQuery({
    enabled: can(Perm.ShipsRead),
    queryKey: keys.ships(conversation),
    queryFn: ({ pageParam }) => api.ships(conversation, PAGE, pageParam),
    initialPageParam: 0,
    getNextPageParam: nextOffset<Ship>,
  })
}

// Read queries run only with their permission (the server would answer
// 403); pages show NoAccess instead.

export function useConversation(id: string) {
  const can = useCan()
  return useQuery({
    queryKey: keys.conversation(id),
    queryFn: () => api.conversation(id),
    enabled: can(Perm.ConversationsRead),
  })
}

export function useDocks() {
  const can = useCan()
  return useQuery({ queryKey: keys.docks, queryFn: api.docks, enabled: can(Perm.DocksRead) })
}

export function useDock(id: string) {
  const can = useCan()
  return useQuery({ queryKey: keys.dock(id), queryFn: () => api.dock(id), enabled: can(Perm.DocksRead) })
}

export function useCustoms(dock: string | undefined) {
  const can = useCan()
  return useQuery({
    queryKey: keys.customs(dock ?? ""),
    queryFn: () => api.customs(dock!),
    enabled: !!dock && can(Perm.CustomsRead),
  })
}

export function useCrew() {
  const can = useCan()
  return useQuery({ queryKey: keys.crew, queryFn: api.crew, enabled: can(Perm.CrewRead) })
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

/** The signed-in session (loaded by the gate before the app renders). */
export function useSession() {
  return useQuery({ queryKey: keys.session, queryFn: api.session, staleTime: Infinity })
}

/**
 * can(p) says whether the user holds p. The server enforces it on every
 * route; the UI only hides what would fail. none/token mode sessions hold
 * the whole catalog.
 */
export function useCan() {
  const session = useSession()
  const held = session.data?.permissions
  return useCallback((p: Permission) => !!held?.includes(p), [held])
}

export function useCrewTokens() {
  const can = useCan()
  return useQuery({ queryKey: keys.crewTokens, queryFn: api.crewTokens, enabled: can(Perm.CrewTokensRead) })
}

export function useCreateCrewToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ agent_id, name, expires_at }: { agent_id: string; name: string; expires_at?: string }) =>
      api.createCrewToken(agent_id, name, expires_at),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.crewTokens }),
    onError: fail("No se pudo crear el token"),
  })
}

export function useRevokeCrewToken() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.revokeCrewToken(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.crewTokens })
      toast.success("Token revocado", { description: "Sus agentes conectados se desconectaron." })
    },
    onError: fail("No se pudo revocar el token"),
  })
}

export function useAgents() {
  const can = useCan()
  return useQuery({ queryKey: keys.agents, queryFn: api.agents, enabled: can(Perm.AgentsRead) })
}

export function useDockTeam(dock: string) {
  const can = useCan()
  return useQuery({ queryKey: keys.team(dock), queryFn: () => api.team(dock), enabled: can(Perm.AgentsRead) })
}

/** Agents and every dock team change together (a default demotes another). */
function useAgentsChanged() {
  const qc = useQueryClient()
  return () => {
    qc.invalidateQueries({ queryKey: keys.agents })
    qc.invalidateQueries({ queryKey: keys.teamAll })
  }
}

export function useCreateAgent() {
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: ({ name, description }: { name: string; description: string }) => api.createAgent(name, description),
    onSuccess: changed,
    onError: fail("No se pudo crear el agente"),
  })
}

export function useUpdateAgent() {
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: ({ id, ...patch }: { id: string; name?: string; description?: string }) => api.updateAgent(id, patch),
    onSuccess: changed,
    onError: fail("No se pudo guardar el agente"),
  })
}

export function useDeleteAgent() {
  const qc = useQueryClient()
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: (id: string) => api.deleteAgent(id),
    onSuccess: () => {
      changed()
      qc.invalidateQueries({ queryKey: keys.crewTokens })
      toast.success("Agente eliminado", { description: "Sus tokens se revocaron y sus conexiones se cerraron." })
    },
    onError: fail("No se pudo eliminar el agente"),
  })
}

export function useAssignAgent() {
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: ({ dock, agent, isDefault }: { dock: string; agent: string; isDefault: boolean }) =>
      api.assignAgent(dock, agent, isDefault),
    onSuccess: changed,
    onError: fail("No se pudo asignar el agente"),
  })
}

export function useUnassignAgent() {
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: ({ dock, agent }: { dock: string; agent: string }) => api.unassignAgent(dock, agent),
    onSuccess: changed,
    onError: fail("No se pudo quitar el agente"),
  })
}

export function useAgent(id: string) {
  const can = useCan()
  return useQuery({ queryKey: keys.agent(id), queryFn: () => api.agent(id), enabled: can(Perm.AgentsRead) })
}

/** An agent's profile: the current one (version 0) or a past version. */
export function useProfile(id: string, version = 0) {
  const can = useCan()
  return useQuery({
    queryKey: keys.profile(id, version),
    queryFn: () => api.profile(id, version),
    enabled: can(Perm.AgentsRead),
  })
}

export function useProfileVersions(id: string) {
  const can = useCan()
  return useQuery({
    queryKey: keys.profileVersions(id),
    queryFn: () => api.profileVersions(id),
    enabled: can(Perm.AgentsRead),
  })
}

export function useSecrets(id: string) {
  const can = useCan()
  return useQuery({ queryKey: keys.secrets(id), queryFn: () => api.secrets(id), enabled: can(Perm.AgentsRead) })
}

/** A saved profile or secret changes the agent's kit: its connections
 * restart once idle. */
function useKitChanged(id: string) {
  const qc = useQueryClient()
  return () => {
    qc.invalidateQueries({ queryKey: keys.agents })
    qc.invalidateQueries({ queryKey: ["profile", id] })
    qc.invalidateQueries({ queryKey: keys.secrets(id) })
  }
}

export function useSaveProfile(id: string) {
  const changed = useKitChanged(id)
  return useMutation({
    mutationFn: ({ base, profile }: { base: number; profile: ProfileInput }) => api.saveProfile(id, base, profile),
    onSuccess: (p) => {
      changed()
      toast.success(`Perfil v${p.version} guardado`, {
        description: "Sus conexiones reinician el agente con él al terminar lo que están atendiendo.",
      })
    },
    onError: fail("No se pudo guardar el perfil"),
  })
}

export function useSetSecret(id: string) {
  const changed = useKitChanged(id)
  return useMutation({
    mutationFn: ({ name, value, env }: { name: string; value: string; env: boolean }) =>
      api.setSecret(id, name, value, env),
    onSuccess: changed,
    onError: fail("No se pudo guardar el secreto"),
  })
}

export function useDeleteSecret(id: string) {
  const changed = useKitChanged(id)
  return useMutation({
    mutationFn: (name: string) => api.deleteSecret(id, name),
    onSuccess: changed,
    onError: fail("No se pudo eliminar el secreto"),
  })
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
