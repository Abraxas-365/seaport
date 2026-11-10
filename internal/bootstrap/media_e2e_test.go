package bootstrap_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// png is a tiny valid PNG header (sniffed as image/png).
var png = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

// mediaPort is a port that keeps media on disk, sealed with a test key.
func mediaPort(t *testing.T) (*port, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "blobs")
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "media-test-secret-key-0123456789abcdef"
		c.BlobURL = config.LocalBlobURL(dir)
	}, bootstrap.Options{SweepInterval: 300 * time.Millisecond})
	return p, dir
}

// fetch GETs path with token and returns status, content type and body.
func (p *port) fetch(path, token string) (int, string, []byte) {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, p.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

// blobFiles lists the files under dir (gocloud's sidecars excluded).
func blobFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasSuffix(path, ".attrs") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// An image that comes with a message is kept sealed (never in clear on
// disk), travels with the ship to the crew (metadata), and is read back
// by the crew, the admin API and the MCP attachment tool; the caption is
// the text. A redelivery stores nothing more; an unsupported file
// becomes a note.
func TestImagesTravelWithShips(t *testing.T) {
	p, dir := mediaPort(t)
	d := p.apiDock("fotos")
	p.letIn(d)
	c := p.dialCrew(1)

	s := p.injectWith(d, map[string]any{"chat_id": "200", "message_id": "img1", "sender_id": "200",
		"text": "mira esto", "images": []map[string]any{{"name": "foto.png", "data": png}}})
	if len(s.Attachments) != 1 || s.Attachments[0].MIME != "image/png" || s.Attachments[0].Size != int64(len(png)) || s.Text != "mira esto" {
		t.Fatalf("ship: %+v", s)
	}
	a := s.Attachments[0]

	// Sealed at rest: one blob, and not the image's bytes.
	files := blobFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("blobs: %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	if bytes.Contains(raw, png[:16]) {
		t.Fatal("image stored in clear")
	}

	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships[0].Attachments) != 1 || f.Delivery.Ships[0].Attachments[0].ID != a.ID {
		t.Fatalf("delivery: %+v", f)
	}
	ack(c, f)

	for name, get := range map[string]func() (int, string, []byte){
		"crew":  func() (int, string, []byte) { return p.fetch("/crew/attachments/"+a.ID.String(), p.crewToken()) },
		"admin": func() (int, string, []byte) { return p.fetch("/api/v1/attachments/"+a.ID.String(), adminToken) },
	} {
		code, ct, body := get()
		if code != http.StatusOK || ct != "image/png" || !bytes.Equal(body, png) {
			t.Fatalf("%s: %d %s %d bytes", name, code, ct, len(body))
		}
	}

	// History names it; the MCP tool shows it.
	hist := p.history(s.Conversation)
	if len(hist) != 1 || len(hist[0].Attachments) != 1 {
		t.Fatalf("history: %+v", hist)
	}
	sess := mcpSession(t, p, p.crewToken())
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "attachment",
		Arguments: map[string]any{"attachment_id": a.ID.String()}})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("attachment tool: %v %+v", err, res)
	}
	if img, ok := res.Content[0].(*mcp.ImageContent); !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, png) {
		t.Fatalf("attachment content: %+v", res.Content[0])
	}

	// A redelivery stores nothing more.
	var dup struct {
		ship.Ship
		Duplicate bool `json:"duplicate"`
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/ships", map[string]any{"chat_id": "200",
		"message_id": "img1", "sender_id": "200", "images": []map[string]any{{"data": png}}}, &dup); code != http.StatusOK ||
		!dup.Duplicate || len(dup.Attachments) != 1 || dup.Attachments[0].ID != a.ID {
		t.Fatalf("duplicate: %d %+v", code, dup)
	}

	if files := blobFiles(t, dir); len(files) != 1 {
		t.Fatalf("a redelivery stored its image again: %v", files)
	}

	// Not an image: a note in the text, no attachment.
	svg := p.injectWith(d, map[string]any{"chat_id": "200", "message_id": "img2", "sender_id": "200",
		"images": []map[string]any{{"data": []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")}}})
	if len(svg.Attachments) != 0 || svg.Text != ship.MediaNote(ship.AttachmentImage, "unsupported format") {
		t.Fatalf("svg: %+v", svg)
	}
}

// An agent reads only the attachments of its docks' conversations (else
// 404), and an attachment of a deleted dock goes with it: its blob is
// swept.
func TestAttachmentsStayWithTheirDock(t *testing.T) {
	p, dir := mediaPort(t)
	d := p.apiDock("fotos")
	p.letIn(d)
	s := p.injectWith(d, map[string]any{"chat_id": "300", "message_id": "m1", "sender_id": "300",
		"images": []map[string]any{{"data": png}}})
	a := s.Attachments[0]

	other := p.agent("other")
	tok := p.issueAs(adminToken, other.ID, "other").Value
	if code, _, _ := p.fetch("/crew/attachments/"+a.ID.String(), tok); code != http.StatusNotFound {
		t.Fatalf("another agent's attachment: %d", code)
	}
	if code, _, _ := p.fetch("/crew/attachments/"+a.ID.String(), adminToken); code != http.StatusUnauthorized {
		t.Fatalf("admin token on the crew route: %d", code)
	}

	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+d.ID.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete dock: %d", code)
	}
	if code, _, _ := p.fetch("/api/v1/attachments/"+a.ID.String(), adminToken); code != http.StatusNotFound {
		t.Fatalf("attachment of a deleted dock: %d", code)
	}
	// The sweeper deletes the blob.
	deadline := time.Now().Add(10 * time.Second)
	for len(blobFiles(t, dir)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("blob not swept: %v", blobFiles(t, dir))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Without a blob store media arrive as notes; a caption-less image still
// makes a ship.
func TestMediaOffLeavesNotes(t *testing.T) {
	p := startPort(t)
	d := p.apiDock("fotos")
	p.letIn(d)
	s := p.injectWith(d, map[string]any{"chat_id": "400", "message_id": "m1", "sender_id": "400",
		"images": []map[string]any{{"data": png}}})
	if len(s.Attachments) != 0 || s.Text != ship.MediaNote(ship.AttachmentImage, "media storage is off") {
		t.Fatalf("ship: %+v", s)
	}
}

// A denied chat's images aren't stored (anyone can message a dock): the
// ship keeps a note instead.
func TestDeniedSendersImagesAreNotKept(t *testing.T) {
	p, dir := mediaPort(t)
	d := p.apiDock("fotos")
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String()+"/customs/policy", map[string]string{"default": "deny"}, nil); code != http.StatusOK {
		t.Fatalf("deny: %d", code)
	}
	s := p.injectWith(d, map[string]any{"chat_id": "500", "message_id": "m1", "sender_id": "500",
		"text": "hola", "images": []map[string]any{{"data": png}}})
	if len(s.Attachments) != 0 || s.Text != "hola\n"+ship.MediaNote(ship.AttachmentImage, "not kept from a denied sender") {
		t.Fatalf("ship: %+v", s)
	}
	if files := blobFiles(t, dir); len(files) != 0 {
		t.Fatalf("blobs: %v", files)
	}
}
