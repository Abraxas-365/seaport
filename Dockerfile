# syntax=docker/dockerfile:1
# Seaport: web app (pnpm) → Go binary with the app embedded → distroless.
#   docker build -t seaport --build-arg VERSION=$(git describe --always --dirty) .

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
      -ldflags "-s -w -X github.com/Abraxas-365/seaport/internal/bootstrap.Version=${VERSION}" \
      -o /out/seaport ./cmd/seaport

RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/seaport /usr/local/bin/seaport
# /data keeps the generated SEAPORT_SECRET_KEY (when it is not set) and
# message images (/data/blobs, unless SEAPORT_BLOB_URL says otherwise):
# mount a volume there, or the key, every linked WhatsApp dock and every
# image die with the container.
COPY --from=build --chown=nonroot:nonroot --chmod=700 /out/data /data
ENV SEAPORT_SECRET_KEY_FILE=/data/secret.key
# Inside a container the port must listen on all interfaces; publish it only
# to the reverse proxy (deploy/compose.yaml), never straight to the internet.
ENV SEAPORT_ADDR=0.0.0.0:8740
EXPOSE 8740
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/seaport", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/seaport"]
CMD ["serve"]
