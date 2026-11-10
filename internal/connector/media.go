package connector

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	acp "github.com/coder/acp-go-sdk"
)

// maxPromptImages bounds the images one prompt carries (the rest are
// named in the text only).
const maxPromptImages = 8

// image is an attachment's bytes, fetched for a prompt.
type image struct {
	mime string
	data []byte
}

// shown says which attachments go with a prompt as images (others are
// only named in its text).
type shown map[identity.AttachmentID]bool

// gather fetches the images of ships for an agent that takes them
// (promptCapabilities.image): in order, up to maxPromptImages. One that
// can't be fetched is only named.
func gather(ctx context.Context, cfg Config, takesImages bool, ships []ship.Ship) ([]image, shown) {
	var out []image
	ok := shown{}
	if !takesImages {
		return nil, ok
	}
	for _, s := range ships {
		for _, a := range s.Attachments {
			if a.Kind != ship.AttachmentImage || len(out) == maxPromptImages {
				continue
			}
			img, err := fetchAttachment(ctx, cfg, a.ID)
			if err != nil {
				slog.Warn("fetch attachment; naming it only", "attachment", a.ID, "err", err)
				continue
			}
			out = append(out, img)
			ok[a.ID] = true
		}
	}
	return out, ok
}

// fetchAttachment reads GET /crew/attachments/{id} with the crew token.
func fetchAttachment(ctx context.Context, cfg Config, id identity.AttachmentID) (image, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := strings.TrimRight(cfg.Server, "/") + "/crew/attachments/" + id.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return image{}, errx.Wrap(err, "attachment request", errx.TypeValidation)
	}
	req.Header = authHeader(cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return image{}, errx.Wrap(err, "fetch attachment", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, ship.MaxImage+1))
	if err != nil {
		return image{}, errx.Wrap(err, "read attachment", errx.TypeExternal)
	}
	if resp.StatusCode != http.StatusOK {
		return image{}, errx.External("fetch attachment: " + portMessage(data, resp.Status))
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(mt, "image/") || len(data) == 0 || len(data) > ship.MaxImage {
		return image{}, errx.External("fetch attachment: not an image within limits")
	}
	return image{mime: mt, data: data}, nil
}

// blocks is a prompt: its text, then its images.
func blocks(text string, images []image) []acp.ContentBlock {
	out := []acp.ContentBlock{acp.TextBlock(text)}
	for _, img := range images {
		out = append(out, acp.ImageBlock(base64.StdEncoding.EncodeToString(img.data), img.mime))
	}
	return out
}

// writeAttachments names a ship's attachments under its line: the images
// that follow the text, in order, or why one isn't there.
func writeAttachments(b *strings.Builder, s ship.Ship, ok shown) {
	for _, a := range s.Attachments {
		name := ""
		if a.Name != "" {
			name = " " + a.Name
		}
		if ok[a.ID] {
			fmt.Fprintf(b, "    [%s%s attached below]\n", a.Kind, name)
		} else {
			fmt.Fprintf(b, "    [%s%s attached; not shown to you]\n", a.Kind, name)
		}
	}
}
