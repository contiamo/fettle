// Package server: review/outcome section assembly.
//
// The Finding and Group detail pages embed a review form + history feed
// and an outcome form + history feed. Building those views requires
// loading every reviews_<author>.jsonl plus outcomes.jsonl and applying
// the FETTLE.md derivedCurrentLabels semantics: latest entry per author
// replaces that author's set; cross-author display unions them.
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

// buildReviewView reads every reviews_<author>.jsonl in the run, filters
// to entries on the given subject, and returns the section view: union
// of latest-per-author labels, plus the chronological entry list with
// each author's latest entry flagged. Errors propagate so the caller
// can choose between rendering an inline error or a 500.
func buildReviewView(rp *run.Path, runName string, subject schema.Subject) (templates.ReviewSectionView, error) {
	all, err := rp.LoadAllReviews()
	if err != nil {
		return templates.ReviewSectionView{}, err
	}
	var matching []run.FlatReview
	for _, e := range all {
		if e.Subject == subject {
			matching = append(matching, e)
		}
	}

	latestByAuthor := map[string]int{}
	for i, e := range matching {
		j, ok := latestByAuthor[e.Author]
		if !ok || matching[i].At.After(matching[j].At) {
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
	for i, e := range matching {
		if e.Labels == nil {
			continue
		}
		j, ok := latestLabelsByAuthor[e.Author]
		if !ok || matching[i].At.After(matching[j].At) {
			latestLabelsByAuthor[e.Author] = i
		}
	}
	labelSet := map[string]struct{}{}
	for _, idx := range latestLabelsByAuthor {
		for _, l := range *matching[idx].Labels {
			labelSet[l] = struct{}{}
		}
	}
	currentLabels := make([]string, 0, len(labelSet))
	for l := range labelSet {
		currentLabels = append(currentLabels, l)
	}
	sort.Strings(currentLabels)

	entries := make([]templates.ReviewEntryView, len(matching))
	for i, e := range matching {
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

	// InitialLabels seeds the labels editor with the subject's own
	// LLM-set labels (Finding.Labels / Group.Labels). The reviewer
	// curates from "what the LLM said" — adds, removes, replaces —
	// rather than from the post-override effective set. A returning
	// reviewer who'd already curated sees the LLM's labels again
	// and re-applies their changes; their prior override is still
	// visible in the entries feed below the form, so nothing's lost.
	initial := []string{}
	if labels, err := subjectLabels(rp, subject); err == nil {
		initial = labels
	}

	return templates.ReviewSectionView{
		RunName:       runName,
		SubjectKind:   subject.Kind,
		InitialLabels: initial,
		SubjectID:     subject.ID,
		CurrentLabels: currentLabels,
		Entries:       entries,
	}, nil
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

// subjectLabels resolves the LLM-set labels for the given subject
// (Finding.Labels or Group.Labels). Used as the fallback initial
// pre-fill for the review form's labels editor when no reviewer has
// overridden yet — the reviewer sees the original LLM labels and
// curates from there. Returns (nil, nil) for an unknown id rather
// than failing — a missing subject is the caller's problem to
// surface, not ours.
func subjectLabels(rp *run.Path, subject schema.Subject) ([]string, error) {
	switch subject.Kind {
	case schema.SubjectFinding:
		findings, err := rp.LoadFindings()
		if err != nil {
			return nil, err
		}
		for _, f := range findings {
			if f.ID == subject.ID {
				return f.Labels, nil
			}
		}
	}
	return nil, nil
}

// buildOutcomeView reads outcomes.jsonl, filters to the subject, and
// returns the section view: chronological history with the last entry
// also surfaced as Latest. Empty history yields a nil Latest so the
// template can render the "no outcome yet" branch.
func buildOutcomeView(rp *run.Path, runName string, subject schema.Subject) (templates.OutcomeSectionView, error) {
	all, err := rp.LoadOutcomes()
	if err != nil {
		return templates.OutcomeSectionView{}, err
	}
	history := make([]schema.Outcome, 0, len(all))
	for _, o := range all {
		if o.Subject == subject {
			history = append(history, o)
		}
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].At.Before(history[j].At) })

	view := templates.OutcomeSectionView{
		RunName:     runName,
		SubjectKind: subject.Kind,
		SubjectID:   subject.ID,
		History:     history,
	}
	if len(history) > 0 {
		view.Latest = &history[len(history)-1]
	}
	return view, nil
}
