// Package static embeds the built CSS, JS, and templui per-component
// scripts that ship with the fettle UI. The dist/ subtree is populated
// by `task go:tailwind` and `task ts:build`; templui adds its own
// per-component JS into dist/js/ via the templui CLI.
package static

import (
	"crypto/sha256"
	"embed"
	"fmt"
)

//go:embed all:dist
var FS embed.FS

// CSSHash is a short content hash of styles.css for cache busting.
// Computed once at init from the embedded file.
var CSSHash string

// JSHash is a short content hash of app.js for cache busting.
var JSHash string

func init() {
	CSSHash = assetHash("dist/styles.css")
	JSHash = assetHash("dist/app.js")
}

// assetHash reads name from FS and returns the first 4 bytes of its
// SHA-256 hash as a hex string. Returns "" if the file cannot be read
// (e.g. on a fresh clone before tailwind/esbuild have run).
func assetHash(name string) string {
	data, err := FS.ReadFile(name)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:4])
}
