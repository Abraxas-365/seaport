package blob

import (
	"net/http"
	"slices"
	"strings"
)

// ImageType sniffs an image from its bytes, never from a name or a
// channel's header: one of allowed, or "" when it is none of them.
func ImageType(data []byte, allowed []string) string {
	ct := http.DetectContentType(data)
	if slices.Contains(allowed, ct) {
		return ct
	}
	return ""
}

// FileType sniffs any file's type from its bytes for a download: a type
// a browser could render as a page (HTML, SVG, XML) or as an image is
// "application/octet-stream", so the file is only ever saved.
func FileType(data []byte) string {
	ct := http.DetectContentType(data)
	mt, _, _ := strings.Cut(ct, ";")
	switch {
	case mt == "text/plain", mt == "application/pdf", mt == "application/zip", mt == "application/x-gzip",
		mt == "application/json", mt == "video/mp4", mt == "audio/mpeg":
		return ct
	default:
		return "application/octet-stream"
	}
}
