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

// makeFindRun builds a minimal find run with one finding and returns
// (projectDir, runName, findingID). Manifest is hand-rolled so the
// test doesn't depend on the higher-level Create* helpers.
func makeFindRun(t *testing.T) (string, string, string) {
	t.Helper()
	projectDir := t.TempDir()
	runName := "find_20260101T000000Z_test"
	runDir := filepath.Join(projectDir, "runs", runName)
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
	findingID := "abc123"
	if err := os.WriteFile(filepath.Join(runDir, "findings.jsonl"),
		[]byte(`{"id":"`+findingID+`","file":"x.go","line":1,"title":"T","description":"D","suggestion":"S","severity":null,"labels":[],"references":[],"created_by":"agent:claude","created_at":"2026-01-01T00:00:00Z"}`+"\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, runName, findingID
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

	// Verify the entry actually landed on disk via the run.Path API.
	rp, err := run.Open(filepath.Join(projectDir, "runs", runName))
	if err != nil {
		t.Fatal(err)
	}
	all, err := rp.LoadAllReviews()
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
	if got.AuthorSlug != "alice" {
		t.Errorf("AuthorSlug = %q, want alice", got.AuthorSlug)
	}
	if got.Subject.Kind != schema.SubjectFinding || got.Subject.ID != findingID {
		t.Errorf("Subject = %+v", got.Subject)
	}
	wantLabels := []string{"ack", "severity:high"}
	if len(got.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", got.Labels, wantLabels)
	}
	for i := range wantLabels {
		if got.Labels[i] != wantLabels[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, got.Labels[i], wantLabels[i])
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
	if !strings.Contains(rec.Body.String(), "label or a comment") {
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

	rp, err := run.Open(filepath.Join(projectDir, "runs", runName))
	if err != nil {
		t.Fatal(err)
	}
	all, err := rp.LoadOutcomes()
	if err != nil {
		t.Fatal(err)
	}
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
	rp, _ := run.Open(filepath.Join(projectDir, "runs", runName))
	all, _ := rp.LoadOutcomes()
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

func TestReviewView_DerivedCurrentLabels(t *testing.T) {
	projectDir, runName, findingID := makeFindRun(t)
	rp, err := run.Open(filepath.Join(projectDir, "runs", runName))
	if err != nil {
		t.Fatal(err)
	}
	subject := schema.Subject{Kind: schema.SubjectFinding, ID: findingID}

	// alice: two entries, latest wins (replaces, not unions)
	mustAppendReview(t, rp, "alice", schema.Review{Subject: subject, Labels: []string{"old", "ack"}, At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	mustAppendReview(t, rp, "alice", schema.Review{Subject: subject, Labels: []string{"ack", "fp"}, At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)})
	// bob: one entry
	mustAppendReview(t, rp, "bob", schema.Review{Subject: subject, Labels: []string{"sev:high"}, At: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)})

	view, err := buildReviewView(rp, runName, subject)
	if err != nil {
		t.Fatal(err)
	}

	// alice's "old" must be gone (replaced by her later set), bob's
	// "sev:high" must appear.
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
	// Latest flagging: alice's first entry must NOT be flagged latest;
	// her second entry and bob's only entry must be.
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

func mustAppendReview(t *testing.T, rp *run.Path, author string, r schema.Review) {
	t.Helper()
	// The author slug is what the filename uses for routing/locking;
	// the record's Author field is the canonical "who reviewed this"
	// stamp that buildReviewView keys on. Default it to the human form
	// of the slug so tests don't have to repeat it on every literal.
	if r.Author == "" {
		r.Author = "human:" + author
	}
	if err := rp.AppendReview(author, r); err != nil {
		t.Fatalf("AppendReview: %v", err)
	}
}

func TestIdentitySave_RoundTripsThroughResolver(t *testing.T) {
	projectDir := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "identity")
	envSet(t, "FETTLE_IDENTITY_FILE", tmp)
	envSet(t, identity.EnvAgent, "")
	envSet(t, identity.EnvAuthor, "")

	form := url.Values{
		"slug": {"alice"},
		"next": {"/runs/abc"},
	}
	req := httptest.NewRequest(http.MethodPost, "/identity",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/runs/abc" {
		t.Errorf("Location = %q, want /runs/abc", got)
	}
	r, err := identity.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Slug != "alice" {
		t.Errorf("persisted slug = %q", r.Slug)
	}
}

func TestIdentitySave_RejectsExternalNext(t *testing.T) {
	projectDir := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "identity")
	envSet(t, "FETTLE_IDENTITY_FILE", tmp)
	envSet(t, identity.EnvAgent, "")
	envSet(t, identity.EnvAuthor, "")

	form := url.Values{
		"slug": {"alice"},
		"next": {"https://evil.example.com/"},
	}
	req := httptest.NewRequest(http.MethodPost, "/identity",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newTestHandler(projectDir).ServeHTTP(rec, req)

	// Open-redirect prevention: next must be a relative path.
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want / (rejected external)", got)
	}
}
