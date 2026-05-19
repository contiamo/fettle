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

// testRunSlug / testRunTs are the canonical slug + timestamp used
// for every test fixture run directory in this package. Hard-coded
// so the expected artifact filenames in assertions don't move when
// the random slug or wall-clock time changes.
const (
	testRunSlug = "aaa111"
	testRunTs   = "20260101T000000Z"
)

// newTestRun creates a bare run directory layout sufficient for the
// appenders to write into. The directory name matches the canonical
// `run_<slug>_<ts>` shape so `Path.Slug()` / `Path.StartTime()`
// resolve correctly without needing a manifest.
func newTestRun(t *testing.T) *Path {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "run_"+testRunSlug+"_"+testRunTs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
	if err := rp.AppendReviewEntry(e); err != nil {
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
	wantName := "reviews_" + testRunSlug + "_" + testRunTs + "_michael.jsonl"
	if name != wantName {
		t.Errorf("filename mismatch:\n  got:  %s\n  want: %s", name, wantName)
	}
	meta, ok := ParseArtifactFilename(name)
	if !ok {
		t.Fatalf("file %q doesn't parse as artifact", name)
	}
	if meta.Kind != ArtifactReviews || meta.Slug != testRunSlug || meta.Author != "michael" {
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
	if err := rp.AppendReviewEntry(bad); err == nil {
		t.Fatalf("want error for nil add")
	}
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
		if err := rp.AppendReviewEntry(e); err != nil {
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

func TestAppendEntry_DifferentAuthorsGetDifferentFiles(t *testing.T) {
	rp := newTestRun(t)
	t.Cleanup(func() { _ = rp.Close() })

	for _, author := range []string{"human:michael", "agent:claude/sonnet", "human:alice"} {
		e := schema.ReviewEntry{
			Kind:   schema.SubjectFinding,
			ID:     "abc",
			Author: author,
			At:     time.Now().UTC(),
			Add:    []string{"x"},
			Remove: []string{},
		}
		if err := rp.AppendReviewEntry(e); err != nil {
			t.Fatalf("append for %s: %v", author, err)
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
		t.Errorf("want 3 distinct files (one per author slug), got %d: %v", len(reviews), reviews)
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
			if err := rp.AppendReviewEntry(e); err != nil {
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

func TestAppendFindingEntry_FlatJSONOneFilePerRun(t *testing.T) {
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
	// Two appends — should share one file, since findings is one per run.
	if err := rp.AppendFindingEntry(e); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	e.ID = "def"
	if err := rp.AppendFindingEntry(e); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := rp.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var findingFiles []string
	files, _ := os.ReadDir(rp.dir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "findings_") {
			findingFiles = append(findingFiles, f.Name())
		}
	}
	if len(findingFiles) != 1 {
		t.Fatalf("want 1 findings file (one-per-run), got %d", len(findingFiles))
	}
	wantName := "findings_" + testRunSlug + "_" + testRunTs + ".jsonl"
	if findingFiles[0] != wantName {
		t.Errorf("filename: got %q, want %q", findingFiles[0], wantName)
	}
	data, _ := os.ReadFile(filepath.Join(rp.dir, wantName))
	s := string(data)
	if !strings.Contains(s, `"id":"abc"`) || !strings.Contains(s, `"id":"def"`) {
		t.Errorf("file missing one of the appended entries: %s", s)
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
	if err := rp.AppendOutcomeEntry(bad); err == nil {
		t.Fatalf("want error for empty status")
	}
}

func TestAppendEntry_RejectsBadRunDir(t *testing.T) {
	// Path whose dir name doesn't match `run_<slug>_<ts>` should
	// surface a clear error from the appender, since the filename
	// derivation relies on parsing the dir.
	parent := t.TempDir()
	dir := filepath.Join(parent, "not-a-canonical-name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rp := &Path{dir: dir}
	e := schema.FindingEntry{
		Kind: schema.SubjectFinding,
		Finding: schema.Finding{
			ID: "x", File: "y.go", Line: 1, Title: "t",
			CreatedAt: time.Now().UTC(),
			Labels:    []string{}, References: []schema.Reference{},
		},
	}
	if err := rp.AppendFindingEntry(e); err == nil {
		t.Fatalf("want error for non-canonical run dir, got nil")
	}
}
