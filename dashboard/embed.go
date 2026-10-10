package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// NewStaticHandler returns an http.Handler that serves the embedded static
// assets at the given URL prefix. The assets are served with the content type
// that matches the file extension, and the handler only serves files directly
// under the static/ directory (no path traversal).
func NewStaticHandler(prefix string) http.Handler {
	fsys, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Should never happen at build time.
		panic(err)
	}
	h := http.FileServer(http.FS(fsys))
	return http.StripPrefix(prefix, h)
}
