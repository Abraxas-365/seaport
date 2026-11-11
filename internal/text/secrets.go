package text

import "regexp"

// Secret is a kind of credential-looking string (high confidence: fixed
// prefixes of providers' key formats). Clients get the patterns too
// (hosting.HomeRules), so keep them to syntax RE2 and JavaScript share.
type Secret struct {
	What string
	Re   *regexp.Regexp
}

// Secrets are the strings Seaport refuses to keep (homes) or redacts
// (what agents did).
var Secrets = []Secret{
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"an OpenAI API key", regexp.MustCompile(`sk-(proj-|svcacct-)?[A-Za-z0-9_-]{40,}`)},
	{"a GitHub token", regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`)},
	{"a GitLab token", regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)},
	{"a Slack token", regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"an AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"a Google API key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"a Seaport crew token", regexp.MustCompile(`sp_crew_[A-Za-z0-9_-]{16,}`)},
	{"an IAMKit API key", regexp.MustCompile(`ik_key_[A-Za-z0-9_-]{16,}`)},
}

// Redacted replaces a secret in Redact's output.
const Redacted = "[redacted]"

// Redact replaces every credential-looking string of s (Secrets) with
// Redacted. A secret split across two strings is not caught: it is a
// safety net, not a guarantee.
func Redact(s string) string {
	for _, p := range Secrets {
		s = p.Re.ReplaceAllLiteralString(s, Redacted)
	}
	return s
}
