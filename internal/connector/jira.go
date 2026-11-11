package connector

import "github.com/Abraxas-365/seaport/internal/dock"

// jiraHint tells the agent how to act on a Jira conversation (an issue
// key): its reply is a comment on the issue, written in Markdown.
func jiraHint(chat string) string {
	if !dock.ValidIssueKey(chat) {
		return ""
	}
	return "Jira: issue " + chat + ". Your reply is posted there as a comment: write Markdown " +
		"(headings, lists, tables, code blocks with a language; it is converted to Jira's format). " +
		"In anything you write, link with full URLs: relative links break in Jira. " +
		"Jira docks don't send files yet: put long output in a link instead.\n"
}
