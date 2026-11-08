# syntax=docker/dockerfile:1
# Puerto: web app (pnpm) → Go binary with the app embedded → distroless.
#   docker build -t puerto --build-arg VERSION=$(git describe --always --dirty) .

FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@8.6.12 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/Abraxas-365/puerto/internal/bootstrap.Version=${VERSION}" \
      -o /out/puerto ./cmd/puerto

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/puerto /usr/local/bin/puerto
# Inside a container the port must listen on all interfaces; publish it only
# to the reverse proxy (deploy/compose.yaml), never straight to the internet.
ENV PUERTO_ADDR=0.0.0.0:8740
EXPOSE 8740
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/puerto", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/puerto"]
CMD ["serve"]
