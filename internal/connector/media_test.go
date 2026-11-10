package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 32)...)

// A port serving one attachment with the crew token; others are missing.
func mediaServer(t *testing.T, ok identity.AttachmentID) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/crew/attachments/"+ok.String() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"attachment not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGatherFetchesImagesForAgentsThatTakeThem(t *testing.T) {
	good, gone := identity.NewAttachmentID(), identity.NewAttachmentID()
	srv := mediaServer(t, good)
	cfg := Config{Server: srv.URL, Token: "tok"}
	ships := []ship.Ship{{SenderName: "Ana", Text: "mira", Attachments: []ship.Attachment{
		{ID: good, Kind: ship.AttachmentImage, MIME: "image/png", Name: "foto.png"},
		{ID: gone, Kind: ship.AttachmentImage, MIME: "image/png"},
	}}}

	images, shownIDs := gather(context.Background(), cfg, true, ships)
	if len(images) != 1 || images[0].mime != "image/png" || !bytes.Equal(images[0].data, pngBytes) || !shownIDs[good] || shownIDs[gone] {
		t.Fatalf("gather: %d images, shown %v", len(images), shownIDs)
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

	// An agent that doesn't take images fetches nothing; the text names them.
	images, shownIDs = gather(context.Background(), cfg, false, ships)
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
	srv := mediaServer(t, id)
	var many []ship.Attachment
	for range maxPromptImages + 3 {
		many = append(many, ship.Attachment{ID: id, Kind: ship.AttachmentImage})
	}
	images, _ := gather(context.Background(), Config{Server: srv.URL, Token: "tok"}, true, []ship.Ship{{Attachments: many}})
	if len(images) != maxPromptImages {
		t.Fatalf("%d images", len(images))
	}
}
