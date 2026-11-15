package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 32)...)

// A port serving attachments with the crew token (each with its type and
// bytes, counting fetches); others are missing.
func mediaServer(t *testing.T, files map[identity.AttachmentID][2]string, hits map[identity.AttachmentID]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for id, f := range files {
			if r.URL.Path == "/crew/attachments/"+id.String() {
				if hits != nil {
					hits[id]++
				}
				w.Header().Set("Content-Type", f[0])
				_, _ = w.Write([]byte(f[1]))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"attachment not found"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pngServer(t *testing.T, ok identity.AttachmentID) *httptest.Server {
	return mediaServer(t, map[identity.AttachmentID][2]string{ok: {"image/png", string(pngBytes)}}, nil)
}

func TestGatherFetchesImagesForAgentsThatTakeThem(t *testing.T) {
	good, gone := identity.NewAttachmentID(), identity.NewAttachmentID()
	srv := pngServer(t, good)
	cfg := Config{Server: srv.URL, Token: "tok"}
	ships := []ship.Ship{{SenderName: "Ana", Text: "mira", Attachments: []ship.Attachment{
		{ID: good, Kind: ship.AttachmentImage, MIME: "image/png", Name: "foto.png"},
		{ID: gone, Kind: ship.AttachmentImage, MIME: "image/png"},
	}}}

	images, shownIDs := gather(context.Background(), cfg, true, "", ships)
	if len(images) != 1 || images[0].mime != "image/png" || !bytes.Equal(images[0].data, pngBytes) || !shownIDs[good].image {
		t.Fatalf("gather: %d images, shown %v", len(images), shownIDs)
	}
	if _, found := shownIDs[gone]; found {
		t.Fatalf("a missing attachment is shown: %v", shownIDs)
	}
	text := Prompt(ship.Delivery{Ships: ships}, false, shownIDs)
	if !strings.Contains(text, "[image foto.png attached below]") || !strings.Contains(text, "[image attached; not shown to you]") {
		t.Fatalf("prompt:\n%s", text)
	}
	b := blocks(text, images)
	if len(b) != 2 || b[1].Image == nil || b[1].Image.MimeType != "image/png" ||
		b[1].Image.Data != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("blocks: %+v", b)
	}

	// An agent that doesn't take images (and no directory) fetches
	// nothing; the text names them.
	images, shownIDs = gather(context.Background(), cfg, false, "", ships)
	if len(images) != 0 || len(shownIDs) != 0 {
		t.Fatalf("gather without image support: %d", len(images))
	}
	if b := blocks(SteerPrompt(ship.Delivery{Ships: ships}, shownIDs), images); len(b) != 1 ||
		!strings.Contains(b[0].Text.Text, "not shown to you") {
		t.Fatalf("steer blocks: %+v", b)
	}
}

func TestGatherCapsImagesPerPrompt(t *testing.T) {
	id := identity.NewAttachmentID()
	srv := pngServer(t, id)
	var many []ship.Attachment
	for range maxPromptImages + 3 {
		many = append(many, ship.Attachment{ID: id, Kind: ship.AttachmentImage})
	}
	images, _ := gather(context.Background(), Config{Server: srv.URL, Token: "tok"}, true, "", []ship.Ship{{Attachments: many}})
	if len(images) != maxPromptImages {
		t.Fatalf("%d images", len(images))
	}
}

// Documents (and images an agent can't take) are saved in the session's
// working directory, kept out of git, named in the prompt by path, and not
// fetched again on a redelivery; withheld ones never are.
func TestGatherSavesFiles(t *testing.T) {
	doc, img, held := identity.NewAttachmentID(), identity.NewAttachmentID(), identity.NewAttachmentID()
	pdf := "%PDF-1.7 informe"
	hits := map[identity.AttachmentID]int{}
	srv := mediaServer(t, map[identity.AttachmentID][2]string{
		doc:  {"application/pdf", pdf},
		img:  {"image/png", string(pngBytes)},
		held: {"text/plain", "secret"},
	}, hits)
	cfg := Config{Server: srv.URL, Token: "tok"}
	dir := t.TempDir()
	ships := []ship.Ship{{SenderName: "Ana", Text: "el informe", Attachments: []ship.Attachment{
		{ID: doc, Kind: ship.AttachmentDocument, MIME: "application/pdf", Name: "../informe Q3.pdf", Size: int64(len(pdf))},
		{ID: img, Kind: ship.AttachmentImage, MIME: "image/png", Name: "foto.png"},
		{ID: held, Kind: ship.AttachmentDocument, Name: "x.txt", Withheld: true},
	}}}

	images, ok := gather(context.Background(), cfg, false, dir, ships)
	want := filepath.Join(dir, ".seaport", "attachments", doc.String(), "informe Q3.pdf")
	if len(images) != 0 || ok[doc].path != want || ok[img].path == "" || ok[img].image {
		t.Fatalf("gather: %d images, %v", len(images), ok)
	}
	if _, found := ok[held]; found || hits[held] != 0 {
		t.Fatalf("withheld fetched: %v %v", ok, hits)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != pdf {
		t.Fatalf("saved: %q %v", got, err)
	}
	if ig, err := os.ReadFile(filepath.Join(dir, ".seaport", "attachments", ".gitignore")); err != nil || string(ig) != "*\n" {
		t.Fatalf("gitignore: %q %v", ig, err)
	}
	text := Prompt(ship.Delivery{Ships: ships}, false, ok)
	if !strings.Contains(text, "[document ../informe Q3.pdf saved at "+want+"]") ||
		!strings.Contains(text, "[document x.txt attached; its dock's owner keeps files from agents]") {
		t.Fatalf("prompt:\n%s", text)
	}

	// Redelivered: the saved file is reused.
	if _, ok = gather(context.Background(), cfg, false, dir, ships); ok[doc].path != want || hits[doc] != 1 {
		t.Fatalf("redelivery: %v hits %d", ok, hits[doc])
	}
}

// A work tree whose .seaport is a symlink out of it (a repository may
// commit one) gets nothing written through it: the file is only named.
func TestGatherStaysInTheWorkTree(t *testing.T) {
	doc := identity.NewAttachmentID()
	srv := mediaServer(t, map[identity.AttachmentID][2]string{doc: {"application/pdf", "%PDF-1.7"}}, nil)
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, ".seaport")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	ships := []ship.Ship{{Attachments: []ship.Attachment{{ID: doc, Kind: ship.AttachmentDocument, Name: "a.pdf"}}}}
	if _, ok := gather(context.Background(), Config{Server: srv.URL, Token: "tok"}, false, dir, ships); len(ok) != 0 {
		t.Fatalf("saved through a symlink: %v", ok)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the work tree: %v", entries)
	}
}
