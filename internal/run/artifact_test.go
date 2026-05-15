package run

import (
	"testing"
	"time"
)

func TestArtifactFilename(t *testing.T) {
	at := time.Date(2026, 5, 15, 10, 30, 22, 0, time.UTC)

	cases := []struct {
		name    string
		kind    ArtifactKind
		human   string
		agent   string
		want    string
		wantErr bool
	}{
		{
			name:  "human-only review",
			kind:  ArtifactReviews,
			human: "michael",
			want:  "reviews_2026-05-15T103022.000000Z_michael.jsonl",
		},
		{
			name:  "agent-driven find",
			kind:  ArtifactFindings,
			human: "michael",
			agent: "claude-sonnet",
			want:  "findings_2026-05-15T103022.000000Z_michael_claude-sonnet.jsonl",
		},
		{
			name:  "outcomes",
			kind:  ArtifactOutcomes,
			human: "michael",
			want:  "outcomes_2026-05-15T103022.000000Z_michael.jsonl",
		},
		{
			name:  "hyphenated human slug",
			kind:  ArtifactReviews,
			human: "michael-dietze",
			want:  "reviews_2026-05-15T103022.000000Z_michael-dietze.jsonl",
		},
		{
			name:    "unknown kind",
			kind:    ArtifactKind("rumours"),
			human:   "michael",
			wantErr: true,
		},
		{
			name:    "empty human",
			kind:    ArtifactReviews,
			human:   "",
			wantErr: true,
		},
		{
			name:    "underscore in human",
			kind:    ArtifactReviews,
			human:   "michael_d",
			wantErr: true,
		},
		{
			name:    "underscore in agent",
			kind:    ArtifactReviews,
			human:   "michael",
			agent:   "bad_agent",
			wantErr: true,
		},
		{
			name:    "slash in agent",
			kind:    ArtifactReviews,
			human:   "michael",
			agent:   "claude/sonnet",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ArtifactFilename(tc.kind, at, tc.human, tc.agent)
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

func TestArtifactFilenameLocalTimeNormalisedToUTC(t *testing.T) {
	// 09:30 in a +03:00 zone is 06:30 UTC. The filename must reflect
	// the UTC normalisation so directory listings across writers in
	// different timezones still sort chronologically.
	loc := time.FixedZone("+03", 3*60*60)
	at := time.Date(2026, 5, 15, 9, 30, 22, 0, loc)
	got, err := ArtifactFilename(ArtifactReviews, at, "michael", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "reviews_2026-05-15T063022.000000Z_michael.jsonl"
	if got != want {
		t.Fatalf("filename mismatch:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestParseArtifactFilename(t *testing.T) {
	at := time.Date(2026, 5, 15, 10, 30, 22, 0, time.UTC)

	cases := []struct {
		name      string
		filename  string
		wantOk    bool
		wantKind  ArtifactKind
		wantHuman string
		wantAgent string
	}{
		{
			name:      "human-only review",
			filename:  "reviews_2026-05-15T103022.000000Z_michael.jsonl",
			wantOk:    true,
			wantKind:  ArtifactReviews,
			wantHuman: "michael",
		},
		{
			name:      "agent-driven find",
			filename:  "findings_2026-05-15T103022.000000Z_michael_claude-sonnet.jsonl",
			wantOk:    true,
			wantKind:  ArtifactFindings,
			wantHuman: "michael",
			wantAgent: "claude-sonnet",
		},
		{
			name:      "hyphenated human, no agent",
			filename:  "reviews_2026-05-15T103022.000000Z_michael-dietze.jsonl",
			wantOk:    true,
			wantKind:  ArtifactReviews,
			wantHuman: "michael-dietze",
		},
		{
			name:      "hyphenated human and agent",
			filename:  "outcomes_2026-05-15T103022.000000Z_michael-dietze_claude-sonnet.jsonl",
			wantOk:    true,
			wantKind:  ArtifactOutcomes,
			wantHuman: "michael-dietze",
			wantAgent: "claude-sonnet",
		},
		// Non-artifact filenames in the run dir must be skipped silently.
		{name: "not an artifact", filename: "run.json"},
		{name: "wrong kind", filename: "rumours_2026-05-15T103022.000000Z_michael.jsonl"},
		{name: "missing datetime", filename: "reviews_michael.jsonl"},
		{name: "datetime missing Z", filename: "reviews_2026-05-15T103022.000000_michael.jsonl"},
		{name: "datetime missing fraction", filename: "reviews_2026-05-15T103022Z_michael.jsonl"},
		{name: "datetime with colons", filename: "reviews_2026-05-15T10:30:22.000000Z_michael.jsonl"},
		{name: "trailing junk", filename: "reviews_2026-05-15T103022.000000Z_michael.jsonl.bak"},
		{name: "empty human", filename: "reviews_2026-05-15T103022.000000Z_.jsonl"},
		{name: "empty agent (trailing underscore)", filename: "reviews_2026-05-15T103022.000000Z_michael_.jsonl"},
		{name: "underscore in slug", filename: "reviews_2026-05-15T103022.000000Z_michael_d.jsonl_unused.jsonl"},
		{name: "impossible date", filename: "reviews_2026-13-15T103022.000000Z_michael.jsonl"},
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
			if !got.At.Equal(at) {
				t.Errorf("at: got %v, want %v", got.At, at)
			}
			if got.Human != tc.wantHuman {
				t.Errorf("human: got %q, want %q", got.Human, tc.wantHuman)
			}
			if got.Agent != tc.wantAgent {
				t.Errorf("agent: got %q, want %q", got.Agent, tc.wantAgent)
			}
		})
	}
}

func TestArtifactFilenameRoundTrip(t *testing.T) {
	at := time.Date(2026, 5, 15, 10, 30, 22, 0, time.UTC)
	cases := []struct {
		kind  ArtifactKind
		human string
		agent string
	}{
		{ArtifactReviews, "michael", ""},
		{ArtifactReviews, "michael", "claude-sonnet"},
		{ArtifactFindings, "michael-dietze", "codex-gpt-5"},
		{ArtifactOutcomes, "ci-bot", ""},
	}
	for _, tc := range cases {
		name, err := ArtifactFilename(tc.kind, at, tc.human, tc.agent)
		if err != nil {
			t.Fatalf("build %v: %v", tc, err)
		}
		got, ok := ParseArtifactFilename(name)
		if !ok {
			t.Fatalf("parse %q: not ok", name)
		}
		if got.Kind != tc.kind || got.Human != tc.human || got.Agent != tc.agent || !got.At.Equal(at) {
			t.Fatalf("roundtrip mismatch:\n  in:  %+v at %v\n  out: %+v", tc, at, got)
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
