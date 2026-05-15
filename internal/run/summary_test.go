package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// TestSummarize_findRun verifies the JSON shape and counts after a
// run has been written through the new JSONL stream layout — three
// findings, three review entries, one outcome entry. Mirrors the
// shape every consumer of `fettle list runs` / `fettle show run`
// depends on.
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

	rp := &Path{dir: dir}
	for _, f := range []struct {
		id, file string
	}{
		{"a", "x.go"},
		{"b", "y.go"},
		{"c", "z.go"},
	} {
		if err := rp.AppendFindingEntry(schema.FindingEntry{
			Kind: schema.SubjectFinding,
			Finding: schema.Finding{
				ID: f.id, File: f.file, Line: 1, Title: f.id,
				CreatedBy: "agent:claude/sonnet",
				CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Labels:    []string{}, References: []schema.Reference{},
			},
		}, "tester", "claude-sonnet"); err != nil {
			t.Fatal(err)
		}
	}
	// Three reviews across findings.
	for i, e := range []schema.ReviewEntry{
		{Kind: schema.SubjectFinding, ID: "a", Author: "agent:claude/sonnet", At: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Add: []string{}, Remove: []string{}, Comment: "x"},
		{Kind: schema.SubjectFinding, ID: "a", Author: "human:michael", At: time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC), Add: []string{}, Remove: []string{}, Comment: "y"},
		{Kind: schema.SubjectFinding, ID: "b", Author: "human:michael", At: time.Date(2026, 1, 1, 0, 3, 0, 0, time.UTC), Add: []string{}, Remove: []string{}, Comment: "z"},
	} {
		if err := rp.AppendReviewEntry(e, "tester", ""); err != nil {
			t.Fatalf("append review %d: %v", i, err)
		}
	}
	// One outcome.
	if err := rp.AppendOutcomeEntry(schema.OutcomeEntry{
		Kind: schema.SubjectFinding, ID: "b", Author: "human:michael",
		At: time.Date(2026, 1, 1, 0, 4, 0, 0, time.UTC), Status: "wontfix",
	}, "tester", ""); err != nil {
		t.Fatal(err)
	}
	if err := rp.Close(); err != nil {
		t.Fatal(err)
	}

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

// TestSummarize_malformedLineSkipped covers the torn-append case: a
// findings_*.jsonl file with one good line and one corrupt line. The
// good line counts; the bad one is skipped (the loader logs but
// doesn't fail). Reviews / outcomes counts only reflect parsed lines.
func TestSummarize_malformedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"),
		`{"name":"r","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")

	rp := &Path{dir: dir}
	if err := rp.AppendFindingEntry(schema.FindingEntry{
		Kind: schema.SubjectFinding,
		Finding: schema.Finding{
			ID: "good", File: "x.go", Line: 1, Title: "T",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Labels:    []string{}, References: []schema.Reference{},
		},
	}, "tester", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := rp.AppendReviewEntry(schema.ReviewEntry{
		Kind: schema.SubjectFinding, ID: "good", Author: "a",
		At: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC),
		Add: []string{}, Remove: []string{},
	}, "tester", ""); err != nil {
		t.Fatal(err)
	}
	if err := rp.Close(); err != nil {
		t.Fatal(err)
	}

	// Append a corrupt line to the findings file.
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "findings_") {
			p := filepath.Join(dir, f.Name())
			fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			fh.WriteString("not valid json\n")
			fh.Close()
		}
	}

	s, err := Summarize(dir)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.Counts.Findings == nil || *s.Counts.Findings != 1 {
		t.Errorf("Findings = %v, want 1 (malformed line skipped)", s.Counts.Findings)
	}
	if s.Counts.Reviews != 1 {
		t.Errorf("Reviews = %d, want 1", s.Counts.Reviews)
	}
}

// TestSummarize_emptyRun: run dir with only the manifest. Every
// count is zero, no errors.
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
// (the harness's per-file ledger) may not exist on a partial run.
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
