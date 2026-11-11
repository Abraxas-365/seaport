package dockjira

import (
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// doc is an ADF document: what Jira's REST API v3 takes as a comment body.
type doc struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Content []node `json:"content"`
}

var parser = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify, extension.TaskList)).Parser()

// toADF converts the Markdown an agent writes to ADF: paragraphs,
// headings, emphasis, inline code, code blocks with language, lists
// (nested), links, blockquotes, rules and tables. What ADF can't hold
// (raw HTML, images, HTML in lists) degrades to text, never to an error.
func toADF(md string) doc {
	src := []byte(md)
	root := parser.Parse(text.NewReader(src))
	w := &writer{src: src}
	content := w.blocks(root, topBlocks)
	if len(content) == 0 {
		content = []node{{Type: "paragraph"}}
	}
	return doc{Type: "doc", Version: 1, Content: content}
}

type writer struct{ src []byte }

// Block types each ADF container accepts.
var (
	topBlocks   = map[string]bool{"paragraph": true, "heading": true, "bulletList": true, "orderedList": true, "codeBlock": true, "blockquote": true, "rule": true, "table": true}
	quoteBlocks = map[string]bool{"paragraph": true, "bulletList": true, "orderedList": true, "codeBlock": true}
	itemBlocks  = map[string]bool{"paragraph": true, "bulletList": true, "orderedList": true, "codeBlock": true}
)

// blocks converts the block children of n, keeping to what the
// container allows.
func (w *writer) blocks(n ast.Node, allowed map[string]bool) []node {
	var out []node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, restrict(w.block(c), allowed)...)
	}
	return out
}

func (w *writer) block(n ast.Node) []node {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		content := w.inlines(n, nil)
		for len(content) > 0 && content[len(content)-1].Type == "hardBreak" {
			content = content[:len(content)-1]
		}
		if len(content) == 0 {
			return nil
		}
		return []node{{Type: "paragraph", Content: content}}
	case *ast.Heading:
		content := w.inlines(n, nil)
		if len(content) == 0 {
			return nil
		}
		return []node{{Type: "heading", Attrs: map[string]any{"level": v.Level}, Content: content}}
	case *ast.FencedCodeBlock:
		lang := string(v.Language(w.src))
		return []node{w.code(v, lang)}
	case *ast.CodeBlock:
		return []node{w.code(v, "")}
	case *ast.Blockquote:
		content := w.blocks(v, quoteBlocks)
		if len(content) == 0 {
			return nil
		}
		return []node{{Type: "blockquote", Content: content}}
	case *ast.ThematicBreak:
		return []node{{Type: "rule"}}
	case *ast.List:
		return []node{w.list(v)}
	case *ast.HTMLBlock:
		var b strings.Builder
		for i := 0; i < v.Lines().Len(); i++ {
			seg := v.Lines().At(i)
			b.Write(seg.Value(w.src))
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return []node{{Type: "paragraph", Content: []node{{Type: "text", Text: s}}}}
		}
		return nil
	case *east.Table:
		return []node{w.table(v)}
	}
	// Unknown container: its children.
	var out []node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, w.block(c)...)
	}
	return out
}

// code is a codeBlock node holding the block's lines verbatim.
func (w *writer) code(n ast.Node, lang string) node {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(w.src))
	}
	cb := node{Type: "codeBlock"}
	if lang = strings.TrimSpace(lang); lang != "" {
		cb.Attrs = map[string]any{"language": lang}
	}
	if code := strings.TrimRight(b.String(), "\n"); code != "" {
		cb.Content = []node{{Type: "text", Text: code}}
	}
	return cb
}

func (w *writer) list(l *ast.List) node {
	out := node{Type: "bulletList"}
	if l.IsOrdered() {
		out = node{Type: "orderedList"}
		if l.Start > 1 {
			out.Attrs = map[string]any{"order": l.Start}
		}
	}
	for c := l.FirstChild(); c != nil; c = c.NextSibling() {
		content := w.blocks(c, itemBlocks)
		if len(content) == 0 {
			content = []node{{Type: "paragraph"}}
		}
		out.Content = append(out.Content, node{Type: "listItem", Content: content})
	}
	return out
}

func (w *writer) table(t *east.Table) node {
	out := node{Type: "table", Attrs: map[string]any{"layout": "default"}}
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		kind := "tableCell"
		if _, ok := r.(*east.TableHeader); ok {
			kind = "tableHeader"
		}
		row := node{Type: "tableRow"}
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			para := node{Type: "paragraph", Content: w.inlines(c, nil)}
			row.Content = append(row.Content, node{Type: kind, Content: []node{para}})
		}
		if len(row.Content) > 0 {
			out.Content = append(out.Content, row)
		}
	}
	return out
}

// restrict keeps nodes of an allowed type; others give up their structure
// but not their text (a heading in a list becomes a bold paragraph, a
// table in a quote its rows as paragraphs, a rule vanishes).
func restrict(nodes []node, allowed map[string]bool) []node {
	var out []node
	for _, n := range nodes {
		if allowed[n.Type] {
			out = append(out, n)
			continue
		}
		switch n.Type {
		case "heading":
			c := make([]node, len(n.Content))
			for i, t := range n.Content {
				t.Marks = withMark(t.Marks, mark{Type: "strong"})
				c[i] = t
			}
			out = append(out, node{Type: "paragraph", Content: c})
		case "blockquote", "listItem":
			out = append(out, restrict(n.Content, allowed)...)
		case "table":
			for _, row := range n.Content {
				var cells []node
				for i, cell := range row.Content {
					if i > 0 {
						cells = append(cells, node{Type: "text", Text: " | "})
					}
					for _, p := range cell.Content {
						cells = append(cells, p.Content...)
					}
				}
				if len(cells) > 0 {
					out = append(out, node{Type: "paragraph", Content: cells})
				}
			}
		}
	}
	return out
}

// inlines converts n's inline children under the marks in force.
func (w *writer) inlines(n ast.Node, marks []mark) []node {
	var out []node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, w.inline(c, marks)...)
	}
	return out
}

func (w *writer) inline(n ast.Node, marks []mark) []node {
	switch v := n.(type) {
	case *ast.Text:
		var out []node
		if s := string(v.Segment.Value(w.src)); s != "" {
			out = append(out, node{Type: "text", Text: s, Marks: marks})
		}
		if v.HardLineBreak() || v.SoftLineBreak() {
			out = append(out, node{Type: "hardBreak"})
		}
		return out
	case *ast.String:
		if len(v.Value) == 0 {
			return nil
		}
		return []node{{Type: "text", Text: string(v.Value), Marks: marks}}
	case *ast.CodeSpan:
		var b strings.Builder
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(w.src))
			}
		}
		if b.Len() == 0 {
			return nil
		}
		// code excludes every mark but link.
		var keep []mark
		for _, m := range marks {
			if m.Type == "link" {
				keep = append(keep, m)
			}
		}
		return []node{{Type: "text", Text: strings.ReplaceAll(b.String(), "\n", " "), Marks: withMark(keep, mark{Type: "code"})}}
	case *ast.Emphasis:
		kind := "em"
		if v.Level >= 2 {
			kind = "strong"
		}
		return w.inlines(v, withMark(marks, mark{Type: kind}))
	case *east.Strikethrough:
		return w.inlines(v, withMark(marks, mark{Type: "strike"}))
	case *ast.Link:
		return w.link(v, string(v.Destination), marks)
	case *ast.Image:
		// ADF images need uploaded media: link the picture instead.
		out := w.link(v, string(v.Destination), marks)
		if len(out) == 0 {
			out = []node{{Type: "text", Text: string(v.Destination), Marks: withMark(marks, mark{Type: "link", Attrs: map[string]any{"href": string(v.Destination)}})}}
		}
		return out
	case *ast.AutoLink:
		u := string(v.URL(w.src))
		label := string(v.Label(w.src))
		return []node{linked(label, u, marks)}
	case *east.TaskCheckBox:
		if v.IsChecked {
			return []node{{Type: "text", Text: "☑ ", Marks: marks}}
		}
		return []node{{Type: "text", Text: "☐ ", Marks: marks}}
	case *ast.RawHTML:
		var b strings.Builder
		for i := 0; i < v.Segments.Len(); i++ {
			seg := v.Segments.At(i)
			b.Write(seg.Value(w.src))
		}
		if b.Len() == 0 {
			return nil
		}
		return []node{{Type: "text", Text: b.String(), Marks: marks}}
	}
	return w.inlines(n, marks)
}

func (w *writer) link(n ast.Node, dest string, marks []mark) []node {
	if !safeHref(dest) {
		// Not linkable (a relative path, javascript:): the text stays.
		return w.inlines(n, marks)
	}
	return w.inlines(n, withMark(marks, mark{Type: "link", Attrs: map[string]any{"href": dest}}))
}

func linked(label, href string, marks []mark) node {
	if !safeHref(href) {
		return node{Type: "text", Text: label, Marks: marks}
	}
	if label == "" {
		label = href
	}
	return node{Type: "text", Text: label, Marks: withMark(marks, mark{Type: "link", Attrs: map[string]any{"href": href}})}
}

// safeHref: absolute http(s) and mailto links only.
func safeHref(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != ""
	}
	return false
}

// withMark adds m to marks (a copy: siblings share the outer slice); a
// mark already in force (nested emphasis) is not repeated.
func withMark(marks []mark, m mark) []mark {
	out := make([]mark, 0, len(marks)+1)
	out = append(out, marks...)
	for _, x := range marks {
		if x.Type == m.Type && m.Type != "link" {
			return out
		}
	}
	return append(out, m)
}
