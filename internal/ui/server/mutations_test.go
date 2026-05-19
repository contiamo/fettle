package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
)

// setIdentity points the resolver at a temp file and pre-populates it
// with slug. Returns a cleanup-aware *testing.T helper that callers
// don't have to call themselves — t.Cleanup unwinds the env vars.
func setIdentity(t *testing.T, slug string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "identity")
	if slug != "" {
		if err := os.WriteFile(tmp, []byte(slug+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envSet(t, "FETTLE_IDENTITY_FILE", tmp)
	envSet(t, identity.EnvAgent, "")
	envSet(t, identity.EnvAuthor, "")
}

// envSet sets an env var for the duration of t. Empty value clears.
func envSet(t *testing.T, k, v string) {
	t.Helper()
	old, had := os.LookupEnv(k)
	if v == "" {
		os.Unsetenv(k)
	} else {
		os.Setenv(k, v)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(k, old)
		} else {
			os.Unsetenv(k)
		}
	})
}

// makeFindRun builds a minimal find run with one finding entry and
// returns (projectDir, runName, findingID).
func makeFindRun(t *testing.T) (string, string, string) {
	t.Helper()
	projectDir, runName := makeRunDir(t, "run_test01_20260101T000000Z")
	findingID := "abc123"
	writeFindingEntry(t, projectDir, runName, findingID, "")
	return projectDir, runName, findingID
}

// makeFindRunMulti is the multi-finding twin of makeFindRun for
// bulk-action tests. Returns (projectDir, runName, ids).
func makeFindRunMulti(t *testing.T, ids ...string) (string, string, []string) {
	t.Helper()
	projectDir, runName := makeRunDir(t, "run_bulk01_20260101T000000Z")
	for _, id := range ids {
		writeFindingEntry(t, projectDir, runName, id, "medium")
	}
	return projectDir, runName, ids
}

// makeRunDir writes the run folder + manifest.
func makeRunDir(t *testing.T, runName string) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	runDir := filepath.Join(project.RunsDir(projectDir), runName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "name": "` + runName + `",
  "stage": "find",
  "fettle_version": "0.1.0",
  "created_at": "2026-01-01T00:00:00Z",
  "target_repo": "/tmp/repo"
}
`
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, runName
}

// writeFindingEntry appends one entry to the run's findings_*.jsonl
// stream via the AppendFindingEntry helper. severity "" means nil.
func writeFindingEntry(t *testing.T, projectDir, runName, id, severity string) {
	t.Helper()
	runDir := filepath.Join(project.RunsDir(projectDir), runName)
	rp, err := run.Open(runDir)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	var sev *string
	if severity != "" {
		s := severity
		sev = &s
	}
	entry := schema.FindingEntry{
		Kind: schema.SubjectFinding,
		Finding: schema.Finding{
			ID:          id,
			File:        "x.go",
			Line:        1,
			Title:       "T",
			Description: "D",
			Suggestion:  "S",
			Severity:    sev,
			Labels:      []string{},
			References:  []schema.Reference{},
			CreatedBy:   "agent:claude",
			CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	if err := rp.AppendFindingEntry(entry); err != nil {
		t.Fatal(err)
	}
}

func newTestHandler(projectDir string) http.Handler {
	return New(projectDir, project.Config{})
}

func TestReviewPost_RejectsWithoutIdentity(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "") // no slug saved → resolver returns ErrNoIdentity

	form := url.Values{"labels": {"ack"}}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to /identity)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/identity?next=") {
		t.Errorf("Location = %q, want /identity?next=…", loc)
	}
}

func TestReviewPost_HTMXRedirectHeader(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "")

	form := url.Values{"labels": {"ack"}}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with HX-Redirect", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(got, "/identity?next=") {
		t.Errorf("HX-Redirect = %q, want /identity?next=…", got)
	}
}

func TestReviewPost_AppendsAndRendersSwap(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	form := url.Values{
		"labels":  {"ack, severity:high"},
		"comment": {"looks fine"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="review-section"`) {
		t.Errorf("response missing review-section root: %s", body[:min(len(body), 200)])
	}
	if !strings.Contains(body, "alice") {
		t.Errorf("response missing alice author: %s", body[:min(len(body), 200)])
	}
	if !strings.Contains(body, "looks fine") {
		t.Errorf("response missing comment: %s", body[:min(len(body), 200)])
	}

	// Verify the entry actually landed on disk.
	rp, err := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	if err != nil {
		t.Fatal(err)
	}
	all, err := rp.LoadReviewEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d reviews, want 1", len(all))
	}
	got := all[0]
	if got.Author != "human:alice" {
		t.Errorf("Author = %q, want human:alice", got.Author)
	}
	if got.Kind != schema.SubjectFinding || got.ID != findingID {
		t.Errorf("Subject = %s/%s", got.Kind, got.ID)
	}
	wantAdd := map[string]bool{"ack": true, "severity:high": true}
	if len(got.Add) != len(wantAdd) {
		t.Fatalf("Add = %v, want %v", got.Add, wantAdd)
	}
	for _, l := range got.Add {
		if !wantAdd[l] {
			t.Errorf("Add unexpectedly contains %q", l)
		}
	}
	if got.Comment != "looks fine" {
		t.Errorf("Comment = %q", got.Comment)
	}
}

func TestReviewPost_RejectsEmptySubmit(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "label, a comment, or a severity") {
		t.Errorf("body missing inline error: %s", rec.Body.String())
	}
}

func TestReviewPost_404OnUnknownFinding(t *testing.T) {
	projectDir, runName, _ := makeFindRun(t)
	setIdentity(t, "alice")

	form := url.Values{"labels": {"ack"}}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/deadbeef/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOutcomePost_AppendsWithAuthor(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	form := url.Values{
		"status": {"merged"},
		"pr_url": {"https://example.com/pr/1"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/outcome",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rp, err := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	if err != nil {
		t.Fatal(err)
	}
	all := loadFindingOutcomeEntries(t, rp, findingID)
	if len(all) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(all))
	}
	got := all[0]
	if got.Status != "merged" {
		t.Errorf("Status = %q", got.Status)
	}
	if got.Author != "human:alice" {
		t.Errorf("Author = %q, want human:alice", got.Author)
	}
	if got.PRURL != "https://example.com/pr/1" {
		t.Errorf("PRURL = %q", got.PRURL)
	}
}

func TestOutcomePost_OtherStatusUsesFreeText(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	form := url.Values{
		"status":       {"other"},
		"status_other": {"deferred-to-q3"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/outcome",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	rp, _ := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	all := loadFindingOutcomeEntries(t, rp, findingID)
	if len(all) != 1 || all[0].Status != "deferred-to-q3" {
		t.Errorf("got %+v, want status=deferred-to-q3", all)
	}
}

func TestOutcomePost_RejectsEmptyStatus(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/outcome",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestReviewView_ResolvedCurrentLabels exercises the resolver-driven
// "current labels" rendering: each entry's add/remove arrays roll
// forward in chronological order. Alice adds {ack, foo}, then later
// {fp} and removes {foo}; Bob adds {sev:high}. The view should show
// the union ack/fp/sev:high.
func TestReviewView_ResolvedCurrentLabels(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	rp, err := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()

	mustAppendReviewEntry(t, rp, schema.ReviewEntry{
		Kind: schema.SubjectFinding, ID: findingID,
		Author: "human:alice", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Add: []string{"ack", "foo"}, Remove: []string{},
	})
	mustAppendReviewEntry(t, rp, schema.ReviewEntry{
		Kind: schema.SubjectFinding, ID: findingID,
		Author: "human:alice", At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Add: []string{"fp"}, Remove: []string{"foo"},
	})
	mustAppendReviewEntry(t, rp, schema.ReviewEntry{
		Kind: schema.SubjectFinding, ID: findingID,
		Author: "human:bob", At: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		Add: []string{"sev:high"}, Remove: []string{},
	})

	finding, err := loadFindingEntry(rp, findingID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := buildReviewView(runName, rp, *finding)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"ack", "fp", "sev:high"}
	if len(view.CurrentLabels) != len(want) {
		t.Fatalf("CurrentLabels = %v, want %v", view.CurrentLabels, want)
	}
	for i := range want {
		if view.CurrentLabels[i] != want[i] {
			t.Errorf("CurrentLabels[%d] = %q, want %q", i, view.CurrentLabels[i], want[i])
		}
	}
	if len(view.Entries) != 3 {
		t.Fatalf("Entries = %d, want 3", len(view.Entries))
	}
	if view.Entries[0].IsLatest {
		t.Errorf("alice's first entry shouldn't be latest")
	}
	if !view.Entries[1].IsLatest {
		t.Errorf("alice's second entry should be latest")
	}
	if !view.Entries[2].IsLatest {
		t.Errorf("bob's only entry should be latest")
	}
}

// mustAppendReviewEntry is the new-model write helper. Validates the
// entry through the same path the handler uses so tests don't
// accidentally feed the resolver malformed data.
func mustAppendReviewEntry(t *testing.T, rp *run.Path, e schema.ReviewEntry) {
	t.Helper()
	if e.Add == nil {
		e.Add = []string{}
	}
	if e.Remove == nil {
		e.Remove = []string{}
	}
	human := schema.AuthorSlug(e.Author)
	if human == "" {
		human = "test"
	}
	if err := rp.AppendReviewEntry(e); err != nil {
		t.Fatalf("AppendReviewEntry: %v", err)
	}
}

// loadFindingOutcomeEntries returns the outcome entries targeting a
// specific finding id. Mirrors the old loadFindingOutcomes that read
// the embedded outcomes[] off the finding doc.
func loadFindingOutcomeEntries(t *testing.T, rp *run.Path, id string) []schema.OutcomeEntry {
	t.Helper()
	all, err := rp.LoadOutcomeEntries()
	if err != nil {
		t.Fatalf("LoadOutcomeEntries: %v", err)
	}
	out := make([]schema.OutcomeEntry, 0, len(all))
	for _, e := range all {
		if e.Kind == schema.SubjectFinding && e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

func TestIdentitySave_RoundTripsThroughResolver(t *testing.T) {
	projectDir := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "identity")
	envSet(t, "FETTLE_IDENTITY_FILE", tmp)
	envSet(t, identity.EnvAgent, "")
	envSet(t, identity.EnvAuthor, "")

	form := url.Values{"slug": {"michael"}}
	req := httptest.NewRequest(http.MethodPost, "/identity",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	r, err := identity.Resolve()
	if err != nil {
		t.Fatalf("resolve after save: %v", err)
	}
	if r.Slug != "michael" || r.IsAgent {
		t.Errorf("resolved = %+v, want human:michael", r)
	}
}

func TestIdentitySave_RejectsExternalNext(t *testing.T) {
	projectDir := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "identity")
	envSet(t, "FETTLE_IDENTITY_FILE", tmp)

	form := url.Values{"slug": {"michael"}, "next": {"https://evil.example/"}}
	req := httptest.NewRequest(http.MethodPost, "/identity",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
}

func TestBulkReview_AppendsOneEntryPerFinding(t *testing.T) {
	projectDir, runName, ids := makeFindRunMulti(t, "f1", "f2", "f3")
	setIdentity(t, "alice")

	form := url.Values{
		"finding_ids": ids,
		"add_label":   {"ack, fp"},
		"severity":    {"low"},
		"comment":     {"sweep"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/bulk/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %s", rec.Code, rec.Body.String())
	}
	rp, err := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	if err != nil {
		t.Fatal(err)
	}
	all, err := rp.LoadReviewEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(ids) {
		t.Fatalf("got %d entries, want %d", len(all), len(ids))
	}
	seen := map[string]bool{}
	for _, e := range all {
		seen[e.ID] = true
		if e.Author != "human:alice" {
			t.Errorf("Author = %q, want human:alice", e.Author)
		}
		if e.Severity == nil || *e.Severity != "low" {
			t.Errorf("Severity = %v, want low", e.Severity)
		}
		if e.Comment != "sweep" {
			t.Errorf("Comment = %q, want sweep", e.Comment)
		}
		if len(e.Add) != 2 {
			t.Errorf("Add = %v, want 2 entries", e.Add)
		}
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("missing entry for %s", id)
		}
	}
}

func TestBulkReview_RejectsUnknownID(t *testing.T) {
	projectDir, runName, ids := makeFindRunMulti(t, "f1", "f2")
	setIdentity(t, "alice")

	// f3 was never created — whole call must fail rather than write
	// partial entries.
	form := url.Values{
		"finding_ids": append(ids, "f3"),
		"add_label":   {"ack"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/bulk/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	rp, _ := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	all, _ := rp.LoadReviewEntries()
	if len(all) != 0 {
		t.Errorf("got %d entries, want 0 (rejected, no partial writes)", len(all))
	}
}

func TestBulkReview_RejectsEmptyPayload(t *testing.T) {
	projectDir, runName, ids := makeFindRunMulti(t, "f1")
	setIdentity(t, "alice")

	// finding_ids set but no add/remove/severity/comment — there's
	// nothing to write.
	form := url.Values{"finding_ids": ids}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/bulk/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestReviewLabelOpsDiff exercises the form-snapshot → add/remove
// translation: the form posts the labels the reviewer wants the
// finding to end up with, and the handler diffs against the
// currently resolved set.
func TestReviewLabelOpsDiff(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	setIdentity(t, "alice")

	// First submit: starting from empty seed, add ack + fp.
	form := url.Values{
		"labels": {"ack, fp"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first submit: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Wait a microsecond so the second filename's timestamp differs.
	time.Sleep(2 * time.Microsecond)

	// Second submit: post a different snapshot {ack, sev:high}. The
	// handler should diff against {ack, fp} and emit remove=[fp],
	// add=[sev:high].
	form = url.Values{
		"labels": {"ack, sev:high"},
	}
	req = httptest.NewRequest(http.MethodPost,
		"/runs/"+runName+"/finding/"+findingID+"/review",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second submit: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rp, _ := run.Open(filepath.Join(project.RunsDir(projectDir), runName))
	all, _ := rp.LoadReviewEntries()
	if len(all) != 2 {
		t.Fatalf("got %d entries, want 2", len(all))
	}
	// Find the later entry by timestamp.
	second := all[0]
	if all[1].At.After(second.At) {
		second = all[1]
	}
	if len(second.Add) != 1 || second.Add[0] != "sev:high" {
		t.Errorf("second entry Add = %v, want [sev:high]", second.Add)
	}
	if len(second.Remove) != 1 || second.Remove[0] != "fp" {
		t.Errorf("second entry Remove = %v, want [fp]", second.Remove)
	}
}
