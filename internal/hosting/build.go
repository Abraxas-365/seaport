package hosting

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Build states.
const (
	BuildBuilding = "building"
	BuildReady    = "ready"
	BuildFailed   = "failed"
)

// Labels every hosted image carries: Seaport's base images set
// LabelBase and LabelHarness (images/Dockerfile), a derived image inherits
// them, so a client image that isn't FROM our base is refused.
const (
	LabelBase         = "io.seaport.base"
	LabelHarness      = "io.seaport.harness"
	LabelOrganization = "io.seaport.organization"
	LabelDigest       = "io.seaport.digest"
)

// MaxBuildLog bounds the build log kept (its tail).
const MaxBuildLog = 256 << 10

// BuildStale: a build whose process stopped reporting this long ago is
// dead (its process died) and may be started again.
const BuildStale = time.Minute

// Build is an image Seaport built for hosted agents, keyed by its content
// (BuildRequest.Digest: harness, base, Dockerfile, setup): agents whose
// machines build the same share it, and an edit that leaves the image
// alone builds nothing.
type Build struct {
	Digest string `json:"digest"`
	// Agent is the agent whose start or build asked for it last.
	Agent identity.AgentID `json:"agent_id"`
	State string           `json:"state"`
	// Base is the image it was built FROM.
	Base string `json:"base"`
	// Image is the built image's id (sha256:…) once ready.
	Image string `json:"image"`
	// Detail says why it failed.
	Detail string `json:"detail"`
	// Log is the build output's tail (MaxBuildLog); events carry none.
	Log        string     `json:"log,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// BuildRequest is what Runtime.Build builds.
type BuildRequest struct {
	Organization identity.OrganizationID
	Spec         Spec
	// Base is the image to build FROM (Machine.Image, else the harness's).
	Base string
	// Force builds again even when the tag exists, pulling the base anew
	// (a moving tag such as :latest).
	Force bool
	// Home is the normalized tar of Machine.Home (ReadHome), baked into the
	// home (HomeContext in the build context).
	Home []byte
}

// BuildAgent is the input of Commands.StartBuild.
type BuildAgent struct {
	// Force builds the image again even when it is ready (not while a
	// build of it is in progress).
	Force bool
}

func (b BuildAgent) Validate() error { return nil }

// Digest is the build's content key: sha256 of the harness, the base, the
// Dockerfile and the setup script (the home's digest is in the
// Dockerfile), as hex.
func (r BuildRequest) Digest() string {
	sum := sha256.Sum256([]byte(r.Spec.Harness + "\x00" + r.Base + "\x00" + Dockerfile(r.Spec, r.Base) + "\x00" + r.Spec.Setup))
	return hex.EncodeToString(sum[:])
}

// Tag is the build's image name: seaport-build-<org>:<digest>, both cut
// to 12 hex digits (the labels carry them whole).
func (r BuildRequest) Tag() string {
	org := strings.ReplaceAll(r.Organization.String(), "-", "")
	if len(org) > 12 {
		org = org[:12]
	}
	return "seaport-build-" + org + ":" + r.Digest()[:12]
}

// SetupFile is the setup script's name in the build context.
const SetupFile = "setup.sh"

// HomeContext is the dir of the build context holding the home's files,
// under the harness's HomeRoot (copied over /home/agent).
const HomeContext = "home"

// Dockerfile derives an agent's image from base: packages (apt, as
// root), then the setup script (bash, as root, with HOME=/home/agent; the
// home is handed back to uid 1000 after it), then back to the agent user,
// then the harness home (spec.Home, over whatever setup wrote) and
// `seaport home-setup` (connector.HomeSetup: the plugins it enables,
// Seaport's lines kept). The script is SetupFile in the build context when
// spec.Setup is set, the home HomeContext.
func Dockerfile(spec Spec, base string) string {
	var b strings.Builder
	b.WriteString("FROM " + base + "\n")
	if spec.Browser.Enabled() {
		writeBrowserSteps(&b)
	}
	if len(spec.Packages) > 0 || spec.Setup != "" {
		writeRootSteps(&b, spec)
	}
	if spec.Home != "" {
		b.WriteString("# home " + spec.Home + "\n")
		b.WriteString("COPY --chown=1000:1000 " + HomeContext + "/ /home/agent/\n")
		b.WriteString("RUN /opt/seaport/bin/seaport home-setup\n")
	}
	return b.String()
}

// Browser stage: Debian's chromium (the base is bookworm-slim), Xvfb (the
// virtual display it runs headed on: connector.startDisplay), fonts, a
// pinned Node (the browser MCP is a Node program; the tarball's SHA-256 is
// checked, per architecture) and chrome-devtools-mcp installed under
// /opt/seaport/browser (connector.BrowserMCP wraps it). The versions are
// part of the Dockerfile, so a bump changes the image tag.
const (
	BrowserNode    = "22.23.5"
	BrowserNodeX64 = "c54118104acc524633b5abe106bbf44a53126ec8b4e5fa105cb92b5d7de8a1a1"
	BrowserNodeA64 = "8bd3dc69eee8f3f226315a80d7d1d4faccf217a4c8fae500168ac2e38f3bdba3"
	BrowserMCPPkg  = "chrome-devtools-mcp@1.13.0"
)

func writeBrowserSteps(b *strings.Builder) {
	b.WriteString("# browser chromium\n")
	b.WriteString("USER root\n")
	b.WriteString("RUN apt-get update \\\n && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends" +
		" chromium xvfb fonts-liberation fonts-noto-color-emoji ca-certificates xz-utils \\\n && rm -rf /var/lib/apt/lists/*\n")
	b.WriteString("RUN set -eu; arch=\"$(dpkg --print-architecture)\"; case \"$arch\" in" +
		" amd64) node=linux-x64; sum=" + BrowserNodeX64 + ";;" +
		" arm64) node=linux-arm64; sum=" + BrowserNodeA64 + ";;" +
		" *) echo \"unsupported architecture $arch\" >&2; exit 1;; esac;" +
		" curl -fsSL -o /tmp/node.tar.gz \"https://nodejs.org/dist/v" + BrowserNode + "/node-v" + BrowserNode + "-$node.tar.gz\";" +
		" echo \"$sum  /tmp/node.tar.gz\" | sha256sum -c -;" +
		" mkdir -p /opt/seaport/browser/node; tar -xzf /tmp/node.tar.gz -C /opt/seaport/browser/node --strip-components=1; rm /tmp/node.tar.gz;" +
		" PATH=/opt/seaport/browser/node/bin:$PATH npm install -g --prefix /opt/seaport/browser/mcp " + BrowserMCPPkg + ";" +
		" mkdir -p /opt/seaport/browser/bin;" +
		" printf '#!/bin/sh\\nexport PATH=/opt/seaport/browser/node/bin:$PATH\\nexec /opt/seaport/browser/mcp/bin/chrome-devtools-mcp \"$@\"\\n'" +
		" > /opt/seaport/browser/bin/chrome-devtools-mcp; chmod 755 /opt/seaport/browser/bin/chrome-devtools-mcp;" +
		" /opt/seaport/browser/bin/chrome-devtools-mcp --version;" +
		" rm -rf /root/.npm\n")
	b.WriteString("USER 1000:1000\n")
}

func writeRootSteps(b *strings.Builder, spec Spec) {
	b.WriteString("USER root\n")
	if len(spec.Packages) > 0 {
		b.WriteString("RUN apt-get update \\\n && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " +
			strings.Join(spec.Packages, " ") + " \\\n && rm -rf /var/lib/apt/lists/*\n")
	}
	if spec.Setup != "" {
		b.WriteString("COPY " + SetupFile + " /opt/seaport/" + SetupFile + "\n")
		b.WriteString("RUN HOME=/home/agent bash -euo pipefail /opt/seaport/" + SetupFile +
			" \\\n && rm /opt/seaport/" + SetupFile + " \\\n && chown -R 1000:1000 /home/agent\n")
	}
	b.WriteString("USER 1000:1000\n")
}
