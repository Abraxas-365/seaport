package bootstrap_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/connector"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// An agent's reply carries files: kept sealed as the outbound ship's
// attachments (images inline, other files only as downloads), refused
// when invalid, over MCP (base64) and POST /crew/reply (seaport reply).
func TestRepliesCarryFiles(t *testing.T) {
	p, dir := mediaPort(t)
	d := p.apiDock("files")
	p.letIn(d)
	in := p.inject(d, "+34600000000", "m-1", "send me the report")
	report := []byte("# QA report\n\n- checkout: ok\n")

	session := mcpSession(t, p, p.crewToken())
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{
		"conversation_id": in.Conversation.String(), "text": "Here it is.",
		"files": []map[string]string{
			{"name": "qa-report.md", "content_base64": base64.StdEncoding.EncodeToString(report)},
			{"name": "shot.png", "content_base64": base64.StdEncoding.EncodeToString(png)},
		}}})
	if err != nil || res.IsError {
		t.Fatalf("reply with files: %v %+v", err, res)
	}
	h := p.history(in.Conversation)
	out := h[0]
	if out.Direction != ship.DirectionOut || out.Text != "Here it is." || len(out.Attachments) != 2 {
		t.Fatalf("reply = %+v", out)
	}
	doc, img := out.Attachments[0], out.Attachments[1]
	if doc.Kind != ship.AttachmentDocument || doc.Name != "qa-report.md" || !strings.HasPrefix(doc.MIME, "text/plain") ||
		img.Kind != ship.AttachmentImage || img.MIME != "image/png" {
		t.Fatalf("attachments = %+v", out.Attachments)
	}
	if n := len(blobFiles(t, dir)); n != 2 {
		t.Fatalf("blobs = %d", n)
	}
	// A document is a download (never rendered); an image shows.
	req, _ := http.NewRequest(http.MethodGet, p.srv.URL+"/api/v1/attachments/"+doc.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "qa-report.md") {
		t.Fatalf("download: %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	if code, ct, body := p.fetch("/crew/attachments/"+doc.ID.String(), p.crewToken()); code != http.StatusOK || !bytes.Equal(body, report) || !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("crew download: %d %s", code, ct)
	}
	// The attachment tool hands a document's text back.
	res, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "attachment", Arguments: map[string]any{"attachment_id": doc.ID.String()}})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("attachment tool: %v %+v", err, res)
	}
	if r, ok := res.Content[0].(*mcp.EmbeddedResource); !ok || r.Resource.Text != string(report) {
		t.Fatalf("attachment content = %#v", res.Content[0])
	}

	// HTML or SVG is never served as such.
	html := []byte("<html><script>alert(1)</script></html>")
	res, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{
		"conversation_id": in.Conversation.String(),
		"files":           []map[string]string{{"name": "page.html", "content_base64": base64.StdEncoding.EncodeToString(html)}}}})
	if res == nil || res.IsError {
		t.Fatalf("html reply: %+v", res)
	}
	if a := p.history(in.Conversation)[0].Attachments; len(a) != 1 || a[0].MIME != "application/octet-stream" || a[0].Kind != ship.AttachmentDocument {
		t.Fatalf("html attachment = %+v", a)
	}

	// Bad files are refused, nothing stored.
	before := len(p.history(in.Conversation))
	for _, files := range [][]map[string]string{
		{{"name": "../etc/passwd", "content_base64": "eA=="}},
		{{"name": "a.txt", "content_base64": "not base64!"}},
		{{"name": "a.txt", "content_base64": ""}},
		{{"name": "a.txt", "content_base64": "eA=="}, {"name": "A.txt", "content_base64": "eA=="}},
	} {
		res, _ := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{
			"conversation_id": in.Conversation.String(), "files": files}})
		if res == nil || !res.IsError {
			t.Fatalf("bad files %v accepted", files)
		}
	}
	if after := len(p.history(in.Conversation)); after != before {
		t.Fatalf("refused replies stored ships: %d → %d", before, after)
	}

	// seaport reply -file sends files from disk as the token's agent.
	work := t.TempDir()
	path := filepath.Join(work, "log.txt")
	if err := os.WriteFile(path, []byte("all green\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(hosting.EnvURL, p.srv.URL)
	t.Setenv(hosting.EnvToken, p.crewToken())
	var printed bytes.Buffer
	if err := connector.ReplyCommand(t.Context(), []string{in.Conversation.String(), "-file", path, "Logs", "attached"}, &printed); err != nil {
		t.Fatal(err)
	}
	last := p.history(in.Conversation)[0]
	if !strings.HasPrefix(printed.String(), last.ID.String()+" sent") || last.Text != "Logs attached" || len(last.Attachments) != 1 ||
		last.Attachments[0].Name != "log.txt" || last.Agent == nil || *last.Agent != p.agent(testAgent).ID {
		t.Fatalf("seaport reply: %q %+v", printed.String(), last)
	}
	// Admin tokens can't reply as an agent there.
	body := crew.ReplyRequest{ConversationID: in.Conversation, Text: "hi"}
	if code := p.call(http.MethodPost, crew.ReplyPath, adminToken, body, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin on /crew/reply: %d", code)
	}
}

// A port without blob storage refuses files instead of dropping them.
func TestFilesNeedStorage(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("nofiles")
	p.letIn(d)
	in := p.inject(d, "+34600000000", "m-1", "hi")
	res, _ := mcpSession(t, p, p.crewToken()).CallTool(t.Context(), &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{
		"conversation_id": in.Conversation.String(), "text": "x",
		"files": []map[string]string{{"name": "a.txt", "content_base64": "eA=="}}}})
	if res == nil || !res.IsError || !strings.Contains(fmtJSON(res.Content), "keeps no files") {
		t.Fatalf("reply without storage: %+v", res)
	}
}

// On GitHub, a reply's files are committed (one commit, on top of the
// files branch, retried when another reply moved it) and the comment
// shows the images and links the rest at that commit.
func TestGitHubReplyFiles(t *testing.T) {
	gh := newFakeGitHub(t)
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "github-test-secret-key-0123456789abcdef"
		c.BlobURL = config.LocalBlobURL(filepath.Join(t.TempDir(), "blobs"))
		c.GitHubURL, c.GitHubAPIURL = gh.srv.URL, gh.srv.URL
		c.GitHubOIDCURL = gh.srv.URL + "/oidc"
	}, bootstrap.Options{})
	d := p.githubDock(gh)
	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)
	if code, out := p.webhook(d, gh.secret, "issue_comment", ghComment(700, "@test QA please", "bob", "User")); code != http.StatusAccepted || out["created"] != true {
		t.Fatalf("webhook: %d %v", code, out)
	}
	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	conv := convs.Items[0].ID.String()
	session := mcpSession(t, p, p.crewToken())
	reply := func(text string, files ...map[string]string) string {
		t.Helper()
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{
			"conversation_id": conv, "text": text, "files": files}})
		if err != nil || res.IsError {
			t.Fatalf("reply: %v %+v", err, res)
		}
		gh.mu.Lock()
		defer gh.mu.Unlock()
		return gh.comments[len(gh.comments)-1]
	}
	file := func(name string, data []byte) map[string]string {
		return map[string]string{"name": name, "content_base64": base64.StdEncoding.EncodeToString(data)}
	}

	first := reply("QA done.", file("qa report.md", []byte("# ok\n")), file("shot.png", png))
	files := gh.gitFiles()
	var prefix string
	for path := range files {
		prefix, _, _ = strings.Cut(path, "/gh-")
	}
	if prefix != "pr-7" || len(files) != 2 {
		t.Fatalf("committed = %v", files)
	}
	gh.mu.Lock()
	head := gh.gitHead
	gh.mu.Unlock()
	link := gh.srv.URL + "/acme/web/blob/" + head + "/pr-7/gh-"
	if !strings.HasPrefix(first, "QA done.\n\n📎 [qa report.md]("+link) || !strings.Contains(first, "/qa%20report.md)") ||
		!strings.Contains(first, "![shot.png]("+link) || !strings.Contains(first, "/shot.png?raw=true)") ||
		!strings.Contains(first, "<!-- seaport-message gh-") {
		t.Fatalf("comment = %q", first)
	}
	// The next reply commits on top; one that lost a race tries again.
	gh.mu.Lock()
	gh.gitRace = 1
	gh.mu.Unlock()
	second := reply("", file("log.txt", []byte("green")))
	gh.mu.Lock()
	parents := gh.git[gh.gitHead]["parents"].([]any)
	newHead := gh.gitHead
	gh.mu.Unlock()
	if newHead == head || len(parents) != 1 || parents[0] == head || !strings.HasPrefix(second, "📎 [log.txt]("+gh.srv.URL+"/acme/web/blob/"+newHead+"/pr-7/gh-") {
		t.Fatalf("second reply: %q (head %s parents %v)", second, newHead, parents)
	}
	if f := gh.gitFiles(); len(f) != 1 {
		t.Fatalf("second commit files = %v", f)
	}
}
