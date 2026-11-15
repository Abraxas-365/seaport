import * as React from "react"
import { KeyRound, MailPlus, Plus, UserPlus } from "lucide-react"

import {
  useCreateMemberApiKey,
  useInvite,
  useMemberAccess,
  useMemberApiKeys,
  useMemberInvitations,
  useMembers,
  useNow,
  useRemoveMember,
  useResendInvitation,
  useRevokeInvitation,
  useRevokeMemberApiKey,
  useSetMemberAccess,
} from "@/lib/hooks"
import type { Invitation, IssuedMemberApiKey } from "@/lib/types"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ApiKeyRow,
  CopyField,
  EmptyState,
  ErrorState,
  InvitationRow,
  LoadingState,
  MemberRow,
  NoAccess,
  PageHeader,
  RoleSelect,
  SegmentedChoice,
  memberRoles,
  roleLabel,
  type ChoiceOption,
} from "@/components/seaport"

/**
 * "Miembros y accesos": the organization's people, invitations and CLI API
 * keys, administered from Seaport (iam mode). What shows follows
 * `useMemberAccess` (IAMKit's iam:org:* permissions), not `useCan`: those
 * permissions are not in Seaport's catalog.
 */
export function MembersPage() {
  const access = useMemberAccess()
  if (access.pending) return <LoadingState className="h-full" />
  if (!access.members) return <NoAccess what="administrar miembros" className="h-full" />
  return (
    <div className="min-h-0 flex-1 scrollbar-thin overflow-y-auto">
      <PageHeader
        title="Miembros y accesos"
        description="Quién entra a tu organización y con qué rol. Los cambios los aplica IAMKit; a los miembros les llegan en su próximo inicio de sesión."
      />
      <div className="mx-auto max-w-4xl p-6">
        <Tabs defaultValue="members">
          <TabsList>
            <TabsTrigger value="members">Miembros</TabsTrigger>
            <TabsTrigger value="invitations">Invitaciones</TabsTrigger>
            <TabsTrigger value="keys">Llaves de API</TabsTrigger>
          </TabsList>
          <TabsContent value="members" className="pt-3">
            <MembersTab />
          </TabsContent>
          <TabsContent value="invitations" className="pt-3">
            <InvitationsTab />
          </TabsContent>
          <TabsContent value="keys" className="pt-3">
            <KeysTab />
          </TabsContent>
        </Tabs>
      </div>
    </div>
  )
}

function ListSkeleton() {
  return (
    <div className="grid gap-3">
      <Skeleton className="h-14 rounded-xl" />
      <Skeleton className="h-14 rounded-xl" />
    </div>
  )
}

function MembersTab() {
  const access = useMemberAccess()
  const members = useMembers()
  const set = useSetMemberAccess()
  const remove = useRemoveMember()
  if (members.isPending) return <ListSkeleton />
  if (members.isError) return <ErrorState error={members.error} onRetry={() => members.refetch()} />
  return (
    <ul className="divide-y rounded-xl border bg-card">
      {members.data.items.map((m) => (
        <MemberRow
          key={m.id}
          member={m}
          canAssign={access.assign_roles}
          canRemove={access.remove}
          busy={set.isPending && set.variables?.id === m.id}
          onChange={(change) => set.mutate({ id: m.id, ...change })}
          onRemove={() => remove.mutateAsync(m.id)}
        />
      ))}
    </ul>
  )
}

function InvitationsTab() {
  const access = useMemberAccess()
  const now = useNow()
  const list = useMemberInvitations()
  const resend = useResendInvitation()
  const revoke = useRevokeInvitation()
  const [fresh, setFresh] = React.useState<Invitation>()
  const resendOne = (id: string) => resend.mutate(id, { onSuccess: (i) => i.link && setFresh(i) })
  return (
    <div className="space-y-3">
      {access.invite && (
        <div className="flex justify-end">
          <InviteDialog />
        </div>
      )}
      {list.isPending ? (
        <ListSkeleton />
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.items.length === 0 ? (
        <EmptyState
          icon={MailPlus}
          title="Sin invitaciones pendientes"
          description="Invita por correo a quien deba entrar a tu organización."
          action={access.invite && <InviteDialog />}
        />
      ) : (
        <ul className="divide-y rounded-xl border bg-card">
          {list.data.items.map((i) => (
            <InvitationRow
              key={i.id}
              invitation={i}
              now={now}
              busy={resend.isPending && resend.variables === i.id}
              onResend={access.invite ? () => resendOne(i.id) : undefined}
              onRevoke={access.invite ? () => revoke.mutateAsync(i.id) : undefined}
            />
          ))}
        </ul>
      )}
      <Dialog open={!!fresh} onOpenChange={(o) => !o && setFresh(undefined)}>
        <DialogContent className="sm:max-w-lg">
          {fresh && <InvitationLink invitation={fresh} onDone={() => setFresh(undefined)} />}
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** The accept link, when IAMKit didn't email it: whoever invites hands it over. */
function InvitationLink({ invitation, onDone }: { invitation: Invitation; onDone: () => void }) {
  return (
    <>
      <DialogHeader>
        <DialogTitle>Invitación para {invitation.email}</DialogTitle>
        <DialogDescription>
          IAMKit no envió el correo. Comparte este enlace con la persona: no se vuelve a mostrar.
        </DialogDescription>
      </DialogHeader>
      <div className="min-w-0 py-5">
        <CopyField value={invitation.link ?? ""} label="Enlace de invitación" testId="invitation-link" />
      </div>
      <DialogFooter>
        <Button onClick={onDone}>Listo</Button>
      </DialogFooter>
    </>
  )
}

function InviteDialog() {
  const invite = useInvite()
  const [open, setOpen] = React.useState(false)
  const [email, setEmail] = React.useState("")
  const [role, setRole] = React.useState("Viewer")
  const [manages, setManages] = React.useState(false)
  const [sent, setSent] = React.useState<Invitation>()
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      setEmail("")
      setRole("Viewer")
      setManages(false)
      setSent(undefined)
    }
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>
        <Button>
          <UserPlus /> Invitar
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {sent ? (
          sent.link ? (
            <InvitationLink invitation={sent} onDone={() => close(false)} />
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>Invitación enviada</DialogTitle>
                <DialogDescription>IAMKit envió el correo a {sent.email}.</DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button onClick={() => close(false)}>Listo</Button>
              </DialogFooter>
            </>
          )
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              invite.mutate({ email: email.trim(), role, manages_members: manages }, { onSuccess: setSent })
            }}
          >
            <DialogHeader>
              <DialogTitle>Invitar a la organización</DialogTitle>
              <DialogDescription>La persona recibe el rol elegido al aceptar la invitación.</DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field>
                <FieldLabel htmlFor="invite-email">Correo</FieldLabel>
                <Input
                  id="invite-email"
                  type="email"
                  value={email}
                  placeholder="persona@example.com"
                  onChange={(e) => setEmail(e.target.value)}
                  autoFocus
                />
              </Field>
              <Field>
                <FieldLabel>Rol en Seaport</FieldLabel>
                <RoleSelect value={role} onChange={setRole} />
                <FieldDescription>{memberRoles.find((r) => r.value === role)?.hint}</FieldDescription>
              </Field>
              <Field orientation="horizontal">
                <Switch id="invite-manages" checked={manages} onCheckedChange={setManages} />
                <FieldLabel htmlFor="invite-manages">Puede administrar miembros y llaves de API</FieldLabel>
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => close(false)}>
                Cancelar
              </Button>
              <Button type="submit" disabled={!email.trim() || invite.isPending}>
                {invite.isPending && <Spinner />} Invitar
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

function KeysTab() {
  const access = useMemberAccess()
  const now = useNow()
  const list = useMemberApiKeys(access.keys_read)
  const revoke = useRevokeMemberApiKey()
  if (!access.keys_read) return <NoAccess what="las llaves de API" />
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        Llaves de la organización para el CLI y la automatización: actúan como la organización, con el rol que elijas, no como una
        persona.
      </p>
      {access.keys_write && (
        <div className="flex justify-end">
          <NewKeyDialog />
        </div>
      )}
      {list.isPending ? (
        <ListSkeleton />
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.items.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title="Sin llaves de API"
          description="Crea una para usar el CLI de Seaport sin iniciar sesión como persona."
          action={access.keys_write && <NewKeyDialog />}
        />
      ) : (
        <ul className="divide-y rounded-xl border bg-card">
          {list.data.items.map((k) => (
            <ApiKeyRow key={k.id} apiKey={k} now={now} onRevoke={access.keys_write ? () => revoke.mutateAsync(k.id) : undefined} />
          ))}
        </ul>
      )}
    </div>
  )
}

type Lifetime = "720h" | "2160h" | "8760h" | "never"

const lifetimeOptions: ChoiceOption<Lifetime>[] = [
  { value: "720h", label: "30 días" },
  { value: "2160h", label: "90 días" },
  { value: "8760h", label: "1 año" },
  { value: "never", label: "Sin vencimiento" },
]

function NewKeyDialog() {
  const create = useCreateMemberApiKey()
  const [open, setOpen] = React.useState(false)
  const [name, setName] = React.useState("")
  const [role, setRole] = React.useState("Viewer")
  const [lifetime, setLifetime] = React.useState<Lifetime>("2160h")
  const [issued, setIssued] = React.useState<IssuedMemberApiKey>()
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      // The value is gone once the dialog closes; it is never shown again.
      setName("")
      setRole("Viewer")
      setIssued(undefined)
    }
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogTrigger asChild>
        <Button>
          <Plus /> Nueva llave
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {issued ? (
          <>
            <DialogHeader>
              <DialogTitle>Llave «{issued.name}» creada</DialogTitle>
              <DialogDescription>Cópiala ahora: no se vuelve a mostrar. Rol: {roleLabel(issued.role)}.</DialogDescription>
            </DialogHeader>
            <div className="min-w-0 space-y-3 py-5">
              <CopyField value={issued.value} label="Valor de la llave" testId="issued-api-key" />
              <p className="text-xs break-words text-muted-foreground">
                En el CLI: <code>SEAPORT_ADMIN_TOKEN=&lt;llave&gt; seaport …</code>
              </p>
            </div>
            <DialogFooter>
              <Button onClick={() => close(false)}>Listo</Button>
            </DialogFooter>
          </>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              create.mutate(
                { name: name.trim(), role, expires_in: lifetime === "never" ? undefined : lifetime },
                { onSuccess: setIssued },
              )
            }}
          >
            <DialogHeader>
              <DialogTitle>Nueva llave de API</DialogTitle>
              <DialogDescription>Una llave por uso, para poder revocarlas por separado.</DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-5">
              <Field>
                <FieldLabel htmlFor="key-name">Nombre</FieldLabel>
                <Input
                  id="key-name"
                  value={name}
                  placeholder="CI de producción"
                  maxLength={80}
                  onChange={(e) => setName(e.target.value)}
                  autoFocus
                />
              </Field>
              <Field>
                <FieldLabel>Rol</FieldLabel>
                <RoleSelect value={role} onChange={setRole} />
                <FieldDescription>{memberRoles.find((r) => r.value === role)?.hint}</FieldDescription>
              </Field>
              <Field>
                <FieldLabel>Vigencia</FieldLabel>
                <SegmentedChoice
                  aria-label="Vigencia"
                  size="sm"
                  value={lifetime}
                  options={lifetimeOptions}
                  onChange={setLifetime}
                  className="w-full sm:w-full"
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => close(false)}>
                Cancelar
              </Button>
              <Button type="submit" disabled={!name.trim() || create.isPending}>
                {create.isPending && <Spinner />} Crear llave
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

