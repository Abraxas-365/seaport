// Package web embeds the built admin UI (React SPA in this directory,
// built by `pnpm build` into dist/). Only the composition root imports it.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built UI rooted at dist/ (only .gitkeep before a build).
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees dist exists
	}
	return sub
}
