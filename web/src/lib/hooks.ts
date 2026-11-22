// Data hooks: one place for query keys, fetchers and mutations.

import { useCallback, useEffect, useState } from "react"
import {
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query"
import { toast } from "sonner"

import { api, ApiError } from "./api"
import { Perm, type Permission } from "./permissions.gen"
import { keys } from "./query"
import type {
  Access,
  Approval,
  Channel,
  CustomsStatus,
  DefaultAccess,
  DockTools,
  Effect,
  GroupMode,
  Harness,
  HostingPatch,
  InboxItem,
  MemberAccess,
  Paginated,
  ProfileInput,
  ScheduleInput,
  Ship,
  VaultEntrySummary,
  VaultMode,
  WhatsAppSettings,
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

/** A conversation's ships; thread: those of every door of its thread. */
export function useShips(conversation: string, thread = false) {
  const can = useCan()
  return useInfiniteQuery({
    enabled: can(Perm.ShipsRead),
    queryKey: keys.ships(conversation, thread),
    queryFn: ({ pageParam }) => api.ships(conversation, PAGE, pageParam, thread),
    initialPageParam: 0,
    getNextPageParam: nextOffset<Ship>,
  })
}

/** The doors of a conversation's thread, oldest first. */
export function useThread(conversation: string) {
  const can = useCan()
  return useQuery({
    enabled: can(Perm.ConversationsRead),
    queryKey: keys.thread(conversation),
    queryFn: () => api.thread(conversation),
  })
}

/** A conversation's latest turn summaries (newest first). */
export function useTurns(conversation: string) {
  const can = useCan()
  return useQuery({
    enabled: can(Perm.ShipsRead),
    queryKey: keys.turns(conversation),
    queryFn: () => api.turns(conversation),
  })
}

/** A conversation's approvals (newest first); live through ship.approval. */
export function useApprovals(conversation: string) {
  const can = useCan()
  return useQuery({
    enabled: can(Perm.ShipsRead),
    queryKey: keys.approvals(conversation),
    queryFn: () => api.approvals(conversation),
  })
}

function patchApproval(qc: ReturnType<typeof useQueryClient>, a: Approval) {
  qc.setQueryData<Paginated<Approval>>(keys.approvals(a.conversation_id), (old) =>
    old ? { ...old, items: old.items.map((x) => (x.id === a.id ? a : x)) } : old,
  )
}

export function useAnswerApproval() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, option }: { id: string; option: string }) => api.answerApproval(id, option),
    onSuccess: (a) => patchApproval(qc, a),
    onError: fail("No se pudo responder la aprobación"),
  })
}

export function useCancelApproval() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.cancelApproval(id),
    onSuccess: (a) => patchApproval(qc, a),
    onError: fail("No se pudo cancelar la aprobación"),
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

/** A GitHub dock's App and installations (refetched when the dock changes). */
export function useGitHub(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.github(dock), queryFn: () => api.github(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** A Jira dock's site (the webhook secret never comes back from here). */
export function useJira(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.jira(dock), queryFn: () => api.jira(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** An email dock's mailbox (the password never comes back). */
export function useEmail(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.email(dock), queryFn: () => api.email(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** A Slack dock's App (refetched when the dock changes). */
export function useSlack(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.slack(dock), queryFn: () => api.slack(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** A webhook dock's endpoint and template (the secret never comes back from here). */
export function useWebhook(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.webhook(dock), queryFn: () => api.webhook(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** A Datadog dock's endpoint and mirror (the secret never comes back from here). */
export function useDatadog(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({ queryKey: keys.datadog(dock), queryFn: () => api.datadog(dock), enabled: enabled && can(Perm.DocksRead) })
}

/** What a WhatsApp dock does to a message an agent has (reaction, read receipt). */
export function useWhatsAppSettings(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({
    queryKey: keys.whatsapp(dock),
    queryFn: () => api.whatsapp(dock),
    enabled: enabled && can(Perm.DocksRead),
  })
}

export function useSaveWhatsAppSettings(dock: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: Partial<WhatsAppSettings>) => api.saveWhatsApp(dock, body),
    onSuccess: (s) => {
      qc.setQueryData(keys.whatsapp(dock), s)
      toast.success("Ajustes guardados")
    },
    onError: fail("No se pudo guardar"),
  })
}

export function useDestinations(dock: string, enabled = true) {
  const can = useCan()
  return useQuery({
    queryKey: keys.destinations(dock),
    queryFn: () => api.destinations(dock),
    enabled: enabled && can(Perm.DocksRead),
  })
}

/**
 * The destination names of several docks (an agent's), sorted and without
 * repeats: where a profile's browser_assist can point. Undefined while any
 * dock's list is loading or the caller can't read docks.
 */
export function useDocksDestinations(docks: string[]): string[] | undefined {
  const can = useCan()
  const enabled = can(Perm.DocksRead)
  return useQueries({
    queries: docks.map((dock) => ({
      queryKey: keys.destinations(dock),
      queryFn: () => api.destinations(dock),
      enabled,
    })),
    combine: (results) =>
      !enabled || results.some((r) => !r.data)
        ? undefined
        : [...new Set(results.flatMap((r) => (r.data ?? []).map((d) => d.name)))].sort(),
  })
}

/** The owner's destinations of a dock: set (upsert by name), remove, open a conversation at one. */
export function useDestinationActions(dock: string) {
  const qc = useQueryClient()
  const changed = () => qc.invalidateQueries({ queryKey: keys.destinations(dock) })
  return {
    put: useMutation({
      mutationFn: ({ name, target, title }: { name: string; target: string; title: string }) =>
        api.putDestination(dock, name, { target, title }),
      onSuccess: (d) => {
        changed()
        toast.success("Destino guardado", { description: `${d.name} → ${d.target}` })
      },
      onError: fail("No se pudo guardar el destino"),
    }),
    remove: useMutation({
      mutationFn: (name: string) => api.removeDestination(dock, name),
      onSuccess: () => {
        changed()
        toast.success("Destino eliminado")
      },
      onError: fail("No se pudo eliminar el destino"),
    }),
    open: useMutation({
      mutationFn: ({ name, text }: { name: string; text: string }) => api.openDestination(dock, name, { text }),
      onSuccess: () => {
        qc.invalidateQueries({ queryKey: keys.inboxAll })
        toast.success("Conversación abierta")
      },
      onError: fail("No se pudo abrir la conversación"),
    }),
  }
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

export function useLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, with_ }: { id: string; with_: string }) => api.link(id, with_),
    onSuccess: (doors) => {
      for (const c of doors) qc.setQueryData(keys.conversation(c.id), c)
      qc.invalidateQueries({ queryKey: keys.threadAll })
      qc.invalidateQueries({ queryKey: ["ships"] })
    },
    onError: fail("No se pudo enlazar la conversación"),
  })
}

export function useUnlink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.unlink(id),
    onSuccess: (c) => {
      qc.setQueryData(keys.conversation(c.id), c)
      qc.invalidateQueries({ queryKey: keys.threadAll })
      qc.invalidateQueries({ queryKey: ["ships"] })
    },
    onError: fail("No se pudo separar la conversación"),
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
    replies: useMutation({
      mutationFn: ({ id, replies }: { id: string; replies: boolean }) => api.setDockReplies(id, replies),
      onSuccess: (d) => {
        onDock(d)
        qc.invalidateQueries({ queryKey: keys.inboxAll })
        toast.success(d.replies ? "Respuestas activadas" : "Respuestas desactivadas", {
          description: d.replies
            ? "La tripulación vuelve a poder responder en este muelle."
            : "Los mensajes siguen despertando al agente; nada se publica aquí.",
        })
      },
      onError: fail("No se pudo cambiar"),
    }),
    tools: useMutation({
      mutationFn: ({ id, tools }: { id: string; tools: Partial<DockTools> }) => api.setDockTools(id, tools),
      onSuccess: (d) => {
        onDock(d)
        toast.success("Herramientas del agente actualizadas")
      },
      onError: fail("No se pudo cambiar"),
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

// Tenant administration (iam mode, optional). These routes are guarded by
// IAMKit's own iam:org:* permissions, which are not in Seaport's catalog, so
// `useCan` can't know them: the page and the nav item follow what
// GET /api/v1/members/access answers instead (all false: nothing to show).

const noMemberAccess: MemberAccess = {
  members: false,
  invite: false,
  assign_roles: false,
  remove: false,
  keys_read: false,
  keys_write: false,
}

export function useMemberAccess(): MemberAccess & { pending: boolean } {
  const config = useQuery({ queryKey: keys.config, queryFn: api.config, staleTime: Infinity })
  const session = useSession()
  const access = useQuery({
    queryKey: keys.memberAccess,
    queryFn: api.memberAccess,
    // Only iam mode has the routes (elsewhere they answer 404).
    enabled: config.data?.auth === "iam" && !!session.data?.authenticated,
    staleTime: 60_000,
    retry: false,
  })
  return { ...(access.data ?? noMemberAccess), pending: config.isPending || (access.isPending && access.fetchStatus !== "idle") }
}

export function useMembers(enabled = true) {
  return useQuery({ queryKey: keys.members, queryFn: api.members, enabled })
}

export function useSetMemberAccess() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, role, manages_members }: { id: string; role: string; manages_members: boolean }) =>
      api.setMemberAccess(id, role, manages_members),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.members })
      toast.success("Acceso actualizado", { description: "Les llega en su próximo inicio de sesión." })
    },
    onError: fail("No se pudo cambiar el acceso"),
  })
}

export function useRemoveMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.removeMember(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.members })
      toast.success("Miembro eliminado", { description: "Su sesión deja de funcionar al instante." })
    },
    onError: fail("No se pudo eliminar al miembro"),
  })
}

export function useMemberInvitations(enabled = true) {
  return useQuery({ queryKey: keys.memberInvitations, queryFn: api.memberInvitations, enabled })
}

export function useInvite() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ email, role, manages_members }: { email: string; role: string; manages_members: boolean }) =>
      api.invite(email, role, manages_members),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.memberInvitations }),
    onError: fail("No se pudo invitar"),
  })
}

export function useResendInvitation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.resendInvitation(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.memberInvitations }),
    onError: fail("No se pudo reenviar la invitación"),
  })
}

export function useRevokeInvitation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.revokeInvitation(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.memberInvitations })
      toast.success("Invitación revocada")
    },
    onError: fail("No se pudo revocar la invitación"),
  })
}

export function useMemberApiKeys(enabled = true) {
  return useQuery({ queryKey: keys.memberApiKeys, queryFn: api.memberApiKeys, enabled })
}

export function useCreateMemberApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, role, expires_in }: { name: string; role: string; expires_in?: string }) =>
      api.createMemberApiKey(name, role, expires_in),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.memberApiKeys }),
    onError: fail("No se pudo crear la llave"),
  })
}

export function useRevokeMemberApiKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.revokeMemberApiKey(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.memberApiKeys })
      toast.success("Llave revocada", { description: "Deja de funcionar al instante." })
    },
    onError: fail("No se pudo revocar la llave"),
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

export function useCloneAgent() {
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: ({ id, name, secrets }: { id: string; name: string; secrets: boolean }) =>
      api.cloneAgent(id, name, secrets),
    onSuccess: changed,
    onError: fail("No se pudo clonar el agente"),
  })
}

/** What the "Nuevo agente" wizard asks for. */
export interface NewAgent {
  name: string
  description: string
  /** Its first config (version 1); null: none yet. */
  config: ProfileInput | null
  /** A model key to keep as the agent's secret before the config names it. */
  key?: { name: string; value: string }
  /** Host it with this many instances (0: remote). */
  slots: number
}

/**
 * Creates an agent with its key, first config and hosting, in that order
 * (each step needs the previous one). The agent stays when a later step
 * fails: the result names it and the step's error, so the page can open it.
 */
export function useNewAgent() {
  const qc = useQueryClient()
  const changed = useAgentsChanged()
  return useMutation({
    mutationFn: async (n: NewAgent) => {
      const agent = await api.createAgent(n.name, n.description)
      try {
        if (n.key) await api.setSecret(agent.id, n.key.name, n.key.value, false)
        if (n.config) await api.saveProfile(agent.id, 0, n.config)
        if (n.slots > 0) qc.setQueryData(keys.hosted(agent.id), await api.host(agent.id, n.slots))
        return { agent, error: null as Error | null }
      } catch (err) {
        return { agent, error: err as Error }
      }
    },
    onSuccess: ({ agent, error }) => {
      changed()
      qc.invalidateQueries({ queryKey: keys.hosting, exact: true })
      if (error)
        toast.error(`@${agent.name} se creó, pero falta un paso`, {
          description: `${error.message}. Termínalo en su página.`,
        })
      else toast.success(`@${agent.name} creado`)
    },
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

/** A saved config or secret changes the agent's kit (its connections
 * restart once idle) and maybe its machine (outdated slots, another build). */
function useKitChanged(id: string) {
  const qc = useQueryClient()
  return () => {
    qc.invalidateQueries({ queryKey: keys.agents })
    qc.invalidateQueries({ queryKey: ["profile", id] })
    qc.invalidateQueries({ queryKey: keys.secrets(id) })
    qc.invalidateQueries({ queryKey: keys.hosted(id) })
    qc.invalidateQueries({ queryKey: keys.build(id) })
  }
}

export function useSaveProfile(id: string) {
  const changed = useKitChanged(id)
  return useMutation({
    mutationFn: ({ base, profile }: { base: number; profile: ProfileInput }) => api.saveProfile(id, base, profile),
    onSuccess: (p, { base }) => {
      changed()
      if (p.version === base) toast.info("Sin cambios: la configuración ya era así")
      else
        toast.success(`Configuración v${p.version} guardada`, {
          description: "Sus conexiones e instancias la toman al terminar lo que están atendiendo.",
        })
    },
    onError: fail("No se pudo guardar la configuración"),
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

/** Ends a browser takeover the agent asked for (once a person did it). */
export function useEndTakeover(id: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (host: string) => api.endTakeover(id, host),
    onSuccess: (a) => {
      qc.setQueryData(keys.agent(id), a)
      qc.invalidateQueries({ queryKey: keys.agents, exact: true })
    },
    onError: fail("No se pudo dar por hecho el relevo"),
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

// Hosting: the agent's image build, its hosting and slots.

/** The build of the agent's current config; null when it was never built.
 * While one runs it refetches (for its log; events carry none). */
export function useBuild(agent: string, enabled = true) {
  const can = useCan()
  return useQuery({
    queryKey: keys.build(agent),
    queryFn: () => api.build(agent).catch(notFoundIsNull),
    enabled: enabled && !!agent && can(Perm.HostingRead),
    refetchInterval: (q) => (q.state.data?.state === "building" ? 2000 : false),
  })
}

function notFoundIsNull(err: unknown): null {
  if (err instanceof ApiError && err.status === 404) return null
  throw err
}

// Harness homes: uploaded config (immutable, by digest).

export function useHomeRules() {
  const can = useCan()
  return useQuery({
    queryKey: keys.homeRules,
    queryFn: api.homeRules,
    enabled: can(Perm.AgentsRead),
    staleTime: Infinity,
  })
}

/** A home's manifest; immutable, so never stale. */
export function useHome(digest: string) {
  const can = useCan()
  return useQuery({
    queryKey: keys.home(digest),
    queryFn: () => api.home(digest),
    enabled: !!digest && can(Perm.AgentsRead),
    staleTime: Infinity,
  })
}

/** One file of a home as text. */
export function useHomeFile(digest: string, path: string, enabled = true) {
  const can = useCan()
  return useQuery({
    queryKey: keys.homeFile(digest, path),
    queryFn: () => api.homeFile(digest, path),
    enabled: enabled && !!digest && !!path && can(Perm.AgentsRead),
    staleTime: Infinity,
  })
}

export function useUploadHome() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ harness, tgz }: { harness: Harness; tgz: Uint8Array }) => api.uploadHome(harness, tgz),
    onSuccess: (h) => qc.setQueryData(keys.home(h.digest), h),
    onError: fail("No se pudo subir la configuración"),
  })
}

export function useStartBuild(agent: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (force: boolean) => api.startBuild(agent, force),
    onSuccess: (b) => {
      qc.setQueryData(keys.build(agent), b)
    },
    onError: fail("No se pudo construir la imagen"),
  })
}

/** Which agents are hosted, and whether this port runs any. */
export function useHosting() {
  const can = useCan()
  return useQuery({ queryKey: keys.hosting, queryFn: api.hosting, enabled: can(Perm.HostingRead) })
}

/** An agent's hosting; null when it isn't hosted. */
export function useHosted(agent: string) {
  const can = useCan()
  return useQuery({
    queryKey: keys.hosted(agent),
    queryFn: () => api.hosted(agent).catch(notFoundIsNull),
    enabled: can(Perm.HostingRead),
  })
}

function useHostingChanged(agent: string) {
  const qc = useQueryClient()
  return (h?: unknown) => {
    if (h !== undefined) qc.setQueryData(keys.hosted(agent), h)
    qc.invalidateQueries({ queryKey: keys.hosting, exact: true })
    qc.invalidateQueries({ queryKey: keys.crewTokens })
  }
}

export function useHost(agent: string) {
  const changed = useHostingChanged(agent)
  return useMutation({
    mutationFn: ({ slots }: { slots: number }) => api.host(agent, slots),
    onSuccess: (h) => changed(h),
    onError: fail("No se pudo alojar el agente"),
  })
}

export function useUpdateHosting(agent: string) {
  const changed = useHostingChanged(agent)
  return useMutation({
    mutationFn: (patch: HostingPatch) => api.updateHosting(agent, patch),
    onSuccess: (h) => changed(h),
    onError: fail("No se pudo cambiar el alojamiento"),
  })
}

export function useUnhost(agent: string) {
  const changed = useHostingChanged(agent)
  return useMutation({
    mutationFn: () => api.unhost(agent),
    onSuccess: () => changed(null),
    onError: fail("No se pudo dejar de alojar el agente"),
  })
}

export function useSlotAction(agent: string) {
  const changed = useHostingChanged(agent)
  return useMutation({
    mutationFn: ({ slot, action }: { slot: number; action: "start" | "stop" }) => api.slotAction(agent, slot, action),
    onSuccess: (h) => changed(h),
    onError: fail("No se pudo cambiar la instancia"),
  })
}

/** A slot's log tail, refreshed every few seconds while open. */
export function useSlotLog(agent: string, slot: number, open: boolean) {
  const can = useCan()
  return useQuery({
    queryKey: keys.slotLog(agent, slot),
    queryFn: () => api.slotLog(agent, slot),
    enabled: open && can(Perm.HostingWrite),
    refetchInterval: open ? 3000 : false,
    retry: false,
  })
}

/** A slot's account (signed in or not), while the slot runs. */
export function useSlotAccount(agent: string, slot: number, enabled: boolean) {
  const can = useCan()
  return useQuery({
    queryKey: keys.slotAccount(agent, slot),
    queryFn: () => api.slotAccount(agent, slot),
    enabled: enabled && can(Perm.HostingRead),
    staleTime: 60_000,
    retry: false,
  })
}

/** The slot's sign-in, polled while it goes (the URL shows once waiting). */
export function useSlotLogin(agent: string, slot: number, enabled: boolean) {
  const can = useCan()
  return useQuery({
    queryKey: keys.slotLogin(agent, slot),
    queryFn: () => api.slotLogin(agent, slot),
    enabled: enabled && can(Perm.HostingWrite),
    refetchInterval: (q) => {
      const s = q.state.data?.state
      return s === "starting" || s === "waiting" || s === "verifying" ? 1000 : false
    },
    retry: false,
  })
}

export function useStartLogin(agent: string, slot: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.startLogin(agent, slot),
    onSuccess: (l) => {
      // Over an older read in flight (the dialog opening), then poll it.
      qc.setQueryData(keys.slotLogin(agent, slot), l)
      qc.invalidateQueries({ queryKey: keys.slotLogin(agent, slot) })
    },
    onError: fail("No se pudo iniciar la conexión"),
  })
}

export function useLoginCode(agent: string, slot: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (code: string) => api.loginCode(agent, slot, code),
    onSuccess: (l) => qc.setQueryData(keys.slotLogin(agent, slot), l),
    onError: fail("No se pudo enviar el código"),
  })
}

export function useEgress(agent: string, enabled: boolean) {
  const can = useCan()
  return useQuery({
    queryKey: keys.egress(agent),
    queryFn: () => api.egress(agent),
    enabled: enabled && can(Perm.HostingRead),
    refetchInterval: 60_000,
  })
}

export function useUsage(agent: string, days = 30) {
  const can = useCan()
  return useQuery({
    queryKey: keys.usage(agent, days),
    queryFn: () => api.usage(agent, days),
    enabled: can(Perm.AgentsRead),
    refetchInterval: 60_000,
  })
}

// Schedules: prompts the port posts into a conversation at set times.

export function useSchedules(conversation: string) {
  const can = useCan()
  return useQuery({
    queryKey: keys.schedules(conversation),
    queryFn: () => api.schedules(conversation),
    enabled: can(Perm.SchedulesRead),
  })
}

export function useCreateSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ScheduleInput) => api.createSchedule(body),
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: keys.schedules(s.conversation_id) })
      toast.success("Programación creada", { description: s.name })
    },
    onError: fail("No se pudo crear la programación"),
  })
}

export function useUpdateSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: { id: string } & Parameters<typeof api.updateSchedule>[1]) =>
      api.updateSchedule(id, body),
    onSuccess: (s) => qc.invalidateQueries({ queryKey: keys.schedules(s.conversation_id) }),
    onError: fail("No se pudo guardar la programación"),
  })
}

export function useDeleteSchedule(conversation: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.deleteSchedule(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.schedules(conversation) })
      toast.success("Programación eliminada")
    },
    onError: fail("No se pudo eliminar la programación"),
  })
}

export function useRunSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.runSchedule(id),
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: keys.schedulesAll })
      qc.invalidateQueries({ queryKey: keys.ships(s.conversation_id) })
      toast.success("Mensaje enviado ahora", { description: "Pasó por la aduana como cualquier otro." })
    },
    onError: fail("No se pudo ejecutar la programación"),
  })
}

/** The next fire times of an expression (the "new schedule" dialog); empty while it doesn't parse. */
export function useSchedulePreview(cron: string, timezone: string) {
  const can = useCan()
  const c = cron.trim()
  return useQuery({
    queryKey: ["schedule-preview", c, timezone],
    queryFn: () => api.previewSchedule(c, timezone),
    enabled: can(Perm.SchedulesRead) && c !== "",
    staleTime: 60_000,
    retry: false,
  })
}

// Vaults: shared markdown memory, its entries and who may use it.

export function useVaults() {
  const can = useCan()
  return useQuery({ queryKey: keys.vaults, queryFn: api.vaults, enabled: can(Perm.VaultsRead) })
}

export function useVault(id: string) {
  const can = useCan()
  return useQuery({ queryKey: keys.vault(id), queryFn: () => api.vault(id), enabled: can(Perm.VaultsRead) })
}

/** The vault's entries newest first; with q, the full-text hits instead. */
export function useVaultEntries(id: string) {
  const can = useCan()
  return useInfiniteQuery({
    enabled: can(Perm.VaultsRead),
    queryKey: keys.vaultEntries(id),
    queryFn: ({ pageParam }) => api.vaultEntries(id, PAGE, pageParam),
    initialPageParam: 0,
    getNextPageParam: nextOffset<VaultEntrySummary>,
  })
}

export function useVaultSearch(id: string, q: string) {
  const can = useCan()
  return useQuery({
    enabled: can(Perm.VaultsRead) && q !== "",
    queryKey: keys.vaultEntries(id, q),
    queryFn: () => api.searchVault(id, q),
  })
}

export function useVaultEntry(id: string, entry: string | undefined) {
  const can = useCan()
  return useQuery({
    enabled: !!entry && can(Perm.VaultsRead),
    queryKey: keys.vaultEntry(id, entry ?? ""),
    queryFn: () => api.vaultEntry(id, entry!),
  })
}

export function useVaultVersions(id: string, entry: string | undefined, enabled: boolean) {
  const can = useCan()
  return useQuery({
    enabled: !!entry && enabled && can(Perm.VaultsRead),
    queryKey: keys.vaultVersions(id, entry ?? ""),
    queryFn: () => api.vaultVersions(id, entry!),
  })
}

/** The vaults an agent or a dock is granted, with the mode of each. */
export function useVaultsOf(kind: "agent" | "dock", id: string) {
  const can = useCan()
  return useQuery({
    queryKey: kind === "agent" ? keys.agentVaults(id) : keys.dockVaults(id),
    queryFn: () => (kind === "agent" ? api.agentVaults(id) : api.dockVaults(id)),
    enabled: can(Perm.VaultsRead),
  })
}

/** What a vault change touches: its lists and every grants view. */
function useVaultsChanged() {
  const qc = useQueryClient()
  return (id?: string) => {
    qc.invalidateQueries({ queryKey: keys.vaults, exact: true })
    if (id) qc.invalidateQueries({ queryKey: keys.vault(id), exact: true })
    qc.invalidateQueries({ queryKey: ["agent-vaults"] })
    qc.invalidateQueries({ queryKey: ["dock-vaults"] })
  }
}

export function useCreateVault() {
  const changed = useVaultsChanged()
  return useMutation({
    mutationFn: (b: { name: string; title: string; description: string }) => api.createVault(b),
    onSuccess: () => changed(),
    onError: fail("No se pudo crear la bóveda"),
  })
}

export function useUpdateVault(id: string) {
  const changed = useVaultsChanged()
  return useMutation({
    mutationFn: (patch: { title?: string; description?: string }) => api.updateVault(id, patch),
    onSuccess: () => changed(id),
    onError: fail("No se pudo guardar la bóveda"),
  })
}

export function useDeleteVault() {
  const qc = useQueryClient()
  const changed = useVaultsChanged()
  return useMutation({
    mutationFn: (id: string) => api.deleteVault(id),
    onSuccess: (_, id) => {
      qc.removeQueries({ queryKey: keys.vault(id) })
      changed()
      toast.success("Bóveda eliminada")
    },
    onError: fail("No se pudo eliminar la bóveda"),
  })
}

/** Grants and revokes of a vault to an agent or a dock (a new grant on a taken pair changes its mode). */
export function useVaultGrants() {
  const changed = useVaultsChanged()
  return {
    grant: useMutation({
      mutationFn: ({ vault, kind, target, mode }: { vault: string; kind: "agent" | "dock"; target: string; mode: VaultMode }) =>
        kind === "agent" ? api.grantVaultAgent(vault, target, mode) : api.grantVaultDock(vault, target, mode),
      onSuccess: (_, { vault }) => changed(vault),
      onError: fail("No se pudo dar acceso"),
    }),
    revoke: useMutation({
      mutationFn: ({ vault, kind, target }: { vault: string; kind: "agent" | "dock"; target: string }) =>
        kind === "agent" ? api.revokeVaultAgent(vault, target) : api.revokeVaultDock(vault, target),
      onSuccess: (_, { vault }) => changed(vault),
      onError: fail("No se pudo quitar el acceso"),
    }),
  }
}

/**
 * Entry writes. Errors are not toasted: the editor shows them next to the
 * text (a 409 means someone saved meanwhile, a 422 is the API's own words,
 * e.g. the secret scan's).
 */
export function useVaultEntryActions(id: string) {
  const qc = useQueryClient()
  const changed = () => {
    qc.invalidateQueries({ queryKey: keys.vaultEntries(id) })
    qc.invalidateQueries({ queryKey: keys.vault(id), exact: true })
    qc.invalidateQueries({ queryKey: keys.vaults, exact: true })
  }
  return {
    create: useMutation({
      mutationFn: ({ name, body }: { name: string; body: string }) => api.createVaultEntry(id, name, body),
      onSuccess: changed,
    }),
    save: useMutation({
      mutationFn: ({ entry, body, base }: { entry: string; body: string; base: number }) =>
        api.saveVaultEntry(id, entry, body, base),
      onSuccess: changed,
    }),
    remove: useMutation({
      mutationFn: (entry: string) => api.deleteVaultEntry(id, entry),
      onSuccess: (_, entry) => {
        qc.removeQueries({ queryKey: keys.vaultEntry(id, entry) })
        changed()
        toast.success("Entrada eliminada")
      },
      onError: fail("No se pudo eliminar la entrada"),
    }),
    revert: useMutation({
      mutationFn: ({ entry, version }: { entry: string; version: number }) => api.revertVaultEntry(id, entry, version),
      onSuccess: (e, { version }) => {
        changed()
        toast.success(`Entrada revertida a la versión ${version}`, { description: `Ahora es la v${e.version}.` })
      },
      onError: fail("No se pudo revertir"),
    }),
  }
}
