package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/ui/templates"
)

// identityHandler renders the first-edit identity prompt form. The
// form prefills the slug input from `git config user.name` (taken
// from the project directory) or, failing that, $USER. An optional
// ?next= URL is bounced back through the form so the user lands back
// on the page they were trying to mutate after saving.
func identityHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next := safeNextParam(r.URL.Query().Get("next"))
		prefill := suggestedSlug(projectDir)

		current, err := identity.Resolve()
		var currentSlug string
		if err == nil {
			currentSlug = current.Slug
		} else if !errors.Is(err, identity.ErrNoIdentity) {
			http.Error(w, fmt.Sprintf("read identity: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		view := templates.IdentityView{
			Next:        next,
			Prefill:     prefill,
			CurrentSlug: currentSlug,
		}
		if err := templates.Identity(view).Render(r.Context(), w); err != nil {
			fmt.Fprintf(os.Stderr, "fettle ui: render identity: %v\n", err)
		}
	}
}

// identitySaveHandler persists the submitted slug and redirects back
// to the original page. Validation errors render the form again with
// the error inline rather than 400ing — first-edit is the user's
// first interaction with the UI and a hard error here is unfriendly.
func identitySaveHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form", http.StatusBadRequest)
			return
		}
		slug := strings.TrimSpace(r.FormValue("slug"))
		next := safeNextParam(r.FormValue("next"))

		if err := identity.ValidateSlug(slug); err != nil {
			view := templates.IdentityView{
				Next:    next,
				Prefill: slug,
				Error:   err.Error(),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = templates.Identity(view).Render(r.Context(), w)
			return
		}
		if err := identity.Save(slug); err != nil {
			view := templates.IdentityView{
				Next:    next,
				Prefill: slug,
				Error:   err.Error(),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = templates.Identity(view).Render(r.Context(), w)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	}
}

// requireIdentity is the gate every mutation handler runs first. On
// success it returns the resolved identity. On no-identity it sends
// the client to the identity prompt — using HX-Redirect for HTMX
// submissions so the form doesn't try to replace the page chunk with
// the identity page's HTML — and reports false so the handler stops.
func requireIdentity(w http.ResponseWriter, r *http.Request) (identity.Resolved, bool) {
	res, err := identity.Resolve()
	if err == nil {
		return res, true
	}
	if !errors.Is(err, identity.ErrNoIdentity) {
		http.Error(w, fmt.Sprintf("resolve identity: %v", err), http.StatusInternalServerError)
		return identity.Resolved{}, false
	}
	target := "/identity?next=" + url.QueryEscape(r.URL.RequestURI())
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
	return identity.Resolved{}, false
}

// safeNextParam validates that ?next= is a relative path inside the
// app — open redirect to an external host would be a phishing vector
// even on a local-only server (a malicious file:// or javascript:
// URL pasted in chat can land here as a redirect target).
func safeNextParam(s string) string {
	if s == "" {
		return "/"
	}
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return "/"
	}
	if _, err := url.Parse(s); err != nil {
		return "/"
	}
	return s
}

// suggestedSlug picks a sensible prefill for the identity input.
// Tries `git config user.name` from the project directory first, then
// $USER. Empty string if neither is available. Spaces in git names
// (the common case: "Michael Dietze") get collapsed to dashes since
// the slug must satisfy the [A-Za-z0-9_-]+ filename pattern.
func suggestedSlug(projectDir string) string {
	if name := tryGitName(projectDir); name != "" {
		return slugify(name)
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return slugify(u.Username)
	}
	return ""
}

func tryGitName(dir string) string {
	cmd := exec.Command("git", "config", "user.name")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// slugify turns "Michael Dietze" into "Michael-Dietze". Punctuation
// outside the allowed class is dropped (not replaced) to keep the
// suggestion stable when a stray comma or apostrophe sneaks in.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
