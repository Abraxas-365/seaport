package gocloudblob

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/blob"
	"github.com/Abraxas-365/seaport/internal/errx"
)

func stores(t *testing.T) map[string]*Store {
	t.Helper()
	out := map[string]*Store{}
	for name, u := range map[string]string{
		"mem":  "mem://",
		"file": "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "blobs")) + "?create_dir=1&no_tmp_dir=1",
	} {
		s, err := Open(context.Background(), u)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		out[name] = s
	}
	return out
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			key := "org/o1/attachments/a1"
			if err := s.Put(ctx, key, strings.NewReader("hello"), blob.PutOptions{ContentType: "application/octet-stream"}); err != nil {
				t.Fatal(err)
			}
			r, err := s.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			_ = r.Close()
			if string(got) != "hello" {
				t.Fatalf("got %q", got)
			}
			// Immutable: a second Put conflicts and keeps the first bytes.
			err = s.Put(ctx, key, strings.NewReader("other"), blob.PutOptions{ContentType: "text/plain"})
			if !errx.IsType(err, errx.TypeConflict) {
				t.Fatalf("second put: %v", err)
			}
			if err := s.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, key); !errx.IsType(err, errx.TypeNotFound) {
				t.Fatalf("get deleted: %v", err)
			}
			if err := s.Delete(ctx, key); !errx.IsType(err, errx.TypeNotFound) {
				t.Fatalf("delete missing: %v", err)
			}
		})
	}
}

func TestStoreRefusesBadKeys(t *testing.T) {
	ctx := context.Background()
	s := stores(t)["file"]
	for _, key := range []string{"", "/abs", "a/../b", "a//b", "a/", "a\\b"} {
		if err := s.Put(ctx, key, strings.NewReader("x"), blob.PutOptions{ContentType: "text/plain"}); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("key %q: %v", key, err)
		}
	}
	if err := s.Put(ctx, "k", strings.NewReader("x"), blob.PutOptions{}); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("no content type: %v", err)
	}
}

// Concurrent Puts of one key on disk: exactly one wins (fileblob alone
// lets both win).
func TestFilePutIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := stores(t)["file"]
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(ctx, "race", strings.NewReader("x"), blob.PutOptions{ContentType: "text/plain"}); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d puts won", won)
	}
}

func TestOpenRefusesUnknownScheme(t *testing.T) {
	if _, err := Open(context.Background(), "nope://x"); err == nil {
		t.Fatal("opened an unknown scheme")
	}
}
