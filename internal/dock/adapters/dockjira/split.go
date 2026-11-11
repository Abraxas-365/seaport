package dockjira

import "strings"

// MaxComment bounds the Markdown of one Jira comment: Jira refuses
// comment bodies past 32 767 characters and ADF's own markup counts too, so a
// longer reply goes out as several comments.
const MaxComment = 24000

// split cuts Markdown into chunks of at most max bytes, at blank lines
// between blocks where it can, at lines otherwise; a code fence that a cut
// falls in is closed and opened again in the next chunk.
func split(md string, max int) []string {
	md = strings.TrimSpace(md)
	if len(md) <= max {
		if md == "" {
			return nil
		}
		return []string{md}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, block := range blocksOf(md) {
		if len(block) > max {
			flush()
			chunks = append(chunks, cutBlock(block, max)...)
			continue
		}
		if cur.Len() > 0 && cur.Len()+2+len(block) > max {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(block)
	}
	flush()
	return chunks
}

// blocksOf splits at blank lines outside fenced code.
func blocksOf(md string) []string {
	var blocks []string
	var cur []string
	fence := ""
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if f := fenceOf(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(f, fence) && strings.TrimSpace(line) == f:
				fence = ""
			}
		}
		if fence == "" && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

// fenceOf returns the run of backticks or tildes opening a fence line.
func fenceOf(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return ""
	}
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(t) && t[n] == c {
			n++
		}
		if n >= 3 {
			return t[:n]
		}
	}
	return ""
}

// cutBlock splits one oversized block by lines (and, for a single huge
// line, by size), keeping fenced code fenced.
func cutBlock(block string, max int) []string {
	lines := strings.Split(block, "\n")
	open, close := "", ""
	if f := fenceOf(lines[0]); f != "" {
		open, close = lines[0], f
		lines = lines[1:]
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) == close {
			lines = lines[:n-1]
		}
	}
	budget := max
	if open != "" {
		budget = max - len(open) - len(close) - 2
	}
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		s := strings.TrimRight(cur.String(), "\n")
		if open != "" {
			s = open + "\n" + s + "\n" + close
		}
		parts = append(parts, s)
		cur.Reset()
	}
	for _, line := range lines {
		for len(line) > budget {
			cut := budget
			for cut > 0 && !runeStart(line[cut]) {
				cut--
			}
			if cur.Len() > 0 {
				flush()
			}
			cur.WriteString(line[:cut])
			flush()
			line = line[cut:]
		}
		if cur.Len() > 0 && cur.Len()+1+len(line) > budget {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	return parts
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }
