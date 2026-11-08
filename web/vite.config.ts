import { writeFileSync } from "node:fs"
import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, type Plugin } from "vite"

// `pnpm dev` proxies the API to a running `seaport serve` (SEAPORT_URL,
// default http://127.0.0.1:8740), so the session cookie stays same-origin.
const backend = process.env.SEAPORT_URL ?? "http://127.0.0.1:8740"

// dist/ is embedded in the Go binary (web/embed.go); its tracked .gitkeep
// keeps `go build` working before the first frontend build.
const keepDist: Plugin = {
  name: "seaport-keep-dist",
  apply: "build",
  closeBundle() {
    writeFileSync(resolve(import.meta.dirname, "dist/.gitkeep"), "")
  },
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    proxy: {
      "/api": { target: backend },
      "/healthz": { target: backend },
    },
  },
  build: {
    // Embedded in the Go binary (web/embed.go).
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 800,
  },
})
