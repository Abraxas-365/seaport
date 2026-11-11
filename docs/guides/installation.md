# Install Seaport

Seaport is one binary, `seaport`: the port (`seaport serve`), its admin CLI and the agent connector (`seaport connect`). Install it on the server that runs the port and on every machine that runs an agent.

## Release binaries

[GitHub Releases](https://github.com/Abraxas-365/seaport/releases) has archives for Linux, macOS and Windows, on amd64 and arm64, with the web app embedded:

```sh
# Example: macOS on Apple silicon. Pick your OS and architecture from the release page.
VERSION=0.7.0
curl -sSfLO "https://github.com/Abraxas-365/seaport/releases/download/v$VERSION/seaport_${VERSION}_darwin_arm64.tar.gz"
tar -xzf "seaport_${VERSION}_darwin_arm64.tar.gz" seaport
sudo mv seaport /usr/local/bin/
seaport --help
```

Each release has a `checksums.txt` to verify the archives against.

## `go install`

With Go 1.26 or newer:

```sh
go install github.com/Abraxas-365/seaport/cmd/seaport@latest
```

`go install` can't run the web build, so this binary serves the API but not the web app. That is fine on an agent's machine (`connect` and the CLI); a port should use a release binary or the image.

## From a checkout

The web app is embedded into the binary, so build it first (Node 22 and pnpm):

```sh
git clone https://github.com/Abraxas-365/seaport.git
cd seaport
(cd web && pnpm install && pnpm build) && go build -o bin/seaport ./cmd/seaport
```

## Docker image

The repository's `Dockerfile` builds the web app and the binary into a distroless, non-root image (about 24 MB) with `seaport healthcheck` as its health check:

```sh
docker build -t seaport --build-arg VERSION=$(git describe --always --dirty) .
docker run --rm -e DATABASE_URL=… -e SEAPORT_ADMIN_TOKEN=… -e SEAPORT_ADDR=0.0.0.0:8740 \
  -v seaport_data:/data -p 8740:8740 seaport serve
```

`/data` holds the generated secret key and the default blob store: mount a volume. For a server with HTTPS, use the compose stack in `deploy/` instead; see [Deploy on one server](operations/deploy.md).

## What runs where

| Machine | Needs |
| :--- | :--- |
| The port | `seaport serve`, Postgres 16, optionally Docker for [hosted agents](hosting/hosted-agents.md) |
| An agent's machine | `seaport connect` and the agent harness (rness, Claude Code, any ACP agent). No public address: the connector dials out |
| An admin's machine | the CLI (`SEAPORT_URL`, `SEAPORT_ADMIN_TOKEN`), or just a browser |

## Next

- [Your first port](../tutorials/first-port.md).
- [Configuration reference](../reference/configuration.md).
