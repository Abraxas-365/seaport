package shipsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/blob"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// mediaSealInfo derives the key that seals media at rest (from
// SEAPORT_SECRET_KEY; a new info re-keys).
const mediaSealInfo = "seaport media v1"

// Media is what the service needs to keep message media: blob storage
// and the secret key that seals it ("" refuses media: notes only).
type Media struct {
	Blobs     blob.Store
	SecretKey string
}

type media struct {
	blobs  blob.Store
	sealer *seal.Sealer
}

func newMedia(m Media) (media, error) {
	out := media{blobs: m.Blobs}
	if m.Blobs == nil || m.SecretKey == "" {
		return out, nil
	}
	s, err := seal.New(m.SecretKey, mediaSealInfo)
	if err != nil {
		return out, err
	}
	out.sealer = &s
	return out, nil
}

func (m media) on() bool { return m.sealer != nil }

// refused are the holds whose messages' media isn't fetched or stored:
// chats and senders the owner denied (anyone can message a dock; a
// denied stranger must not fill its storage). Pending chats' media is
// kept, since allowing the chat delivers what it sent meanwhile.
var refused = map[string]bool{conversation.HoldDenied: true, conversation.HoldSenderDenied: true}

// keep stores the message's media (sealed) before its ship: the
// attachments to insert with it, and the text with a note for each file
// that can't be kept. The blobs are staged in the trash first, so one
// whose ship never commits (a crash, a duplicate) is deleted later.
func (s *Service) keep(ctx context.Context, org identity.OrganizationID, in ship.Inbound, hold string) ([]ship.Attachment, string, error) {
	body := in.Text
	note := func(kind ship.AttachmentKind, why string) {
		if body != "" {
			body += "\n"
		}
		body += ship.MediaNote(kind, why)
	}
	var out []ship.Attachment
	var data [][]byte // bytes of out[i]
	for _, m := range in.Media {
		switch {
		case !s.media.on():
			note(m.Kind, "media storage is off")
			continue
		case refused[hold]:
			note(m.Kind, "not kept from a denied sender")
			continue
		}
		b, why := fetch(ctx, m)
		if why != "" {
			note(m.Kind, why)
			continue
		}
		mime := blob.ImageType(b, ship.ImageTypes)
		if mime == "" {
			note(m.Kind, "unsupported format")
			continue
		}
		sum := sha256.Sum256(b)
		id := identity.NewAttachmentID()
		out = append(out, ship.Attachment{ID: id, Kind: m.Kind, MIME: mime, Size: int64(len(b)),
			Name: text.Clean(m.Name), SHA256: hex.EncodeToString(sum[:]),
			Key: blob.TenantKey(org, "attachments", id.String())})
		data = append(data, b)
	}
	if len(out) == 0 {
		return nil, body, nil
	}
	keys := make([]string, len(out))
	for i, a := range out {
		keys[i] = a.Key
	}
	if err := s.repo.StageBlobs(ctx, org, keys, s.now().Add(ship.TrashGrace)); err != nil {
		return nil, "", errx.Wrap(err, "stage media", errx.TypeInternal)
	}
	var kept []ship.Attachment
	for i, a := range out {
		sealed := s.media.sealer.Seal(ship.AttachmentPurpose(org, a.ID), data[i])
		if err := s.media.blobs.Put(ctx, a.Key, bytes.NewReader(sealed), blob.PutOptions{ContentType: "application/octet-stream"}); err != nil {
			// The message still arrives; the owner sees that a file was lost.
			slog.Error("store media", "organization", org, "attachment", a.ID, "err", err)
			note(a.Kind, "it could not be stored")
			continue
		}
		kept = append(kept, a)
	}
	return kept, body, nil
}

// fetch gives the media's bytes (downloading them if the channel left
// that for now), or why they can't be kept.
func fetch(ctx context.Context, m ship.Media) ([]byte, string) {
	b := m.Data
	if m.Fetch != nil {
		var err error
		if b, err = m.Fetch(ctx); errors.Is(err, ship.ErrMediaTooLarge) {
			return nil, "too large to keep"
		} else if err != nil {
			slog.Warn("download media", "err", err)
			return nil, "the download failed"
		}
	}
	if len(b) > ship.MaxImage {
		return nil, "too large to keep"
	}
	if len(b) == 0 {
		return nil, "the download failed"
	}
	return b, ""
}

func (s *Service) OpenAttachment(ctx context.Context, org identity.OrganizationID, input ship.OpenAttachment) (ship.AttachmentFile, error) {
	if err := input.Validate(); err != nil {
		return ship.AttachmentFile{}, err
	}
	a, convID, err := s.repo.Attachment(ctx, org, input.ID)
	if err != nil {
		return ship.AttachmentFile{}, err
	}
	if input.Agent != nil {
		conv, err := s.convs.Find(ctx, org, convID)
		if err != nil {
			return ship.AttachmentFile{}, err
		}
		if err := s.onDock(ctx, org, *input.Agent, conv.Dock); errx.IsType(err, errx.TypeNotFound) {
			return ship.AttachmentFile{}, errx.NotFound("attachment not found")
		} else if err != nil {
			return ship.AttachmentFile{}, err
		}
	}
	if !s.media.on() {
		return ship.AttachmentFile{}, errx.Business("media storage is off (SEAPORT_SECRET_KEY)")
	}
	r, err := s.media.blobs.Get(ctx, a.Key)
	if err != nil {
		return ship.AttachmentFile{}, errx.Wrap(err, "read attachment", errx.TypeInternal)
	}
	defer func() { _ = r.Close() }()
	sealed, err := io.ReadAll(io.LimitReader(r, a.Size+1024))
	if err != nil {
		return ship.AttachmentFile{}, errx.Wrap(err, "read attachment", errx.TypeExternal)
	}
	data, err := s.media.sealer.Open(ship.AttachmentPurpose(org, a.ID), sealed)
	if err != nil {
		return ship.AttachmentFile{}, errx.Wrap(err, "open attachment (sealed with another key?)", errx.TypeInternal)
	}
	return ship.AttachmentFile{Attachment: a, Data: data}, nil
}

func (s *Service) SweepTrash(ctx context.Context, now time.Time) (int, error) {
	if s.media.blobs == nil {
		return 0, nil
	}
	keys, err := s.repo.DueTrash(ctx, now, ship.MaxTrashSweep)
	if err != nil {
		return 0, errx.Wrap(err, "due blobs", errx.TypeInternal)
	}
	gone := keys[:0]
	for _, k := range keys {
		err := s.media.blobs.Delete(ctx, k)
		if err != nil && !errx.IsType(err, errx.TypeNotFound) {
			slog.Warn("delete blob; retrying later", "err", err)
			continue
		}
		gone = append(gone, k)
	}
	if len(gone) == 0 {
		return 0, nil
	}
	if err := s.repo.DropTrash(ctx, gone, now); err != nil {
		return 0, errx.Wrap(err, "forget deleted blobs", errx.TypeInternal)
	}
	return len(gone), nil
}
