package blob

import (
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
)

var images = []string{"image/jpeg", "image/png", "image/webp", "image/gif"}

func TestImageTypeSniffsBytes(t *testing.T) {
	for want, data := range map[string][]byte{
		"image/png":  []byte("\x89PNG\r\n\x1a\n0000"),
		"image/jpeg": []byte("\xff\xd8\xff\xe0000"),
		"image/gif":  []byte("GIF89a000"),
		"image/webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"":           []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"),
	} {
		if got := ImageType(data, images); got != want {
			t.Errorf("%q: got %q, want %q", data, got, want)
		}
	}
	if got := ImageType([]byte("<html><script>"), images); got != "" {
		t.Errorf("html sniffed as %q", got)
	}
}

func TestTenantKeyIsValid(t *testing.T) {
	k := TenantKey(identity.MustParseOrganizationID("00000000-0000-0000-0000-000000000001"), "attachments", identity.NewAttachmentID().String())
	if err := ValidateKey(k); err != nil {
		t.Fatal(err)
	}
}
