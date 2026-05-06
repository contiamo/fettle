package server

import (
	"bufio"
	"cmp"
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

// runNamePattern enforces that the {name} URL segment can only contain
// the characters a fettle run folder may have. Defending against path
// traversal (`..`, `/`) and invalid filesystem characters here means
// the handler can join the segment with the project's runs/ dir
// without further sanitization.
var runNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// runHandler renders the per-run page. For find / merge / dedupe runs
// it renders the three-pane workspace; for group runs it lists groups.
// The stage is taken from run.json so the template choice is data-
// driven, not URL-driven (the URL just identifies the run folder).
func runHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !runNamePattern.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		runDir := filepath.Join(projectDir, "runs", name)
		// Stat run.json directly: this distinguishes "no such run" from
		// "subdirectory exists but isn't a fettle run", and both deserve
		// a 404 rather than a 500.
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

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch manifest.Stage {
		case "group":
			groups, err := rp.LoadGroups()
			if err != nil {
				http.Error(w, fmt.Sprintf("load groups: %v", err), http.StatusInternalServerError)
				return
			}
			if err := templates.RunGroups(manifest, groups).Render(r.Context(), w); err != nil {
				fmt.Fprintf(os.Stderr, "fettle ui: render groups: %v\n", err)
			}
		case "find", "merge", "dedupe":
			findings, err := rp.LoadFindings()
			if err != nil {
				http.Error(w, fmt.Sprintf("load findings: %v", err), http.StatusInternalServerError)
				return
			}
			sortFindings(findings)

			anchors, staleCount := computeAnchorStates(manifest.TargetRepo, findings)
			var currentGit *schema.GitInfo
			if manifest.TargetRepo != "" {
				currentGit = run.ReadGit(manifest.TargetRepo)
			}
			view := templates.RunFindingsView{
				Manifest:   manifest,
				Findings:   findings,
				Facets:     computeFacets(findings),
				Anchors:    anchors,
				StaleCount: staleCount,
				CurrentGit: currentGit,
			}
			// Pre-select the finding identified by ?focus= (the workspace
			// pushes this on row click) so refresh / share lands on the
			// same detail. With no focus, default to the first finding.
			focus := r.URL.Query().Get("focus")
			selected := pickSelected(findings, focus)
			if selected != nil {
				detail, err := buildFindingDetail(rp, manifest, *selected)
				if err != nil {
					http.Error(w, fmt.Sprintf("build detail: %v", err), http.StatusInternalServerError)
					return
				}
				view.Selected = selected
				view.Detail = detail
			}

			if err := templates.RunFindings(view).Render(r.Context(), w); err != nil {
				fmt.Fprintf(os.Stderr, "fettle ui: render findings: %v\n", err)
			}
		default:
			http.Error(w, fmt.Sprintf("unsupported stage %q", manifest.Stage), http.StatusInternalServerError)
		}
	}
}

// pickSelected returns the finding whose ID matches focus, or the
// first finding when no match is found. nil only when the slice is
// empty — keeps the empty-state branch single-purpose.
func pickSelected(findings []schema.Finding, focus string) *schema.Finding {
	if len(findings) == 0 {
		return nil
	}
	if focus != "" {
		for i := range findings {
			if findings[i].ID == focus {
				return &findings[i]
			}
		}
	}
	return &findings[0]
}

// computeFacets extracts the unique severities and labels present in
// the run, with counts. Severities are bucketed into a fixed canonical
// order (high → medium → low → unrecognised → none) so the rail reads
// the same regardless of which value the agent emitted first. Labels
// are split into one group per `prefix:` (e.g. "category:smell" sits
// in the "category" group); unprefixed labels collapse into a single
// "Other" bucket.
func computeFacets(findings []schema.Finding) templates.FindingFacets {
	sevCounts := map[string]int{}
	labelCounts := map[string]int{}
	for _, f := range findings {
		if f.Severity != nil && *f.Severity != "" {
			sevCounts[*f.Severity]++
		}
		for _, l := range f.Labels {
			labelCounts[l]++
		}
	}

	return templates.FindingFacets{
		Severities: orderedSeverities(sevCounts),
		Labels:     groupedLabelFacets(labelCounts),
	}
}

// orderedSeverities ranks severities by their canonical bucket so the
// rail reads high → low even if the agent emitted them in mixed order.
// Within a bucket, ties break alphabetically.
func orderedSeverities(counts map[string]int) []templates.FacetItem {
	out := make([]templates.FacetItem, 0, len(counts))
	for v, c := range counts {
		out = append(out, templates.FacetItem{Value: v, Display: v, Count: c})
	}
	slices.SortStableFunc(out, func(a, b templates.FacetItem) int {
		return cmp.Or(
			cmp.Compare(severityRank(a.Value), severityRank(b.Value)),
			cmp.Compare(a.Value, b.Value),
		)
	})
	return out
}

// severityRank maps a severity to its canonical position in the rail.
// Mirrors the buckets in templates.severityVariant; unrecognised
// values land below the named buckets but above "(none)".
func severityRank(s string) int {
	switch templates.SeverityKey(s) {
	case "critical", "high", "p0", "p1":
		return 0
	case "medium", "med", "p2":
		return 1
	case "low", "info", "informational", "p3", "p4":
		return 2
	default:
		return 3
	}
}

// groupedLabelFacets splits labels by their "prefix:" segment. A label
// without a colon collapses into the "Other" group at the bottom.
// Within each group items sort alphabetically by display name.
func groupedLabelFacets(counts map[string]int) []templates.LabelFacetGroup {
	type bucket struct {
		prefix string
		items  []templates.FacetItem
	}
	byPrefix := map[string]*bucket{}
	for value, count := range counts {
		prefix, display := splitLabelPrefix(value)
		b, ok := byPrefix[prefix]
		if !ok {
			b = &bucket{prefix: prefix}
			byPrefix[prefix] = b
		}
		b.items = append(b.items, templates.FacetItem{Value: value, Display: display, Count: count})
	}
	// Stable order: prefixed groups alphabetically, then "Other"
	// (unprefixed) at the end.
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	slices.SortFunc(prefixes, func(a, b string) int {
		// Empty prefix ("Other") sinks to the end regardless of
		// alphabetical order; otherwise straight string compare.
		if (a == "") != (b == "") {
			if b == "" {
				return -1
			}
			return 1
		}
		return cmp.Compare(a, b)
	})

	groups := make([]templates.LabelFacetGroup, 0, len(prefixes))
	for _, p := range prefixes {
		b := byPrefix[p]
		slices.SortStableFunc(b.items, func(a, c templates.FacetItem) int {
			return cmp.Compare(a.Display, c.Display)
		})
		groups = append(groups, templates.LabelFacetGroup{
			Prefix: p,
			Title:  prefixTitle(p),
			Items:  b.items,
		})
	}
	return groups
}

// splitLabelPrefix returns ("category", "smell") for "category:smell"
// and ("", "ack") for an unprefixed label. We only split on the first
// colon, so values that themselves contain ":" stay intact in the
// display half.
func splitLabelPrefix(label string) (prefix, display string) {
	for i := 0; i < len(label); i++ {
		if label[i] == ':' {
			return label[:i], label[i+1:]
		}
	}
	return "", label
}

// prefixTitle is the human-facing rail header for a label prefix.
// "category" → "Category"; the empty prefix renders as "Other".
func prefixTitle(prefix string) string {
	if prefix == "" {
		return "Other"
	}
	// Capitalise the first byte; label prefixes are ASCII in practice
	// (they go into JSON keys + URL slugs), so we don't need full
	// Unicode handling here.
	if prefix[0] >= 'a' && prefix[0] <= 'z' {
		return string(prefix[0]-32) + prefix[1:]
	}
	return prefix
}

// sortFindings orders findings by severity bucket (high → medium →
// low → unrecognised → none) so the most pressing items lead the
// list. Within a bucket we fall back to file then line, which keeps
// the in-bucket reading order aligned with how a reviewer would skim
// a diff. Stable sort means findings that match on every key keep
// their on-disk append order.
func sortFindings(findings []schema.Finding) {
	slices.SortStableFunc(findings, func(a, b schema.Finding) int {
		return cmp.Or(
			cmp.Compare(severityRankOf(a.Severity), severityRankOf(b.Severity)),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
		)
	})
}

// severityRankOf is the *string-aware variant of severityRank: a nil
// or empty severity ranks below every named bucket so unscored
// findings sink to the bottom.
func severityRankOf(s *string) int {
	if s == nil || *s == "" {
		return 4
	}
	return severityRank(*s)
}

// computeAnchorStates resolves drift state for every finding so the
// list-view rows can carry a data-anchor attribute. Reading happens
// once per unique file via fileLineCache below — large runs typically
// concentrate findings into a handful of files, so caching turns
// O(findings * file) into roughly O(unique_files * file + findings).
//
// repoRoot empty (merge / dedupe runs without a target_repo on the
// manifest) returns "unknown" for every finding without any I/O —
// drift is meaningless when we don't know which checkout to compare
// against. Per-file read failures (missing file, traversal-rejected,
// I/O error) likewise mark the affected findings unknown rather than
// failing the whole page.
func computeAnchorStates(repoRoot string, findings []schema.Finding) (map[string]string, int) {
	states := make(map[string]string, len(findings))
	stale := 0
	if repoRoot == "" {
		for _, f := range findings {
			states[f.ID] = anchorStateName(anchor.StateUnknown)
		}
		return states, 0
	}
	cache := newFileLineCache()
	for _, f := range findings {
		lines, truncated, ok := cache.get(repoRoot, f.File)
		if !ok {
			states[f.ID] = anchorStateName(anchor.StateUnknown)
			continue
		}
		res := anchor.ApplyTruncationDemotion(anchor.ResolveFromLines(lines, f), truncated)
		name := anchorStateName(res.State)
		states[f.ID] = name
		if res.State == anchor.StateStale {
			stale++
		}
	}
	return states, stale
}

// anchorStateName mirrors the wire vocabulary the templates use. Kept
// here so the templates package doesn't need to import anchor (and so
// callers can treat the value as opaque from Go's perspective).
func anchorStateName(s anchor.State) string {
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

// fileLineCache memoises one file's contents (and whether the read
// was truncated by bufio.ErrTooLong) for the lifetime of a single
// request. The negative-cache entry (lines=nil, ok=false) prevents
// retrying I/O on every finding that lives in the same broken file.
type fileLineCache struct {
	entries map[string]fileLineCacheEntry
}

type fileLineCacheEntry struct {
	lines     []string
	truncated bool
	ok        bool
}

func newFileLineCache() *fileLineCache {
	return &fileLineCache{entries: map[string]fileLineCacheEntry{}}
}

func (c *fileLineCache) get(repoRoot, repoRel string) ([]string, bool, bool) {
	if e, found := c.entries[repoRel]; found {
		return e.lines, e.truncated, e.ok
	}
	lines, truncated, err := readRepoFileLines(repoRoot, repoRel)
	e := fileLineCacheEntry{lines: lines, truncated: truncated, ok: err == nil}
	c.entries[repoRel] = e
	return e.lines, e.truncated, e.ok
}

// readRepoFileLines is the list-view counterpart of loadPreview's file
// scan: same buffer settings, same soft-handling of bufio.ErrTooLong.
// We don't share code with loadPreview because that function bundles
// safeJoin + window-rendering + drift detection in one pass and lives
// behind a different result type; pulling out a shared scanner adds
// more indirection than it saves.
func readRepoFileLines(repoRoot, repoRel string) ([]string, bool, error) {
	abs, err := safeJoin(repoRoot, repoRel)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return lines, true, nil
		}
		return nil, false, err
	}
	return lines, false, nil
}
