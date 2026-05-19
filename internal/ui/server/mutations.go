// Package server: POST handlers for reviews and outcomes.
//
// Each handler runs the same gate (requireIdentity → look up subject
// → validate → append → render swap). Identity errors bounce the
// client to /identity?next=… via HX-Redirect; validation errors
// re-render the section with an inline message; everything else is a
// 500. Reviews and outcomes append to per-session JSONL streams in
// the run dir; see internal/run for the append + read helpers.

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

	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/project"
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
		defer rp.Close()

		ident, ok := requireIdentity(w, r)
		if !ok {
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "parse form", http.StatusBadRequest)
			return
		}
		add, remove, labelsTouched, labelErr := parseReviewLabelOps(rp, subject.ID, r.PostForm)
		if labelErr != "" {
			renderReviewSwap(w, r, rp, runName, subject, labelErr, http.StatusBadRequest)
			return
		}
		comment := strings.TrimSpace(r.FormValue("comment"))
		severity := parseReviewSeverity(r.FormValue("severity"))

		// Empty submit (no labels touched, no comment, no severity
		// change) rejected — the entry would carry no meaning.
		if !labelsTouched && comment == "" && severity == nil {
			renderReviewSwap(w, r, rp, runName, subject, "Add at least one label, a comment, or a severity.", http.StatusBadRequest)
			return
		}

		entry := schema.ReviewEntry{
			Kind:     subject.Kind,
			ID:       subject.ID,
			Author:   ident.String(),
			At:       time.Now().UTC(),
			Add:      add,
			Remove:   remove,
			Severity: severity,
			Comment:  comment,
		}
		// Validate up front so a malformed entry surfaces as a 400
		// (inline error in the swap response) rather than the 500
		// AppendReviewEntry's internal validate would produce.
		if err := schema.ValidateReviewEntry(entry); err != nil {
			renderReviewSwap(w, r, rp, runName, subject, err.Error(), http.StatusBadRequest)
			return
		}
		if err := rp.AppendReviewEntry(entry); err != nil {
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
		defer rp.Close()

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

		entry := schema.OutcomeEntry{
			Kind:   subject.Kind,
			ID:     subject.ID,
			Author: ident.String(),
			At:     time.Now().UTC(),
			Status: status,
			PRURL:  prURL,
		}
		if err := schema.ValidateOutcomeEntry(entry); err != nil {
			renderOutcomeSwap(w, r, rp, runName, subject, err.Error(), http.StatusBadRequest)
			return
		}
		if err := rp.AppendOutcomeEntry(entry); err != nil {
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
	runDir := filepath.Join(project.RunsDir(projectDir), name)
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
		return rp.FindingEntryExists(id)
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

// parseReviewLabelOps converts the single-finding form's `labels`
// field into the add/remove arrays the JSONL entry carries. The form
// posts a snapshot of the labels the reviewer wants the finding to
// end up with; the handler diffs that against the *currently
// resolved* label set and emits add for "in target, not in current"
// + remove for "in current, not in target".
//
// labelsTouched is true when the form's labels field was present in
// the post body (the reviewer engaged the editor), regardless of
// whether the diff yields any ops. This preserves the existing
// "submit must have at least one of labels|comment|severity" check
// at the call site.
//
// Returns a non-empty errMsg when the diff can't be computed (the
// review-load fails, etc.).
func parseReviewLabelOps(rp *run.Path, findingID string, form url.Values) (add, remove []string, labelsTouched bool, errMsg string) {
	raw, ok := form["labels"]
	if !ok {
		return []string{}, []string{}, false, ""
	}
	labelsTouched = true
	target := parseLabels(strings.Join(raw, ","))
	finding, err := loadFindingEntry(rp, findingID)
	if err != nil {
		return nil, nil, false, fmt.Sprintf("load finding: %v", err)
	}
	if finding == nil {
		return nil, nil, false, "finding not found"
	}
	subjectReviews, err := loadSubjectReviews(rp, findingID)
	if err != nil {
		return nil, nil, false, fmt.Sprintf("load reviews: %v", err)
	}
	current := schema.ResolveLabels(finding.Labels, subjectReviews)
	add, remove = diffLabelSets(current, target)
	return add, remove, labelsTouched, ""
}

// diffLabelSets returns (add, remove) such that applying remove
// then add to current yields target (modulo order, which we
// normalise via the resolver). Both result slices are non-nil and
// sorted so the entry marshals deterministically.
func diffLabelSets(current, target []string) (add, remove []string) {
	currentSet := make(map[string]struct{}, len(current))
	for _, l := range current {
		currentSet[l] = struct{}{}
	}
	targetSet := make(map[string]struct{}, len(target))
	for _, l := range target {
		targetSet[l] = struct{}{}
	}
	add = []string{}
	for l := range targetSet {
		if _, ok := currentSet[l]; !ok {
			add = append(add, l)
		}
	}
	remove = []string{}
	for l := range currentSet {
		if _, ok := targetSet[l]; !ok {
			remove = append(remove, l)
		}
	}
	slicesSort(add)
	slicesSort(remove)
	return add, remove
}

// slicesSort is a tiny indirection so this file doesn't import the
// stdlib `slices` package just for one sort call when the rest of
// the file's helpers stay self-contained.
func slicesSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// uiWriterIdentity resolves the (human, agent) filename segments for
// a UI write. The UI runs as the active reviewer's identity — never
// in agent mode — so agent is always "" and human is the identity
// slug (already validated against the artifact slug class via
// internal/identity).
func uiWriterIdentity(ident identity.Resolved) (string, string, error) {
	if ident.Slug == "" {
		return "", "", fmt.Errorf("ui: identity has no slug")
	}
	if ident.IsAgent {
		// The UI doesn't drive agent mode today, but if a future
		// surface does (e.g. a "review as <agent>" affordance), the
		// human segment still needs to come from somewhere — fall back
		// to ResolveOperator's chain.
		human, err := identity.ResolveOperator()
		if err != nil {
			return "", "", err
		}
		return human, ident.Slug, nil
	}
	return ident.Slug, "", nil
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

// renderReviewSwap rebuilds the review section and renders it. HTMX
// swaps replace the section in place; non-HTMX submits get the same
// partial (the form's hx-post wraps a regular form post, so a no-JS
// user just sees the section HTML). The rebuild is what surfaces the
// entry the handler just appended.
func renderReviewSwap(w http.ResponseWriter, r *http.Request, rp *run.Path, runName string, subject schema.Subject, inlineErr string, status int) {
	finding, err := loadFindingEntry(rp, subject.ID)
	if err != nil {
		http.Error(w, fmt.Sprintf("rebuild review section: %v", err), http.StatusInternalServerError)
		return
	}
	if finding == nil {
		http.Error(w, "finding disappeared mid-request", http.StatusInternalServerError)
		return
	}
	view, err := buildReviewView(runName, rp, *finding)
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
	view, err := buildOutcomeView(runName, rp, subject.ID)
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
