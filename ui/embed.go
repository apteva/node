// Package ui embeds the built device UI (ui/dist, produced by `bun run build`).
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
