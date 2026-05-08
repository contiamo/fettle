// Package server: POST handlers for reviews and outcomes.
//
// Each handler runs the same gate (requireIdentity → look up subject
// → validate → append → render swap). Identity errors bounce the
// client to /identity?next=… via HX-Redirect; validation errors
// re-render the section with an inline message; everything else is a
// 500. Append correctness rides on internal/run's flock-based writers,
// so concurrent CLI + UI writes serialize through the kernel without
// the handler having to coordinate locks itself.

package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/ui/templates"
	"github.com/go-chi/chi/v5"
)

func findingReviewHandler(projectDir string) http.HandlerFunc {
	return reviewPostHandler(projectDir, schema.SubjectFinding)
}

func findingOutcomeHandler(projectDir string) http.HandlerFunc {
	return outcomePostHandler(projectDir, schema.SubjectFinding)
}

// reviewPostHandler is the shared body for review POSTs. Today only
// finding subjects exist; the indirection keeps the door open for
// future subject kinds without restructuring the handler.
func reviewPostHandler(projectDir, subjectKind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rp, runName, subject, ok := openSubjectForMutation(w, r, projectDir, subjectKind)
		if !ok {
			return
		}

		ident, ok := requireIdentity(w, r)
		if !ok {
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form", http.StatusBadRequest)
			return
		}
		labels := parseReviewLabels(r.PostForm)
		comment := strings.TrimSpace(r.FormValue("comment"))
		severity := parseReviewSeverity(r.FormValue("severity"))

		// Empty submit (no labels touched, no comment, no severity
		// change) rejected — the entry would carry no meaning. Note
		// that an explicit "clear my labels" (Labels = &[]) counts
		// as touched, distinguishable from "didn't touch" (nil).
		if labels == nil && comment == "" && severity == nil {
			renderReviewSwap(w, r, rp, runName, subject, "Add at least one label, a comment, or a severity.", http.StatusBadRequest)
			return
		}

		entry := schema.Review{
			Subject:  subject,
			Author:   ident.String(),
			Labels:   labels,
			Severity: severity,
			Comment:  comment,
			At:       time.Now().UTC(),
		}
		if err := rp.AppendReview(ident.Slug, entry); err != nil {
			renderReviewSwap(w, r, rp, runName, subject, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
			return
		}
		renderReviewSwap(w, r, rp, runName, subject, "", http.StatusOK)
	}
}

func outcomePostHandler(projectDir, subjectKind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rp, runName, subject, ok := openSubjectForMutation(w, r, projectDir, subjectKind)
		if !ok {
			return
		}

		ident, ok := requireIdentity(w, r)
		if !ok {
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form", http.StatusBadRequest)
			return
		}
		status, statusErr := resolveStatus(r.FormValue("status"), r.FormValue("status_other"))
		if statusErr != "" {
			renderOutcomeSwap(w, r, rp, runName, subject, statusErr, http.StatusBadRequest)
			return
		}
		prURL := strings.TrimSpace(r.FormValue("pr_url"))

		o := schema.Outcome{
			Subject: subject,
			Author:  ident.String(),
			Status:  status,
			PRURL:   prURL,
			At:      time.Now().UTC(),
		}
		if err := rp.AppendOutcome(o); err != nil {
			renderOutcomeSwap(w, r, rp, runName, subject, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
			return
		}
		renderOutcomeSwap(w, r, rp, runName, subject, "", http.StatusOK)
	}
}

// openSubjectForMutation validates the {name}/{id} URL params, opens
// the run, confirms the subject id exists in the appropriate JSONL,
// and returns the bits the handler needs. Centralised so the four
// POST handlers don't each re-implement the same eight checks.
func openSubjectForMutation(w http.ResponseWriter, r *http.Request, projectDir, subjectKind string) (*run.Path, string, schema.Subject, bool) {
	name := chi.URLParam(r, "name")
	id := chi.URLParam(r, "id")
	if !runNamePattern.MatchString(name) || !findingIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return nil, "", schema.Subject{}, false
	}
	runDir := filepath.Join(projectDir, "runs", name)
	if _, err := os.Stat(filepath.Join(runDir, "run.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			http.Error(w, fmt.Sprintf("stat run: %v", err), http.StatusInternalServerError)
		}
		return nil, "", schema.Subject{}, false
	}
	rp, err := run.Open(runDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("open run: %v", err), http.StatusInternalServerError)
		return nil, "", schema.Subject{}, false
	}
	manifest, err := rp.Manifest()
	if err != nil {
		http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
		return nil, "", schema.Subject{}, false
	}
	if err := stageAccepts(manifest.Stage, subjectKind); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, "", schema.Subject{}, false
	}

	exists, err := subjectExists(rp, subjectKind, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("check subject: %v", err), http.StatusInternalServerError)
		return nil, "", schema.Subject{}, false
	}
	if !exists {
		http.NotFound(w, r)
		return nil, "", schema.Subject{}, false
	}
	return rp, name, schema.Subject{Kind: subjectKind, ID: id}, true
}

func subjectExists(rp *run.Path, kind, id string) (bool, error) {
	switch kind {
	case schema.SubjectFinding:
		return rp.FindingExists(id)
	default:
		return false, fmt.Errorf("unknown subject kind %q", kind)
	}
}

// stageAccepts is the stage→subject-kind gate: today only "find" runs
// exist and they hold findings. Any other combination is a hard 400.
func stageAccepts(stage, subjectKind string) error {
	if stage != "find" {
		return fmt.Errorf("unsupported run stage %q", stage)
	}
	if subjectKind != schema.SubjectFinding {
		return fmt.Errorf("find runs hold findings, not %ss", subjectKind)
	}
	return nil
}

// parseReviewLabels reads the "labels" form value with nil-means-
// don't-touch semantics:
//   - The form omits the labels field entirely (or sets a hidden
//     edit-toggle to off) → no "labels" key in the post body → nil.
//     The reviewer's prior label override (if any) carries forward;
//     otherwise the LLM's Finding.Labels stay in effect.
//   - The form posts labels="" (edit-toggle on, input cleared) →
//     &[]string{} — explicit clear.
//   - The form posts labels="ack, fp" (edit-toggle on, content) →
//     &[]string{"ack","fp"} — override.
//
// We key on map presence rather than empty-string-vs-omitted
// because Go's url.Values returns "" for both. The form ensures the
// labels input is `disabled` while in "no change" preview mode so
// the field doesn't appear in PostForm at all when the user didn't
// engage the editor.
func parseReviewLabels(form url.Values) *[]string {
	raw, ok := form["labels"]
	if !ok {
		return nil
	}
	parsed := parseLabels(strings.Join(raw, ","))
	return &parsed
}

// parseReviewSeverity reads the "severity" form value. Empty (the
// "— defer —" select option, or omission) returns nil so the entry
// carries no severity opinion and the effective severity falls back
// to whatever the LLM set. Anything non-empty becomes a *string
// override; we trust the value rather than enforcing a fixed enum
// here so reviewers can score a finding "p0" or "blocker" if their
// project's vocabulary differs from high/medium/low.
func parseReviewSeverity(raw string) *string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	return &s
}

// parseLabels splits the labels input on commas and whitespace,
// trims, dedupes, and preserves first-seen order. Empty input yields
// an empty slice (a deliberate "clear my labels" submit).
func parseLabels(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// resolveStatus enforces "pick one of merged/closed/wontfix or supply
// a free-text status_other". Returns the validated status string, or
// a user-facing error message (empty string on success). The CLI
// accepts any non-empty string here too; the UI's stricter front-end
// is just to nudge consistency, not to lock down the schema.
func resolveStatus(status, other string) (string, string) {
	status = strings.TrimSpace(status)
	other = strings.TrimSpace(other)
	switch status {
	case "":
		return "", "Pick a status."
	case "merged", "closed", "wontfix":
		return status, ""
	case "other":
		if other == "" {
			return "", `Picked "other" — fill in the free-text status field.`
		}
		return other, ""
	default:
		// Any value the dropdown didn't list — accept it as-is.
		// Lets a future template add options without changing the
		// handler.
		return status, ""
	}
}

// renderReviewSwap rebuilds the section view from disk and renders it
// with optional inline error. HTMX swaps replace the section in place;
// non-HTMX submits get the same partial (the form's hx-post wraps a
// regular form post, so a no-JS user just sees the section HTML).
func renderReviewSwap(w http.ResponseWriter, r *http.Request, rp *run.Path, runName string, subject schema.Subject, inlineErr string, status int) {
	view, err := buildReviewView(rp, runName, subject)
	if err != nil {
		http.Error(w, fmt.Sprintf("rebuild review section: %v", err), http.StatusInternalServerError)
		return
	}
	view.Error = inlineErr
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := templates.ReviewSection(view).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "fettle ui: render review section: %v\n", err)
	}
}

func renderOutcomeSwap(w http.ResponseWriter, r *http.Request, rp *run.Path, runName string, subject schema.Subject, inlineErr string, status int) {
	view, err := buildOutcomeView(rp, runName, subject)
	if err != nil {
		http.Error(w, fmt.Sprintf("rebuild outcome section: %v", err), http.StatusInternalServerError)
		return
	}
	view.Error = inlineErr
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := templates.OutcomeSection(view).Render(r.Context(), w); err != nil {
		fmt.Fprintf(os.Stderr, "fettle ui: render outcome section: %v\n", err)
	}
}
