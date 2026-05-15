package schema

import "time"

// FindingEntry is the JSONL line shape for a finding emitted by the
// find stage. It embeds Finding so the on-disk JSON is flat
// (`{"kind":"finding","id":"...","file":"...",...}`), keeping each
// line self-describing.
//
// Kind is always SubjectFinding today; the field exists for
// forward-compat with additional subject kinds and so a streamed
// reader doesn't have to consult the filename to know what the
// entry is. The find writer fills Kind explicitly; readers tolerate
// an empty Kind on legacy lines.
type FindingEntry struct {
	Kind string `json:"kind"`
	Finding
}

// ReviewEntry is one append to a reviews_*.jsonl stream. Kind+ID
// name the subject the review touches (today always a finding);
// Author/At identify the writer; Add/Remove are the label delta;
// Severity (optional, pointer-to-distinguish) overrides the seed
// severity; Comment is free-form.
//
// Add and Remove are required arrays (may be empty). Same label
// appearing in both is rejected at write — see ValidateReviewEntry —
// so the resolver can apply remove-then-add in any order within an
// entry without ambiguity.
type ReviewEntry struct {
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	At       time.Time `json:"at"`
	Add      []string  `json:"add"`
	Remove   []string  `json:"remove"`
	Severity *string   `json:"severity,omitempty"`
	Comment  string    `json:"comment,omitempty"`
}

// OutcomeEntry is one append to an outcomes_*.jsonl stream. Same
// Kind+ID subject convention as ReviewEntry. Latest entry across
// every outcome file wins for "current outcome" display.
type OutcomeEntry struct {
	Kind   string    `json:"kind"`
	ID     string    `json:"id"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	PRURL  string    `json:"pr_url,omitempty"`
}
