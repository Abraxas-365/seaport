package connector

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	acp "github.com/coder/acp-go-sdk"
)

const (
	// maxPromptImages bounds the images one prompt carries (the rest are
	// saved as files, or named in the text only).
	maxPromptImages = 8
	// maxPromptFiles bounds the files one prompt saves for the agent.
	maxPromptFiles = 16
	// AttachmentsDir is where, under a session's working directory, the
	// connector saves the files of the messages it hands the agent
	// (<dir>/<attachment id>/<name>).
	AttachmentsDir = ".seaport/attachments"
)

// image is an attachment's bytes, fetched for a prompt.
type image struct {
	mime string
	data []byte
}

// placed is where an attachment of a prompt went: an image block after
// the text, or a file saved for the agent (path).
type placed struct {
	image bool
	path  string
}

// shown says where the attachments of a prompt went (those it lacks are
// only named in its text).
type shown map[identity.AttachmentID]placed

// gather prepares the attachments of ships for the agent, in order: the
// images go as image blocks to an agent that takes them
// (promptCapabilities.image), up to maxPromptImages; every other file
// (documents, and images past that) is saved under dir (the session's
// working directory; "" saves nothing), up to maxPromptFiles. One that
// can't be fetched or saved is only named; withheld ones aren't fetched.
func gather(ctx context.Context, cfg Config, takesImages bool, dir string, ships []ship.Ship) ([]image, shown) {
	var out []image
	ok := shown{}
	files := 0
	for _, s := range ships {
		for _, a := range s.Attachments {
			if a.Withheld {
				continue
			}
			if a.Kind == ship.AttachmentImage && takesImages && len(out) < maxPromptImages {
				img, err := fetchImage(ctx, cfg, a.ID)
				if err != nil {
					slog.Warn("fetch attachment; naming it only", "attachment", a.ID, "err", err)
					continue
				}
				out = append(out, img)
				ok[a.ID] = placed{image: true}
				continue
			}
			if dir == "" || files == maxPromptFiles {
				continue
			}
			path, err := saveAttachment(ctx, cfg, dir, a)
			if err != nil {
				slog.Warn("save attachment; naming it only", "attachment", a.ID, "err", err)
				continue
			}
			files++
			ok[a.ID] = placed{path: path}
		}
	}
	return out, ok
}

// hasFiles says whether ships carry an attachment the agent may get.
func hasFiles(ships []ship.Ship) bool {
	for _, s := range ships {
		for _, a := range s.Attachments {
			if !a.Withheld {
				return true
			}
		}
	}
	return false
}

// fetchImage reads an image attachment within ship.MaxImage.
func fetchImage(ctx context.Context, cfg Config, id identity.AttachmentID) (image, error) {
	mt, data, err := fetchAttachment(ctx, cfg, id, ship.MaxImage)
	if err != nil {
		return image{}, err
	}
	if !strings.HasPrefix(mt, "image/") || len(data) == 0 {
		return image{}, errx.External("fetch attachment: not an image within limits")
	}
	return image{mime: mt, data: data}, nil
}

// saveAttachment writes an attachment under dir/AttachmentsDir/<id>/ with
// its (cleaned) name and returns the file's path. A file already there
// (a redelivery) is not fetched again. Everything goes through an os.Root
// on dir, so a symlink in the work tree (a repository may commit one as
// .seaport) never takes the write outside it.
func saveAttachment(ctx context.Context, cfg Config, dir string, a ship.Attachment) (string, error) {
	name := fileName(a)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", errx.Wrap(err, "attachments directory", errx.TypeInternal)
	}
	defer func() { _ = root.Close() }()
	base := filepath.FromSlash(AttachmentsDir)
	rel := filepath.Join(base, a.ID.String(), name)
	if st, err := root.Lstat(rel); err == nil && st.Mode().IsRegular() && (a.Size == 0 || st.Size() == a.Size) {
		return filepath.Join(dir, rel), nil
	}
	_, data, err := fetchAttachment(ctx, cfg, a.ID, max(ship.MaxFile, ship.MaxImage))
	if err != nil {
		return "", err
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return "", errx.Wrap(err, "attachments directory", errx.TypeInternal)
	}
	// Received files are never the project's: keep them out of git.
	ignore := filepath.Join(base, ".gitignore")
	if _, err := root.Lstat(ignore); os.IsNotExist(err) {
		_ = root.WriteFile(ignore, []byte("*\n"), 0o600)
	}
	if err := root.WriteFile(rel, data, 0o600); err != nil {
		return "", errx.Wrap(err, "save attachment", errx.TypeInternal)
	}
	return filepath.Join(dir, rel), nil
}

// fileName is the name an attachment is saved under: its display name
// (ship.CleanName), made a name every OS takes. Characters Windows (or a
// shell) refuses become "_", trailing dots and spaces go, and a reserved
// device name (CON, NUL, COM1...) gets a "_" in front, so a name that
// works on one machine works on all; "" becomes the kind.
func fileName(a ship.Attachment) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, ship.CleanName(a.Name))
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return string(a.Kind)
	}
	stem, _, _ := strings.Cut(name, ".")
	switch s := strings.ToUpper(strings.TrimSpace(stem)); {
	case s == "CON" || s == "PRN" || s == "AUX" || s == "NUL",
		len(s) == 4 && (strings.HasPrefix(s, "COM") || strings.HasPrefix(s, "LPT")) && s[3] >= '0' && s[3] <= '9':
		name = "_" + name
	}
	return name
}

// fetchAttachment reads GET /crew/attachments/{id} with the crew token, at
// most limit bytes, and gives its media type and bytes.
func fetchAttachment(ctx context.Context, cfg Config, id identity.AttachmentID, limit int) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := strings.TrimRight(cfg.Server, "/") + "/crew/attachments/" + id.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", nil, errx.Wrap(err, "attachment request", errx.TypeValidation)
	}
	req.Header = authHeader(cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, errx.Wrap(err, "fetch attachment", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", nil, errx.Wrap(err, "read attachment", errx.TypeExternal)
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, errx.External("fetch attachment: " + portMessage(data, resp.Status))
	}
	if len(data) > limit {
		return "", nil, errx.External("fetch attachment: too large")
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mt, data, nil
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
// that follow the text, in order, the files saved for the agent, or why
// one isn't there.
func writeAttachments(b *strings.Builder, s ship.Ship, ok shown) {
	for _, a := range s.Attachments {
		name := ""
		if a.Name != "" {
			name = " " + a.Name
		}
		p, found := ok[a.ID]
		switch {
		case found && p.image:
			fmt.Fprintf(b, "    [%s%s attached below]\n", a.Kind, name)
		case found:
			fmt.Fprintf(b, "    [%s%s saved at %s]\n", a.Kind, name, p.path)
		case a.Withheld:
			fmt.Fprintf(b, "    [%s%s attached; its dock's owner keeps files from agents]\n", a.Kind, name)
		default:
			fmt.Fprintf(b, "    [%s%s attached; not shown to you]\n", a.Kind, name)
		}
	}
}
