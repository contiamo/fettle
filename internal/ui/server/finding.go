package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/contiamo/fettle/internal/anchor"
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
		runDir := filepath.Join(projectDir, "runs", name)
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
		switch manifest.Stage {
		case "find", "merge", "dedupe":
			// supported
		default:
			http.Error(w, fmt.Sprintf("findings not supported on %s runs", manifest.Stage), http.StatusBadRequest)
			return
		}

		findings, err := rp.LoadFindings()
		if err != nil {
			http.Error(w, fmt.Sprintf("load findings: %v", err), http.StatusInternalServerError)
			return
		}
		var found *schema.Finding
		for i := range findings {
			if findings[i].ID == id {
				found = &findings[i]
				break
			}
		}
		if found == nil {
			http.NotFound(w, r)
			return
		}

		view, err := buildFindingDetail(rp, manifest, *found)
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
// Reviews are loaded once here and used for two things: building the
// review section (per-author entries + current-labels union) and
// resolving effective severity (latest reviewer Severity override
// across authors, falling back to Finding.Severity). The detail
// header's SeverityPill renders the resolved value.
func buildFindingDetail(rp *run.Path, manifest schema.RunManifest, f schema.Finding) (templates.FindingView, error) {
	preview := loadPreview(manifest.TargetRepo, f, previewWindow)

	subject := schema.Subject{Kind: schema.SubjectFinding, ID: f.ID}
	reviewView, err := buildReviewView(rp, manifest.Name, subject)
	if err != nil {
		return templates.FindingView{}, fmt.Errorf("load reviews: %w", err)
	}
	outcomeView, err := buildOutcomeView(rp, manifest.Name, subject)
	if err != nil {
		return templates.FindingView{}, fmt.Errorf("load outcomes: %w", err)
	}

	effective := f.Severity
	if override, ok := latestReviewerSeverity(reviewView.Entries); ok {
		effective = &override
	}

	// Effective labels: union of every per-author latest entry that
	// touched labels; falls back to Finding.Labels when no reviewer
	// has overridden. Computed off the already-loaded review entries.
	effectiveLabels := f.Labels
	if union, ok := unionReviewerLabels(reviewView.Entries); ok {
		effectiveLabels = union
	}

	return templates.FindingView{
		Manifest:          manifest,
		Finding:           f,
		EffectiveSeverity: effective,
		EffectiveLabels:   effectiveLabels,
		Preview: templates.CodePreview{
			Path:          preview.Path,
			Error:         preview.Error,
			Lines:         toTemplateLines(preview.Lines),
			Target:        f.Line,
			Anchor:        anchorStateToTemplate(preview.Anchor),
			OriginalLine:  preview.OriginalLine,
			EffectiveLine: preview.EffectiveLine,
		},
		Review:  reviewView,
		Outcome: outcomeView,
	}, nil
}

// unionReviewerLabels returns the union of every per-author latest
// entry's labels, plus a flag indicating whether any reviewer
// touched labels at all. When ok=false the caller falls back to
// Finding.Labels; when ok=true the slice (possibly empty) is the
// reviewer-asserted set. Walks entries in chronological order
// (sections.go's invariant) so per-author latest is the last seen.
func unionReviewerLabels(entries []templates.ReviewEntryView) ([]string, bool) {
	latestByAuthor := map[string][]string{}
	any := false
	for _, e := range entries {
		if !e.LabelsTouched {
			continue
		}
		any = true
		latestByAuthor[e.Author] = e.Labels
	}
	if !any {
		return nil, false
	}
	seen := map[string]struct{}{}
	for _, ls := range latestByAuthor {
		for _, l := range ls {
			seen[l] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	slices.Sort(out)
	return out, true
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
