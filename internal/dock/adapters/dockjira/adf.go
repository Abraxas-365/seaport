package dockjira

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Jira text reaches Seaport as Atlassian Document Format (ADF, a JSON
// tree: what the REST API v3 and new editors produce) or as wiki markup
// (what admin webhooks still carry as a string). Both are normalized to
// Markdown, the one format agents and the web read.

// node is one ADF node.
type node struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []node         `json:"content,omitempty"`
	Marks   []mark         `json:"marks,omitempty"`
}

type mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

func (n node) attr(key string) string {
	switch v := n.Attrs[key].(type) {
	case string:
		return v
	case float64:
		return strconv.Itoa(int(v))
	}
	return ""
}

func (m mark) attr(key string) string {
	v, _ := m.Attrs[key].(string)
	return v
}

// reader turns Jira text into Markdown and notes whether the bot's
// account was mentioned (a mention node or [~accountid:…]).
type reader struct {
	botID, botName string
	mentioned      bool
}

// markdown converts a webhook's text field: a JSON string (wiki markup)
// or an ADF document. Anything else reads as empty.
func (r *reader) markdown(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return r.wiki(s)
	case '{':
		var doc node
		if json.Unmarshal(raw, &doc) != nil {
			return ""
		}
		return strings.TrimSpace(r.blocks(doc.Content))
	}
	return ""
}

// blocks renders sibling block nodes separated by blank lines.
func (r *reader) blocks(nodes []node) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if s := r.block(n); strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (r *reader) block(n node) string {
	switch n.Type {
	case "paragraph":
		return strings.TrimSpace(r.inlines(n.Content))
	case "heading":
		level := 1
		if l, err := strconv.Atoi(n.attr("level")); err == nil && l >= 1 && l <= 6 {
			level = l
		}
		return strings.Repeat("#", level) + " " + strings.TrimSpace(r.inlines(n.Content))
	case "bulletList", "orderedList":
		return r.list(n)
	case "taskList", "decisionList":
		return r.tasks(n)
	case "codeBlock":
		var b strings.Builder
		for _, c := range n.Content {
			b.WriteString(c.Text)
		}
		code := strings.TrimRight(b.String(), "\n")
		fence := "```"
		for strings.Contains(code, fence) {
			fence += "`"
		}
		return fence + n.attr("language") + "\n" + code + "\n" + fence
	case "blockquote", "panel":
		return quote(r.blocks(n.Content))
	case "rule":
		return "---"
	case "table":
		return r.table(n)
	case "mediaSingle", "mediaGroup", "media":
		return "(attachment)"
	case "expand", "nestedExpand":
		body := r.blocks(n.Content)
		if t := n.attr("title"); t != "" {
			return "**" + t + "**\n\n" + body
		}
		return body
	}
	// Unknown containers: keep what text they hold.
	if len(n.Content) > 0 {
		if hasBlocks(n.Content) {
			return r.blocks(n.Content)
		}
		return strings.TrimSpace(r.inlines(n.Content))
	}
	return strings.TrimSpace(r.inline(n))
}

func hasBlocks(nodes []node) bool {
	for _, n := range nodes {
		switch n.Type {
		case "paragraph", "heading", "bulletList", "orderedList", "codeBlock", "blockquote", "panel", "table", "rule", "taskList":
			return true
		}
	}
	return false
}

func quote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// list renders a bullet or ordered list; nested lists indent under their
// item by the width of its marker.
func (r *reader) list(n node) string {
	start := 1
	if s, err := strconv.Atoi(n.attr("order")); err == nil && s >= 0 {
		start = s
	}
	var out []string
	for i, item := range n.Content {
		marker := "- "
		if n.Type == "orderedList" {
			marker = strconv.Itoa(start+i) + ". "
		}
		out = append(out, indentItem(marker, r.itemBody(item)))
	}
	return strings.Join(out, "\n")
}

// itemBody renders a list item's children: text first, nested lists on
// the following lines, other blocks after a blank line.
func (r *reader) itemBody(item node) string {
	var b strings.Builder
	for i, c := range item.Content {
		s := r.block(c)
		if strings.TrimSpace(s) == "" {
			continue
		}
		if i > 0 && b.Len() > 0 {
			if c.Type == "bulletList" || c.Type == "orderedList" {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(s)
	}
	return b.String()
}

func indentItem(marker, body string) string {
	pad := strings.Repeat(" ", len(marker))
	lines := strings.Split(body, "\n")
	for i := range lines {
		switch {
		case i == 0:
			lines[i] = marker + lines[i]
		case lines[i] != "":
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func (r *reader) tasks(n node) string {
	var out []string
	for _, item := range n.Content {
		box := "[ ] "
		if item.attr("state") == "DONE" {
			box = "[x] "
		}
		out = append(out, "- "+box+strings.TrimSpace(r.inlines(item.Content)))
	}
	return strings.Join(out, "\n")
}

func (r *reader) table(n node) string {
	var rows [][]string
	header := false
	for i, row := range n.Content {
		var cells []string
		for _, cell := range row.Content {
			if i == 0 && cell.Type == "tableHeader" {
				header = true
			}
			text := strings.ReplaceAll(r.blocks(cell.Content), "\n", " ")
			cells = append(cells, strings.ReplaceAll(text, "|", `\|`))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	line := func(cells []string) string {
		for len(cells) < width {
			cells = append(cells, "")
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	sep := make([]string, width)
	for i := range sep {
		sep[i] = "---"
	}
	var out []string
	if !header {
		// Markdown tables need a header row.
		out = append(out, line(make([]string, width)), line(sep))
	}
	for i, row := range rows {
		out = append(out, line(row))
		if i == 0 && header {
			out = append(out, line(sep))
		}
	}
	return strings.Join(out, "\n")
}

func (r *reader) inlines(nodes []node) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(r.inline(n))
	}
	return b.String()
}

func (r *reader) inline(n node) string {
	switch n.Type {
	case "text":
		return marked(n.Text, n.Marks)
	case "hardBreak":
		return "  \n"
	case "mention":
		name := strings.TrimSpace(n.attr("text"))
		if name == "" {
			name = "@user"
		} else if !strings.HasPrefix(name, "@") {
			name = "@" + name
		}
		if r.botID != "" && n.attr("id") == r.botID {
			r.mentioned = true
			if r.botName != "" {
				name = "@" + r.botName
			}
		}
		return name
	case "emoji":
		if t := n.attr("text"); t != "" {
			return t
		}
		return n.attr("shortName")
	case "inlineCard", "blockCard", "embedCard":
		if u := n.attr("url"); u != "" {
			return "<" + u + ">"
		}
		return ""
	case "status":
		return "[" + n.attr("text") + "]"
	case "date":
		return n.attr("timestamp")
	case "media", "mediaInline":
		return "(attachment)"
	}
	if len(n.Content) > 0 {
		return r.inlines(n.Content)
	}
	return n.Text
}

// marked wraps text in the Markdown of its marks, keeping the spaces at
// its edges outside the markers (`**a **` is not bold).
func marked(text string, marks []mark) string {
	if text == "" {
		return ""
	}
	var link, code, strong, em, strike bool
	href := ""
	for _, m := range marks {
		switch m.Type {
		case "link":
			link, href = true, m.attr("href")
		case "code":
			code = true
		case "strong":
			strong = true
		case "em":
			em = true
		case "strike":
			strike = true
		}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}
	lead := text[:len(text)-len(strings.TrimLeft(text, " \t\n"))]
	trail := text[len(strings.TrimRight(text, " \t\n")):]
	s := trimmed
	if code {
		fence := "`"
		if strings.Contains(s, "`") {
			fence = "``"
		}
		s = fence + s + fence
	}
	if strong {
		s = "**" + s + "**"
	}
	if em {
		s = "*" + s + "*"
	}
	if strike {
		s = "~~" + s + "~~"
	}
	if link && href != "" {
		s = "[" + s + "](" + href + ")"
	}
	return lead + s + trail
}

// ---- wiki markup ----

var (
	wikiCode     = regexp.MustCompile(`(?s)(^|[^{])\{(code|noformat)(?::([^}]*))?\}(.*?)\{(?:code|noformat)\}`)
	wikiInline   = regexp.MustCompile(`\{\{(.+?)\}\}`)
	wikiMention  = regexp.MustCompile(`\[~(?:accountid:)?([^\]\s]+)\]`)
	wikiLink     = regexp.MustCompile(`\[([^\]|\n]+)\|([^\]\n]+)\]`)
	wikiBare     = regexp.MustCompile(`\[(https?://[^\]\s|]+)\]`)
	wikiBold     = regexp.MustCompile(`(^|[\s(])\*(\S(?:[^*\n]*?\S)?)\*($|[\s).,;:!?])`)
	wikiItalic   = regexp.MustCompile(`(^|[\s(])_(\S(?:[^_\n]*?\S)?)_($|[\s).,;:!?])`)
	wikiStrike   = regexp.MustCompile(`(^|\s)-(\S(?:[^-\n]*?\S)?)-($|[\s).,;:!?])`)
	wikiColor    = regexp.MustCompile(`\{color(?::[^}]*)?\}`)
	wikiImage    = regexp.MustCompile(`!([^!\s|]+)(?:\|[^!\n]*)?!`)
	wikiHeading  = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	wikiListLine = regexp.MustCompile(`^([*#-]+)\s+(.*)$`)
	wikiRule     = regexp.MustCompile(`^-{4,}\s*$`)
)

// wiki converts Jira wiki markup to Markdown (the subset people write in
// comments: code, headings, lists, quotes, tables, emphasis, links).
func (r *reader) wiki(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var held []string
	hold := func(md string) string {
		held = append(held, md)
		return "\x00" + strconv.Itoa(len(held)-1) + "\x00"
	}
	s = wikiCode.ReplaceAllStringFunc(s, func(m string) string {
		sub := wikiCode.FindStringSubmatch(m)
		pre := sub[1] // the character before the opening tag, kept
		sub = append([]string{sub[0]}, sub[2:]...)
		lang := ""
		if sub[1] == "code" {
			lang, _, _ = strings.Cut(sub[2], "|")
			lang, _, _ = strings.Cut(lang, ":")
			lang = strings.TrimSpace(lang)
			if strings.Contains(lang, "=") {
				lang = ""
			}
		}
		code := strings.Trim(sub[3], "\n")
		fence := "```"
		for strings.Contains(code, fence) {
			fence += "`"
		}
		return pre + "\n" + hold(fence+lang+"\n"+code+"\n"+fence) + "\n"
	})
	s = wikiInline.ReplaceAllStringFunc(s, func(m string) string {
		inner := wikiInline.FindStringSubmatch(m)[1]
		fence := "`"
		if strings.Contains(inner, "`") {
			fence = "``"
		}
		return hold(fence + inner + fence)
	})
	s = wikiMention.ReplaceAllStringFunc(s, func(m string) string {
		id := wikiMention.FindStringSubmatch(m)[1]
		if r.botID != "" && id == r.botID {
			r.mentioned = true
			if r.botName != "" {
				return hold("@" + r.botName)
			}
		}
		return hold("@user")
	})
	lines := strings.Split(s, "\n")
	inQuote := false
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		switch {
		case strings.HasPrefix(trimmed, "\x00") && strings.HasSuffix(trimmed, "\x00"):
			lines[i] = trimmed
			continue
		case trimmed == "{quote}":
			inQuote = !inQuote
			lines[i] = ""
			continue
		case wikiRule.MatchString(trimmed):
			lines[i] = "---"
			continue
		}
		prefix := ""
		body := trimmed
		switch {
		case wikiHeading.MatchString(body):
			m := wikiHeading.FindStringSubmatch(body)
			n, _ := strconv.Atoi(m[1])
			prefix, body = strings.Repeat("#", n)+" ", m[2]
		case strings.HasPrefix(body, "bq. "):
			prefix, body = "> ", strings.TrimPrefix(body, "bq. ")
		case strings.HasPrefix(body, "||"):
			lines[i] = wikiRow(r.wikiInline(body), true)
			continue
		case strings.HasPrefix(body, "|") && strings.HasSuffix(body, "|") && len(body) > 1:
			lines[i] = wikiRow(r.wikiInline(body), false)
			continue
		case wikiListLine.MatchString(body):
			m := wikiListLine.FindStringSubmatch(body)
			marker, rest := m[1], m[2]
			// "*bold*" starts a line without a space; a list marker has one.
			pad := ""
			for _, c := range marker[:len(marker)-1] {
				if c == '#' {
					pad += "   "
				} else {
					pad += "  "
				}
			}
			bullet := "- "
			if marker[len(marker)-1] == '#' {
				bullet = "1. "
			}
			prefix, body = pad+bullet, rest
		}
		line = prefix + r.wikiInline(body)
		if inQuote && !strings.HasPrefix(line, "> ") {
			line = "> " + line
		}
		lines[i] = line
	}
	out := strings.Join(lines, "\n")
	for i := len(held) - 1; i >= 0; i-- {
		out = strings.ReplaceAll(out, "\x00"+strconv.Itoa(i)+"\x00", held[i])
	}
	return strings.TrimSpace(out)
}

// wikiInline converts the emphasis, links and images inside one line.
func (r *reader) wikiInline(s string) string {
	s = wikiColor.ReplaceAllString(s, "")
	s = wikiImage.ReplaceAllString(s, "(image: $1)")
	s = wikiLink.ReplaceAllString(s, "[$1]($2)")
	s = wikiBare.ReplaceAllString(s, "<$1>")
	s = strings.ReplaceAll(s, `\\`, "  \n")
	// Markers go in as control characters so one rule's output is never
	// read by another; adjacent matches share a boundary character, hence
	// two passes.
	for range 2 {
		s = wikiBold.ReplaceAllString(s, "$1\x01$2\x01$3")
		s = wikiItalic.ReplaceAllString(s, "$1\x02$2\x02$3")
		s = wikiStrike.ReplaceAllString(s, "$1\x03$2\x03$3")
	}
	return strings.NewReplacer("\x01", "**", "\x02", "*", "\x03", "~~").Replace(s)
}

// wikiRow turns |a|b| (or ||a||b|| for a header) into a Markdown table
// row; a header gets its separator line.
func wikiRow(s string, header bool) string {
	var cells []string
	sep := "|"
	if header {
		sep = "||"
	}
	for _, c := range strings.Split(strings.Trim(s, "|"), sep) {
		cells = append(cells, strings.TrimSpace(strings.ReplaceAll(c, "|", `\|`)))
	}
	row := "| " + strings.Join(cells, " | ") + " |"
	if header {
		seps := make([]string, len(cells))
		for i := range seps {
			seps[i] = "---"
		}
		row += "\n| " + strings.Join(seps, " | ") + " |"
	}
	return row
}
