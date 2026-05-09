// Package server wires the fettle UI's HTTP routes. The server reads
// the project directory directly off disk on each request — there's no
// in-memory state beyond the configured project root.
package server

import (
	"io/fs"
	"net/http"

	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/ui/static"
	"github.com/go-chi/chi/v5"

	// Load-bearing blank import: triggers the parent ui package's
	// init(), which copies the asset hashes from the static package
	// into templates.CSSVersion / templates.JSVersion. Without this,
	// the rendered <link>/<script> tags would lose their cache-bust
	// query string on the first request.
	_ "github.com/contiamo/fettle/internal/ui"
)

// Handler is the chi router with all routes registered. Construct via
// New; pass directly to http.Serve.
type Handler = chi.Router

// New returns a router serving the fettle UI for the given project
// directory. The project config is taken once at construction time;
// run folders are read from disk per-request, so adding/removing runs
// while the server is up is reflected on the next page load.
func New(projectDir string, cfg project.Config) Handler {
	_ = cfg // reserved for future routes that read .fettle/config.json fields

	r := chi.NewRouter()

	// Resolve the active identity once per request and stash it in
	// the context so Layout can render the "Reviewing as: <slug>"
	// indicator without every handler threading it through.
	r.Use(withReviewer)

	r.Get("/static/dist/*", staticHandler())
	r.Get("/", runsHandler(projectDir))
	r.Get("/runs/{name}", runHandler(projectDir))
	r.Get("/runs/{name}/finding/{id}", findingHandler(projectDir))

	r.Get("/identity", identityHandler(projectDir))
	r.Post("/identity", identitySaveHandler(projectDir))

	r.Post("/runs/{name}/finding/{id}/review", findingReviewHandler(projectDir))
	r.Post("/runs/{name}/finding/{id}/outcome", findingOutcomeHandler(projectDir))
	r.Post("/runs/{name}/bulk/review", bulkReviewHandler(projectDir))

	// Quietly 404 favicon requests. We don't ship one yet; without a
	// dedicated route, chi would 404 anyway, but this stops console
	// noise on first load.
	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return r
}

// staticHandler serves files from the embedded dist FS at /static/dist/*.
// We strip the leading /static/ so the FS sub-tree (rooted at "dist")
// matches the request path.
func staticHandler() http.HandlerFunc {
	sub, err := fs.Sub(static.FS, ".")
	if err != nil {
		// Should never happen — static.FS is a known-good embed.FS.
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/static/", fileServer).ServeHTTP(w, r)
	}
}
