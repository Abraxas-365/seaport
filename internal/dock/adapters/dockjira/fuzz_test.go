package dockjira

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// maxGrowth bounds how much bigger than its input a conversion may be: a
// webhook body is capped (1 MiB), so a blow-up would be a memory problem.
func tooBig(in, out int) bool { return out > 8*in+1024 }

// FuzzADF reads an arbitrary text field of a webhook: wiki markup (a JSON
// string) or an ADF document. It never panics and never blows up.
func FuzzADF(f *testing.F) {
	for _, s := range []string{
		`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"id":"5b10bot","text":"@Seaport Bot"}},{"type":"text","text":" hi","marks":[{"type":"link","attrs":{"href":"https://example.com/x"}},{"type":"strong"}]}]}]}`,
		`{"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"bulletList","content":[{"type":"listItem"}]}]}]}]}`,
		`{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph"}]},{"type":"tableCell"}]}]}]}`,
		`{"type":"doc","content":[{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"a\nb"}]},{"type":"rule"},{"type":"blockquote","content":[{"type":"paragraph"}]}]}`,
		`{"type":"doc","content":[{"type":"orderedList","attrs":{"order":-3},"content":[{"type":"listItem"}]}]}`,
		`{"type":"doc","content":[{"type":"unknown","content":[{"type":"text","text":"x"}]}]}`,
		`"h1. Title\n* one\n** two\n# n1\n{code:java}\nint x;\n{code}\n||a||b||\n|1|2|\nbq. quote\n*bold* _it_ -del- [link|https://example.com] !img.png! [~accountid:5b10bot]"`,
		`"{noformat}\n{code}\n{color:red}x{color}\r\n----\n{quote}q{quote}"`,
		`"\u0000 0 \u00001\u0000"`, `null`, `[]`, `42`, `{`, ``,
	} {
		f.Add(s, "5b10bot")
	}
	f.Fuzz(func(t *testing.T, raw, bot string) {
		r := &reader{botID: bot, botName: "Seaport"}
		out := r.markdown(json.RawMessage(raw))
		if tooBig(len(raw), len(out)) {
			t.Fatalf("%d bytes in, %d out", len(raw), len(out))
		}
		_ = r.mentioned
	})
}

// FuzzWiki: wiki markup alone, including text with the NUL placeholders the
// converter uses internally.
func FuzzWiki(f *testing.F) {
	for _, s := range []string{
		"h2. Steps\n* a\n** b\n# one\n## two\n- dash", "{code:java}x{code} and {noformat}y{noformat}", "||h1||h2||\n|c1|c2|",
		"bq. quoted\n{quote}\nmore\n{quote}", "*bold* _italic_ +under+ -strike- ^sup^ ~sub~ {{mono}} ??cite??",
		"[a|https://example.com/x] [https://example.com] [~accountid:abc] !pic.png|width=10!", "\x000\x00 \x00999999999999\x00",
		"----\r\n{color:#ff0000}red{color}", strings.Repeat("*", 200) + " x", strings.Repeat("{code}", 50),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := (&reader{botID: "x"}).wiki(s)
		if tooBig(len(s), len(out)) {
			t.Fatalf("%d bytes in, %d out", len(s), len(out))
		}
	})
}

// FuzzMarkdownToADF: whatever an agent writes becomes a valid ADF document
// (JSON, doc/version/non-empty content, valid UTF-8 text), splits within the
// limit, and reads back without panicking.
func FuzzMarkdownToADF(f *testing.F) {
	for _, s := range []string{
		"## Title\n\nSome **bold** and `code` with [a link](https://example.com/a).\n\n- one\n  - nested\n- two\n\n```go\nfmt.Println(1)\n```\n\n> quote\n\n| a | b |\n| --- | --- |\n| 1 | 2 |",
		"", "  ", "<b>raw</b>", "![img](https://example.com/a.png)", "1. a\n2. b\n   - c\n\n---\n", "~~strike~~ https://example.com auto",
		"- [ ] todo\n- [x] done", "> - quoted list\n> ```\n> code\n> ```", "\x00\xff", strings.Repeat("> ", 100) + "deep", strings.Repeat("- a\n  ", 60),
		"```\nunclosed", "| a |\n|---|\n", "# " + strings.Repeat("x", 500),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		d := toADF(md)
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) || d.Type != "doc" || d.Version != 1 || len(d.Content) == 0 {
			t.Fatalf("not an ADF doc: %s", b)
		}
		if tooBig(len(md), len(b)) && len(b) > 64<<10 {
			t.Fatalf("%d bytes of Markdown became %d of ADF", len(md), len(b))
		}
		_ = (&reader{}).markdown(b)
		for _, c := range split(md, 200) {
			if len(c) > 200 {
				t.Fatalf("chunk of %d bytes", len(c))
			}
			if utf8.ValidString(md) && !utf8.ValidString(c) {
				t.Fatalf("split broke UTF-8: %q", c)
			}
		}
	})
}

// FuzzSplit: a reply of any shape is cut into chunks that never exceed the
// limit (beyond the few bytes of a reopened fence) and always terminates.
func FuzzSplit(f *testing.F) {
	f.Add("```"+strings.Repeat("x", 300)+"\n"+strings.Repeat("code\n", 100)+"```", 200)
	f.Add("ht"+strings.Repeat("\xb5", 600), 200)
	f.Add("a\n\nb\n\n"+strings.Repeat("word ", 100), 50)
	f.Add("~~~go\nx\n~~~\n\n```\ny", 10)
	f.Fuzz(func(t *testing.T, md string, max int) {
		if max < 8 || max > MaxComment {
			return
		}
		for _, c := range split(md, max) {
			if len(c) > max {
				t.Fatalf("chunk of %d bytes for max %d", len(c), max)
			}
		}
	})
}

// FuzzJiraSignature: with a fixed secret, only "sha256=<hex HMAC of the
// body>" (trimmed) passes, and the bearer proof needs the exact secret.
func FuzzJiraSignature(f *testing.F) {
	const secret = "fuzz-jira-secret"
	sign := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	body := []byte(`{"webhookEvent":"comment_created"}`)
	f.Add(body, sign(body))
	f.Add(body, "  "+sign(body)+"\n")
	f.Add(body, "sha256=")
	f.Add(body, "Bearer "+secret)
	f.Add(body, "Bearer ")
	f.Add(body, "")
	f.Fuzz(func(t *testing.T, body []byte, header string) {
		want := sign(body)
		if !validSignature(secret, body, want) {
			t.Fatal("the right signature was refused")
		}
		if validSignature(secret, body, header) && !strings.EqualFold(strings.TrimSpace(header), want) {
			t.Fatalf("accepted %q, the signature is %q", header, want)
		}
		if validBearer(secret, header) && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Bearer ")) != secret {
			t.Fatalf("bearer accepted %q", header)
		}
	})
}
