package ship

import (
	"context"
	"errors"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// AttachmentKind says how an attachment is shown and handed to agents.
// A message is text plus attachments: channels normalize to that, nothing
// downstream branches on a channel's message type.
type AttachmentKind string

const AttachmentImage AttachmentKind = "image"

const (
	// MaxImage bounds an image's size (3.75 MiB): its base64 fits the 5 MB
	// the models agents use take. Larger ones aren't stored (a note says
	// one came).
	MaxImage = 15 << 18
	// MaxAttachments bounds the attachments of one message.
	MaxAttachments = 10
	// MaxAttachmentName bounds an attachment's display name.
	MaxAttachmentName = 200
	// TrashGrace: a blob staged for an attachment whose row never came
	// (the port stopped mid-arrival, a duplicate) is deleted after it.
	TrashGrace = time.Hour
	// MaxTrashSweep bounds the blobs one sweep deletes.
	MaxTrashSweep = 100
)

// ImageTypes are the image formats stored (and handed to agents), sniffed
// from the bytes (blob.ImageType).
var ImageTypes = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

// Attachment is a file that came with a ship. Its bytes are sealed in blob
// storage; the key never leaves the port.
type Attachment struct {
	ID           identity.AttachmentID   `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Ship         identity.ShipID         `db:"ship_id" json:"-"`
	Position     int                     `db:"position" json:"-"`
	Kind         AttachmentKind          `db:"kind" json:"kind"`
	MIME         string                  `db:"mime" json:"mime"`
	Size         int64                   `db:"size" json:"size"`
	Name         string                  `db:"name" json:"name,omitempty"`
	SHA256       string                  `db:"sha256" json:"sha256"`
	Key          string                  `db:"blob_key" json:"-"`
}

// Media is a file arriving with an inbound message: its bytes (at most
// MaxImage) in Data, or Fetch to download them, which the service calls
// only for a message it keeps (not a duplicate, not from a denied chat or
// sender). The service checks the type from the bytes (blob.ImageType
// with ImageTypes); another type becomes a note.
type Media struct {
	Kind AttachmentKind
	// Name is the file name the channel gave, display only.
	Name string
	Data []byte
	// Fetch downloads the bytes now (a channel whose URLs expire calls it
	// within the arrival); it returns ErrMediaTooLarge past MaxImage
	// without reading further.
	Fetch func(context.Context) ([]byte, error)
}

// ErrMediaTooLarge is what Media.Fetch returns (wrapped or not) for a
// file over MaxImage.
var ErrMediaTooLarge = errors.New("media over the size limit")

func (m Media) Validate() error {
	if m.Kind != AttachmentImage {
		return errx.InvalidField("media.kind", "only images are supported")
	}
	if (m.Data == nil) == (m.Fetch == nil) {
		return errx.InvalidField("media.data", "media needs its bytes or a way to fetch them")
	}
	if m.Data != nil {
		if err := CheckMediaSize(m.Data); err != nil {
			return err
		}
	}
	return text.Optional("media.name", m.Name, MaxAttachmentName)
}

// CheckMediaSize refuses empty media and media over MaxImage.
func CheckMediaSize(data []byte) error {
	if len(data) == 0 {
		return errx.InvalidField("media.data", "media is empty")
	}
	if len(data) > MaxImage {
		return errx.InvalidField("media.data", "image is too large (max 3.75 MiB)")
	}
	return nil
}

// MediaNote stands in the text for media that couldn't be kept, so the
// agent and the owner know it arrived.
func MediaNote(kind AttachmentKind, why string) string {
	return "[" + string(kind) + " received; " + why + "]"
}

// AttachmentPurpose binds an attachment's sealed bytes to it: a blob
// copied under another attachment (or organization) doesn't open.
func AttachmentPurpose(organization identity.OrganizationID, id identity.AttachmentID) string {
	return "attachment/" + organization.String() + "/" + id.String()
}

// AttachmentFile is an attachment's bytes, opened for a reader allowed to
// see its ship.
type AttachmentFile struct {
	Attachment
	Data []byte
}

// OpenAttachment asks for an attachment's bytes. Agent, when set, is the
// reader's agent: it sees only attachments of conversations on its docks.
type OpenAttachment struct {
	ID    identity.AttachmentID
	Agent *identity.AgentID
}

func (o OpenAttachment) Validate() error {
	if o.ID.IsZero() {
		return errx.InvalidField("attachment_id", "attachment_id is required")
	}
	return nil
}
