package schema

import (
	"reflect"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(s string) *string { return &s }

func TestResolveLabels(t *testing.T) {
	t.Run("empty reviews returns seed", func(t *testing.T) {
		got := ResolveLabels([]string{"a", "b"}, nil)
		if !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("got %v, want [a b]", got)
		}
	})
	t.Run("empty seed and no reviews returns empty slice", func(t *testing.T) {
		got := ResolveLabels(nil, nil)
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})
	t.Run("add labels onto seed", func(t *testing.T) {
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Add: []string{"priority:p1"}},
		}
		got := ResolveLabels([]string{"category:perf"}, reviews)
		want := []string{"category:perf", "priority:p1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("remove from seed", func(t *testing.T) {
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Remove: []string{"category:perf"}},
		}
		got := ResolveLabels([]string{"category:perf", "priority:p1"}, reviews)
		want := []string{"priority:p1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("remove label not present is a noop", func(t *testing.T) {
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Remove: []string{"missing"}, Add: []string{"x"}},
		}
		got := ResolveLabels(nil, reviews)
		want := []string{"x"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("later add wins over earlier remove", func(t *testing.T) {
		// Bob adds x at 10:00; Alice removes x at 11:00 → x gone.
		// Bob adds x again at 12:00 → x back.
		reviews := []ReviewEntry{
			{Author: "human:bob", At: ts("2026-01-01T10:00:00Z"), Add: []string{"x"}},
			{Author: "human:alice", At: ts("2026-01-01T11:00:00Z"), Remove: []string{"x"}},
			{Author: "human:bob", At: ts("2026-01-01T12:00:00Z"), Add: []string{"x"}},
		}
		got := ResolveLabels(nil, reviews)
		if !reflect.DeepEqual(got, []string{"x"}) {
			t.Fatalf("got %v, want [x]", got)
		}
	})
	t.Run("review file ordering does not matter", func(t *testing.T) {
		// Same as above but reviews fed in reverse order.
		reviews := []ReviewEntry{
			{Author: "human:bob", At: ts("2026-01-01T12:00:00Z"), Add: []string{"x"}},
			{Author: "human:alice", At: ts("2026-01-01T11:00:00Z"), Remove: []string{"x"}},
			{Author: "human:bob", At: ts("2026-01-01T10:00:00Z"), Add: []string{"x"}},
		}
		got := ResolveLabels(nil, reviews)
		if !reflect.DeepEqual(got, []string{"x"}) {
			t.Fatalf("got %v, want [x]", got)
		}
	})
	t.Run("output is sorted and deduplicated", func(t *testing.T) {
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Add: []string{"z", "a", "m"}},
			{Author: "human:b", At: ts("2026-01-01T11:00:00Z"), Add: []string{"a"}}, // dup
		}
		got := ResolveLabels(nil, reviews)
		want := []string{"a", "m", "z"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("identical timestamps tie-broken by author for determinism", func(t *testing.T) {
		// alice removes x; bob adds y. Same timestamp; order doesn't
		// affect the final set here, but adding a same-label collision
		// would: alice adds x, bob removes x — alice sorts first, so
		// remove wins (final state has no x).
		reviews := []ReviewEntry{
			{Author: "human:bob", At: ts("2026-01-01T10:00:00Z"), Remove: []string{"x"}},
			{Author: "human:alice", At: ts("2026-01-01T10:00:00Z"), Add: []string{"x"}},
		}
		got := ResolveLabels(nil, reviews)
		if len(got) != 0 {
			t.Fatalf("got %v, want empty (bob's remove applies after alice's add)", got)
		}
	})
}

func TestResolveSeverity(t *testing.T) {
	t.Run("empty reviews returns seed", func(t *testing.T) {
		seed := ptr("low")
		got := ResolveSeverity(seed, nil)
		if got == nil || *got != "low" {
			t.Fatalf("got %v, want low", got)
		}
	})
	t.Run("nil severities don't touch the seed", func(t *testing.T) {
		seed := ptr("low")
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Add: []string{"x"}},
			{Author: "human:b", At: ts("2026-01-01T11:00:00Z"), Comment: "looks fine"},
		}
		got := ResolveSeverity(seed, reviews)
		if got == nil || *got != "low" {
			t.Fatalf("got %v, want low", got)
		}
	})
	t.Run("latest non-nil severity wins", func(t *testing.T) {
		reviews := []ReviewEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Severity: ptr("low")},
			{Author: "human:b", At: ts("2026-01-01T11:00:00Z"), Severity: ptr("high")},
			{Author: "human:c", At: ts("2026-01-01T12:00:00Z"), Add: []string{"x"}}, // no severity
		}
		got := ResolveSeverity(ptr("medium"), reviews)
		if got == nil || *got != "high" {
			t.Fatalf("got %v, want high", got)
		}
	})
	t.Run("seed nil and no reviews returns nil", func(t *testing.T) {
		got := ResolveSeverity(nil, nil)
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}

func TestResolveOutcome(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		if ResolveOutcome(nil) != nil {
			t.Fatalf("want nil for empty")
		}
	})
	t.Run("latest wins", func(t *testing.T) {
		entries := []OutcomeEntry{
			{Author: "human:a", At: ts("2026-01-01T10:00:00Z"), Status: "wontfix"},
			{Author: "human:b", At: ts("2026-01-01T12:00:00Z"), Status: "merged", PRURL: "x"},
			{Author: "human:c", At: ts("2026-01-01T11:00:00Z"), Status: "pending"},
		}
		got := ResolveOutcome(entries)
		if got == nil || got.Status != "merged" || got.PRURL != "x" {
			t.Fatalf("got %+v, want merged/x", got)
		}
	})
}

func TestValidateReviewEntry(t *testing.T) {
	good := ReviewEntry{
		Kind:   "finding",
		ID:     "abc",
		Author: "human:a",
		At:     ts("2026-01-01T10:00:00Z"),
		Add:    []string{"x"},
		Remove: []string{"y"},
	}
	// Hardening cases for the writer-side contract: nil arrays
	// marshal as `null` (not `[]`), so we reject them at validate
	// time; empty-string labels are nonsensical and would also
	// confuse any sentinel-based overlap check. Test these
	// explicitly so a future refactor can't silently weaken them.
	if err := ValidateReviewEntry(good); err != nil {
		t.Fatalf("good: %v", err)
	}
	missing := []struct {
		name string
		mut  func(*ReviewEntry)
	}{
		{"kind", func(e *ReviewEntry) { e.Kind = "" }},
		{"id", func(e *ReviewEntry) { e.ID = "" }},
		{"author", func(e *ReviewEntry) { e.Author = "" }},
		{"at", func(e *ReviewEntry) { e.At = time.Time{} }},
	}
	for _, tc := range missing {
		t.Run("missing_"+tc.name, func(t *testing.T) {
			e := good
			tc.mut(&e)
			if err := ValidateReviewEntry(e); err == nil {
				t.Fatalf("want error for missing %s", tc.name)
			}
		})
	}
	t.Run("add/remove overlap rejected", func(t *testing.T) {
		e := good
		e.Add = []string{"foo", "bar"}
		e.Remove = []string{"bar"}
		if err := ValidateReviewEntry(e); err == nil {
			t.Fatalf("want error for overlap")
		}
	})
	t.Run("empty add and remove arrays are fine", func(t *testing.T) {
		// Wire contract is "non-nil, may be empty" — empty array
		// marshals as `[]`, nil marshals as `null`. Use the empty
		// slice form.
		e := good
		e.Add = []string{}
		e.Remove = []string{}
		if err := ValidateReviewEntry(e); err != nil {
			t.Fatalf("empty add/remove: %v", err)
		}
	})
	t.Run("nil add rejected", func(t *testing.T) {
		e := good
		e.Add = nil
		if err := ValidateReviewEntry(e); err == nil {
			t.Fatalf("want error for nil add")
		}
	})
	t.Run("nil remove rejected", func(t *testing.T) {
		e := good
		e.Remove = nil
		if err := ValidateReviewEntry(e); err == nil {
			t.Fatalf("want error for nil remove")
		}
	})
	t.Run("empty-string label in add rejected", func(t *testing.T) {
		e := good
		e.Add = []string{"x", ""}
		if err := ValidateReviewEntry(e); err == nil {
			t.Fatalf("want error for empty-string label in add")
		}
	})
	t.Run("empty-string label in remove rejected", func(t *testing.T) {
		e := good
		e.Remove = []string{""}
		if err := ValidateReviewEntry(e); err == nil {
			t.Fatalf("want error for empty-string label in remove")
		}
	})
}

func TestValidateOutcomeEntry(t *testing.T) {
	good := OutcomeEntry{
		Kind:   "finding",
		ID:     "abc",
		Author: "human:a",
		At:     ts("2026-01-01T10:00:00Z"),
		Status: "merged",
	}
	if err := ValidateOutcomeEntry(good); err != nil {
		t.Fatalf("good: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*OutcomeEntry)
	}{
		{"kind", func(e *OutcomeEntry) { e.Kind = "" }},
		{"id", func(e *OutcomeEntry) { e.ID = "" }},
		{"author", func(e *OutcomeEntry) { e.Author = "" }},
		{"at", func(e *OutcomeEntry) { e.At = time.Time{} }},
		{"status", func(e *OutcomeEntry) { e.Status = "" }},
	}
	for _, tc := range cases {
		t.Run("missing_"+tc.name, func(t *testing.T) {
			e := good
			tc.mut(&e)
			if err := ValidateOutcomeEntry(e); err == nil {
				t.Fatalf("want error")
			}
		})
	}
}
