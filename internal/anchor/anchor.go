// Package anchor tracks whether a finding's anchor line has stayed
// in place, shifted to a new line, or disappeared since the finding
// was recorded.
//
// A finding stores the exact text of its target line at creation time
// (schema.Finding.AnchorLine, capped to MaxLen). At read time, callers
// compare the stored anchor against the file as it sits on disk now to
// produce a State and an effective line number. We never try to judge
// whether the finding's *intent* still applies — that's a human or LLM
// decision. We only track whether the line we anchored to survived,
// the same model GitHub uses for inline PR comments.
package anchor

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/contiamo/fettle/internal/schema"
)

// MaxLen caps how much of a line we store in Finding.AnchorLine.
// Source files in the wild include vendored minified bundles where one
// "line" is megabytes; persisting that into findings.jsonl would bloat
// the run directory and slow every read. 256 chars covers virtually
// every hand-written line; on the rare case where two lines share an
// identical 256-char prefix, drift detection demotes to State Ambiguous
// (and picks the nearest match), which is exactly the right outcome.
const MaxLen = 256

// State is the drift verdict for one finding's anchor.
type State int

const (
	// StateUnknown is returned for legacy findings that predate
	// AnchorLine — we have no anchor to compare against, so the UI
	// renders the line as-is with no badge.
	StateUnknown State = iota
	// StateCurrent — the line at Finding.Line still matches the
	// stored anchor (after both are truncated to MaxLen).
	StateCurrent
	// StateShifted — the anchor appears on exactly one different
	// line; we know precisely where the finding moved.
	StateShifted
	// StateAmbiguous — the anchor appears on multiple lines (e.g. a
	// lone "}"); we pick the match closest to the original line and
	// flag the result as approximate.
	StateAmbiguous
	// StateStale — the anchor no longer appears anywhere in the file.
	// The finding's target was deleted or rewritten.
	StateStale
)

// String renders a short label suitable for log lines and CLI badges.
func (s State) String() string {
	switch s {
	case StateCurrent:
		return "current"
	case StateShifted:
		return "shifted"
	case StateAmbiguous:
		return "ambiguous"
	case StateStale:
		return "stale"
	default:
		return "unknown"
	}
}

// Result is the drift outcome for one finding.
//
// EffectiveLine is the line the UI should highlight:
//   - StateCurrent / StateUnknown:        OriginalLine
//   - StateShifted / StateAmbiguous:      the resolved match
//   - StateStale:                         0 (no line to highlight)
type Result struct {
	State         State
	OriginalLine  int
	EffectiveLine int
}

// Capture reads file at line (1-based) under repoRoot and returns the
// anchor text for storage on a new Finding. The returned string is
// truncated to MaxLen runes.
//
// Empty repoRoot, missing files, or out-of-range line numbers all
// return ("", err). Callers (the `add finding` CLI) should treat any
// error as "skip the anchor" rather than failing the whole add — a
// finding without an anchor degrades gracefully to StateUnknown.
func Capture(repoRoot, file string, line int) (string, error) {
	if repoRoot == "" {
		return "", errors.New("repoRoot is empty")
	}
	if line < 1 {
		return "", fmt.Errorf("line %d: must be >= 1", line)
	}
	abs, err := safeJoin(repoRoot, file)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("file not found: %s", file)
		}
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		if n == line {
			return truncate(sc.Text()), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scan file: %w", err)
	}
	return "", fmt.Errorf("file has %d lines; line %d is out of range", n, line)
}

// Resolve reads the file referenced by f and returns the drift
// verdict. Convenience wrapper for callers that don't already hold the
// file in memory (the CLI's `show finding`, future stage outputs).
func Resolve(repoRoot string, f schema.Finding) (Result, error) {
	if repoRoot == "" {
		return Result{State: StateUnknown, OriginalLine: f.Line, EffectiveLine: f.Line},
			errors.New("repoRoot is empty")
	}
	abs, err := safeJoin(repoRoot, f.File)
	if err != nil {
		return Result{State: StateUnknown, OriginalLine: f.Line, EffectiveLine: f.Line}, err
	}
	lines, err := readLines(abs)
	if err != nil {
		return Result{State: StateUnknown, OriginalLine: f.Line, EffectiveLine: f.Line}, err
	}
	return ResolveFromLines(lines, f), nil
}

// ResolveFromLines is the pure form: caller supplies the file as a
// slice of lines (1-based access via index+1). Used by the UI's
// preview path, which already reads the file to render a code window
// — re-opening it in Resolve would double the I/O.
func ResolveFromLines(lines []string, f schema.Finding) Result {
	res := Result{OriginalLine: f.Line, EffectiveLine: f.Line}
	if f.AnchorLine == nil {
		res.State = StateUnknown
		return res
	}

	// Compare prefixes of the same length so a stored anchor truncated
	// at MaxLen still matches a current line whose prefix is identical
	// up to that point. We keep MaxLen-sized prefixes only — extra
	// chars on the current line beyond MaxLen are intentionally
	// ignored (matches the truncation semantics used at Capture time).
	want := truncate(*f.AnchorLine)

	// Fast path: original line still matches.
	if f.Line >= 1 && f.Line <= len(lines) && truncate(lines[f.Line-1]) == want {
		res.State = StateCurrent
		return res
	}

	var matches []int // 1-based line numbers
	for i, ln := range lines {
		if truncate(ln) == want {
			matches = append(matches, i+1)
		}
	}
	switch len(matches) {
	case 0:
		res.State = StateStale
		res.EffectiveLine = 0
		return res
	case 1:
		res.State = StateShifted
		res.EffectiveLine = matches[0]
		return res
	default:
		res.State = StateAmbiguous
		res.EffectiveLine = nearest(matches, f.Line)
		return res
	}
}

// ApplyTruncationDemotion enforces the rule that a partial file read
// (e.g. a bufio.ErrTooLong while scanning) makes anything other than
// StateCurrent untrustworthy: the unread suffix could contain the
// original line still intact, additional duplicates, or the only
// matching line. StateCurrent short-circuits on the f.Line position
// before scanning the rest of the file, so it survives truncation.
//
// Callers that scan the file themselves (the UI's loadPreview, the
// list-view drift-state batcher) wrap their ResolveFromLines result
// with this helper so both code paths apply the same conservative
// rule. truncated=false is a no-op pass-through.
func ApplyTruncationDemotion(r Result, truncated bool) Result {
	if !truncated {
		return r
	}
	if r.State == StateCurrent || r.State == StateUnknown {
		return r
	}
	r.State = StateUnknown
	r.EffectiveLine = r.OriginalLine
	return r
}

// nearest returns the entry of matches closest to target. matches is
// non-empty by construction (callers only reach this path with len > 1).
// On ties, the smaller line number wins — deterministic, and tends to
// pick the "earlier" of two duplicate code blocks, which is what most
// reviewers expect when scanning top-to-bottom.
func nearest(matches []int, target int) int {
	best := matches[0]
	bestDist := abs(best - target)
	for _, m := range matches[1:] {
		d := abs(m - target)
		if d < bestDist {
			best, bestDist = m, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// truncate canonicalizes a candidate line for storage or comparison:
// it replaces invalid UTF-8 with U+FFFD and caps the result at
// MaxLen bytes, cutting only on a rune boundary. Both steps matter
// because the stored anchor must round-trip through json.Marshal —
// which silently sanitizes invalid UTF-8 to U+FFFD on write — without
// changing value, and the same canonicalization has to apply to lines
// read fresh from disk so equality holds. Source files that are not
// well-formed UTF-8 (rare, but real for some legacy sources) get a
// lossy but consistent representation on both sides.
func truncate(s string) string {
	// Normalize first, then truncate. Doing it the other way around
	// would let utf8.RuneStart misclassify an invalid leading byte
	// (e.g. 0xff) as a rune start and leave it in the result; after
	// ToValidUTF8 the string is well-formed and the rune-boundary
	// scan below is reliable.
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= MaxLen {
		return s
	}
	end := MaxLen
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// readLines slurps the whole file as a []string. Used by Resolve when
// the caller doesn't already hold the file in memory.
func readLines(abs string) ([]string, error) {
	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("file not found: %s", abs)
		}
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}
	return out, nil
}

// safeJoin resolves repoRel inside repoRoot, rejecting absolute paths
// or any rel that escapes via "..". Mirrors the check in
// internal/ui/server/preview.go so anchor.Capture (CLI-side) gets the
// same containment guarantees the UI applies — a malicious or buggy
// agent passing `--file ../../etc/passwd` to `add finding` is rejected
// here, not just at render time.
func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("file path is absolute: %q", rel)
	}
	cleaned := filepath.Clean(rel)
	if escapesRoot(cleaned) {
		return "", fmt.Errorf("file path escapes repo root: %q", rel)
	}
	abs := filepath.Join(root, cleaned)
	relCheck, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if escapesRoot(relCheck) {
		return "", fmt.Errorf("file path escapes repo root: %q", rel)
	}
	return abs, nil
}

func escapesRoot(cleaned string) bool {
	if cleaned == ".." {
		return true
	}
	return strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
