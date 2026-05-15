package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitiseToSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"michael", "michael"},
		{"Michael123", "Michael123"},
		{"michael-dietze", "michael-dietze"},
		{"michael_dietze", "michael-dietze"},
		{"michael.dietze", "michael-dietze"},
		{"michael dietze", "michael-dietze"},
		{"  michael  ", "michael"},
		{"___michael___", "michael"},
		{"michael__dietze", "michael-dietze"},
		{"!!!!", ""},
		{"", ""},
		// Already-clean slugs pass through.
		{"ci-bot", "ci-bot"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := sanitiseToSlug(c.in)
			if got != c.want {
				t.Errorf("sanitiseToSlug(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestResolveOperator_PrefersFettleAuthor(t *testing.T) {
	t.Setenv(EnvAuthor, "michael_dietze")
	t.Setenv("FETTLE_IDENTITY_FILE", filepath.Join(t.TempDir(), "identity"))
	got, err := ResolveOperator()
	if err != nil {
		t.Fatalf("ResolveOperator: %v", err)
	}
	// `_` in $FETTLE_AUTHOR gets folded to `-`.
	if got != "michael-dietze" {
		t.Errorf("got %q, want michael-dietze", got)
	}
}

func TestResolveOperator_FallsBackToConfigFile(t *testing.T) {
	t.Setenv(EnvAuthor, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "identity")
	if err := os.WriteFile(path, []byte("alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FETTLE_IDENTITY_FILE", path)
	got, err := ResolveOperator()
	if err != nil {
		t.Fatalf("ResolveOperator: %v", err)
	}
	if got != "alice" {
		t.Errorf("got %q, want alice", got)
	}
}

func TestResolveOperator_FallsBackToOSUser(t *testing.T) {
	// With no env override and no config file, ResolveOperator should
	// not error — it falls through to the OS username. We can't assert
	// a specific value (it depends on the test host), only that
	// either a slug came back or the well-defined ErrNoIdentity if
	// the OS user lookup somehow yielded nothing usable.
	t.Setenv(EnvAuthor, "")
	t.Setenv("FETTLE_IDENTITY_FILE", filepath.Join(t.TempDir(), "missing"))
	got, err := ResolveOperator()
	if err != nil {
		if !errors.Is(err, ErrNoIdentity) {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if got == "" {
		t.Fatalf("got empty slug with nil error")
	}
}
