import * as React from "react"
import { useLocation } from "react-router"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowRight, KeyRound, Lock, LogIn } from "lucide-react"

import { api, ApiError } from "@/lib/api"
import { keys } from "@/lib/query"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { SeaportMark } from "@/components/seaport"

export function LoginPage() {
  const config = useQuery({
    queryKey: keys.config,
    queryFn: api.config,
    staleTime: Infinity,
  })
  return (
    <div className="relative flex min-h-svh items-center justify-center overflow-hidden bg-canvas p-6">
      <Harbor />
      <div className="relative w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <SeaportMark className="size-12" />
          <div>
            <h1 className="font-heading text-2xl font-semibold tracking-tight">Seaport</h1>
            <p className="mt-1 text-sm text-muted-foreground">Donde llegan los mensajes y trabajan tus agentes.</p>
          </div>
        </div>
        {config.isPending ? (
          <div className="flex justify-center">
            <Spinner />
          </div>
        ) : config.data?.auth === "iam" ? (
          <SignInForm />
        ) : (
          <TokenForm />
        )}
      </div>
    </div>
  )
}

/** iam mode: IAMKit's hosted sign-in (a full-page navigation, not fetch). */
function SignInForm() {
  const location = useLocation()
  const error = new URLSearchParams(location.search).get("signin_error")
  const [leaving, setLeaving] = React.useState(false)
  // Come back to where the user was (the server only accepts local paths).
  const next = location.pathname === "/" ? undefined : location.pathname + location.search
  return (
    <div className="rounded-2xl border bg-card p-6 shadow-sm">
      <FieldGroup>
        {error && (
          <p
            role="alert"
            className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive"
          >
            {error}
          </p>
        )}
        <Button asChild className="w-full" onClick={() => setLeaving(true)}>
          <a href={api.signInURL({ next })}>
            {leaving ? <Spinner /> : <LogIn />} Iniciar sesión <ArrowRight className="ml-auto" />
          </a>
        </Button>
        <FieldDescription className="text-center">
          Con la cuenta de tu organización. Ves y gestionas solo lo de tu organización, según tus permisos.
        </FieldDescription>
      </FieldGroup>
    </div>
  )
}

/** token mode: the admin token, exchanged for an HttpOnly cookie. */
function TokenForm() {
  const qc = useQueryClient()
  const [token, setToken] = React.useState("")
  const login = useMutation({
    mutationFn: () => api.login(token),
    onSuccess: (s) => qc.setQueryData(keys.session, s),
  })
  const error =
    login.error instanceof ApiError && login.error.status === 401
      ? "Ese token no es el del puerto."
      : login.error
        ? login.error.message
        : undefined

  return (
    <form
      className="rounded-2xl border bg-card p-6 shadow-sm"
      onSubmit={(e) => {
        e.preventDefault()
        if (token.trim()) login.mutate()
      }}
    >
      <FieldGroup>
        <Field data-invalid={!!error || undefined}>
          <FieldLabel htmlFor="token">Token de administrador</FieldLabel>
          <InputGroup>
            <InputGroupAddon>
              <KeyRound />
            </InputGroupAddon>
            <InputGroupInput
              id="token"
              type="password"
              autoFocus
              autoComplete="current-password"
              placeholder="SEAPORT_ADMIN_TOKEN"
              value={token}
              aria-invalid={!!error || undefined}
              onChange={(e) => setToken(e.target.value)}
            />
          </InputGroup>
          {error ? (
            <FieldError>{error}</FieldError>
          ) : (
            <FieldDescription>
              El valor de <code>SEAPORT_ADMIN_TOKEN</code> del servidor. Se cambia por una cookie segura; no queda
              guardado en el navegador.
            </FieldDescription>
          )}
        </Field>
        <Button type="submit" className="w-full" disabled={!token.trim() || login.isPending}>
          {login.isPending ? <Spinner /> : <Lock />} Entrar <ArrowRight className="ml-auto" />
        </Button>
      </FieldGroup>
    </form>
  )
}

/** Soft waves at the bottom of the login screen. */
function Harbor() {
  return (
    <svg
      aria-hidden
      className="pointer-events-none absolute inset-x-0 bottom-0 h-56 w-full text-primary"
      viewBox="0 0 1440 220"
      preserveAspectRatio="none"
    >
      <path
        fill="currentColor"
        fillOpacity="0.05"
        d="M0 120c120-30 240-30 360 0s240 30 360 0 240-30 360 0 240 30 360 0v100H0z"
      />
      <path
        fill="currentColor"
        fillOpacity="0.07"
        d="M0 160c120-24 240-24 360 0s240 24 360 0 240-24 360 0 240 24 360 0v60H0z"
      />
    </svg>
  )
}
