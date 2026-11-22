// Package web holds the browser assets (CSS, JS, PWA manifest, service
// worker) embedded into the binary. Go code for pages lives in internal/web.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static is the static/ folder, served at /static/.
var Static, _ = fs.Sub(files, "static")
