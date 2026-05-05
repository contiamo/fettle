// Package ui owns the wiring between the static-asset hashes
// (computed by internal/ui/static) and the templ templates that embed
// them as ?v=<hash> cache busters.
//
// This package's init() is load-bearing: any package serving HTML
// pages must blank-import "github.com/contiamo/fettle/internal/ui"
// (typically via the server package) so the version vars are
// populated before the first request.
package ui

import (
	"github.com/contiamo/fettle/internal/ui/static"
	"github.com/contiamo/fettle/internal/ui/templates"
)

func init() {
	templates.CSSVersion = static.CSSHash
	templates.JSVersion = static.JSHash
}
