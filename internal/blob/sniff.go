package blob

import (
	"net/http"
	"slices"
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
