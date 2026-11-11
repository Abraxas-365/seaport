package connector

import "strings"

// slackHint tells an agent where a Slack conversation is and how its reply
// reads there. chat is the conversation id ("<channel>/<thread ts>" for a
// thread, "<channel>" for a DM); title the channel's name or the person's.
func slackHint(chat, title string) string {
	channel, thread, threaded := strings.Cut(chat, "/")
	if channel == "" {
		return ""
	}
	where := "a direct message with " + title
	if threaded {
		where = "channel " + channel
		if strings.HasPrefix(title, "#") {
			where = "channel " + title + " (" + channel + ")"
		}
		where += ", thread " + thread
	}
	goesTo := "that conversation"
	if threaded {
		goesTo = "that thread"
	}
	return "Slack: " + where + ". Your reply goes to " + goesTo +
		". Write standard Markdown (**bold**, [text](url), lists, code fences), not Slack's mrkdwn. " +
		"Mention a person as <@USERID> only when you were given their user id; never invent one.\n"
}
