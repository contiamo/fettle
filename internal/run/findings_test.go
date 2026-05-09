package run

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// newRun is the per-test setup: create a run dir, write a minimal
// run.json plus the empty findings/ subdirectory, and return the
// opened Path.
func newRun(t *testing.T) *Path {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"),
		`{"name":"r","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	if err := os.Mkdir(filepath.Join(dir, findingsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir findings: %v", err)
	}
	rp, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return rp
}

// fixedTime parses an RFC3339 timestamp; panics on bad input. Used to
// make test docs read at a glance instead of needing time.Now().
func fixedTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return v
}

// sampleDoc returns a minimal valid FindingDoc for round-trip tests.
func sampleDoc(t *testing.T, id, file string) schema.FindingDoc {
	return schema.FindingDoc{
		Finding: schema.Finding{
			ID:          id,
			File:        file,
			Line:        1,
			Title:       "T",
			Description: "D",
			Suggestion:  "S",
			Labels:      []string{},
			References:  []schema.Reference{},
			CreatedBy:   "agent:claude",
			CreatedAt:   fixedTime(t, "2026-01-01T00:00:00Z"),
		},
	}
}

func TestWriteAndLoadFinding_roundTrip(t *testing.T) {
	rp := newRun(t)
	want := sampleDoc(t, "abc123", "internal/foo.go")
	want.Reviews = []schema.Review{{
		Author: "human:michael", Comment: "lgtm",
		At: fixedTime(t, "2026-01-02T00:00:00Z"),
	}}
	if err := rp.WriteFinding(want); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	got, err := rp.LoadFinding("abc123")
	if err != nil {
		t.Fatalf("LoadFinding: %v", err)
	}
	if got.ID != want.ID || got.File != want.File {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].Author != "human:michael" {
		t.Errorf("reviews not round-tripped: %+v", got.Reviews)
	}
}

func TestWriteFinding_rejectsExisting(t *testing.T) {
	rp := newRun(t)
	doc := sampleDoc(t, "x1", "a.go")
	if err := rp.WriteFinding(doc); err != nil {
		t.Fatalf("WriteFinding initial: %v", err)
	}
	err := rp.WriteFinding(doc)
	if err == nil {
		t.Fatalf("WriteFinding on existing: want error, got nil")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("err = %v, want wrapping fs.ErrExist", err)
	}
}

func TestWriteFinding_concurrentExclusive(t *testing.T) {
	// Two parallel WriteFinding calls on the same id: exactly one
	// should succeed; the other should see fs.ErrExist. This locks
	// in the os.Link-based publish so a future change to plain
	// rename can't silently regress to "last writer wins."
	rp := newRun(t)
	doc := sampleDoc(t, "race", "a.go")
	const N = 8
	errs := make(chan error, N)
	start := make(chan struct{})
	for range N {
		go func() {
			<-start
			errs <- rp.WriteFinding(doc)
		}()
	}
	close(start)
	successes, failures := 0, 0
	for range N {
		switch err := <-errs; {
		case err == nil:
			successes++
		case errors.Is(err, fs.ErrExist):
			failures++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 || failures != N-1 {
		t.Errorf("succ=%d, fail=%d; want exactly 1 winner among %d attempts", successes, failures, N)
	}
}

func TestLoadFinding_missing(t *testing.T) {
	rp := newRun(t)
	_, err := rp.LoadFinding("nope")
	if err == nil {
		t.Fatalf("want error on missing finding")
	}
	var nf FindingNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %v, want FindingNotFoundError", err)
	}
}

func TestLoadFinding_idMismatch(t *testing.T) {
	rp := newRun(t)
	// Hand-write a doc whose ID doesn't match its filename.
	path := filepath.Join(rp.Dir(), findingsSubdir, "abc.json")
	mustWrite(t, path, `{"id":"different","file":"x","line":1,"title":"t","description":"","suggestion":"","severity":null,"labels":null,"references":null,"created_by":"","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	_, err := rp.LoadFinding("abc")
	if err == nil || !strings.Contains(err.Error(), "mismatched id") {
		t.Errorf("err = %v, want mismatched-id error", err)
	}
}

func TestFindingPath_rejectsTraversal(t *testing.T) {
	rp := newRun(t)
	for _, bad := range []string{"../etc", "a/b", "..", ""} {
		if _, err := rp.LoadFinding(bad); err == nil {
			t.Errorf("LoadFinding(%q): want error, got nil", bad)
		}
		if _, err := rp.FindingExists(bad); err == nil {
			t.Errorf("FindingExists(%q): want error, got nil", bad)
		}
		err := rp.UpdateFinding(bad, func(d *schema.FindingDoc) error { return nil })
		if err == nil {
			t.Errorf("UpdateFinding(%q): want error, got nil", bad)
		}
	}
	// Also: WriteFinding with a bad id.
	bad := sampleDoc(t, "../slash", "x.go")
	if err := rp.WriteFinding(bad); err == nil {
		t.Errorf("WriteFinding with traversal id: want error, got nil")
	}
}

func TestFindingExists(t *testing.T) {
	rp := newRun(t)
	if ok, _ := rp.FindingExists("nope"); ok {
		t.Errorf("FindingExists(nope) = true, want false")
	}
	if err := rp.WriteFinding(sampleDoc(t, "yep", "a.go")); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	if ok, err := rp.FindingExists("yep"); err != nil || !ok {
		t.Errorf("FindingExists(yep) = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestListFindingIDs_skipsTmpAndNonJSON(t *testing.T) {
	rp := newRun(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := rp.WriteFinding(sampleDoc(t, id, "x.go")); err != nil {
			t.Fatalf("WriteFinding %s: %v", id, err)
		}
	}
	// Drop in a stale .tmp + an unrelated file, both should be ignored.
	mustWrite(t, filepath.Join(rp.Dir(), findingsSubdir, "stale.deadbeef.tmp"), "garbage")
	mustWrite(t, filepath.Join(rp.Dir(), findingsSubdir, "README.md"), "ignore me")
	ids, err := rp.ListFindingIDs()
	if err != nil {
		t.Fatalf("ListFindingIDs: %v", err)
	}
	if got, want := strings.Join(ids, ","), "a,b,c"; got != want {
		t.Errorf("ids = %q, want %q (sorted)", got, want)
	}
}

func TestListFindingIDs_missingDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "run.json"),
		`{"name":"r","stage":"find","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`+"\n")
	rp, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ids, err := rp.ListFindingIDs()
	if err != nil {
		t.Errorf("ListFindingIDs missing dir: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

func TestLoadAllFindings_skipsMalformed(t *testing.T) {
	rp := newRun(t)
	if err := rp.WriteFinding(sampleDoc(t, "good1", "a.go")); err != nil {
		t.Fatalf("WriteFinding good1: %v", err)
	}
	mustWrite(t, filepath.Join(rp.Dir(), findingsSubdir, "bad.json"), "not valid json")
	if err := rp.WriteFinding(sampleDoc(t, "good2", "b.go")); err != nil {
		t.Fatalf("WriteFinding good2: %v", err)
	}
	docs, err := rp.LoadAllFindings()
	if err != nil {
		t.Fatalf("LoadAllFindings: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("got %d docs, want 2 (bad.json should have been skipped)", len(docs))
	}
}

func TestUpdateFinding_appendsReview(t *testing.T) {
	rp := newRun(t)
	if err := rp.WriteFinding(sampleDoc(t, "u1", "a.go")); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	at := fixedTime(t, "2026-01-02T00:00:00Z")
	err := rp.UpdateFinding("u1", func(d *schema.FindingDoc) error {
		d.Reviews = append(d.Reviews, schema.Review{
			Author: "human:m", Comment: "ok", At: at,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateFinding: %v", err)
	}
	got, err := rp.LoadFinding("u1")
	if err != nil {
		t.Fatalf("LoadFinding: %v", err)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].Author != "human:m" {
		t.Errorf("after update: %+v", got.Reviews)
	}
}

func TestUpdateFinding_missingID(t *testing.T) {
	rp := newRun(t)
	err := rp.UpdateFinding("nope", func(d *schema.FindingDoc) error { return nil })
	var nf FindingNotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %v, want FindingNotFoundError", err)
	}
}

func TestUpdateFinding_rejectsMutatorIDChange(t *testing.T) {
	rp := newRun(t)
	if err := rp.WriteFinding(sampleDoc(t, "fixed", "a.go")); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	err := rp.UpdateFinding("fixed", func(d *schema.FindingDoc) error {
		d.ID = "renamed"
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "mutator changed doc.ID") {
		t.Errorf("err = %v, want mutator-id-change error", err)
	}
}

func TestUpdateFinding_atomicRenameLeavesNoTmp(t *testing.T) {
	rp := newRun(t)
	if err := rp.WriteFinding(sampleDoc(t, "atomic", "a.go")); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	for i := range 5 {
		err := rp.UpdateFinding("atomic", func(d *schema.FindingDoc) error {
			d.Reviews = append(d.Reviews, schema.Review{
				Author: "human:m", Comment: "x", At: time.Now().UTC(),
			})
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateFinding iter %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(rp.Dir(), findingsSubdir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp file: %s", e.Name())
		}
	}
}

func TestCountFindingsForFile(t *testing.T) {
	rp := newRun(t)
	for _, p := range []struct {
		id, file string
	}{
		{"f1", "a.go"},
		{"f2", "a.go"},
		{"f3", "b.go"},
	} {
		if err := rp.WriteFinding(sampleDoc(t, p.id, p.file)); err != nil {
			t.Fatalf("WriteFinding: %v", err)
		}
	}
	if got, _ := rp.CountFindingsForFile("a.go"); got != 2 {
		t.Errorf("a.go count = %d, want 2", got)
	}
	if got, _ := rp.CountFindingsForFile("b.go"); got != 1 {
		t.Errorf("b.go count = %d, want 1", got)
	}
	if got, _ := rp.CountFindingsForFile("c.go"); got != 0 {
		t.Errorf("c.go count = %d, want 0", got)
	}
}

func TestLoadAllReviews_synthesizesSubject(t *testing.T) {
	rp := newRun(t)
	doc := sampleDoc(t, "rev1", "a.go")
	doc.Reviews = []schema.Review{{
		Author: "agent:claude/sonnet", Comment: "x",
		At: fixedTime(t, "2026-01-01T00:01:00Z"),
	}}
	if err := rp.WriteFinding(doc); err != nil {
		t.Fatalf("WriteFinding: %v", err)
	}
	reviews, err := rp.LoadAllReviews()
	if err != nil {
		t.Fatalf("LoadAllReviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("got %d reviews, want 1", len(reviews))
	}
	r := reviews[0]
	if r.Subject.Kind != schema.SubjectFinding || r.Subject.ID != "rev1" {
		t.Errorf("Subject = %+v, want {finding rev1}", r.Subject)
	}
	if r.AuthorSlug != "claude" {
		t.Errorf("AuthorSlug = %q, want %q (slug strips :model)", r.AuthorSlug, "claude")
	}
}

func TestLoadAllOutcomes_synthesizesAndOrders(t *testing.T) {
	rp := newRun(t)
	for _, p := range []struct{ id, status string }{
		{"o1", "merged"},
		{"o2", "wontfix"},
	} {
		doc := sampleDoc(t, p.id, "x.go")
		doc.Outcomes = []schema.Outcome{{
			Author: "human:m", Status: p.status,
			At: fixedTime(t, "2026-01-0"+p.id[1:]+"T00:00:00Z"),
		}}
		if err := rp.WriteFinding(doc); err != nil {
			t.Fatalf("WriteFinding %s: %v", p.id, err)
		}
	}
	got, err := rp.LoadAllOutcomes()
	if err != nil {
		t.Fatalf("LoadAllOutcomes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(got))
	}
	if got[0].Status != "merged" || got[1].Status != "wontfix" {
		t.Errorf("ordering: %+v", got)
	}
	if got[0].Subject.ID != "o1" || got[0].Subject.Kind != schema.SubjectFinding {
		t.Errorf("Subject = %+v, want {finding o1}", got[0].Subject)
	}
}

func TestLoadAllReviews_chronologicalAcrossDocs(t *testing.T) {
	rp := newRun(t)
	for _, x := range []struct {
		id, comment string
		at          time.Time
	}{
		{"a", "first", fixedTime(t, "2026-01-01T00:00:00Z")},
		{"b", "third", fixedTime(t, "2026-01-03T00:00:00Z")},
		{"a", "second", fixedTime(t, "2026-01-02T00:00:00Z")},
	} {
		exists, _ := rp.FindingExists(x.id)
		if !exists {
			doc := sampleDoc(t, x.id, "x.go")
			if err := rp.WriteFinding(doc); err != nil {
				t.Fatalf("WriteFinding: %v", err)
			}
		}
		err := rp.UpdateFinding(x.id, func(d *schema.FindingDoc) error {
			d.Reviews = append(d.Reviews, schema.Review{
				Author: "human:m", Comment: x.comment, At: x.at,
			})
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateFinding: %v", err)
		}
	}
	reviews, err := rp.LoadAllReviews()
	if err != nil {
		t.Fatalf("LoadAllReviews: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(reviews) != len(want) {
		t.Fatalf("got %d reviews, want %d", len(reviews), len(want))
	}
	for i, w := range want {
		if reviews[i].Comment != w {
			t.Errorf("reviews[%d].Comment = %q, want %q", i, reviews[i].Comment, w)
		}
	}
}

func TestAtomicWriteJSON_recoversFromBadFile(t *testing.T) {
	// Round-trip a doc through atomicWriteJSON directly to confirm
	// the file ends up with valid JSON and no temp leftover.
	dir := t.TempDir()
	target := filepath.Join(dir, "x.json")
	in := map[string]any{"hello": "world"}
	if err := atomicWriteJSON(target, in); err != nil {
		t.Fatalf("atomicWriteJSON: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out["hello"] != "world" {
		t.Errorf("out = %+v, want hello=world", out)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("found %d entries, want 1 (no leftover tmp): %+v", len(entries), entries)
	}
}
