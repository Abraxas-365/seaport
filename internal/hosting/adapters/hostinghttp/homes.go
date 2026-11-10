package hostinghttp

import (
	"net/http"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
)

// Harness homes: POST /api/v1/homes?harness=… takes a tar (or tar.gz) of
// the home's files and answers its manifest; a spec names it by digest.

func (h *Handler) uploadHome(w http.ResponseWriter, r *http.Request) error {
	p, err := httpx.Caller(r)
	if err != nil {
		return err
	}
	body, err := httpx.ReadBody(r, hosting.MaxHomeUpload, "application/x-tar", "application/gzip")
	if err != nil {
		return err
	}
	home, err := h.commands.UploadHome(r.Context(), p.Organization, hosting.UploadHome{
		Harness: r.URL.Query().Get("harness"), Archive: body, CreatedBy: by(p)})
	if err != nil {
		return err
	}
	return httpx.Created(w, r, home.Digest, home)
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	home, err := h.queries.Home(r.Context(), org, r.PathValue("digest"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, home)
}

func (h *Handler) homeArchive(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	tarball, err := h.queries.HomeArchive(r.Context(), org, r.PathValue("digest"))
	if err != nil {
		return err
	}
	return httpx.File(w, "application/x-tar", tarball)
}

// homeFile answers one file of a home as text when it is UTF-8 (the web
// shows it), else as bytes; sandboxed like every stored file.
func (h *Handler) homeFile(w http.ResponseWriter, r *http.Request) error {
	org, err := httpx.Organization(r)
	if err != nil {
		return err
	}
	data, err := h.queries.HomeFile(r.Context(), org, r.PathValue("digest"), r.PathValue("path"))
	if err != nil {
		return err
	}
	ct := "application/octet-stream"
	if utf8.Valid(data) {
		ct = "text/plain; charset=utf-8"
	}
	return httpx.File(w, ct, data)
}
