import * as React from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ArrowRight, KeyRound, Lock } from "lucide-react"

import { api, ApiError } from "@/lib/api"
import { keys } from "@/lib/query"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { PuertoMark } from "@/components/puerto"

export function LoginPage() {
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
    <div className="relative flex min-h-svh items-center justify-center overflow-hidden bg-canvas p-6">
      <Harbor />
      <div className="relative w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <PuertoMark className="size-12" />
          <div>
            <h1 className="font-heading text-2xl font-semibold tracking-tight">Puerto</h1>
            <p className="mt-1 text-sm text-muted-foreground">Donde llegan los mensajes y trabajan tus agentes.</p>
          </div>
        </div>
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
                  placeholder="PUERTO_ADMIN_TOKEN"
                  value={token}
                  aria-invalid={!!error || undefined}
                  onChange={(e) => setToken(e.target.value)}
                />
              </InputGroup>
              {error ? (
                <FieldError>{error}</FieldError>
              ) : (
                <FieldDescription>
                  El valor de <code>PUERTO_ADMIN_TOKEN</code> del servidor. Se cambia por una cookie segura; no queda
                  guardado en el navegador.
                </FieldDescription>
              )}
            </Field>
            <Button type="submit" className="w-full" disabled={!token.trim() || login.isPending}>
              {login.isPending ? <Spinner /> : <Lock />} Entrar <ArrowRight className="ml-auto" />
            </Button>
          </FieldGroup>
        </form>
      </div>
    </div>
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
