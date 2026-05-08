package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSummarize_findRun verifies the JSON shape and counts for a find
// run with findings, multiple reviews_<author>.jsonl files, and outcomes.
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
	mustWrite(t, filepath.Join(dir, "findings.jsonl"), "{\"id\":\"a\"}\n{\"id\":\"b\"}\n{\"id\":\"c\"}\n")
	mustWrite(t, filepath.Join(dir, "reviews_claude.jsonl"), "{\"x\":1}\n{\"x\":2}\n")
	mustWrite(t, filepath.Join(dir, "reviews_michael.jsonl"), "{\"y\":1}\n")
	mustWrite(t, filepath.Join(dir, "outcomes.jsonl"), "{\"z\":1}\n")

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

// TestCountLines_missingFile returns zero, not an error — partial runs
// (no outcomes.jsonl yet, no findings.jsonl yet) report cleanly.
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
// don't inflate the count, since record files round-trip through editors.
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

// TestLoadFindings_skipsMalformedLines verifies the same tolerance the
// rest of the harness has — a torn append shouldn't take down the
// whole UI page; it just drops the bad line.
func TestLoadFindings_skipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"), `{"name":"x","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	mustWrite(t, filepath.Join(dir, "findings.jsonl"),
		`{"id":"a","file":"x.go","line":1,"title":"A"}`+"\n"+
			`not valid json`+"\n"+
			`{"id":"b","file":"y.go","line":2,"title":"B"}`+"\n",
	)
	rp, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := rp.LoadFindings()
	if err != nil {
		t.Fatalf("LoadFindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("ids = [%s, %s], want [a, b]", got[0].ID, got[1].ID)
	}
}

// TestLoadFindings_missingFile mirrors LoadOutcomes — partial runs
// (no findings.jsonl yet) report as empty, not as an error.
func TestLoadFindings_missingFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"), `{"name":"x","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	rp, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := rp.LoadFindings()
	if err != nil {
		t.Fatalf("LoadFindings on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d findings, want 0", len(got))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

