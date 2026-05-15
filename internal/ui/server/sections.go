// Package server: review/outcome section assembly.
//
// The Finding detail page embeds a review form + history feed and an
// outcome form + history feed. Reviews and outcomes live in
// per-session JSONL streams under the run directory; section
// builders load all entries, filter to this subject, sort
// chronologically, and feed the result into the templates view types.
//
// The two POST handlers (review and outcome) re-use these builders so
// the swap response renders identically to the initial GET.

package server

import (
	"sort"

	"github.com/contiamo/fettle/internal/run"
	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/ui/templates"
)

// buildReviewView assembles the review section for one subject. It
// loads every review entry from the run's reviews_*.jsonl streams,
// filters to the entries that reference this finding, and renders the
// chronological history plus the resolver-driven "current labels" set.
//
// The current-labels set is what the template displays at the top of
// the section ("Resolved labels: …"); it's the same value
// schema.ResolveLabels would return for the finding, so every surface
// in the UI sees one consistent number.
func buildReviewView(runName string, rp *run.Path, finding schema.FindingEntry) (templates.ReviewSectionView, error) {
	all, err := rp.LoadReviewEntries()
	if err != nil {
		return templates.ReviewSectionView{}, err
	}
	subjectReviews := filterReviewsForSubject(all, schema.SubjectFinding, finding.ID)
	sort.SliceStable(subjectReviews, func(i, j int) bool {
		if subjectReviews[i].At.Equal(subjectReviews[j].At) {
			return schema.AuthorSlug(subjectReviews[i].Author) < schema.AuthorSlug(subjectReviews[j].Author)
		}
		return subjectReviews[i].At.Before(subjectReviews[j].At)
	})

	resolved := schema.ResolveLabels(finding.Labels, subjectReviews)

	latestByAuthor := map[string]int{}
	for i, e := range subjectReviews {
		j, ok := latestByAuthor[e.Author]
		if !ok || subjectReviews[i].At.After(subjectReviews[j].At) {
			latestByAuthor[e.Author] = i
		}
	}

	entries := make([]templates.ReviewEntryView, len(subjectReviews))
	for i, e := range subjectReviews {
		entries[i] = templates.ReviewEntryView{
			Author:   e.Author,
			Add:      e.Add,
			Remove:   e.Remove,
			Severity: e.Severity,
			Comment:  e.Comment,
			At:       e.At,
			IsLatest: latestByAuthor[e.Author] == i,
		}
	}

	// InitialLabels seeds the labels editor with the *currently
	// resolved* label set — what the reviewer sees on the finding
	// right now — not the LLM's seed. The single-finding form posts
	// a snapshot of the desired set, and the handler diffs it
	// against the resolved set to produce the add/remove arrays;
	// if InitialLabels were the seed instead, opening the editor
	// after someone else added labels would prefill a stale set
	// and the submit would emit spurious removes.
	initial := resolved
	if initial == nil {
		initial = []string{}
	}

	return templates.ReviewSectionView{
		RunName:       runName,
		SubjectKind:   schema.SubjectFinding,
		InitialLabels: initial,
		SubjectID:     finding.ID,
		CurrentLabels: resolved,
		Entries:       entries,
	}, nil
}

// buildOutcomeView assembles the outcome section for one subject.
// Loads every outcome entry, filters to this finding, sorts, and
// surfaces the latest as the "current outcome" plus the chronological
// history.
func buildOutcomeView(runName string, rp *run.Path, subjectID string) (templates.OutcomeSectionView, error) {
	all, err := rp.LoadOutcomeEntries()
	if err != nil {
		return templates.OutcomeSectionView{}, err
	}
	subject := filterOutcomesForSubject(all, schema.SubjectFinding, subjectID)
	sort.SliceStable(subject, func(i, j int) bool {
		if subject[i].At.Equal(subject[j].At) {
			return schema.AuthorSlug(subject[i].Author) < schema.AuthorSlug(subject[j].Author)
		}
		return subject[i].At.Before(subject[j].At)
	})

	view := templates.OutcomeSectionView{
		RunName:     runName,
		SubjectKind: schema.SubjectFinding,
		SubjectID:   subjectID,
		History:     subject,
	}
	if len(subject) > 0 {
		latest := subject[len(subject)-1]
		view.Latest = &latest
	}
	return view, nil
}

// filterReviewsForSubject returns the subset of entries whose
// (kind, id) matches.
func filterReviewsForSubject(entries []schema.ReviewEntry, kind, id string) []schema.ReviewEntry {
	out := make([]schema.ReviewEntry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == kind && e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

// filterOutcomesForSubject returns the subset of entries whose
// (kind, id) matches.
func filterOutcomesForSubject(entries []schema.OutcomeEntry, kind, id string) []schema.OutcomeEntry {
	out := make([]schema.OutcomeEntry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == kind && e.ID == id {
			out = append(out, e)
		}
	}
	return out
}
