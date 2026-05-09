// Bulk-action handlers. The single-finding mutation handlers in
// mutations.go each take one subject and append one record; bulk
// handlers take a list of finding ids and append one record per id
// against the same author/payload, so a reviewer can ack a hundred
// "low / convention" findings in one trip.
//
// The matching/exclude semantics described on the client side
// (findings.ts) are resolved into a flat ids list before the request
// hits this layer — the wire format is always {finding_ids: [...]}
// plus the same Labels/Severity/Comment fields the per-finding form
// accepts. Each id flows through a separate UpdateFinding call; the
// docs are independent files so there's no shared lock to contend on.

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

		labels := parseReviewLabels(r.PostForm)
		severity := parseReviewSeverity(r.FormValue("severity"))
		comment := strings.TrimSpace(r.FormValue("comment"))
		if labels == nil && severity == nil && comment == "" {
			http.Error(w, "set at least one of labels, severity, or comment", http.StatusBadRequest)
			return
		}

		now := time.Now().UTC()
		stamp := ident.String()
		for _, id := range ids {
			entry := schema.Review{
				Author:   stamp,
				Labels:   labels,
				Severity: severity,
				Comment:  comment,
				At:       now,
			}
			if err := rp.UpdateFinding(id, func(d *schema.FindingDoc) error {
				d.Reviews = append(d.Reviews, entry)
				return nil
			}); err != nil {
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

// loadFindingIDSet returns the set of finding ids in the run for
// existence-checks. ListFindingIDs walks the findings/ directory
// directly — much cheaper than parsing every doc just to know which
// ids are valid.
func loadFindingIDSet(rp *run.Path) (map[string]struct{}, error) {
	ids, err := rp.ListFindingIDs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}
