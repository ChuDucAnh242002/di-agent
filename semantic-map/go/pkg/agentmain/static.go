package agentmain

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS embeds the pkg/agentmain/static directory at build time. The
// `all:` prefix is required so dot-prefixed files (e.g. .gitkeep, .well-known)
// are included — Go's default embed pattern silently skips them.
//
//go:embed all:static
var staticFS embed.FS

// staticHandler returns an http.Handler that serves the embedded static
// directory under /ui/. It strips the /ui/ prefix and chroots to the static
// subdirectory so /ui/index.html resolves to static/index.html.
//
// Phase 1 ships a placeholder.html stub here so the embed.FS build succeeds
// before Phase 2B writes the real index.html / app.js / style.css.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// embed guarantees the static subtree exists; this only fires on a
		// developer build with the directory deleted.
		panic("staticHandler: cannot open embedded static subtree: " + err.Error())
	}
	return http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
}
