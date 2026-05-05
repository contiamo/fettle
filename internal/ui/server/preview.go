package server

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// previewContext is the rendered ±N-line window around a finding's
// target line. PreviewLine.Line is the 1-based line number; Highlight
// marks the row that the finding points at so the template can
// emphasize it.
type previewContext struct {
	Path  string // repo-relative path that was actually read
	Lines []previewLine
	Error string // non-empty if the preview couldn't be produced (template renders a placeholder)
}

type previewLine struct {
	Number    int
	Content   string
	Highlight bool
}

// loadPreview returns up to (2*window+1) lines of context around
// targetLine in repoRelPath. repoRoot is the directory the file is
// read relative to (the run's resolved target_repo). targetLine is
// 1-based.
//
// Returns a previewContext with Error set instead of an error so the
// template can render a placeholder; we never fail the whole detail
// page just because the preview is unavailable. Path traversal is
// rejected at this layer — `..` segments or absolute file paths in
// repoRelPath produce a non-empty Error and no Lines.
func loadPreview(repoRoot, repoRelPath string, targetLine, window int) previewContext {
	pc := previewContext{Path: repoRelPath}
	if repoRoot == "" {
		pc.Error = "code preview unavailable: this run has no target_repo on its manifest"
		return pc
	}

	abs, err := safeJoin(repoRoot, repoRelPath)
	if err != nil {
		pc.Error = fmt.Sprintf("code preview unavailable: %v", err)
		return pc
	}

	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			pc.Error = fmt.Sprintf("file not found in target_repo: %s", repoRelPath)
		} else {
			pc.Error = fmt.Sprintf("read file: %v", err)
		}
		return pc
	}
	defer f.Close()

	startLine := targetLine - window
	if startLine < 1 {
		startLine = 1
	}
	endLine := targetLine + window

	sc := bufio.NewScanner(f)
	// Some source files have very long generated lines (vendored single-line
	// minified JS, long comment URLs); 1MiB matches the scanner buffer the
	// rest of the harness uses for JSONL.
	sc.Buffer(make([]byte, 1<<16), 1<<20)

	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		pc.Lines = append(pc.Lines, previewLine{
			Number:    lineNum,
			Content:   sc.Text(),
			Highlight: lineNum == targetLine,
		})
	}
	if err := sc.Err(); err != nil {
		pc.Error = fmt.Sprintf("scan file: %v", err)
		pc.Lines = nil
		return pc
	}
	// lineNum is now the file's total line count. The window may have
	// returned some lines even when targetLine > total — e.g. 10-line
	// file, target 12, window 6 reads lines 6-10 with no highlight.
	// That's confusing UX, so report the mismatch explicitly.
	if targetLine > lineNum {
		pc.Lines = nil
		pc.Error = fmt.Sprintf("file has %d lines; finding refers to line %d", lineNum, targetLine)
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
