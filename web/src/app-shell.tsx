import { NavLink, Outlet, useLocation } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Anchor, Bot, Building2, Inbox, KeyRound, LogOut, Monitor, Moon, ShieldCheck, Sun } from "lucide-react"

import { api } from "@/lib/api"
import { useCan, useCrew, useDocks, useSession } from "@/lib/hooks"
import { Perm } from "@/lib/permissions.gen"
import { keys } from "@/lib/query"
import { useTheme } from "@/components/theme-provider"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  ChannelIcon,
  LiveIndicator,
  SeaportLogo,
  SeaportMark,
  StatusDot,
  dockTone,
} from "@/components/seaport"

export function AppShell() {
  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset className="h-svh min-h-0 overflow-hidden">
        <header className="flex h-12 shrink-0 items-center gap-2 border-b px-3 md:hidden">
          <SidebarTrigger />
          <SeaportLogo />
          <LiveIndicator compact className="ml-auto" />
        </header>
        <Outlet />
      </SidebarInset>
    </SidebarProvider>
  )
}

function AppSidebar() {
  const { pathname } = useLocation()
  const can = useCan()
  const docks = useDocks()
  const crew = useCrew()
  const pending = useQuery({
    queryKey: keys.inbox({ status: "pending", limit: 1 }),
    queryFn: () => api.inbox({ status: "pending", limit: 1 }),
    enabled: can(Perm.ConversationsRead),
  })
  const agents = crew.data?.agents ?? []
  const working = agents.reduce((n, a) => n + a.deliveries.length, 0)

  // Only what the user's role can open (the server enforces it anyway).
  const nav = [
    { to: "/inbox", label: "Bandeja", icon: Inbox, badge: undefined as number | undefined, perm: Perm.ConversationsRead },
    { to: "/customs", label: "Aduana", icon: ShieldCheck, badge: pending.data?.page.total || undefined, perm: Perm.CustomsRead },
    { to: "/docks", label: "Muelles", icon: Anchor, badge: undefined, perm: Perm.DocksRead },
    { to: "/crew", label: "Tripulación", icon: Bot, badge: agents.length || undefined, perm: Perm.CrewRead },
    { to: "/tokens", label: "Tokens de agentes", icon: KeyRound, badge: undefined, perm: Perm.CrewTokensRead },
  ].filter((item) => can(item.perm))

  return (
    <Sidebar collapsible="icon" variant="sidebar">
      <SidebarHeader className="h-14 justify-center border-b border-sidebar-border px-3 group-data-[collapsible=icon]:px-2">
        <div className="flex items-center justify-between">
          <span className="group-data-[collapsible=icon]:hidden">
            <SeaportLogo />
          </span>
          <SeaportMark className="hidden group-data-[collapsible=icon]:block" />
          <SidebarTrigger className="group-data-[collapsible=icon]:hidden" />
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {nav.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton asChild isActive={pathname.startsWith(item.to)} tooltip={item.label}>
                    <NavLink to={item.to}>
                      <item.icon />
                      <span>{item.label}</span>
                    </NavLink>
                  </SidebarMenuButton>
                  {item.badge !== undefined && <SidebarMenuBadge>{item.badge}</SidebarMenuBadge>}
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        {(docks.data?.items.length ?? 0) > 0 && (
          <SidebarGroup>
            <SidebarGroupLabel>Muelles</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {docks.data!.items.map((d) => (
                  <SidebarMenuItem key={d.id}>
                    <SidebarMenuButton asChild size="sm" isActive={pathname === `/docks/${d.id}`} tooltip={d.name}>
                      <NavLink to={`/docks/${d.id}`}>
                        <ChannelIcon channel={d.channel} size="sm" className="-ml-1 rounded-md" />
                        <span className="truncate">{d.name}</span>
                        <StatusDot tone={dockTone[d.status]} pulse={d.status === "linking"} className="ml-auto" />
                      </NavLink>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>
      <SidebarFooter className="border-t border-sidebar-border">
        <div className="flex items-center gap-2 px-2 py-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
          <LiveIndicator />
          <span className="ml-auto">
            {agents.length === 0 ? "sin agentes" : working ? `${working} en curso` : `${agents.length} libre${agents.length === 1 ? "" : "s"}`}
          </span>
        </div>
        <AccountMenu />
      </SidebarFooter>
    </Sidebar>
  )
}

function AccountMenu() {
  const qc = useQueryClient()
  const { theme, setTheme } = useTheme()
  const health = useQuery({ queryKey: keys.health, queryFn: api.health })
  const session = useSession()
  // SEAPORT_AUTH=none has no session to close.
  const config = useQuery({ queryKey: keys.config, queryFn: api.config, staleTime: Infinity })
  const open = config.data?.auth === "none"
  const iam = config.data?.auth === "iam"
  const s = session.data
  const who = iam ? s?.name || s?.email || "Cuenta" : open ? "Local" : "Administrador"
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: (r) => {
      // iam mode: finish at IAMKit too, or its session would sign us back in.
      if (r?.logout_url) window.location.assign(r.logout_url)
    },
    onSettled: (r) => {
      if (r?.logout_url) return
      qc.clear()
      qc.setQueryData(keys.session, { authenticated: false })
    },
  })
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton tooltip="Cuenta y tema" size={iam ? "lg" : "default"}>
              <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-md bg-sidebar-accent text-[0.65rem] font-semibold uppercase">
                {who.slice(0, 1)}
              </span>
              <span className="grid min-w-0 flex-1 leading-tight">
                <span className="truncate">{who}</span>
                {iam && s?.organization_name && (
                  <span className="truncate text-[0.7rem] text-muted-foreground">{s.organization_name}</span>
                )}
              </span>
              <span className="ml-auto max-w-24 shrink-0 truncate text-[0.65rem] text-muted-foreground">{health.data?.version}</span>
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="top" align="start" className="w-60">
            {iam && (
              <>
                <DropdownMenuLabel className="grid font-normal">
                  <span className="truncate font-medium">{s?.name || s?.email}</span>
                  {s?.name && s?.email && <span className="truncate text-xs text-muted-foreground">{s.email}</span>}
                  <span className="truncate text-xs text-muted-foreground">{s?.organization_name}</span>
                </DropdownMenuLabel>
                <DropdownMenuItem asChild>
                  <a href={api.signInURL({ switchAccount: true })}>
                    <Building2 /> Cambiar de organización
                  </a>
                </DropdownMenuItem>
                <DropdownMenuSeparator />
              </>
            )}
            <DropdownMenuLabel>Tema</DropdownMenuLabel>
            <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as typeof theme)}>
              <DropdownMenuRadioItem value="light">
                <Sun /> Claro
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="dark">
                <Moon /> Oscuro
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="system">
                <Monitor /> Sistema
              </DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
            {!open && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={() => logout.mutate()}>
                  <LogOut /> Cerrar sesión
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
