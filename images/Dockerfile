# syntax=docker/dockerfile:1
# Seaport's base images for hosted agents (one per harness), built from the
# repository root:
#   docker build -f images/Dockerfile --target rness -t seaport-rness:dev .
#   docker build -f images/Dockerfile --target claude-code -t seaport-claude-code:dev .
#
# The contract every hosted image keeps (environments with `image:` must be
# built FROM one of these): user agent, uid 1000, HOME=/home/agent; the
# workspace at /workspace and the slot's state at /seaport/state (both owned
# by 1000); Seaport and the harness in /opt/seaport/bin; the entrypoint
# `seaport agent-run`; the labels io.seaport.base and io.seaport.harness.
# Install tools as root, then return to USER 1000:1000, and COPY files into
# the home with --chown=1000:1000. No secrets or per-agent config in images.

# rness has no releases: it is built from source at a pinned commit (its
# repository keeps no Cargo.lock yet, so dependencies resolve at build time).
ARG RNESS_REF=6d9d6da6652a71d693fff8cfc87a86a6c3185dc0
# claude-agent-acp's standalone build (it bundles `claude`; no node needed).
ARG CLAUDE_AGENT_ACP_VERSION=0.102.0
ARG CLAUDE_AGENT_ACP_SHA256_AMD64=b3f99ebf7580b2eb30d3efb1b42a9a81be5669e7e2a341e6bfed722a8f56eacb
ARG CLAUDE_AGENT_ACP_SHA256_ARM64=655926c7c3c6c2e8e12cf847b02e9c16ade7eef3931de8a3e8cfad4287c71ba3

FROM golang:1.26-bookworm AS seaport
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# The agent side serves no web app: an empty dist satisfies the embed.
RUN mkdir -p web/dist && touch web/dist/.gitkeep
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/Abraxas-365/seaport/internal/bootstrap.Version=${VERSION}" \
      -o /out/seaport ./cmd/seaport

FROM rust:1-bookworm AS rness-build
ARG RNESS_REF
RUN git clone --filter=blob:none https://github.com/Abraxas-365/rness.git /src \
 && git -C /src checkout --detach "${RNESS_REF}"
WORKDIR /src
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    cargo build --release -p rness-cli \
 && mkdir -p /out/home/.rness/lua \
 && cp target/release/rness /out/rness \
 && cp -R flavors/default/. /out/home/.rness/
# The default flavor, with Seaport's gateway routing loaded first.
COPY images/rness/seaport.lua /out/home/.rness/lua/seaport.lua
RUN sed -i '1i require("seaport") -- Seaport: models through the port'"'"'s gateway (keep this line)' \
      /out/home/.rness/init.lua

FROM debian:bookworm-slim AS claude-download
ARG TARGETARCH
ARG CLAUDE_AGENT_ACP_VERSION
ARG CLAUDE_AGENT_ACP_SHA256_AMD64
ARG CLAUDE_AGENT_ACP_SHA256_ARM64
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) arch=x64; sum="${CLAUDE_AGENT_ACP_SHA256_AMD64}" ;; \
      arm64) arch=arm64; sum="${CLAUDE_AGENT_ACP_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/acp.tgz \
      "https://github.com/agentclientprotocol/claude-agent-acp/releases/download/v${CLAUDE_AGENT_ACP_VERSION}/claude-agent-acp-linux-${arch}.tar.gz"; \
    echo "${sum}  /tmp/acp.tgz" | sha256sum -c -; \
    mkdir -p /out && tar --no-same-owner -xzf /tmp/acp.tgz -C /out && chmod 755 /out/*

FROM debian:bookworm-slim AS base
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git curl less procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 1000 --user-group --shell /bin/bash agent \
 && mkdir -p /workspace /seaport/state \
 && chown 1000:1000 /workspace /seaport/state
COPY --from=seaport /out/seaport /opt/seaport/bin/seaport
ENV PATH=/opt/seaport/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/home/agent \
    LANG=C.UTF-8
LABEL io.seaport.base="1" \
      org.opencontainers.image.source="https://github.com/Abraxas-365/seaport"
WORKDIR /workspace
USER 1000:1000
ENTRYPOINT ["/opt/seaport/bin/seaport", "agent-run"]

FROM base AS rness
COPY --from=rness-build /out/rness /opt/seaport/bin/rness
COPY --from=rness-build --chown=1000:1000 /out/home/.rness /home/agent/.rness
ENV SEAPORT_HARNESS=rness
LABEL io.seaport.harness="rness" \
      org.opencontainers.image.description="Seaport hosted agent base image: rness"

FROM base AS claude-code
COPY --from=claude-download /out/ /opt/seaport/bin/
# Managed settings (read over every other settings source): agent-run
# writes them at start in the slot's tmpfs, pinning the slot's gateway,
# key and proxy (connector.ClaudeManaged).
USER 0:0
RUN ln -s /tmp/seaport/claude-code /etc/claude-code
USER 1000:1000
# No auto-update, telemetry or error reports: the image pins the version and
# a slot reaches only the port and its environment's egress hosts.
ENV SEAPORT_HARNESS=claude-code \
    DISABLE_AUTOUPDATER=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
LABEL io.seaport.harness="claude-code" \
      org.opencontainers.image.description="Seaport hosted agent base image: Claude Code (claude-agent-acp)"
