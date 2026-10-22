// Package web embeds the compiled single-page application.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded frontend file system rooted at the build output.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return distFS
	}
	return sub
}

// Handler serves the embedded assets.
func Handler() http.Handler {
	return http.FileServer(http.FS(FS()))
}

// ServeIndex writes index.html for a client-side route.
func ServeIndex(c *gin.Context) {
	index, err := distFS.ReadFile("dist/index.html")
	if err != nil {
		c.String(http.StatusServiceUnavailable, "frontend is not built")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", index)
}

// IsAssetPath reports whether a request path points at a static asset rather
// than a client-side route.
func IsAssetPath(path string) bool {
	return strings.HasPrefix(path, "/assets/") ||
		strings.HasPrefix(path, "/favicon") ||
		strings.HasPrefix(path, "/static/")
}
