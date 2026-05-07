// Package templates renders the fettle web UI's HTML pages via templ.
package templates

import (
	"context"
	"net/url"
	"time"

	"github.com/contiamo/fettle/internal/identity"
	"github.com/contiamo/fettle/internal/schema"
)

// reviewerKey is the unexported type used as a context key so the
// templates and server packages can both reference one symbol without
// risking string-collision with other middleware.
type reviewerKey struct{}

// ReviewerContextKey is the key the server's withReviewer middleware
// uses to stash the resolved identity in the request context.
// Templates pull it back out via ReviewerFromContext. Exported as a
// var of an unexported-typed value so external packages can pass it
// through context.WithValue without being able to fabricate a
// type-equivalent key in another package.
var ReviewerContextKey = reviewerKey{}

// ReviewerFromContext returns the active reviewer identity, or nil if
// no identity is configured. Returns nil — never an error — because a
// missing identity is the "Set identity" branch in the header, not a
// failure mode.
func ReviewerFromContext(ctx context.Context) *identity.Resolved {
	if v, ok := ctx.Value(ReviewerContextKey).(*identity.Resolved); ok {
		return v
	}
	return nil
}

// requestURIKey is the context key the server's middleware uses to
// stash the original request path so the header indicator can build
// a "/identity?next=<current>" link without each handler having to
// thread the URL through manually.
type requestURIKey struct{}

// RequestURIContextKey is the exported handle for that key. Same
// pattern as ReviewerContextKey: an unexported-typed value to avoid
// cross-package key collisions.
var RequestURIContextKey = requestURIKey{}

// identityChangeHref builds the URL the header's reviewer-indicator
// links to. ?next= is the current page so saving on /identity bounces
// the user back to where they came from. Falls back to /identity (no
// next) when the request URI isn't in context — should only happen in
// tests that bypass the middleware.
func identityChangeHref(ctx context.Context) string {
	uri, _ := ctx.Value(RequestURIContextKey).(string)
	if uri == "" || uri == "/identity" {
		return "/identity"
	}
	return "/identity?next=" + url.QueryEscape(uri)
}

// CSSVersion and JSVersion are short content hashes appended to the
// asset URLs as ?v=<hash> for cache busting. They're populated by
// internal/ui/init.go from the embedded asset hashes computed in
// internal/ui/static. Empty string is acceptable (just disables cache
// busting until the assets are built).
var (
	CSSVersion string
	JSVersion  string
)

// BreadcrumbItem is one entry in the header breadcrumb. Title is the
// rendered text; Href is the link target ("" renders as plain text for
// the current page).
type BreadcrumbItem struct {
	Title string
	Href  string
}

// FindingView is the data passed to the Finding detail template.
// Bundling the manifest with the finding and the preview keeps the
// template signature tight (one arg) and makes the data shape
// reviewable independently of templ syntax.
//
// EffectiveSeverity is the severity the detail SHOULD render —
// resolves to the latest reviewer override on this finding (across
// all reviewers), falling back to Finding.Severity when no override
// exists. The boundary computes it once so every surface (header
// pill, sort, rail, list) speaks the same value.
type FindingView struct {
	Manifest          schema.RunManifest
	Finding           schema.Finding
	EffectiveSeverity *string
	Preview           CodePreview
	Review            ReviewSectionView
	Outcome           OutcomeSectionView
}

// RunFindingsView backs the three-pane findings workspace. Findings is
// the full sorted list (the left+middle panes filter it client-side);
// Selected and Detail describe the finding pre-rendered into the right
// pane for the initial GET. Both are zero-valued for an empty run.
//
// Anchors maps each finding's id to its drift state ("current",
// "shifted", "ambiguous", "stale", or "unknown") so the list-view
// rows can carry a `data-anchor` attribute and the rail can offer a
// "Hide stale" toggle. StaleCount is the number of findings whose
// state resolved to "stale"; the rail uses it both to decide whether
// to render the drift section at all and to display a count.
//
// CurrentGit is the target repo's git state at render time, used to
// compare against the scan-time snapshot in Manifest.TargetRepoGit.
// nil when git isn't available or the run has no target_repo (merge
// / dedupe). The header surfaces a subtle indicator when the two
// differ so reviewers don't mistake "everything turned stale" for a
// data problem when really they switched branches.
type RunFindingsView struct {
	Manifest   schema.RunManifest
	Findings   []schema.Finding
	Facets     FindingFacets
	Selected   *schema.Finding
	Detail     FindingView
	Anchors    map[string]string
	StaleCount int
	CurrentGit *schema.GitInfo
	// GitDrift carries the rendered git-state comparison for the
	// listing-pane header. Computed server-side from Manifest.TargetRepoGit
	// + the live git read so the template doesn't need to know the
	// rules ("hide when refs match clean", "uncomputable counts get
	// dropped"). When nil or .Show is false, the indicator is skipped.
	GitDrift *GitDrift
	// EffectiveSeverity maps a finding id to its reviewer-overridden
	// severity. Findings absent from the map keep their LLM-set
	// Finding.Severity. EffectiveSeverityOf is the helper templates
	// call to resolve a finding to its display severity.
	EffectiveSeverity map[string]string
}

// EffectiveSeverityOf returns the severity that should drive every
// display surface (rail facet, list pill, detail badge, sort) for f:
// the latest reviewer override if any, otherwise the Finding's own
// Severity. Returning a *string preserves the "no severity at all"
// case (both LLM and reviewer left it unset).
func (v RunFindingsView) EffectiveSeverityOf(f schema.Finding) *string {
	if v.EffectiveSeverity != nil {
		if s, ok := v.EffectiveSeverity[f.ID]; ok {
			return &s
		}
	}
	return f.Severity
}

// FindingFacets are the filter groups on the left rail. Severity is
// always its own group with a fixed canonical order; labels are
// further split by their `prefix:` so a noisy free-form labels list
// reads as several short, scannable sections (one per prefix).
type FindingFacets struct {
	Severities []FacetItem
	Labels     []LabelFacetGroup
}

// LabelFacetGroup is one bucket of labels that share a `prefix:`
// (e.g. "category:smell", "category:convention" → group "category").
// Title is the rendered section header; Items[].Value is the full
// label (still prefix-qualified) so the data attr stays unambiguous,
// and Items[].Display is the shorter post-prefix string the rail
// shows to keep the column quiet.
type LabelFacetGroup struct {
	Prefix string
	Title  string
	Items  []FacetItem
}

// FacetItem is one selectable row in a facet group. Display falls back
// to Value when the row is unprefixed (so the rail still reads, e.g.
// "ack" rather than nothing).
type FacetItem struct {
	Value   string
	Display string
	Count   int
}

// GitDrift is the resolved view of "what changed in the target repo
// between scan time and now". Built server-side once per render so
// the template doesn't need to re-do the comparisons or call git.
//
//   Show is false when there's nothing meaningful to surface (no scan
//   git info on the manifest, no current git readable, or both refs
//   match with a clean tree). Templates skip the indicator in that
//   case so the header stays quiet during normal review flow.
//
//   ScannedShort / CurrentShort are the 7-char abbreviations rendered
//   inline. Ahead / Behind are the rev-list --count results when the
//   refs differ; -1 means "uncomputable" (force-pushed away, weird
//   detached HEAD, git not on PATH at the time of the read) and the
//   template suppresses just that piece of data, not the whole line.
type GitDrift struct {
	Show          bool
	ScannedShort  string
	CurrentShort  string
	HeadChanged   bool
	Dirty         bool
	Ahead, Behind int
	TooltipFull   string // both full SHAs + dirty state, copy-paste friendly
}

// CodePreview is the rendered ±N-line code window for a finding.
// Error is non-empty when the preview couldn't be produced (no
// target_repo, file moved, traversal attempt, etc.) — the template
// shows a placeholder in that case rather than failing the page.
//
// Anchor describes whether the finding's anchored line still sits at
// the original location. One of "current", "shifted", "ambiguous",
// "stale", or "unknown" (legacy findings without an AnchorLine). The
// template branches on this string to surface drift to the reviewer.
// OriginalLine is the line as recorded on the finding; EffectiveLine
// is the resolved current location (0 when stale, equal to
// OriginalLine when current/unknown).
type CodePreview struct {
	Path          string
	Error         string
	Lines         []CodePreviewLine
	Target        int // 1-based line number the finding points at
	Anchor        string
	OriginalLine  int
	EffectiveLine int
}

// CodePreviewLine is one row in the preview block. Highlight marks
// the row that matches the finding's target line so the template can
// emphasize it.
type CodePreviewLine struct {
	Number    int
	Content   string
	Highlight bool
}

// GroupView is the data passed to the GroupDetail template. Members
// are the resolved findings from the input run (in the order recorded
// on the group); MissingIDs are member ids the input run no longer
// has — rendered as "missing" rather than dropped silently.
type GroupView struct {
	Manifest     schema.RunManifest
	Group        schema.Group
	InputRunName string // leaf folder name of manifest.InputRun, used for member links
	Members      []schema.Finding
	MissingIDs   []string
	Review       ReviewSectionView
	Outcome      OutcomeSectionView
}

// ReviewSectionView is the data backing the review form + history
// feed on a finding or group detail page. The same template renders
// for the initial GET and for the HTMX swap after a POST, so the view
// has to carry everything the section needs (subject id + kind for
// the form action; current state; full history; per-author latest
// hints).
type ReviewSectionView struct {
	RunName       string
	SubjectKind   string // "finding" or "group"
	SubjectID     string
	CurrentLabels []string          // union across authors' latest entries
	Entries       []ReviewEntryView // oldest first
	Error         string            // surfaced inline on validation failure
}

// ReviewEntryView is one row in the history feed. IsLatest flags the
// author's most recent entry — earlier entries by the same author are
// rendered as historical context rather than active state. Severity
// is non-nil when this entry expressed a severity judgment; the
// effective severity for the finding is the latest such entry across
// authors, falling back to Finding.Severity when none is set.
type ReviewEntryView struct {
	Author   string
	Labels   []string
	Severity *string
	Comment  string
	At       time.Time
	IsLatest bool
}

// PostURL returns the HTMX target path for the review form on this
// subject. Centralised so the template doesn't have to compose finding
// vs group paths inline.
func (v ReviewSectionView) PostURL() string {
	return "/runs/" + v.RunName + "/" + v.SubjectKind + "/" + v.SubjectID + "/review"
}

// OutcomeSectionView is the data backing the outcome form + history
// feed. Latest is nil when no outcome has been recorded yet (the form
// renders alone in that case).
type OutcomeSectionView struct {
	RunName     string
	SubjectKind string
	SubjectID   string
	Latest      *schema.Outcome
	History     []schema.Outcome // chronological, oldest first; includes Latest as last entry
	Error       string
}

// PostURL returns the HTMX target path for the outcome form.
func (v OutcomeSectionView) PostURL() string {
	return "/runs/" + v.RunName + "/" + v.SubjectKind + "/" + v.SubjectID + "/outcome"
}

