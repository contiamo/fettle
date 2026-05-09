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
	"time"

	"github.com/contiamo/fettle/internal/anchor"
	"github.com/contiamo/fettle/internal/project"
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

// runHandler renders the per-run page: a three-pane workspace over
// the run's findings.
func runHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !runNamePattern.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		runDir := filepath.Join(project.RunsDir(projectDir), name)
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
		// One pass to load every finding doc — each carries its own
		// reviews + outcomes inline. Effective severity / labels /
		// outcome computations all walk these docs without re-reading
		// any per-author or per-event file.
		docs, err := rp.LoadAllFindings()
		if err != nil {
			http.Error(w, fmt.Sprintf("load findings: %v", err), http.StatusInternalServerError)
			return
		}
		findings := make([]schema.Finding, len(docs))
		for i, d := range docs {
			findings[i] = d.Finding
		}
		sevOverrides := severityOverridesFromDocs(docs)
		lblOverrides := labelOverridesFromDocs(docs)
		outcomeMap := outcomeStatusesFromDocs(docs)
		sortFindingsBySeverity(findings, sevOverrides)
		// Re-pair docs to findings after the sort so pickSelected can
		// hand its match through to buildFindingDetail without another
		// scan.
		docByID := make(map[string]schema.FindingDoc, len(docs))
		for _, d := range docs {
			docByID[d.ID] = d
		}

		anchors, staleCount := computeAnchorStates(manifest.TargetRepo, findings)
		var currentGit *schema.GitInfo
		if manifest.TargetRepo != "" {
			currentGit = run.ReadGit(manifest.TargetRepo)
		}
		view := templates.RunFindingsView{
			Manifest:          manifest,
			Findings:          findings,
			Facets:            computeFacetsWithOverrides(findings, sevOverrides, lblOverrides, outcomeMap),
			Anchors:           anchors,
			StaleCount:        staleCount,
			CurrentGit:        currentGit,
			GitDrift:          buildGitDrift(manifest.TargetRepo, manifest.TargetRepoGit, currentGit),
			EffectiveSeverity: sevOverrides,
			EffectiveLabels:   lblOverrides,
			EffectiveOutcome:  outcomeMap,
		}
		// Pre-select the finding identified by ?focus= (the workspace
		// pushes this on row click) so refresh / share lands on the
		// same detail. With no focus, default to the first finding.
		focus := r.URL.Query().Get("focus")
		selected := pickSelected(findings, focus)
		if selected != nil {
			detail, err := buildFindingDetail(rp, manifest, docByID[selected.ID])
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

// sortFindingsBySeverity orders findings by EFFECTIVE severity (the
// latest reviewer override on a finding wins, falling back to the
// LLM's Finding.Severity) so a downgraded finding sinks below same-
// LLM-severity peers. Within a severity bucket we tie-break by file
// then line — keeps the in-bucket reading order aligned with how a
// reviewer would skim a diff. Stable sort preserves on-disk append
// order on full ties.
func sortFindingsBySeverity(findings []schema.Finding, overrides map[string]string) {
	slices.SortStableFunc(findings, func(a, b schema.Finding) int {
		ra := severityRankOfEffective(a, overrides)
		rb := severityRankOfEffective(b, overrides)
		return cmp.Or(
			cmp.Compare(ra, rb),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
		)
	})
}

// labelOverridesFromDocs returns finding-id → effective label set
// for any finding with a reviewer label override. Per-author latest
// non-nil entry wins; the union across authors is the resulting set.
// A finding whose only entries are nil-labels stays out of the map
// (no override → fall back to Finding.Labels); a finding whose
// latest-non-nil entry is &[] stays in the map with an empty slice
// (explicit clear).
func labelOverridesFromDocs(docs []schema.FindingDoc) map[string][]string {
	type stamped struct {
		labels []string
		at     time.Time
	}
	out := map[string][]string{}
	for _, d := range docs {
		perAuthor := map[string]stamped{}
		any := false
		for _, r := range d.Reviews {
			if r.Labels == nil {
				continue
			}
			any = true
			existing, ok := perAuthor[r.Author]
			if !ok || r.At.After(existing.at) {
				perAuthor[r.Author] = stamped{labels: *r.Labels, at: r.At}
			}
		}
		if !any {
			continue
		}
		seen := map[string]struct{}{}
		for _, s := range perAuthor {
			for _, l := range s.labels {
				seen[l] = struct{}{}
			}
		}
		u := make([]string, 0, len(seen))
		for l := range seen {
			u = append(u, l)
		}
		slices.Sort(u)
		out[d.ID] = u
	}
	return out
}

// severityOverridesFromDocs returns finding-id → severity-string for
// findings with a reviewer-set severity. Latest non-nil-severity
// entry wins (across all authors); findings where every entry left
// severity nil stay out of the map and inherit Finding.Severity at
// display time.
func severityOverridesFromDocs(docs []schema.FindingDoc) map[string]string {
	out := map[string]string{}
	for _, d := range docs {
		var latestAt time.Time
		var latestSev string
		found := false
		for _, r := range d.Reviews {
			if r.Severity == nil {
				continue
			}
			if !found || r.At.After(latestAt) {
				latestAt = r.At
				latestSev = *r.Severity
				found = true
			}
		}
		if found {
			out[d.ID] = latestSev
		}
	}
	return out
}

// severityRankOfEffective is severityRankOf applied to the effective
// severity of f. Inlined wrapper rather than a method on Finding so
// the override map dependency stays at the call site instead of
// leaking into the schema package.
func severityRankOfEffective(f schema.Finding, overrides map[string]string) int {
	if s, ok := overrides[f.ID]; ok {
		return severityRank(s)
	}
	return severityRankOf(f.Severity)
}

// computeFacetsWithOverrides counts severity, outcome, AND label
// buckets using the effective state per finding — a downgraded
// "high" → "medium" shifts the severity counts, a reviewer-overridden
// label set replaces the LLM's contribution to the label counts, and
// the latest outcome status drives the outcome counts. The "no
// outcome" bucket gets every finding without a recorded outcome so
// reviewers can filter to "still needs an outcome".
func computeFacetsWithOverrides(findings []schema.Finding, sevOverrides map[string]string, lblOverrides map[string][]string, outcomeMap map[string]string) templates.FindingFacets {
	sevCounts := map[string]int{}
	labelCounts := map[string]int{}
	outcomeCounts := map[string]int{}
	noOutcome := 0
	for _, f := range findings {
		sev := ""
		if s, ok := sevOverrides[f.ID]; ok {
			sev = s
		} else if f.Severity != nil {
			sev = *f.Severity
		}
		if sev != "" {
			sevCounts[sev]++
		}
		labels := f.Labels
		if override, ok := lblOverrides[f.ID]; ok {
			labels = override
		}
		for _, l := range labels {
			labelCounts[l]++
		}
		if status, ok := outcomeMap[f.ID]; ok && status != "" {
			outcomeCounts[status]++
		} else {
			noOutcome++
		}
	}
	return templates.FindingFacets{
		Severities: orderedSeverities(sevCounts),
		Outcomes:   orderedOutcomes(outcomeCounts, noOutcome),
		Labels:     groupedLabelFacets(labelCounts),
	}
}

// outcomeStatusesFromDocs returns finding-id → the latest outcome
// status for findings with at least one outcome event. Findings
// whose outcomes[] is empty (or whose latest entry has an empty
// status — corrupt) stay out of the map; the row's data-outcome
// renders as "" and the rail's "no outcome" facet matches.
func outcomeStatusesFromDocs(docs []schema.FindingDoc) map[string]string {
	out := map[string]string{}
	for _, d := range docs {
		var latestAt time.Time
		var latestStatus string
		found := false
		for _, o := range d.Outcomes {
			if !found || o.At.After(latestAt) {
				latestAt = o.At
				latestStatus = o.Status
				found = true
			}
		}
		if found && latestStatus != "" {
			out[d.ID] = latestStatus
		}
	}
	return out
}

// orderedOutcomes ranks outcome buckets by canonical workflow order
// (merged → closed → wontfix → other → free-form) with the "no
// outcome" bucket pinned last. Mirrors orderedSeverities so the rail
// reads consistently. Sentinel value "" identifies the "no outcome"
// bucket on the wire — rows whose data-outcome is empty match an
// "" filter selection.
func orderedOutcomes(counts map[string]int, noOutcome int) []templates.FacetItem {
	out := make([]templates.FacetItem, 0, len(counts)+1)
	for v, c := range counts {
		out = append(out, templates.FacetItem{Value: v, Display: v, Count: c})
	}
	slices.SortStableFunc(out, func(a, b templates.FacetItem) int {
		return cmp.Or(
			cmp.Compare(outcomeRank(a.Value), outcomeRank(b.Value)),
			cmp.Compare(a.Value, b.Value),
		)
	})
	if noOutcome > 0 {
		out = append(out, templates.FacetItem{Value: "", Display: "no outcome", Count: noOutcome})
	}
	return out
}

// outcomeRank pins the four canonical statuses to a fixed order;
// anything else lands after them but before the "no outcome" bucket
// (which orderedOutcomes appends explicitly so it stays last).
func outcomeRank(s string) int {
	switch s {
	case "merged":
		return 0
	case "closed":
		return 1
	case "wontfix":
		return 2
	case "other":
		return 3
	default:
		return 4
	}
}

// buildGitDrift composes the listing-pane git indicator data. Returns
// a GitDrift with Show=false when there's nothing useful to surface
// (refs match and the tree is clean, or git data is missing on
// either side); the template skips the row in that case so the
// header stays quiet during the common active-review case.
//
// Ahead/Behind use git rev-list --count via run.Diverged. -1 from
// either call survives in the struct so the template can render
// "(N ahead)" with the available number even if the other half is
// uncomputable (force-push edge cases, weird detached HEAD, etc).
func buildGitDrift(repoRoot string, scanned, current *schema.GitInfo) *templates.GitDrift {
	if scanned == nil || current == nil {
		return nil
	}
	headChanged := scanned.Head != current.Head
	dirty := current.Dirty
	if !headChanged && !dirty {
		return nil
	}
	d := &templates.GitDrift{
		Show:         true,
		ScannedShort: shortGitSHA(scanned.Head),
		CurrentShort: shortGitSHA(current.Head),
		HeadChanged:  headChanged,
		Dirty:        dirty,
		Ahead:        -1,
		Behind:       -1,
		TooltipFull:  gitDriftTooltip(scanned, current),
	}
	if headChanged && repoRoot != "" {
		d.Ahead, d.Behind = run.Diverged(repoRoot, scanned.Head, current.Head)
	}
	return d
}

// shortGitSHA is the conventional 7-char abbreviation; anything
// shorter (already-truncated input) is left alone.
func shortGitSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// gitDriftTooltip is the copy-paste-friendly long form: full SHAs +
// dirty state on both sides. Lives on the indicator's title attr so
// the reader can hover for the unabbreviated commit they need to
// `git log`.
func gitDriftTooltip(scanned, current *schema.GitInfo) string {
	scannedDirty := ""
	if scanned.Dirty {
		scannedDirty = " (dirty)"
	}
	currentDirty := ""
	if current.Dirty {
		currentDirty = " (dirty)"
	}
	return "Run on " + scanned.Head + scannedDirty + "; now at " + current.Head + currentDirty
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
