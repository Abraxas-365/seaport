package hosting

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
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
	LabelEnvironment  = "io.seaport.environment"
	LabelVersion      = "io.seaport.version"
)

// MaxBuildLog bounds the build log kept (its tail).
const MaxBuildLog = 256 << 10

// BuildStale: a build whose process stopped reporting this long ago is
// dead (its process died) and may be started again.
const BuildStale = time.Minute

// Build is the image of an environment version: Seaport builds it from the
// spec (FROM the image or the harness's base, packages, setup) and runs
// every slot of that version on it.
type Build struct {
	Environment identity.EnvironmentID `json:"environment_id"`
	Version     int                    `json:"version"`
	State       string                 `json:"state"`
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
	Environment  identity.EnvironmentID
	Version      int
	Spec         Spec
	// Base is the image to build FROM (Spec.Image, else the harness's).
	Base string
	// Force builds again even when the tag exists, pulling the base anew
	// (a moving tag such as :latest).
	Force bool
}

// BuildEnvironment is the input of Commands.StartBuild.
type BuildEnvironment struct {
	// Version to build (0: the current one).
	Version int
	// Force builds a version again even when it is ready (not while a
	// build of it is in progress).
	Force bool
}

func (b BuildEnvironment) Validate() error {
	if b.Version < 0 {
		return errx.InvalidField("version", "version must be positive (0: the current one)")
	}
	return nil
}

// Tag is the build's image name: one per environment version and content
// (a new base or spec makes a new tag).
func (r BuildRequest) Tag() string {
	sum := sha256.Sum256([]byte(r.Spec.Harness + "\x00" + r.Base + "\x00" + Dockerfile(r.Spec, r.Base) + "\x00" + r.Spec.Setup))
	return "seaport-env-" + r.Environment.String() + ":v" + strconv.Itoa(r.Version) + "-" + hex.EncodeToString(sum[:6])
}

// SetupFile is the setup script's name in the build context.
const SetupFile = "setup.sh"

// Dockerfile derives an environment's image from base: packages (apt, as
// root), then the setup script (bash, as root, with HOME=/home/agent; the
// home is handed back to uid 1000 after it), then back to the agent user.
// The script is SetupFile in the build context when spec.Setup is set.
func Dockerfile(spec Spec, base string) string {
	var b strings.Builder
	b.WriteString("FROM " + base + "\n")
	if len(spec.Packages) == 0 && spec.Setup == "" {
		return b.String()
	}
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
	return b.String()
}
