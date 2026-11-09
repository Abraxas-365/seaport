import * as React from "react"
import { BrowserRouter, Navigate, Route, Routes } from "react-router"
import { QueryClientProvider, useQueryClient } from "@tanstack/react-query"

import { setUnauthorizedHandler } from "@/lib/api"
import { useCan, useSession } from "@/lib/hooks"
import { useLiveEvents } from "@/lib/live"
import { Perm, type Permission } from "@/lib/permissions.gen"
import { keys, queryClient } from "@/lib/query"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { LoadingState, NoAccess, SeaportMark } from "@/components/seaport"
import { AppShell } from "@/app-shell"
import { LoginPage } from "@/pages/login"
import { InboxIndex, InboxPage } from "@/pages/inbox"
import { ConversationPage } from "@/pages/conversation"
import { CustomsPage } from "@/pages/customs"
import { DocksPage } from "@/pages/docks"
import { DockPage } from "@/pages/dock"
import { CrewPage } from "@/pages/crew"
import { TokensPage } from "@/pages/tokens"
import { AgentsPage } from "@/pages/agents"
import { DesignSystemPage } from "@/pages/design-system"

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={300}>
        <BrowserRouter>
          <Gate />
        </BrowserRouter>
        <Toaster position="bottom-right" richColors />
      </TooltipProvider>
    </QueryClientProvider>
  )
}

/** Login when there is no session; the app (and its live stream) when there is. */
function Gate() {
  const qc = useQueryClient()
  const session = useSession()
  const authed = !!session.data?.authenticated
  const can = useCan()

  React.useEffect(() => {
    setUnauthorizedHandler(() => qc.setQueryData(keys.session, { authenticated: false }))
  }, [qc])
  useLiveEvents(qc, authed)

  if (session.isPending)
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-4">
        <SeaportMark className="size-10" />
        <LoadingState label="Abriendo el puerto…" className="p-0" />
      </div>
    )
  if (!authed)
    return (
      <Routes>
        <Route path="/design" element={<DesignSystemPage />} />
        <Route path="*" element={<LoginPage />} />
      </Routes>
    )

  // The first section the role can open.
  const home =
    [
      { to: "/inbox", perm: Perm.ConversationsRead },
      { to: "/docks", perm: Perm.DocksRead },
      { to: "/agents", perm: Perm.AgentsRead },
      { to: "/crew", perm: Perm.CrewRead },
      { to: "/tokens", perm: Perm.CrewTokensRead },
    ].find((s) => can(s.perm))?.to ?? "/none"

  return (
    <Routes>
      <Route path="/design" element={<DesignSystemPage />} />
      <Route element={<AppShell />}>
        <Route
          path="/inbox"
          element={
            <Guard perm={Perm.ConversationsRead} what="la bandeja">
              <InboxPage />
            </Guard>
          }
        >
          <Route index element={<InboxIndex />} />
          <Route path=":conversationId" element={<ConversationPage />} />
        </Route>
        <Route
          path="/customs"
          element={
            <Guard perm={Perm.CustomsRead} what="la aduana">
              <CustomsPage />
            </Guard>
          }
        />
        <Route
          path="/docks"
          element={
            <Guard perm={Perm.DocksRead} what="los muelles">
              <DocksPage />
            </Guard>
          }
        />
        <Route
          path="/docks/:dockId"
          element={
            <Guard perm={Perm.DocksRead} what="los muelles">
              <DockPage />
            </Guard>
          }
        />
        <Route
          path="/agents"
          element={
            <Guard perm={Perm.AgentsRead} what="los agentes">
              <AgentsPage />
            </Guard>
          }
        />
        <Route
          path="/crew"
          element={
            <Guard perm={Perm.CrewRead} what="la tripulación">
              <CrewPage />
            </Guard>
          }
        />
        <Route
          path="/tokens"
          element={
            <Guard perm={Perm.CrewTokensRead} what="los tokens de agentes">
              <TokensPage />
            </Guard>
          }
        />
        <Route path="/none" element={<NoAccess what="ninguna sección de Seaport" className="h-full" />} />
        <Route path="*" element={<Navigate to={home} replace />} />
      </Route>
    </Routes>
  )
}

/** A page the role can't open shows why instead of failing requests. */
function Guard({ perm, what, children }: { perm: Permission; what: string; children: React.ReactNode }) {
  const can = useCan()
  return can(perm) ? children : <NoAccess what={what} className="h-full" />
}

export default App
