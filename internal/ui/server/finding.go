package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/contiamo/fettle/internal/anchor"
	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/ui/templates"
	"github.com/go-chi/chi/v5"
)

// findingIDPattern matches the fettle finding-id shape: hex chars
// only (NewFindingID emits 16 hex chars; older runs may have shorter
// ids). Defends against path injection in case we ever construct a
// filesystem path from this id.
var findingIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// previewWindow is the number of lines shown before and after the
// finding's target line on the detail page. Total preview height is
// (2*previewWindow + 1) — picked so a finding at the top of a long
// function still shows a function header above it without dominating
// the viewport.
const previewWindow = 6

// findingHandler renders one finding. When the request is HTMX-driven
// (HX-Request: true) we return just the article, so the workspace can
// swap the right pane in place; otherwise we render the standalone
// page. The handler does the linear scan through findings.jsonl to
// locate the target id — for the find runs we expect (≤ a few thousand
// entries), this is fast enough that an index isn't worth the
// bookkeeping.
func findingHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		id := chi.URLParam(r, "id")
		if !runNamePattern.MatchString(name) || !findingIDPattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		runDir := filepath.Join(project.RunsDir(projectDir), name)
		if _, err := os.Stat(filepath.Join(runDir, "run.json")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, fmt.Sprintf("stat run: %v", err), http.StatusInternalServerError)
			return
		}

		rp, err := run.Open(runDir)
		if err != nil {
			http.Error(w, fmt.Sprintf("open run: %v", err), http.StatusInternalServerError)
			return
		}
		manifest, err := rp.Manifest()
		if err != nil {
			http.Error(w, fmt.Sprintf("read manifest: %v", err), http.StatusInternalServerError)
			return
		}
		if manifest.Stage != "find" {
			http.Error(w, fmt.Sprintf("findings not supported on %s runs", manifest.Stage), http.StatusBadRequest)
			return
		}

		entry, err := loadFindingEntry(rp, id)
		if err != nil {
			http.Error(w, fmt.Sprintf("load finding: %v", err), http.StatusInternalServerError)
			return
		}
		if entry == nil {
			http.NotFound(w, r)
			return
		}

		view, err := buildFindingDetail(rp, manifest, *entry)
		if err != nil {
			http.Error(w, fmt.Sprintf("build finding view: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// HTMX-issued GETs from the workspace want just the article — no
		// surrounding Layout — so the right pane can be swapped in place.
		if r.Header.Get("HX-Request") == "true" {
			if err := templates.FindingArticle(view).Render(r.Context(), w); err != nil {
				fmt.Fprintf(os.Stderr, "fettle ui: render finding fragment: %v\n", err)
			}
			return
		}
		if err := templates.Finding(view).Render(r.Context(), w); err != nil {
			fmt.Fprintf(os.Stderr, "fettle ui: render finding: %v\n", err)
		}
	}
}

// buildFindingDetail assembles a FindingView with preview + review +
// outcome sections. Shared by findingHandler (standalone page) and
// runHandler (pre-render the right pane on workspace load) so both
// paths render identical detail.
//
// Reviews and outcomes load from the run's reviews_*.jsonl and
// outcomes_*.jsonl streams; the resolver computes the effective
// severity / labels so the detail header's pills and the
// InitialLabels editor pre-fill stay in sync across every render.
func buildFindingDetail(rp *run.Path, manifest schema.RunManifest, finding schema.FindingEntry) (templates.FindingView, error) {
	preview := loadPreview(manifest.TargetRepo, finding.Finding, previewWindow)

	reviewView, err := buildReviewView(manifest.Name, rp, finding)
	if err != nil {
		return templates.FindingView{}, err
	}
	outcomeView, err := buildOutcomeView(manifest.Name, rp, finding.ID)
	if err != nil {
		return templates.FindingView{}, err
	}

	// Effective severity / labels: resolve from the same review list
	// the history feed shows. Loading reviews twice (once in
	// buildReviewView, once here) is acceptable for the detail view's
	// single-finding scope; the run-page builder amortises the
	// cost across all findings via loadEffectiveMaps.
	subjectReviews, err := loadSubjectReviews(rp, finding.ID)
	if err != nil {
		return templates.FindingView{}, err
	}
	effectiveSeverity := schema.ResolveSeverity(finding.Severity, subjectReviews)
	effectiveLabels := schema.ResolveLabels(finding.Labels, subjectReviews)

	return templates.FindingView{
		Manifest:          manifest,
		Finding:           finding.Finding,
		EffectiveSeverity: effectiveSeverity,
		EffectiveLabels:   effectiveLabels,
		Preview: templates.CodePreview{
			Path:          preview.Path,
			Error:         preview.Error,
			Lines:         toTemplateLines(preview.Lines),
			Target:        finding.Line,
			Anchor:        anchorStateToTemplate(preview.Anchor),
			OriginalLine:  preview.OriginalLine,
			EffectiveLine: preview.EffectiveLine,
		},
		Review:  reviewView,
		Outcome: outcomeView,
	}, nil
}

// loadFindingEntry returns the finding with the given id, or nil if
// no findings_*.jsonl stream contains it. If two streams contain the
// same id (rare — would mean two find runs overlapped), the latest
// `created_at` wins.
func loadFindingEntry(rp *run.Path, id string) (*schema.FindingEntry, error) {
	entries, err := rp.LoadFindingEntries()
	if err != nil {
		return nil, err
	}
	var best *schema.FindingEntry
	for i, e := range entries {
		if e.ID != id {
			continue
		}
		if best == nil || e.CreatedAt.After(best.CreatedAt) {
			best = &entries[i]
		}
	}
	return best, nil
}

// loadSubjectReviews returns every review entry referencing the
// given finding id, across all reviewers' JSONL streams. Used by
// the detail builder to feed the resolver.
func loadSubjectReviews(rp *run.Path, findingID string) ([]schema.ReviewEntry, error) {
	all, err := rp.LoadReviewEntries()
	if err != nil {
		return nil, err
	}
	out := make([]schema.ReviewEntry, 0, len(all))
	for _, e := range all {
		if e.Kind == schema.SubjectFinding && e.ID == findingID {
			out = append(out, e)
		}
	}
	return out, nil
}


// latestReviewerSeverity scans review entries (in chronological order
// — sections.go preserves that) and returns the most recent non-nil
// Severity along with true. Returns "", false when no reviewer has
// expressed a severity opinion. Mirrors severityOverrides but on the
// already-built ReviewEntryView slice so callers don't need to
// re-load reviews.
func latestReviewerSeverity(entries []templates.ReviewEntryView) (string, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Severity != nil {
			return *entries[i].Severity, true
		}
	}
	return "", false
}

// anchorStateToTemplate converts the internal anchor.State enum into
// the small string the template switches on. Keeping it as a string
// at the template boundary avoids importing internal/anchor from the
// templates package and lets the template author reason about a
// closed set of values.
func anchorStateToTemplate(s anchor.State) string {
	switch s {
	case anchor.StateCurrent:
		return "current"
	case anchor.StateShifted:
		return "shifted"
	case anchor.StateAmbiguous:
		return "ambiguous"
	case anchor.StateStale:
		return "stale"
	default:
		return "unknown"
	}
}

func toTemplateLines(in []previewLine) []templates.CodePreviewLine {
	out := make([]templates.CodePreviewLine, len(in))
	for i, l := range in {
		out[i] = templates.CodePreviewLine{
			Number:    l.Number,
			Content:   l.Content,
			Highlight: l.Highlight,
		}
	}
	return out
}
