package run

import (
	"sort"
	"time"

	"github.com/contiamo/fettle/internal/schema"
)

// FlatReview is one review entry surfaced with the finding it belongs
// to. The on-disk Review (inside its FindingDoc) doesn't carry a
// Subject; FlatReview synthesizes one at the boundary so flat output
// surfaces (CLI `list reviews`, UI "scan all reviews to compute
// effective severity") keep the same shape they had under the
// per-author JSONL layout.
//
// AuthorSlug is the slug portion of the canonical author stamp
// (`agent:claude/sonnet` → `claude`, `human:michael` → `michael`),
// extracted via schema.AuthorSlug. It used to come from the
// reviews_<slug>.jsonl filename; now it's derived from the stamp.
// Resume logic keys on slug only so switching the agent's model
// doesn't force re-review.
type FlatReview struct {
	Subject    schema.Subject `json:"subject"`
	Author     string         `json:"author"`
	AuthorSlug string         `json:"-"`
	Labels     *[]string      `json:"labels,omitempty"`
	Severity   *string        `json:"severity,omitempty"`
	Comment    string         `json:"comment,omitempty"`
	At         time.Time      `json:"at"`
}

// LoadAllReviews walks every finding doc in the run and returns its
// reviews flattened into a chronological list (oldest first), each
// entry annotated with the synthesized Subject and AuthorSlug.
// Malformed docs are skipped (LoadAllFindings handles the warning).
func (p *Path) LoadAllReviews() ([]FlatReview, error) {
	docs, err := p.LoadAllFindings()
	if err != nil {
		return nil, err
	}
	var out []FlatReview
	for _, doc := range docs {
		subj := schema.Subject{Kind: schema.SubjectFinding, ID: doc.ID}
		for _, r := range doc.Reviews {
			out = append(out, FlatReview{
				Subject:    subj,
				Author:     r.Author,
				AuthorSlug: schema.AuthorSlug(r.Author),
				Labels:     r.Labels,
				Severity:   r.Severity,
				Comment:    r.Comment,
				At:         r.At,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// LoadAllOutcomes walks every finding doc and returns each outcome
// event flattened into a chronological list (oldest first), with
// Subject synthesized from the doc id. Mirrors LoadAllReviews; used
// by CLI `list outcomes` and any consumer that wants every event in
// the run regardless of which finding it touches.
func (p *Path) LoadAllOutcomes() ([]FlatOutcome, error) {
	docs, err := p.LoadAllFindings()
	if err != nil {
		return nil, err
	}
	var out []FlatOutcome
	for _, doc := range docs {
		subj := schema.Subject{Kind: schema.SubjectFinding, ID: doc.ID}
		for _, o := range doc.Outcomes {
			out = append(out, FlatOutcome{
				Subject: subj,
				Author:  o.Author,
				Status:  o.Status,
				PRURL:   o.PRURL,
				At:      o.At,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// FlatOutcome is the FlatReview equivalent for outcome events:
// schema.Outcome plus the synthesized Subject identifying the
// finding the event is for.
type FlatOutcome struct {
	Subject schema.Subject `json:"subject"`
	Author  string         `json:"author"`
	Status  string         `json:"status"`
	PRURL   string         `json:"pr_url,omitempty"`
	At      time.Time      `json:"at"`
}
