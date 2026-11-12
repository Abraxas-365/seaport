package hostinghttp

import (
	"net/http"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

// Harness homes: POST /api/v1/homes?harness=… takes a tar (or tar.gz) of
// the home's files and answers its manifest; a spec names it by digest.

func (h *Handler) uploadHome(c fiber.Ctx) error {
	p, err := httpx.Caller(c)
	if err != nil {
		return err
	}
	body, err := httpx.ReadBody(c, hosting.MaxHomeUpload, "application/x-tar", "application/gzip")
	if err != nil {
		return err
	}
	home, err := h.commands.UploadHome(c.Context(), p.Organization, hosting.UploadHome{
		Harness: c.Query("harness"), Archive: body, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.Created(c, home.Digest, home)
}

// homeRules answers what each harness's home may hold, so a client picks
// a dropped folder's files before uploading.
func (h *Handler) homeRules(c fiber.Ctx) error {
	return httpx.JSON(c, http.StatusOK, hosting.HomeRulesOf())
}

func (h *Handler) home(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	home, err := h.queries.Home(c.Context(), org, httpx.Param(c, "digest"))
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, home)
}

func (h *Handler) homeArchive(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	tarball, err := h.queries.HomeArchive(c.Context(), org, httpx.Param(c, "digest"))
	if err != nil {
		return err
	}
	return httpx.File(c, "application/x-tar", tarball)
}

// homeFile answers one file of a home as text when it is UTF-8 (the web
// shows it), else as bytes; sandboxed like every stored file.
func (h *Handler) homeFile(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	data, err := h.queries.HomeFile(c.Context(), org, httpx.Param(c, "digest"), httpx.Param(c, "*"))
	if err != nil {
		return err
	}
	ct := "application/octet-stream"
	if utf8.Valid(data) {
		ct = "text/plain; charset=utf-8"
	}
	return httpx.File(c, ct, data)
}
