package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSummarize_findRun verifies the JSON shape and counts when the run
// folder has a few finding docs — some with reviews and outcomes
// embedded — that Summarize aggregates into the row shown by
// `fettle list runs`.
func TestSummarize_findRun(t *testing.T) {
	dir := t.TempDir()

	manifest := `{
  "name": "find_20260101T000000Z_test",
  "stage": "find",
  "fettle_version": "0.1.0",
  "created_at": "2026-01-01T00:00:00Z",
  "completed_at": "2026-01-01T00:05:00Z",
  "target_repo": "/abs/path",
  "agent": {"name": "claude", "model": "sonnet"}
}
`
	mustWrite(t, filepath.Join(dir, "run.json"), manifest)
	if err := os.Mkdir(filepath.Join(dir, findingsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir findings: %v", err)
	}
	mustWrite(t, filepath.Join(dir, findingsSubdir, "a.json"),
		`{"id":"a","file":"x.go","line":1,"title":"A","description":"","suggestion":"","severity":null,"labels":null,"references":null,"created_by":"","created_at":"2026-01-01T00:00:00Z","reviews":[{"author":"agent:claude/sonnet","at":"2026-01-01T00:01:00Z","comment":"x"},{"author":"human:michael","at":"2026-01-01T00:02:00Z","comment":"y"}]}`+"\n")
	mustWrite(t, filepath.Join(dir, findingsSubdir, "b.json"),
		`{"id":"b","file":"y.go","line":2,"title":"B","description":"","suggestion":"","severity":null,"labels":null,"references":null,"created_by":"","created_at":"2026-01-01T00:00:00Z","reviews":[{"author":"human:michael","at":"2026-01-01T00:03:00Z","comment":"z"}],"outcomes":[{"author":"human:michael","status":"wontfix","at":"2026-01-01T00:04:00Z"}]}`+"\n")
	mustWrite(t, filepath.Join(dir, findingsSubdir, "c.json"),
		`{"id":"c","file":"z.go","line":3,"title":"C","description":"","suggestion":"","severity":null,"labels":null,"references":null,"created_by":"","created_at":"2026-01-01T00:00:00Z"}`+"\n")

	s, err := Summarize(dir)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.Name != "find_20260101T000000Z_test" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Stage != "find" {
		t.Errorf("Stage = %q", s.Stage)
	}
	if s.Counts.Findings == nil || *s.Counts.Findings != 3 {
		t.Errorf("Findings = %v, want 3", s.Counts.Findings)
	}
	if s.Counts.Reviews != 3 {
		t.Errorf("Reviews = %d, want 3", s.Counts.Reviews)
	}
	if s.Counts.Outcomes != 1 {
		t.Errorf("Outcomes = %d, want 1", s.Counts.Outcomes)
	}

	// JSON shape stability — these tags are part of the CLI contract
	// (`fettle list runs`, `fettle show run`).
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"name":"find_20260101T000000Z_test"`,
		`"stage":"find"`,
		`"created_at":"2026-01-01T00:00:00Z"`,
		`"completed_at":"2026-01-01T00:05:00Z"`,
		`"target_repo":"/abs/path"`,
		`"counts":{"findings":3,"reviews":3,"outcomes":1}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON missing %q\nfull: %s", want, got)
		}
	}
	if strings.Contains(got, `"groups"`) {
		t.Errorf("JSON should omit groups; got %s", got)
	}
}

// TestSummarize_malformedDocCountedButNotParsed: a half-written
// finding doc still counts toward Findings (it's a file an agent
// emitted, even if a torn write left it unreadable), but its zero
// reviews/outcomes don't pollute the parsed totals. Locks in the
// "Findings = ListFindingIDs len; Reviews/Outcomes = parsed sums"
// split documented in summary.go's godoc.
func TestSummarize_malformedDocCountedButNotParsed(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"),
		`{"name":"r","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	if err := os.Mkdir(filepath.Join(dir, findingsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWrite(t, filepath.Join(dir, findingsSubdir, "good.json"),
		`{"id":"good","file":"x.go","line":1,"title":"T","description":"","suggestion":"","severity":null,"labels":null,"references":null,"created_by":"","created_at":"2026-01-01T00:00:00Z","reviews":[{"author":"a","at":"2026-01-01T00:01:00Z"}]}`+"\n")
	mustWrite(t, filepath.Join(dir, findingsSubdir, "bad.json"), "not valid json")

	s, err := Summarize(dir)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.Counts.Findings == nil || *s.Counts.Findings != 2 {
		t.Errorf("Findings = %v, want 2 (file count, malformed included)", s.Counts.Findings)
	}
	if s.Counts.Reviews != 1 {
		t.Errorf("Reviews = %d, want 1 (only parsed docs contribute)", s.Counts.Reviews)
	}
}

// TestSummarize_emptyRun: no findings/ directory yet (run created but
// the agent hasn't written anything). Counts should all be zero, no
// errors.
func TestSummarize_emptyRun(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"),
		`{"name":"r","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	s, err := Summarize(dir)
	if err != nil {
		t.Fatalf("Summarize on empty run: %v", err)
	}
	if s.Counts.Findings == nil || *s.Counts.Findings != 0 {
		t.Errorf("Findings = %v, want 0", s.Counts.Findings)
	}
	if s.Counts.Reviews != 0 || s.Counts.Outcomes != 0 {
		t.Errorf("Reviews/Outcomes = %d/%d, want 0/0", s.Counts.Reviews, s.Counts.Outcomes)
	}
}

// TestCountLines_missingFile returns zero, not an error — files.jsonl
// (the last remaining JSONL stream) may not exist on a partial run.
func TestCountLines_missingFile(t *testing.T) {
	n, err := CountLines(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("CountLines on missing file: %v", err)
	}
	if n != 0 {
		t.Errorf("CountLines = %d, want 0", n)
	}
}

// TestCountLines_skipsBlankLines ensures blank/whitespace-only lines
// don't inflate the count.
func TestCountLines_skipsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	mustWrite(t, path, "{\"a\":1}\n\n  \n{\"b\":2}\n")
	n, err := CountLines(path)
	if err != nil {
		t.Fatalf("CountLines: %v", err)
	}
	if n != 2 {
		t.Errorf("CountLines = %d, want 2", n)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
