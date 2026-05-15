package schema

import (
	"fmt"
	"slices"
	"sort"
)

// ValidateReviewEntry checks that a ReviewEntry is well-formed for
// writing: required fields populated, Add and Remove are non-nil
// slices (so the marshalled JSON is `"add":[]` not `"add":null` for
// empty entries — the wire contract is "required array, may be
// empty"), no label is the empty string, and Add ∩ Remove is empty.
// Same label in both arrays is rejected because the resolver
// otherwise has no order-independent semantic to apply within one
// entry — almost certainly a UI bug to emit such an entry, so we
// fail at the boundary rather than guessing intent.
func ValidateReviewEntry(e ReviewEntry) error {
	if e.Kind == "" {
		return fmt.Errorf("review entry: kind is required")
	}
	if e.ID == "" {
		return fmt.Errorf("review entry: id is required")
	}
	if e.Author == "" {
		return fmt.Errorf("review entry: author is required")
	}
	if e.At.IsZero() {
		return fmt.Errorf("review entry: at is required")
	}
	if e.Add == nil {
		return fmt.Errorf("review entry: add must be a non-nil slice (use []string{} for empty)")
	}
	if e.Remove == nil {
		return fmt.Errorf("review entry: remove must be a non-nil slice (use []string{} for empty)")
	}
	if slices.Contains(e.Add, "") {
		return fmt.Errorf("review entry: add contains an empty label")
	}
	if slices.Contains(e.Remove, "") {
		return fmt.Errorf("review entry: remove contains an empty label")
	}
	if overlap, ok := intersectLabel(e.Add, e.Remove); ok {
		return fmt.Errorf("review entry: label %q is in both add and remove", overlap)
	}
	return nil
}

// ValidateOutcomeEntry checks that an OutcomeEntry is well-formed
// for writing.
func ValidateOutcomeEntry(e OutcomeEntry) error {
	if e.Kind == "" {
		return fmt.Errorf("outcome entry: kind is required")
	}
	if e.ID == "" {
		return fmt.Errorf("outcome entry: id is required")
	}
	if e.Author == "" {
		return fmt.Errorf("outcome entry: author is required")
	}
	if e.At.IsZero() {
		return fmt.Errorf("outcome entry: at is required")
	}
	if e.Status == "" {
		return fmt.Errorf("outcome entry: status is required")
	}
	return nil
}

// ResolveLabels returns the effective label set for a finding,
// given the LLM seed labels and every review entry that references
// the finding. Caller filters reviews to the subject; the resolver
// only orders + applies.
//
// Algorithm: start from seed, walk reviews sorted by (At, Author),
// apply each entry's Remove then Add. Return a sorted, de-duplicated
// slice. Identical-timestamp entries are ordered by Author lexically
// so the result is deterministic across machines / runs.
func ResolveLabels(seed []string, reviews []ReviewEntry) []string {
	set := make(map[string]struct{}, len(seed))
	for _, l := range seed {
		set[l] = struct{}{}
	}
	for _, r := range sortedByAt(reviews) {
		for _, l := range r.Remove {
			delete(set, l)
		}
		for _, l := range r.Add {
			set[l] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// ResolveSeverity returns the effective severity for a finding:
// the latest non-nil Severity in chronologically-ordered reviews,
// falling back to seed when no reviewer asserted one. Reviews with
// Severity == nil are "didn't touch" entries (label / comment only)
// and never supersede a prior assertion.
func ResolveSeverity(seed *string, reviews []ReviewEntry) *string {
	cur := seed
	for _, r := range sortedByAt(reviews) {
		if r.Severity != nil {
			cur = r.Severity
		}
	}
	return cur
}

// ResolveOutcome returns the latest outcome entry across every
// outcome file, or nil if there is no outcome history. Caller
// filters to the subject; the resolver only orders.
func ResolveOutcome(outcomes []OutcomeEntry) *OutcomeEntry {
	if len(outcomes) == 0 {
		return nil
	}
	sorted := SortOutcomesChronological(outcomes)
	last := sorted[len(sorted)-1]
	return &last
}

// SortOutcomesChronological returns a copy of outcomes sorted by
// (At, AuthorSlug) ascending. Same ordering ResolveOutcome uses
// internally, exported so CLI / API readers that need the full
// chronological history (e.g. `fettle show outcome --all`) display
// the same order the resolver would use to pick "latest."
func SortOutcomesChronological(outcomes []OutcomeEntry) []OutcomeEntry {
	out := make([]OutcomeEntry, len(outcomes))
	copy(out, outcomes)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return AuthorSlug(out[i].Author) < AuthorSlug(out[j].Author)
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// sortedByAt returns a copy of reviews sorted by (At, AuthorSlug)
// ascending. The author-slug tie-breaker keeps results deterministic
// across machines when timestamps collide (rare in practice but
// possible under bulk scripted use).
func sortedByAt(reviews []ReviewEntry) []ReviewEntry {
	out := make([]ReviewEntry, len(reviews))
	copy(out, reviews)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return AuthorSlug(out[i].Author) < AuthorSlug(out[j].Author)
		}
		return out[i].At.Before(out[j].At)
	})
	return out
}

// intersectLabel returns the first element of a that also appears
// in b, along with `true`, or `"", false` if there is no overlap.
// Returns a `(string, bool)` rather than just a sentinel so the
// caller can distinguish "no overlap" from "overlap is the empty
// string" — the latter is rejected separately by the validator but
// we don't want this helper to silently miss it if those checks
// ever get reordered.
func intersectLabel(a, b []string) (string, bool) {
	for _, x := range a {
		if slices.Contains(b, x) {
			return x, true
		}
	}
	return "", false
}
