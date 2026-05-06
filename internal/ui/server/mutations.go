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

func groupReviewHandler(projectDir string) http.HandlerFunc {
	return reviewPostHandler(projectDir, schema.SubjectGroup)
}

func findingOutcomeHandler(projectDir string) http.HandlerFunc {
	return outcomePostHandler(projectDir, schema.SubjectFinding)
}

func groupOutcomeHandler(projectDir string) http.HandlerFunc {
	return outcomePostHandler(projectDir, schema.SubjectGroup)
}

// reviewPostHandler is the shared body for both subject kinds. The
// caller-supplied subjectKind decides which existence check to run
// and which label the form/template uses; the rest of the flow is
// identical.
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
		labels := parseLabels(r.FormValue("labels"))
		comment := strings.TrimSpace(r.FormValue("comment"))

		// Empty submit (no labels and no comment) rejected. The CLI
		// makes the same call — see review.go's "at least one --label
		// or a --comment is required". Renders inline so the user sees
		// what's missing without losing their place.
		if len(labels) == 0 && comment == "" {
			renderReviewSwap(w, r, rp, runName, subject, "Add at least one label or a comment.", http.StatusBadRequest)
			return
		}

		entry := schema.Review{
			Subject: subject,
			Author:  ident.String(),
			Labels:  labels,
			Comment: comment,
			At:      time.Now().UTC(),
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
	case schema.SubjectGroup:
		return rp.GroupExists(id)
	default:
		return false, fmt.Errorf("unknown subject kind %q", kind)
	}
}

// stageAccepts mirrors the CLI's stage→subject-kind rules: find /
// merge / dedupe runs hold findings; group runs hold groups. Routing
// a finding mutation at a group run (or vice versa) hits this with a
// crisp 400 instead of falling through to an existence-check failure.
func stageAccepts(stage, subjectKind string) error {
	switch stage {
	case "find", "merge", "dedupe":
		if subjectKind != schema.SubjectFinding {
			return fmt.Errorf("%s runs hold findings, not %ss", stage, subjectKind)
		}
	case "group":
		if subjectKind != schema.SubjectGroup {
			return fmt.Errorf("group runs hold groups, not %ss", subjectKind)
		}
	default:
		return fmt.Errorf("unsupported run stage %q", stage)
	}
	return nil
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
