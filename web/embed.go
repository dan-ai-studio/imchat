package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built frontend. It is embedded so the shipped artifact is a
// single executable: no directory beside it except the data file.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
