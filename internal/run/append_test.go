package run

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// newTestRun creates a bare run directory layout sufficient for the
// appenders to write into. Real runs are created via CreateForFind,
// but the append paths only need the directory to exist.
func newTestRun(t *testing.T) *Path {
	t.Helper()
	dir := t.TempDir()
	return &Path{dir: dir}
}

func TestAppendReviewEntry_RoundTrip(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	e := schema.ReviewEntry{
		Kind:   schema.SubjectFinding,
		ID:     "abc",
		Author: "human:michael",
		At:     time.Date(2026, 5, 15, 10, 30, 22, 0, time.UTC),
		Add:    []string{"priority:p1"},
		Remove: []string{},
	}
	if err := rp.AppendReviewEntry(e, "michael", ""); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	files, err := os.ReadDir(rp.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	name := files[0].Name()
	if !strings.HasPrefix(name, "reviews_") || !strings.HasSuffix(name, "_michael.jsonl") {
		t.Errorf("filename %q doesn't match shape", name)
	}
	meta, ok := ParseArtifactFilename(name)
	if !ok {
		t.Fatalf("file %q doesn't parse as artifact", name)
	}
	if meta.Kind != ArtifactReviews || meta.Human != "michael" || meta.Agent != "" {
		t.Errorf("metadata mismatch: %+v", meta)
	}

	data, err := os.ReadFile(filepath.Join(rp.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var got schema.ReviewEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "abc" || got.Author != "human:michael" {
		t.Errorf("entry round-trip mismatch: %+v", got)
	}
	// Empty arrays must marshal as `[]`, not `null`.
	if !strings.Contains(string(data), `"add":["priority:p1"]`) {
		t.Errorf("add not marshalled as array: %s", data)
	}
	if !strings.Contains(string(data), `"remove":[]`) {
		t.Errorf("remove not marshalled as []: %s", data)
	}
}

func TestAppendReviewEntry_RejectsInvalid(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	bad := schema.ReviewEntry{
		Kind:   schema.SubjectFinding,
		ID:     "abc",
		Author: "human:michael",
		At:     time.Now().UTC(),
		Add:    nil, // nil triggers validator
		Remove: []string{},
	}
	if err := rp.AppendReviewEntry(bad, "michael", ""); err == nil {
		t.Fatalf("want error for nil add")
	}
	// And no file should have been created on disk.
	files, _ := os.ReadDir(rp.dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "reviews_") {
			t.Errorf("unexpected reviews file written: %s", f.Name())
		}
	}
}

func TestAppendEntry_MultipleAppendsShareOneFile(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	for range 5 {
		e := schema.ReviewEntry{
			Kind:   schema.SubjectFinding,
			ID:     "abc",
			Author: "human:michael",
			At:     time.Now().UTC(),
			Add:    []string{"x"},
			Remove: []string{},
		}
		if err := rp.AppendReviewEntry(e, "michael", ""); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var reviewFiles []string
	files, _ := os.ReadDir(rp.dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "reviews_") {
			reviewFiles = append(reviewFiles, f.Name())
		}
	}
	if len(reviewFiles) != 1 {
		t.Fatalf("want 1 file, got %d: %v", len(reviewFiles), reviewFiles)
	}

	// Five entries, five lines.
	data, _ := os.ReadFile(filepath.Join(rp.dir, reviewFiles[0]))
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines != 5 {
		t.Errorf("want 5 lines, got %d", lines)
	}
}

func TestAppendEntry_DifferentIdentitiesGetDifferentFiles(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	for _, who := range []struct{ human, agent string }{
		{"michael", ""},
		{"michael", "claude-sonnet"},
		{"alice", ""},
	} {
		e := schema.ReviewEntry{
			Kind:   schema.SubjectFinding,
			ID:     "abc",
			Author: "human:" + who.human,
			At:     time.Now().UTC(),
			Add:    []string{"x"},
			Remove: []string{},
		}
		if err := rp.AppendReviewEntry(e, who.human, who.agent); err != nil {
			t.Fatalf("append for %+v: %v", who, err)
		}
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	files, _ := os.ReadDir(rp.dir)
	var reviews []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "reviews_") {
			reviews = append(reviews, f.Name())
		}
	}
	if len(reviews) != 3 {
		t.Errorf("want 3 distinct files, got %d: %v", len(reviews), reviews)
	}
}

func TestAppendEntry_ConcurrentWritesSerialised(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for range n {
		go func() {
			defer wg.Done()
			e := schema.ReviewEntry{
				Kind:   schema.SubjectFinding,
				ID:     "abc",
				Author: "human:michael",
				At:     time.Now().UTC(),
				Add:    []string{"x"},
				Remove: []string{},
			}
			if err := rp.AppendReviewEntry(e, "michael", ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent append: %v", err)
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	files, _ := os.ReadDir(rp.dir)
	var reviewFiles []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "reviews_") {
			reviewFiles = append(reviewFiles, f.Name())
		}
	}
	if len(reviewFiles) != 1 {
		t.Fatalf("want 1 file, got %d", len(reviewFiles))
	}
	data, _ := os.ReadFile(filepath.Join(rp.dir, reviewFiles[0]))
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lines := 0
	for scanner.Scan() {
		var e schema.ReviewEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Errorf("line %d not valid JSON: %v", lines, err)
		}
		lines++
	}
	if lines != n {
		t.Errorf("want %d lines, got %d", n, lines)
	}
}

func TestAppendFindingEntry_FlatJSON(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	sev := "high"
	e := schema.FindingEntry{
		Kind: schema.SubjectFinding,
		Finding: schema.Finding{
			ID:          "abc",
			File:        "src/foo.go",
			Line:        42,
			Title:       "t",
			Description: "d",
			Suggestion:  "s",
			Severity:    &sev,
			Labels:      []string{"category:perf"},
			References:  []schema.Reference{},
			CreatedBy:   "agent:claude/sonnet",
			CreatedAt:   time.Now().UTC(),
		},
	}
	if err := rp.AppendFindingEntry(e, "michael", "claude-sonnet"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var name string
	files, _ := os.ReadDir(rp.dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "findings_") {
			name = f.Name()
		}
	}
	if name == "" {
		t.Fatalf("no findings file written")
	}
	data, _ := os.ReadFile(filepath.Join(rp.dir, name))
	// JSON should be flat: `kind` and `id` at top level.
	s := string(data)
	if !strings.Contains(s, `"kind":"finding"`) || !strings.Contains(s, `"id":"abc"`) {
		t.Errorf("flat JSON shape missing: %s", s)
	}
}

func TestAppendOutcomeEntry_RejectsInvalid(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })
	bad := schema.OutcomeEntry{
		Kind:   schema.SubjectFinding,
		ID:     "abc",
		Author: "human:michael",
		At:     time.Now().UTC(),
		Status: "",
	}
	if err := rp.AppendOutcomeEntry(bad, "michael", ""); err == nil {
		t.Fatalf("want error for empty status")
	}
}
