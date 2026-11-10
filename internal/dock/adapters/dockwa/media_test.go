package dockwa

import (
	"context"
	"errors"
	"testing"

	"github.com/Abraxas-365/seaport/internal/ship"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

type downloader = func(context.Context, whatsmeow.DownloadableMessage, whatsmeow.File) error

func TestImageMediaFetchesWhenAsked(t *testing.T) {
	ctx := context.Background()
	data := []byte("\x89PNG\r\n\x1a\nimage")
	calls := 0
	ok := func(_ context.Context, _ whatsmeow.DownloadableMessage, f whatsmeow.File) error {
		calls++
		_, err := f.Write(data)
		return err
	}
	fail := func(context.Context, whatsmeow.DownloadableMessage, whatsmeow.File) error { return errors.New("gone") }
	never := func(context.Context, whatsmeow.DownloadableMessage, whatsmeow.File) error {
		t.Fatal("downloaded an image declared over the limit")
		return nil
	}
	// A sender declaring a small size for a large file: the download stops
	// at the limit instead of reading it all.
	liar := func(_ context.Context, _ whatsmeow.DownloadableMessage, f whatsmeow.File) error {
		chunk := make([]byte, 64<<10)
		for range 1 << 10 { // 64 MiB if nothing stopped it
			if _, err := f.Write(chunk); err != nil {
				return err
			}
		}
		return nil
	}

	m, text := imageMedia(&waE2E.ImageMessage{Caption: proto.String("mira")}, ok)
	if m == nil || m.Fetch == nil || m.Data != nil || m.Kind != ship.AttachmentImage || text != "mira" || calls != 0 {
		t.Fatalf("image: %+v %q (downloads %d: none until fetched)", m, text, calls)
	}
	if got, err := m.Fetch(ctx); err != nil || string(got) != string(data) || calls != 1 {
		t.Fatalf("fetch: %q %v", got, err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	m, _ = imageMedia(&waE2E.ImageMessage{}, fail)
	if _, err := m.Fetch(ctx); err == nil || errors.Is(err, ship.ErrMediaTooLarge) {
		t.Fatalf("failed download: %v", err)
	}
	m, _ = imageMedia(&waE2E.ImageMessage{FileLength: proto.Uint64(10)}, liar)
	if _, err := m.Fetch(ctx); !errors.Is(err, ship.ErrMediaTooLarge) {
		t.Fatalf("lying size: %v", err)
	}

	m, text = imageMedia(&waE2E.ImageMessage{Caption: proto.String("big"), FileLength: proto.Uint64(ship.MaxImage + 1)}, downloader(never))
	if m != nil || text != "big\n"+ship.MediaNote(ship.AttachmentImage, "too large to keep") {
		t.Fatalf("large image: %+v %q", m, text)
	}
}

// memFile behaves like the os.File whatsmeow decrypts in place: write,
// read back at offsets, truncate the MAC off, stat the size.
func TestMemFile(t *testing.T) {
	f := newMemFile(16)
	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	mac := make([]byte, 2)
	if _, err := f.ReadAt(mac, 8); err != nil || string(mac) != "89" {
		t.Fatalf("read at: %q %v", mac, err)
	}
	if err := f.Truncate(8); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Stat(); st.Size() != 8 {
		t.Fatalf("size %d", st.Size())
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := f.Read(buf); err != nil || string(buf) != "0123" {
		t.Fatalf("read: %q %v", buf, err)
	}
	if _, err := f.WriteAt([]byte("ab"), 0); err != nil || string(f.Bytes()) != "ab234567" {
		t.Fatalf("write at: %q %v", f.Bytes(), err)
	}
	if _, err := f.WriteAt(make([]byte, 9), 8); !errors.Is(err, errFileFull) {
		t.Fatalf("past the limit: %v", err)
	}
}
