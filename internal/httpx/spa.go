package httpx

import (
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// spaCSP: the UI loads only its own scripts; inline style attributes are
// allowed (Radix positions popovers with them).
const spaCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'%s; object-src 'none'"

// SPA serves a built single-page app from fsys: existing files as they are
// (hashed /assets/* cached forever, everything else revalidated), and
// index.html for any other path without an extension so client-side
// routes survive a reload. Missing files with an extension are 404s.
// formActions are origins besides its own the UI may post forms to (the
// GitHub App manifest goes to GitHub as a form).
func SPA(fsys fs.FS, formActions ...string) http.Handler {
	extra := ""
	for _, o := range formActions {
		extra += " " + o
	}
	csp := fmt.Sprintf(spaCSP, extra)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			if path.Ext(name) != "" && name != "index.html" {
				NotFound(w, r)
				return
			}
			name = "index.html"
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			WriteError(w, r, errx.NotFound("the web UI is not built (cd web && pnpm build)"))
			return
		}
		h := w.Header()
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		if name == "index.html" {
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Frame-Options", "DENY")
		}
		http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
	})
}
