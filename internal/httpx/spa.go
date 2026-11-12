package httpx

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/gofiber/fiber/v3"
)

// spaCSP: the UI loads only its own scripts; inline style attributes are
// allowed (Radix positions popovers with them).
const spaCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'%s; object-src 'none'"

// SPA serves a built single-page app from fsys: existing files as they are
// (hashed /assets/* cached forever, everything else revalidated), and
// index.html for any other path without an extension so client-side
// routes survive a reload. Missing files with an extension are 404s, and so
// is any method but GET and HEAD. Files are served whole (no Range: the
// bundle is small and has no media).
// formActions are origins besides its own the UI may post forms to (the
// GitHub App manifest goes to GitHub as a form).
func SPA(fsys fs.FS, formActions ...string) fiber.Handler {
	extra := ""
	for _, o := range formActions {
		extra += " " + o
	}
	csp := fmt.Sprintf(spaCSP, extra)
	return func(c fiber.Ctx) error {
		if m := c.Method(); m != fiber.MethodGet && m != fiber.MethodHead {
			return errx.NotFound("route not found")
		}
		p := c.Path()
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
		name := strings.TrimPrefix(path.Clean("/"+p), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			if path.Ext(name) != "" && name != "index.html" {
				return errx.NotFound("route not found")
			}
			name = "index.html"
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return errx.NotFound("the web UI is not built (cd web && pnpm build)")
		}
		if strings.HasPrefix(name, "assets/") {
			c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
		} else {
			c.Set(fiber.HeaderCacheControl, "no-cache")
		}
		if name == "index.html" {
			c.Set("Content-Security-Policy", csp)
			c.Set("X-Frame-Options", "DENY")
		}
		ctype := mime.TypeByExtension(path.Ext(name))
		if ctype == "" {
			ctype = http.DetectContentType(data)
		}
		c.Set(fiber.HeaderContentType, ctype)
		return c.Send(data)
	}
}
