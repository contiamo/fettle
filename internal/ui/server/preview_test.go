package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contiamo/fettle/internal/anchor"
	"github.com/contiamo/fettle/internal/schema"
)

func TestSafeJoin_traversalRejected(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"../etc/passwd",
		"../../etc/passwd",
		"foo/../../etc/passwd",
		"..",
		"/etc/passwd",
	} {
		if _, err := safeJoin(root, rel); err == nil {
			t.Errorf("safeJoin(%q, %q) accepted; want rejection", root, rel)
		}
	}
}

func TestSafeJoin_validPaths(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"foo.go",
		"sub/dir/file.go",
		"./relative.go",
		"..foo",          // legitimate file starting with "..", not a traversal
		"...config",      // same shape; common for dotfiles like ...editorconfig
		"sub/..foo.txt",  // nested file with leading ".." in basename
	} {
		got, err := safeJoin(root, rel)
		if err != nil {
			t.Errorf("safeJoin(%q): unexpected error: %v", rel, err)
			continue
		}
		if !strings.HasPrefix(got, root) {
			t.Errorf("safeJoin(%q) = %q, want prefix %q", rel, got, root)
		}
	}
}

// findingAt is a tiny constructor for tests that only care about the
// (file, line) anchor of a finding. AnchorLine empty → StateUnknown,
// which preserves the pre-anchor preview behaviour these tests assert.
func findingAt(file string, line int) schema.Finding {
	return schema.Finding{File: file, Line: line}
}

// TestLoadPreview_window verifies the ±window slice and the highlight
// flag land on the right line.
func TestLoadPreview_window(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "src.go")
	content := ""
	for i := 1; i <= 20; i++ {
		content += "line " + itoa(i) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pc := loadPreview(root, findingAt("src.go", 10), 3)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if len(pc.Lines) != 7 { // lines 7..13
		t.Fatalf("got %d lines, want 7", len(pc.Lines))
	}
	if pc.Lines[0].Number != 7 || pc.Lines[6].Number != 13 {
		t.Errorf("range = [%d..%d], want [7..13]", pc.Lines[0].Number, pc.Lines[6].Number)
	}
	highlights := 0
	for _, l := range pc.Lines {
		if l.Highlight {
			highlights++
			if l.Number != 10 {
				t.Errorf("highlight on line %d, want 10", l.Number)
			}
		}
	}
	if highlights != 1 {
		t.Errorf("got %d highlights, want 1", highlights)
	}
}

// TestLoadPreview_clampedAtTop verifies a finding on an early line
// gets a window that doesn't try to read negative line numbers.
func TestLoadPreview_clampedAtTop(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "src.go")
	content := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := loadPreview(root, findingAt("src.go", 1), 5)
	if pc.Error != "" {
		t.Fatalf("error: %s", pc.Error)
	}
	if pc.Lines[0].Number != 1 {
		t.Errorf("first line = %d, want 1", pc.Lines[0].Number)
	}
}

// TestLoadPreview_targetPastEOF — a legacy finding (no anchor)
// pointing at a line past the file's end (because the file shrank
// since the find run) gets a placeholder, not a misleading window of
// the file's tail with no highlight.
func TestLoadPreview_targetPastEOF(t *testing.T) {
	root := t.TempDir()
	// 10-line file
	content := ""
	for i := 1; i <= 10; i++ {
		content += "line " + itoa(i) + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := loadPreview(root, findingAt("src.go", 12), 6)
	if pc.Error == "" {
		t.Errorf("target past EOF: expected Error, got Lines=%v", pc.Lines)
	}
	if len(pc.Lines) != 0 {
		t.Errorf("target past EOF: expected no Lines, got %d", len(pc.Lines))
	}
}

// TestLoadPreview_missingFile sets Error rather than failing — the
// detail page renders a placeholder instead of returning 500.
func TestLoadPreview_missingFile(t *testing.T) {
	root := t.TempDir()
	pc := loadPreview(root, findingAt("nope.go", 5), 3)
	if pc.Error == "" {
		t.Error("expected non-empty Error for missing file")
	}
	if len(pc.Lines) != 0 {
		t.Errorf("got %d lines, want 0", len(pc.Lines))
	}
}

// TestLoadPreview_emptyTargetRepo handles merge/dedupe runs (no direct
// target_repo on the manifest) — caller passes "" and we render a
// placeholder.
func TestLoadPreview_emptyTargetRepo(t *testing.T) {
	pc := loadPreview("", findingAt("x.go", 5), 3)
	if pc.Error == "" {
		t.Error("expected non-empty Error for empty target_repo")
	}
}

// TestLoadPreview_anchorShifted — anchor still in file but at a new
// line: window is centred on the new line and the highlight follows.
func TestLoadPreview_anchorShifted(t *testing.T) {
	root := t.TempDir()
	// 5-line file; finding's anchor was originally at line 2 ("beta")
	// and after a 2-line insertion above it, the anchor lives at line 4.
	content := "INSERTED1\nINSERTED2\nalpha\nbeta\ngamma\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	beta := "beta"
	f := schema.Finding{File: "src.go", Line: 2, AnchorLine: &beta}
	pc := loadPreview(root, f, 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if pc.Anchor != anchor.StateShifted {
		t.Errorf("Anchor = %v, want Shifted", pc.Anchor)
	}
	if pc.EffectiveLine != 4 {
		t.Errorf("EffectiveLine = %d, want 4", pc.EffectiveLine)
	}
	if pc.OriginalLine != 2 {
		t.Errorf("OriginalLine = %d, want 2", pc.OriginalLine)
	}
	// Window of 1 around line 4 → lines 3,4,5; highlight on 4.
	if len(pc.Lines) != 3 || pc.Lines[0].Number != 3 || pc.Lines[2].Number != 5 {
		t.Errorf("window = %v, want lines 3..5", pc.Lines)
	}
	for _, l := range pc.Lines {
		if l.Highlight && l.Number != 4 {
			t.Errorf("highlight on line %d, want 4", l.Number)
		}
	}
}

// TestLoadPreview_overlongLineLater — a target near the top of a file
// renders correctly even when a later line exceeds the scanner buffer.
// Pre-anchor code achieved this by breaking out of the scan loop once
// the window was filled; the anchor-aware rewrite needs to soft-handle
// bufio.ErrTooLong instead of failing the whole preview.
func TestLoadPreview_overlongLineLater(t *testing.T) {
	root := t.TempDir()
	// 1.5 MiB single line exceeds the 1 MiB scanner buffer.
	overlong := strings.Repeat("x", 1<<20+1<<19)
	content := "alpha\nbeta\ngamma\n" + overlong + "\ndelta\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := loadPreview(root, findingAt("src.go", 2), 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if len(pc.Lines) != 3 || pc.Lines[0].Number != 1 || pc.Lines[2].Number != 3 {
		t.Errorf("window = %+v, want lines 1..3 around line 2", pc.Lines)
	}
}

// TestLoadPreview_overlongDemotesNonCurrent — when a file has an
// overlong line we can't fully scan, only StateCurrent is trustworthy.
// Stale, Shifted, and Ambiguous all demote to Unknown because the
// unread suffix may contain matches (or the still-current original
// line) that would change the verdict.
func TestLoadPreview_overlongDemotesNonCurrent(t *testing.T) {
	root := t.TempDir()
	overlong := strings.Repeat("x", 1<<20+1<<19)
	// "beta" anchor isn't in the readable prefix; the overlong line at
	// position 2 blocks the scanner before any line containing "beta"
	// could be confirmed missing.
	content := "alpha\n" + overlong + "\nbeta\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := "beta"
	f := schema.Finding{File: "src.go", Line: 1, AnchorLine: &missing}
	pc := loadPreview(root, f, 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if pc.Anchor != anchor.StateUnknown {
		t.Errorf("Anchor = %v; want Unknown on truncated read with no f.Line match", pc.Anchor)
	}
}

// TestLoadPreview_overlongDemotesShifted — a single match in the
// readable prefix that isn't at f.Line would normally be Shifted, but
// on a truncated read the unread suffix could contain a duplicate
// (turning the verdict into Ambiguous), or even the original line
// still intact (Current). Demote to Unknown.
func TestLoadPreview_overlongDemotesShifted(t *testing.T) {
	root := t.TempDir()
	overlong := strings.Repeat("x", 1<<20+1<<19)
	// f.Line = 2 is within the readable prefix so the window renders;
	// "foo" matches at line 1 only (so without truncation awareness
	// this is a clean Shifted), but the overlong line at position 3
	// blocks scanning the rest of the file — there could be more
	// "foo" lines later.
	content := "foo\nbar\n" + overlong + "\nfoo\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	foo := "foo"
	f := schema.Finding{File: "src.go", Line: 2, AnchorLine: &foo}
	pc := loadPreview(root, f, 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if pc.Anchor == anchor.StateShifted {
		t.Errorf("Anchor = Shifted on truncated read; want Unknown — the unread suffix might still hold the original line")
	}
}

// TestLoadPreview_overlongCurrentStillTrusted — StateCurrent is
// computed by checking f.Line directly without scanning the rest of
// the file, so it remains trustworthy even on truncated reads. This
// test guards against an over-eager demotion that would erase
// legitimate Current verdicts.
func TestLoadPreview_overlongCurrentStillTrusted(t *testing.T) {
	root := t.TempDir()
	overlong := strings.Repeat("x", 1<<20+1<<19)
	// Anchor at line 1 = "alpha"; matches at f.Line; overlong line is
	// further down and never reached, but we shouldn't care.
	content := "alpha\nbeta\n" + overlong + "\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	alpha := "alpha"
	f := schema.Finding{File: "src.go", Line: 1, AnchorLine: &alpha}
	pc := loadPreview(root, f, 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if pc.Anchor != anchor.StateCurrent {
		t.Errorf("Anchor = %v; want Current (f.Line match should survive truncation)", pc.Anchor)
	}
}

// TestLoadPreview_anchorStale — anchor content is gone: the window
// centres on the original line for context, with no highlighted row.
func TestLoadPreview_anchorStale(t *testing.T) {
	root := t.TempDir()
	content := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	removed := "REMOVED"
	f := schema.Finding{File: "src.go", Line: 2, AnchorLine: &removed}
	pc := loadPreview(root, f, 1)
	if pc.Error != "" {
		t.Fatalf("unexpected error: %s", pc.Error)
	}
	if pc.Anchor != anchor.StateStale {
		t.Errorf("Anchor = %v, want Stale", pc.Anchor)
	}
	if pc.EffectiveLine != 0 {
		t.Errorf("EffectiveLine = %d, want 0", pc.EffectiveLine)
	}
	for _, l := range pc.Lines {
		if l.Highlight {
			t.Errorf("stale finding produced a highlight on line %d", l.Number)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
