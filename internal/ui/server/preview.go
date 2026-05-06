package server

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/contiamo/fettle/internal/anchor"
	"github.com/contiamo/fettle/internal/schema"
)

// previewContext is the rendered ±N-line window around a finding's
// anchor line. The window is centred on EffectiveLine (which equals
// OriginalLine when the anchor hasn't drifted).
type previewContext struct {
	Path          string // repo-relative path that was actually read
	Lines         []previewLine
	Error         string // non-empty if the preview couldn't be produced (template renders a placeholder)
	Anchor        anchor.State
	OriginalLine  int // f.Line as recorded on the finding
	EffectiveLine int // resolved current line; 0 when stale
}

type previewLine struct {
	Number    int
	Content   string
	Highlight bool
}

// loadPreview returns up to (2*window+1) lines of context around the
// finding's effective line in repoRoot. repoRoot is the directory the
// file is read relative to (the run's resolved target_repo).
//
// The returned previewContext also carries the anchor drift verdict
// (Anchor / OriginalLine / EffectiveLine) so the template can surface
// shifted / ambiguous / stale states transparently. When the finding
// has no AnchorLine (legacy data), Anchor is StateUnknown and the
// window centres on the originally-recorded line, matching the
// pre-anchor behaviour.
//
// Errors are surfaced via Error rather than returned, so the template
// can render a placeholder instead of failing the whole detail page.
// Path traversal is rejected at this layer — `..` segments or absolute
// file paths in f.File produce a non-empty Error and no Lines.
func loadPreview(repoRoot string, f schema.Finding, window int) previewContext {
	pc := previewContext{
		Path:          f.File,
		OriginalLine:  f.Line,
		EffectiveLine: f.Line,
	}
	if repoRoot == "" {
		pc.Error = "code preview unavailable: this run has no target_repo on its manifest"
		return pc
	}

	abs, err := safeJoin(repoRoot, f.File)
	if err != nil {
		pc.Error = fmt.Sprintf("code preview unavailable: %v", err)
		return pc
	}

	file, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			pc.Error = fmt.Sprintf("file not found in target_repo: %s", f.File)
		} else {
			pc.Error = fmt.Sprintf("read file: %v", err)
		}
		return pc
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	// Some source files have very long generated lines (vendored single-line
	// minified JS, long comment URLs); 1MiB matches the scanner buffer the
	// rest of the harness uses for JSONL.
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	// bufio.ErrTooLong means we hit a line longer than 1MiB and the
	// scanner stopped. We keep what we read so the preview still works
	// for findings that live before the bad line — that mirrors the
	// pre-anchor behaviour of streaming a window and breaking out
	// early. Other scanner errors (I/O) are still hard failures.
	truncated := false
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			truncated = true
		} else {
			pc.Error = fmt.Sprintf("scan file: %v", err)
			return pc
		}
	}

	res := anchor.ApplyTruncationDemotion(anchor.ResolveFromLines(lines, f), truncated)
	pc.Anchor = res.State
	pc.OriginalLine = res.OriginalLine
	pc.EffectiveLine = res.EffectiveLine

	// Centre the window on the resolved line when we know where the
	// anchor moved to; otherwise centre on the original line. For Stale,
	// EffectiveLine is 0 — fall back to OriginalLine so the user can
	// still see the surrounding context, with no row highlighted.
	target := res.OriginalLine
	if res.State == anchor.StateShifted || res.State == anchor.StateAmbiguous {
		target = res.EffectiveLine
	}
	if target < 1 || target > len(lines) {
		// Either the anchor is gone and the original line is past EOF,
		// or there's no anchor at all and the file shrank below f.Line.
		// Either way, no usable window — match the pre-anchor UX of
		// reporting the line/total mismatch explicitly.
		pc.Error = fmt.Sprintf("file has %d lines; finding refers to line %d", len(lines), res.OriginalLine)
		return pc
	}

	startLine := target - window
	if startLine < 1 {
		startLine = 1
	}
	endLine := target + window
	if endLine > len(lines) {
		endLine = len(lines)
	}

	// Stale findings get no highlight: the anchored content is gone, so
	// no current line legitimately represents the finding's target.
	highlight := res.EffectiveLine
	if res.State == anchor.StateStale {
		highlight = 0
	}
	for i := startLine; i <= endLine; i++ {
		pc.Lines = append(pc.Lines, previewLine{
			Number:    i,
			Content:   lines[i-1],
			Highlight: i == highlight,
		})
	}
	return pc
}

// safeJoin returns filepath.Join(root, rel) only when the resolved
// path stays within root. Rejects absolute rel, parent traversal, or
// any case where filepath.Rel reports a path that escapes the root.
//
// Symlinks are not followed for the containment check (we want to
// allow legitimate symlinks inside the repo); for stricter
// sandboxing the caller would need to filepath.EvalSymlinks both
// sides. The fettle UI is local-only and trusts the user's own
// target_repo, so traversal-via-`..`-in-finding-file is the only
// concern here.
func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("file path is absolute: %q", rel)
	}
	cleaned := filepath.Clean(rel)
	if escapesRoot(cleaned) {
		return "", fmt.Errorf("file path escapes target_repo: %q", rel)
	}
	abs := filepath.Join(root, cleaned)
	relCheck, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if escapesRoot(relCheck) {
		return "", fmt.Errorf("file path escapes target_repo: %q", rel)
	}
	return abs, nil
}

// escapesRoot reports whether a cleaned (filepath.Clean'd or
// filepath.Rel-derived) path begins with a `..` segment. We have to
// match `..` exactly or `..` followed by a separator — `strings.HasPrefix(p, "..")`
// alone would falsely flag legitimate files like `..foo` or
// `...config`. macOS and Linux use `/`; the separator-aware form
// also handles Windows backslashes for free.
func escapesRoot(cleaned string) bool {
	if cleaned == ".." {
		return true
	}
	return strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
