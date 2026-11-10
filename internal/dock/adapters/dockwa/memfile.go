package dockwa

import (
	"errors"
	"io"
	"io/fs"
	"time"
)

// errFileFull: a download wrote past the file's limit.
var errFileFull = errors.New("download over the size limit")

// memFile is a whatsmeow.File in memory that refuses to grow past max,
// so a download is bounded by what Seaport keeps, whatever size the
// sender declared (whatsmeow's Download reads the whole body first).
type memFile struct {
	buf []byte
	pos int64
	max int64
}

func newMemFile(max int64) *memFile { return &memFile{max: max} }

func (f *memFile) Bytes() []byte { return f.buf }

func (f *memFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= int64(len(f.buf)) {
		return 0, io.EOF
	}
	n := copy(p, f.buf[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) Write(p []byte) (int, error) {
	n, err := f.WriteAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	end := off + int64(len(p))
	if end > f.max {
		return 0, errFileFull
	}
	if end > int64(len(f.buf)) {
		f.buf = append(f.buf, make([]byte, end-int64(len(f.buf)))...)
	}
	return copy(f.buf[off:], p), nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += int64(len(f.buf))
	default:
		return 0, errors.New("bad whence")
	}
	if offset < 0 {
		return 0, errors.New("negative position")
	}
	f.pos = offset
	return offset, nil
}

func (f *memFile) Truncate(size int64) error {
	switch {
	case size < 0:
		return errors.New("negative size")
	case size > f.max:
		return errFileFull
	case size <= int64(len(f.buf)):
		f.buf = f.buf[:size]
	default:
		f.buf = append(f.buf, make([]byte, size-int64(len(f.buf)))...)
	}
	return nil
}

func (f *memFile) Stat() (fs.FileInfo, error) { return memInfo{size: int64(len(f.buf))}, nil }

type memInfo struct{ size int64 }

func (i memInfo) Name() string       { return "media" }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0o600 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }
