// Bulk-action handlers. The single-finding mutation handlers in
// mutations.go each take one subject and append one record; bulk
// handlers take a list of finding ids and append one record per id
// against the same author/payload, so a reviewer can ack a hundred
// "low / convention" findings in one trip.
//
// The matching/exclude semantics described on the client side
// (findings.ts) are resolved into a flat ids list before the request
// hits this layer. The wire format is {finding_ids: [...]} plus
// add_label / remove_label / severity / comment. Each id appends one
// ReviewEntry to the same reviews_*.jsonl stream — same author/at,
// same delta — so the bulk action reads as a single coherent
// audit-trail block.

package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/go-chi/chi/v5"
)

// bulkReviewHandler appends one Review entry per finding_id under the
// authenticated reviewer's reviews_<slug>.jsonl. All entries share a
// single timestamp + payload so the bulk action reads as one
// reviewer-event in the audit trail. The set of ids is taken at face
// value; we validate each one has a finding doc on disk and reject
// the whole call if any id is unknown (partial writes would leave
// the audit trail confusing).
func bulkReviewHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runName, rp, ok := openRunForBulkMutation(w, r, projectDir)
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
		ids := r.PostForm["finding_ids"]
		if len(ids) == 0 {
			http.Error(w, "no finding_ids; pick at least one finding", http.StatusBadRequest)
			return
		}
		// Reject the entire request rather than partially apply if any
		// id is unknown. Bulk actions are easier to reason about when
		// "all or nothing" — the user always sees either every id
		// updated or none, never a half-finished log.
		known, err := loadFindingIDSet(rp)
		if err != nil {
			http.Error(w, fmt.Sprintf("load findings: %v", err), http.StatusInternalServerError)
			return
		}
		for _, id := range ids {
			if _, exists := known[id]; !exists {
				http.Error(w, fmt.Sprintf("unknown finding id: %s", id), http.StatusBadRequest)
				return
			}
		}

		// Bulk reviews carry a label delta (add/remove arrays) directly
		// — there's no per-finding "current" to diff against because
		// each finding has its own resolved set. The UI form sends
		// `labels` (additive — the bulk editor only ever asks "add
		// these"); programmatic callers can also send `add_label`
		// and `remove_label` for explicit removal.
		addLabels := parseLabels(strings.Join(append(
			append([]string(nil), r.PostForm["labels"]...),
			r.PostForm["add_label"]...,
		), ","))
		removeLabels := parseLabels(strings.Join(r.PostForm["remove_label"], ","))
		severity := parseReviewSeverity(r.FormValue("severity"))
		comment := strings.TrimSpace(r.FormValue("comment"))
		if len(addLabels) == 0 && len(removeLabels) == 0 && severity == nil && comment == "" {
			http.Error(w, "set at least one of labels, add_label, remove_label, severity, or comment", http.StatusBadRequest)
			return
		}
		if overlap, ok := firstSharedLabel(addLabels, removeLabels); ok {
			http.Error(w, fmt.Sprintf("label %q appears in both add and remove", overlap), http.StatusBadRequest)
			return
		}

		now := time.Now().UTC()
		stamp := ident.String()
		defer rp.Close()
		// Validate one representative entry up front. Add/Remove/
		// Severity/Comment are identical across ids; the only thing
		// that varies is the subject id, which we already validated
		// against the run's id set above. Failing here surfaces
		// "invalid payload" once with a 400 instead of N times with
		// 500s.
		probe := schema.ReviewEntry{
			Kind: schema.SubjectFinding, ID: ids[0], Author: stamp, At: now,
			Add: addLabels, Remove: removeLabels, Severity: severity, Comment: comment,
		}
		if err := schema.ValidateReviewEntry(probe); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, id := range ids {
			entry := schema.ReviewEntry{
				Kind:     schema.SubjectFinding,
				ID:       id,
				Author:   stamp,
				At:       now,
				Add:      addLabels,
				Remove:   removeLabels,
				Severity: severity,
				Comment:  comment,
			}
			if err := rp.AppendReviewEntry(entry); err != nil {
				http.Error(w, fmt.Sprintf("save review for %s: %v", id, err), http.StatusInternalServerError)
				return
			}
		}

		// Bounce back to the run page. Bulk actions are full-page POSTs
		// (forms inside the listing pane, not HTMX swaps) — refreshing
		// the whole pane is the simplest way to surface the new
		// effective severity / label state without partial-update
		// gymnastics.
		http.Redirect(w, r, "/runs/"+runName, http.StatusSeeOther)
	}
}

// openRunForBulkMutation is the bulk equivalent of openSubjectForMutation
// for routes that take only {name} (no subject id). Returns the run
// name + opened path, or writes a 404 / 500 and reports false.
func openRunForBulkMutation(w http.ResponseWriter, r *http.Request, projectDir string) (string, *run.Path, bool) {
	name := chi.URLParam(r, "name")
	if !runNamePattern.MatchString(name) {
		http.NotFound(w, r)
		return "", nil, false
	}
	rp, err := run.Open(filepath.Join(project.RunsDir(projectDir), name))
	if err != nil {
		http.Error(w, fmt.Sprintf("open run: %v", err), http.StatusInternalServerError)
		return "", nil, false
	}
	return name, rp, true
}

// firstSharedLabel returns the first label in a that also appears
// in b (with `true`), or `"", false` if there is no overlap. Used by
// the bulk handler to surface "label X is in both add and remove"
// as a 400 before the entry hits AppendReviewEntry's validator.
func firstSharedLabel(a, b []string) (string, bool) {
	bset := make(map[string]struct{}, len(b))
	for _, l := range b {
		bset[l] = struct{}{}
	}
	for _, l := range a {
		if _, ok := bset[l]; ok {
			return l, true
		}
	}
	return "", false
}

// loadFindingIDSet returns the set of finding ids in the run for
// existence-checks. Scans every findings_*.jsonl stream once and
// builds a small set — cheap enough at the cardinalities fettle
// targets that a persistent index isn't worth the bookkeeping.
func loadFindingIDSet(rp *run.Path) (map[string]struct{}, error) {
	entries, err := rp.LoadFindingEntries()
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		out[e.ID] = struct{}{}
	}
	return out, nil
}
