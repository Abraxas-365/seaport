package dockslack

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxMarkdown is what one chat.postMessage may carry in markdown blocks
// (Slack: 12 000 characters cumulative per payload); longer replies are
// split into several messages.
const MaxMarkdown = 12000

// angle is Slack's <...> syntax: links, mentions, special words.
var angle = regexp.MustCompile(`<([^<>\n]+)>`)

// codeSpan finds fenced blocks and inline code, which are kept as they are.
var codeSpan = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")

var (
	bold   = regexp.MustCompile(`(^|[^\w*])\*([^\s*](?:[^*\n]*[^\s*])?)\*($|[^\w*])`)
	strike = regexp.MustCompile(`(^|[^\w~])~([^\s~](?:[^~\n]*[^\s~])?)~($|[^\w~])`)
	bullet = regexp.MustCompile(`(?m)^(\s*)[•◦▪]\s+`)
)

// Mention resolves a user ("U…") or channel ("C…") to the text shown for
// it; label is what Slack carried after "|" (maybe empty).
type Mention func(kind byte, id, label string) string

const (
	hold    = '\uE000'
	holdEnd = '\uE001'
)

// ToMarkdown turns Slack's mrkdwn (what message events carry) into
// standard Markdown: <url|text> links, <@U…> and <#C…> mentions, *bold*,
// ~strike~, bullets and the &amp; &lt; &gt; escapes. Code is left alone.
func ToMarkdown(in string, mention Mention) string {
	var out strings.Builder
	last := 0
	for _, loc := range codeSpan.FindAllStringIndex(in, -1) {
		out.WriteString(convertText(in[last:loc[0]], mention))
		out.WriteString(unescape(in[loc[0]:loc[1]]))
		last = loc[1]
	}
	out.WriteString(convertText(in[last:], mention))
	return strings.TrimSpace(out.String())
}

func convertText(s string, mention Mention) string {
	if s == "" {
		return ""
	}
	var pieces []string
	s = angle.ReplaceAllStringFunc(s, func(t string) string {
		pieces = append(pieces, convertToken(t[1:len(t)-1], mention))
		return string(hold) + strconv.Itoa(len(pieces)-1) + string(holdEnd)
	})
	s = bullet.ReplaceAllString(s, "${1}- ")
	// Twice: matches share their boundary character ("*a* *b*").
	for range 2 {
		s = bold.ReplaceAllString(s, "${1}**${2}**${3}")
		s = strike.ReplaceAllString(s, "${1}~~${2}~~${3}")
	}
	s = unescape(s)
	for i, p := range pieces {
		s = strings.Replace(s, string(hold)+strconv.Itoa(i)+string(holdEnd), p, 1)
	}
	return s
}

func unescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// convertToken renders the inside of one <...>.
func convertToken(inner string, mention Mention) string {
	target, label, _ := strings.Cut(inner, "|")
	label, target = unescape(label), unescape(target)
	switch {
	case strings.HasPrefix(target, "@"):
		return mention('U', target[1:], label)
	case strings.HasPrefix(target, "#"):
		return mention('C', target[1:], label)
	case strings.HasPrefix(target, "!subteam^"):
		if label != "" {
			return label
		}
		return "@group"
	case strings.HasPrefix(target, "!date^"):
		if label != "" {
			return label
		}
		parts := strings.Split(target, "^")
		if len(parts) > 1 {
			if sec, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04 UTC")
			}
		}
		return ""
	case strings.HasPrefix(target, "!"):
		// <!here>, <!channel>, <!everyone>
		name := strings.TrimPrefix(target, "!")
		if label != "" {
			return label
		}
		return "@" + name
	case strings.HasPrefix(target, "mailto:"):
		addr := strings.TrimPrefix(target, "mailto:")
		if label != "" && label != addr {
			return "[" + escapeLabel(label) + "](" + target + ")"
		}
		return addr
	}
	if label == "" || label == target {
		return target
	}
	return "[" + escapeLabel(label) + "](" + escapeURL(target) + ")"
}

func escapeLabel(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

func escapeURL(s string) string {
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(s)
}

// fallback is the plain text sent next to the blocks (notifications, old
// clients): the Markdown, cut to what Slack shows without truncating.
func fallback(markdown string) string {
	const max = 3000
	if utf8.RuneCountInString(markdown) <= max {
		return markdown
	}
	return string([]rune(markdown)[:max-1]) + "…"
}

// Split cuts a reply into pieces of at most max runes, at a blank line, a
// line or a space when it can; a fenced code block a cut falls inside is
// closed and opened again, so every piece renders on its own.
func Split(text string, max int) []string {
	if max <= 16 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	room := max - 8 // for the fence that closes and opens again
	var out []string
	rest := []rune(text)
	reopen := false
	for len(rest) > 0 {
		prefix := ""
		if reopen {
			prefix = "```\n"
		}
		limit := room - len([]rune(prefix))
		if len(rest) <= max-len([]rune(prefix)) {
			out = append(out, prefix+string(rest))
			break
		}
		cut := breakAt(rest, limit)
		piece := prefix + strings.TrimRight(string(rest[:cut]), " \n")
		reopen = strings.Count(piece, "```")%2 == 1
		if reopen {
			piece += "\n```"
		}
		out = append(out, piece)
		rest = []rune(strings.TrimLeft(string(rest[cut:]), "\n "))
	}
	return out
}

// breakAt picks where to cut rest (at most limit runes in).
func breakAt(rest []rune, limit int) int {
	window := string(rest[:limit])
	for _, sep := range []string{"\n\n", "\n", " "} {
		if i := strings.LastIndex(window, sep); i > limit/2 {
			return utf8.RuneCountInString(window[:i]) + utf8.RuneCountInString(sep)
		}
	}
	return limit
}
