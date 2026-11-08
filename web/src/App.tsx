import * as React from "react"
import { BrowserRouter, Navigate, Route, Routes } from "react-router"
import { QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query"

import { api, setUnauthorizedHandler } from "@/lib/api"
import { useLiveEvents } from "@/lib/live"
import { keys, queryClient } from "@/lib/query"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { LoadingState, SeaportMark } from "@/components/seaport"
import { AppShell } from "@/app-shell"
import { LoginPage } from "@/pages/login"
import { InboxIndex, InboxPage } from "@/pages/inbox"
import { ConversationPage } from "@/pages/conversation"
import { CustomsPage } from "@/pages/customs"
import { DocksPage } from "@/pages/docks"
import { DockPage } from "@/pages/dock"
import { CrewPage } from "@/pages/crew"
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
  const session = useQuery({ queryKey: keys.session, queryFn: api.session, staleTime: Infinity })
  const authed = !!session.data?.authenticated

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

  return (
    <Routes>
      <Route path="/design" element={<DesignSystemPage />} />
      <Route element={<AppShell />}>
        <Route path="/inbox" element={<InboxPage />}>
          <Route index element={<InboxIndex />} />
          <Route path=":conversationId" element={<ConversationPage />} />
        </Route>
        <Route path="/customs" element={<CustomsPage />} />
        <Route path="/docks" element={<DocksPage />} />
        <Route path="/docks/:dockId" element={<DockPage />} />
        <Route path="/crew" element={<CrewPage />} />
        <Route path="*" element={<Navigate to="/inbox" replace />} />
      </Route>
    </Routes>
  )
}

export default App
