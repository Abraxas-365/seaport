package dock

import "regexp"

// issueKey is a Jira issue key: the project key (capital letter first,
// then capitals, digits or underscores) and the issue number.
var issueKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,254}-[1-9][0-9]{0,9}$`)

// ValidIssueKey checks a Jira issue key such as PROJ-123.
func ValidIssueKey(key string) bool { return len(key) <= 280 && issueKey.MatchString(key) }
