package anchor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/contiamo/fettle/internal/schema"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sp is a tiny helper for the *string AnchorLine field. Tests use it
// to express "anchor was captured with this content" succinctly,
// while leaving nil to mean "no anchor".
func sp(s string) *string { return &s }

func TestCapture_basic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src.go", "first\nsecond\nthird\n")
	got, err := Capture(root, "src.go", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
}

func TestCapture_truncates(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", MaxLen+50)
	writeFile(t, root, "src.go", long+"\n")
	got, err := Capture(root, "src.go", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxLen {
		t.Errorf("len = %d, want %d", len(got), MaxLen)
	}
}

func TestCapture_outOfRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src.go", "only one line\n")
	if _, err := Capture(root, "src.go", 5); err == nil {
		t.Error("expected error for line past EOF")
	}
}

func TestCapture_traversalRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := Capture(root, "../etc/passwd", 1); err == nil {
		t.Error("expected traversal rejection")
	}
}

func TestResolveFromLines_unknownNoAnchor(t *testing.T) {
	// Nil AnchorLine = legacy finding (no anchor was ever captured).
	r := ResolveFromLines([]string{"a", "b"}, schema.Finding{File: "x", Line: 1})
	if r.State != StateUnknown {
		t.Errorf("state = %v, want Unknown", r.State)
	}
	if r.EffectiveLine != 1 {
		t.Errorf("EffectiveLine = %d, want 1", r.EffectiveLine)
	}
}

func TestResolveFromLines_current(t *testing.T) {
	lines := []string{"alpha", "beta", "gamma"}
	r := ResolveFromLines(lines, schema.Finding{Line: 2, AnchorLine: sp("beta")})
	if r.State != StateCurrent {
		t.Errorf("state = %v, want Current", r.State)
	}
	if r.EffectiveLine != 2 {
		t.Errorf("EffectiveLine = %d, want 2", r.EffectiveLine)
	}
}

func TestResolveFromLines_shifted(t *testing.T) {
	lines := []string{"alpha", "INSERTED", "beta", "gamma"}
	r := ResolveFromLines(lines, schema.Finding{Line: 2, AnchorLine: sp("beta")})
	if r.State != StateShifted {
		t.Errorf("state = %v, want Shifted", r.State)
	}
	if r.EffectiveLine != 3 {
		t.Errorf("EffectiveLine = %d, want 3", r.EffectiveLine)
	}
}

func TestResolveFromLines_ambiguousPicksNearest(t *testing.T) {
	lines := []string{"}", "x", "}", "x", "}", "x", "}"}
	// Matches at 1,3,5,7. Original line 4 → nearest 3 or 5;
	// equal distance, picks 3 (smaller wins on tie).
	r := ResolveFromLines(lines, schema.Finding{Line: 4, AnchorLine: sp("}")})
	if r.State != StateAmbiguous {
		t.Errorf("state = %v, want Ambiguous", r.State)
	}
	if r.EffectiveLine != 3 {
		t.Errorf("EffectiveLine = %d, want 3 (nearest, ties go to smaller)", r.EffectiveLine)
	}
}

func TestResolveFromLines_stale(t *testing.T) {
	lines := []string{"alpha", "gamma"}
	r := ResolveFromLines(lines, schema.Finding{Line: 2, AnchorLine: sp("beta")})
	if r.State != StateStale {
		t.Errorf("state = %v, want Stale", r.State)
	}
	if r.EffectiveLine != 0 {
		t.Errorf("EffectiveLine = %d, want 0 (stale)", r.EffectiveLine)
	}
}

func TestResolveFromLines_truncatedAnchorMatchesPrefix(t *testing.T) {
	prefix := strings.Repeat("a", MaxLen)
	stored := prefix // already MaxLen long; this is what Capture would have written
	currentLine := prefix + "extra suffix added later"
	lines := []string{"first", currentLine, "third"}
	r := ResolveFromLines(lines, schema.Finding{Line: 2, AnchorLine: sp(stored)})
	// Both sides truncate to MaxLen → equal → StateCurrent (line didn't move).
	if r.State != StateCurrent {
		t.Errorf("state = %v, want Current (truncated prefix should still match)", r.State)
	}
}

// TestResolveFromLines_blankLineAnchor — a finding anchored to a
// legitimately blank source line uses &"" (not nil) so drift detection
// still runs. The blank line is found in the file → StateCurrent.
func TestResolveFromLines_blankLineAnchor(t *testing.T) {
	lines := []string{"alpha", "", "gamma"}
	r := ResolveFromLines(lines, schema.Finding{Line: 2, AnchorLine: sp("")})
	if r.State != StateCurrent {
		t.Errorf("state = %v, want Current (blank line anchor)", r.State)
	}
	if r.EffectiveLine != 2 {
		t.Errorf("EffectiveLine = %d, want 2", r.EffectiveLine)
	}
}

func TestResolveFromLines_originalLineOutOfRangeButAnchorFound(t *testing.T) {
	// File shrank below the original line number, but the anchored
	// content still exists earlier in the file → Shifted, not Stale.
	lines := []string{"only line"}
	r := ResolveFromLines(lines, schema.Finding{Line: 5, AnchorLine: sp("only line")})
	if r.State != StateShifted {
		t.Errorf("state = %v, want Shifted", r.State)
	}
	if r.EffectiveLine != 1 {
		t.Errorf("EffectiveLine = %d, want 1", r.EffectiveLine)
	}
}

func TestResolve_readsFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src.go", "first\nsecond\nthird\n")
	r, err := Resolve(root, schema.Finding{File: "src.go", Line: 2, AnchorLine: sp("second")})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateCurrent {
		t.Errorf("state = %v, want Current", r.State)
	}
}

// TestTruncate_runeBoundary — a stored anchor must not break UTF-8 by
// cutting mid-rune. Otherwise json.Marshal sanitizes invalid bytes to
// U+FFFD and the roundtripped value no longer equals the same byte
// prefix re-read from the file, producing false StateStale results.
func TestTruncate_runeBoundary(t *testing.T) {
	// 254 ASCII bytes + a 3-byte rune ("世") puts the rune across the
	// MaxLen=256 boundary: bytes 254 (leading) + 255,256 (continuation).
	// Naive byte truncation at 256 would keep the leading byte and the
	// first continuation byte, producing invalid UTF-8.
	prefix := strings.Repeat("a", MaxLen-2)
	s := prefix + "世" + "extra"
	got := truncate(s)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if got != prefix {
		t.Errorf("expected truncate to drop the partial rune; got %q", got)
	}
}

// TestTruncate_normalizesInvalidUTF8 — source files with malformed
// bytes (rare, but real for some legacy or binary-tagged sources)
// must not produce an anchor that json.Marshal would silently rewrite
// on persist. truncate replaces invalid bytes with U+FFFD so the
// stored value is already valid and round-trips cleanly.
func TestTruncate_normalizesInvalidUTF8(t *testing.T) {
	// 0xff is never a valid UTF-8 byte; surrounded by ASCII so the
	// rune-boundary loop wouldn't fix it on its own.
	s := "abc\xffdef"
	got := truncate(s)
	if !utf8.ValidString(got) {
		t.Errorf("truncate left invalid UTF-8 in result: %q", got)
	}
	// Both sides of a future comparison go through truncate, so the
	// exact replacement value matters less than consistency: a fresh
	// read of the same bytes from disk must produce the same string.
	again := truncate("abc\xffdef")
	if got != again {
		t.Errorf("truncate is not deterministic on invalid UTF-8: %q vs %q", got, again)
	}
}
