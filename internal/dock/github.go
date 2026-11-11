package dock

import (
	"regexp"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// GitHubTokenPath is where an agent asks for a GitHub App token (crew
// auth; POST GitHubTokenRequest → GitHubToken).
const GitHubTokenPath = "/crew/github/token"

// repoName is owner/name as GitHub allows them.
var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// ValidRepository checks an owner/name repository.
func ValidRepository(repo string) bool {
	_, name, _ := strings.Cut(repo, "/")
	return repoName.MatchString(repo) && name != "." && name != ".."
}

// GitHubTokenRequest asks for a token for one repository.
type GitHubTokenRequest struct {
	// Repository is owner/name.
	Repository string `json:"repository"`
	// Host, when set, is the GitHub host the caller is about to reach
	// (git's credential request): another host gets NotFound.
	Host string `json:"host,omitempty"`
}

func (t GitHubTokenRequest) Validate() error {
	if !ValidRepository(t.Repository) {
		return errx.InvalidField("repository", "repository must be owner/name")
	}
	if len(t.Host) > 253 {
		return errx.InvalidField("host", "host is too long")
	}
	return nil
}

// GitHubToken is what an agent gets: an installation token of a GitHub
// dock's App limited to one repository (the App's permissions are the
// ceiling), valid until ExpiresAt (about an hour).
type GitHubToken struct {
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expires_at"`
	Repository string    `json:"repository"`
	// Host is GitHub's web host (github.com, or the GHES host) and APIURL
	// its REST API: what git and gh need.
	Host   string `json:"host"`
	APIURL string `json:"api_url"`
	// App is the App's slug: the agent acts as <slug>[bot].
	App string `json:"app"`
}
