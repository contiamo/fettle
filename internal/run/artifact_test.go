package run

import (
	"testing"
)

func TestArtifactFilename(t *testing.T) {
	const slug = "3cdf6f"
	const ts = "20260515T103022Z"

	cases := []struct {
		name    string
		kind    ArtifactKind
		slug    string
		ts      string
		author  string
		want    string
		wantErr bool
	}{
		{
			name: "findings — no author",
			kind: ArtifactFindings, slug: slug, ts: ts,
			want: "findings_3cdf6f_20260515T103022Z.jsonl",
		},
		{
			name: "reviews — with author",
			kind: ArtifactReviews, slug: slug, ts: ts, author: "michael",
			want: "reviews_3cdf6f_20260515T103022Z_michael.jsonl",
		},
		{
			name: "outcomes — with author",
			kind: ArtifactOutcomes, slug: slug, ts: ts, author: "claude",
			want: "outcomes_3cdf6f_20260515T103022Z_claude.jsonl",
		},
		{
			name: "agent author with model hyphenated",
			kind: ArtifactReviews, slug: slug, ts: ts, author: "claude-sonnet",
			want: "reviews_3cdf6f_20260515T103022Z_claude-sonnet.jsonl",
		},
		{
			name: "findings with author rejected",
			kind: ArtifactFindings, slug: slug, ts: ts, author: "michael",
			wantErr: true,
		},
		{
			name: "reviews without author rejected",
			kind: ArtifactReviews, slug: slug, ts: ts,
			wantErr: true,
		},
		{
			name: "unknown kind",
			kind: ArtifactKind("rumours"), slug: slug, ts: ts,
			wantErr: true,
		},
		{
			name: "empty slug",
			kind: ArtifactFindings, slug: "", ts: ts,
			wantErr: true,
		},
		{
			name: "underscore in slug",
			kind: ArtifactFindings, slug: "bad_slug", ts: ts,
			wantErr: true,
		},
		{
			name: "underscore in author",
			kind: ArtifactReviews, slug: slug, ts: ts, author: "bad_author",
			wantErr: true,
		},
		{
			name: "slash in author",
			kind: ArtifactReviews, slug: slug, ts: ts, author: "claude/sonnet",
			wantErr: true,
		},
		{
			name: "malformed ts (with colons)",
			kind: ArtifactFindings, slug: slug, ts: "2026-05-15T10:30:22Z",
			wantErr: true,
		},
		{
			name: "malformed ts (missing Z)",
			kind: ArtifactFindings, slug: slug, ts: "20260515T103022",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ArtifactFilename(tc.kind, tc.slug, tc.ts, tc.author)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got filename %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("filename mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

func TestParseArtifactFilename(t *testing.T) {
	const slug = "3cdf6f"
	const ts = "20260515T103022Z"

	cases := []struct {
		name        string
		filename    string
		wantOk      bool
		wantKind    ArtifactKind
		wantAuthor  string
	}{
		{
			name:     "findings",
			filename: "findings_3cdf6f_20260515T103022Z.jsonl",
			wantOk:   true,
			wantKind: ArtifactFindings,
		},
		{
			name:       "reviews with author",
			filename:   "reviews_3cdf6f_20260515T103022Z_michael.jsonl",
			wantOk:     true,
			wantKind:   ArtifactReviews,
			wantAuthor: "michael",
		},
		{
			name:       "outcomes with author",
			filename:   "outcomes_3cdf6f_20260515T103022Z_claude.jsonl",
			wantOk:     true,
			wantKind:   ArtifactOutcomes,
			wantAuthor: "claude",
		},
		{
			name:       "hyphenated author",
			filename:   "reviews_3cdf6f_20260515T103022Z_claude-sonnet.jsonl",
			wantOk:     true,
			wantKind:   ArtifactReviews,
			wantAuthor: "claude-sonnet",
		},
		// Non-artifact filenames should be skipped silently.
		{name: "not an artifact", filename: "run.json"},
		{name: "wrong kind", filename: "rumours_3cdf6f_20260515T103022Z.jsonl"},
		{name: "missing ts", filename: "reviews_3cdf6f_michael.jsonl"},
		{name: "ts with colons", filename: "findings_3cdf6f_2026-05-15T10:30:22Z.jsonl"},
		{name: "trailing junk", filename: "findings_3cdf6f_20260515T103022Z.jsonl.bak"},
		{name: "findings WITH author rejected", filename: "findings_3cdf6f_20260515T103022Z_michael.jsonl"},
		{name: "reviews WITHOUT author rejected", filename: "reviews_3cdf6f_20260515T103022Z.jsonl"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseArtifactFilename(tc.filename)
			if ok != tc.wantOk {
				t.Fatalf("ok mismatch: got %v, want %v", ok, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if got.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Slug != slug {
				t.Errorf("slug: got %q, want %q", got.Slug, slug)
			}
			if got.StartTime != ts {
				t.Errorf("ts: got %q, want %q", got.StartTime, ts)
			}
			if got.Author != tc.wantAuthor {
				t.Errorf("author: got %q, want %q", got.Author, tc.wantAuthor)
			}
		})
	}
}

func TestArtifactFilenameRoundTrip(t *testing.T) {
	cases := []struct {
		kind   ArtifactKind
		slug   string
		ts     string
		author string
	}{
		{ArtifactFindings, "3cdf6f", "20260515T103022Z", ""},
		{ArtifactReviews, "abcdef", "20260101T000000Z", "michael"},
		{ArtifactOutcomes, "ffffff", "20260519T235959Z", "claude-sonnet"},
	}
	for _, tc := range cases {
		name, err := ArtifactFilename(tc.kind, tc.slug, tc.ts, tc.author)
		if err != nil {
			t.Fatalf("build %+v: %v", tc, err)
		}
		got, ok := ParseArtifactFilename(name)
		if !ok {
			t.Fatalf("parse %q: not ok", name)
		}
		if got.Kind != tc.kind || got.Slug != tc.slug || got.StartTime != tc.ts || got.Author != tc.author {
			t.Fatalf("roundtrip mismatch:\n  in:  %+v\n  out: %+v", tc, got)
		}
	}
}

func TestSanitizeAgentSlug(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "claude", want: "claude"},
		{in: "claude/sonnet", want: "claude-sonnet"},
		{in: "claude-sonnet", want: "claude-sonnet"},
		{in: "codex/gpt-5", want: "codex-gpt-5"},
		{in: "bad_agent", wantErr: true},
		{in: "bad agent", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := SanitizeAgentSlug(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRunName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantOk   bool
		wantSlug string
		wantTs   string
	}{
		{name: "canonical", input: "run_3cdf6f_20260515T103022Z", wantOk: true, wantSlug: "3cdf6f", wantTs: "20260515T103022Z"},
		{name: "longer slug accepted", input: "run_security-v1_20260515T103022Z", wantOk: true, wantSlug: "security-v1", wantTs: "20260515T103022Z"},
		{name: "missing prefix", input: "find_3cdf6f_20260515T103022Z"},
		{name: "missing ts", input: "run_3cdf6f"},
		{name: "underscore in slug", input: "run_bad_slug_20260515T103022Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug, ts, ok := ParseRunName(tc.input)
			if ok != tc.wantOk {
				t.Fatalf("ok mismatch: got %v, want %v", ok, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if slug != tc.wantSlug {
				t.Errorf("slug: got %q, want %q", slug, tc.wantSlug)
			}
			if ts != tc.wantTs {
				t.Errorf("ts: got %q, want %q", ts, tc.wantTs)
			}
		})
	}
}
