package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	pc := loadPreview(root, "src.go", 10, 3)
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
	pc := loadPreview(root, "src.go", 1, 5)
	if pc.Error != "" {
		t.Fatalf("error: %s", pc.Error)
	}
	if pc.Lines[0].Number != 1 {
		t.Errorf("first line = %d, want 1", pc.Lines[0].Number)
	}
}

// TestLoadPreview_targetPastEOF — a finding pointing at a line past
// the file's end (because the file shrank since the find run, e.g.
// after a refactor) gets a placeholder, not a misleading window of
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
	pc := loadPreview(root, "src.go", 12, 6)
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
	pc := loadPreview(root, "nope.go", 5, 3)
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
	pc := loadPreview("", "x.go", 5, 3)
	if pc.Error == "" {
		t.Error("expected non-empty Error for empty target_repo")
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
