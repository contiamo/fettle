// Package server: review/outcome section assembly.
//
// The Finding detail page embeds a review form + history feed and an
// outcome form + history feed. With the per-finding-doc storage layout
// the doc carries both arrays inline, so the section builders just walk
// the doc — no cross-file aggregation needed.
//
// The two POST handlers (review and outcome) re-use these builders so
// the swap response renders identically to the initial GET; the
// re-load goes through rp.LoadFinding to pick up the entry the
// handler just appended.

package server

import (
	"sort"

	"github.com/contiamo/fettle/internal/schema"
	"github.com/contiamo/fettle/internal/ui/templates"
)

// buildReviewView walks the doc's Reviews slice and returns the
// section view: union of latest-per-author labels (only counting
// entries that actually touched labels), plus the chronological entry
// list with each author's latest entry flagged. The doc is the single
// source of truth — no separate JSONL load.
//
// Reviews are sorted by At before building entries: ordinary mutators
// (UpdateFinding) append in time order, but a hand-edited doc could
// be out of order. Downstream consumers (latestReviewerSeverity,
// "IsLatest" flagging) assume chronological order, so we normalise
// here.
func buildReviewView(runName string, doc schema.FindingDoc) templates.ReviewSectionView {
	reviews := make([]schema.Review, len(doc.Reviews))
	copy(reviews, doc.Reviews)
	sort.SliceStable(reviews, func(i, j int) bool { return reviews[i].At.Before(reviews[j].At) })

	latestByAuthor := map[string]int{}
	for i, e := range reviews {
		j, ok := latestByAuthor[e.Author]
		if !ok || reviews[i].At.After(reviews[j].At) {
			latestByAuthor[e.Author] = i
		}
	}
	// "Current labels" tracks each reviewer's latest entry where
	// Labels != nil — i.e., the last time they actually asserted
	// something about labels. Comment-only edits (Labels = nil) don't
	// supersede a prior override; an explicit clear (Labels = &[])
	// does. The union across all such latest-touched entries is the
	// label set the UI should treat as "currently in effect from the
	// review process".
	latestLabelsByAuthor := map[string]int{}
	for i, e := range reviews {
		if e.Labels == nil {
			continue
		}
		j, ok := latestLabelsByAuthor[e.Author]
		if !ok || reviews[i].At.After(reviews[j].At) {
			latestLabelsByAuthor[e.Author] = i
		}
	}
	labelSet := map[string]struct{}{}
	for _, idx := range latestLabelsByAuthor {
		for _, l := range *reviews[idx].Labels {
			labelSet[l] = struct{}{}
		}
	}
	currentLabels := make([]string, 0, len(labelSet))
	for l := range labelSet {
		currentLabels = append(currentLabels, l)
	}
	sort.Strings(currentLabels)

	entries := make([]templates.ReviewEntryView, len(reviews))
	for i, e := range reviews {
		var labels []string
		if e.Labels != nil {
			labels = *e.Labels
		}
		entries[i] = templates.ReviewEntryView{
			Author:        e.Author,
			Labels:        labels,
			LabelsTouched: e.Labels != nil,
			Severity:      e.Severity,
			Comment:       e.Comment,
			At:            e.At,
			IsLatest:      latestByAuthor[e.Author] == i,
		}
	}

	// InitialLabels seeds the labels editor with the LLM-set labels
	// (Finding.Labels). The reviewer curates from "what the LLM said"
	// — adds, removes, replaces — rather than from the post-override
	// effective set. A returning reviewer who'd already curated sees
	// the LLM's labels again and re-applies their changes; their
	// prior override is still visible in the entries feed below the
	// form, so nothing's lost.
	initial := doc.Labels
	if initial == nil {
		initial = []string{}
	}

	return templates.ReviewSectionView{
		RunName:       runName,
		SubjectKind:   schema.SubjectFinding,
		InitialLabels: initial,
		SubjectID:     doc.ID,
		CurrentLabels: currentLabels,
		Entries:       entries,
	}
}

// unionReviewerLabels returns the union of every per-author latest
// entry that touched labels, plus a flag indicating whether any
// reviewer touched labels at all. False = no override; the caller
// falls back to the subject's own labels.
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
	sort.Strings(out)
	return out, true
}

// buildOutcomeView walks the doc's Outcomes slice and returns the
// section view: chronological history with the last entry also
// surfaced as Latest. Empty history yields a nil Latest so the
// template can render the "no outcome yet" branch.
func buildOutcomeView(runName string, doc schema.FindingDoc) templates.OutcomeSectionView {
	history := make([]schema.Outcome, len(doc.Outcomes))
	copy(history, doc.Outcomes)
	sort.SliceStable(history, func(i, j int) bool { return history[i].At.Before(history[j].At) })

	view := templates.OutcomeSectionView{
		RunName:     runName,
		SubjectKind: schema.SubjectFinding,
		SubjectID:   doc.ID,
		History:     history,
	}
	if len(history) > 0 {
		view.Latest = &history[len(history)-1]
	}
	return view
}
