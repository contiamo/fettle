package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/ui/templates"
)

// withReviewer resolves the active identity once per request and
// stuffs the result into the request context, so Layout (and any
// other template that wants to surface "Reviewing as: <slug>") can
// read it without every handler having to thread it through manually.
// Also stashes the request URI so the header indicator's
// "/identity?next=<here>" link bounces the user back to their
// current page after saving.
//
// ErrNoIdentity is non-fatal — the indicator just renders as a
// "Set identity" link in that case. Other I/O errors (a config file
// that exists but can't be read for permission reasons) are also
// swallowed: rendering the chrome is not worth a 500. The mutation
// handlers re-call identity.Resolve directly and surface those
// errors there, where they actually matter.
func withReviewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), templates.RequestURIContextKey, r.URL.RequestURI())
		res, err := identity.Resolve()
		if err == nil {
			ctx = context.WithValue(ctx, templates.ReviewerContextKey, &res)
		} else if !errors.Is(err, identity.ErrNoIdentity) {
			// Other I/O errors are silently dropped here; mutation
			// handlers surface them where they're load-bearing.
			_ = err
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
