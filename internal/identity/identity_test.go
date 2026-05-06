package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withEnv sets the named env vars for the test's lifetime, restoring
// the previous values via t.Cleanup. Empty values clear the var.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		old, had := os.LookupEnv(k)
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
		t.Cleanup(func() {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		})
	}
}

// withTempIdentity points FETTLE_IDENTITY_FILE at a temp path and
// returns the path so tests can write to / inspect it.
func withTempIdentity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "identity")
	withEnv(t, map[string]string{"FETTLE_IDENTITY_FILE": path})
	return path
}

func TestResolve_AgentEnvWins(t *testing.T) {
	path := withTempIdentity(t)
	if err := os.WriteFile(path, []byte("file-slug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{
		EnvAgent:  "agent-x",
		EnvAuthor: "author-y",
	})
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Slug != "agent-x" || !r.IsAgent || r.Source != SourceAgentEnv {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_AuthorEnvBeatsConfigFile(t *testing.T) {
	path := withTempIdentity(t)
	if err := os.WriteFile(path, []byte("file-slug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{
		EnvAgent:  "",
		EnvAuthor: "author-y",
	})
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Slug != "author-y" || r.IsAgent || r.Source != SourceAuthorEnv {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_ConfigFileWhenEnvsEmpty(t *testing.T) {
	path := withTempIdentity(t)
	if err := os.WriteFile(path, []byte("  file-slug  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{EnvAgent: "", EnvAuthor: ""})
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Slug != "file-slug" || r.IsAgent || r.Source != SourceConfigFile {
		t.Errorf("got %+v", r)
	}
}

func TestResolve_NoIdentityErrors(t *testing.T) {
	withTempIdentity(t) // points at a path that doesn't exist
	withEnv(t, map[string]string{EnvAgent: "", EnvAuthor: ""})
	_, err := Resolve()
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("Resolve = %v, want ErrNoIdentity", err)
	}
}

func TestResolve_EmptyConfigFileTreatedAsNoIdentity(t *testing.T) {
	path := withTempIdentity(t)
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withEnv(t, map[string]string{EnvAgent: "", EnvAuthor: ""})
	_, err := Resolve()
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("Resolve on whitespace-only file = %v, want ErrNoIdentity", err)
	}
}

func TestResolve_WhitespaceTrimmingInEnvs(t *testing.T) {
	withTempIdentity(t)
	withEnv(t, map[string]string{EnvAgent: "  \n", EnvAuthor: "  spaced  "})
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Agent env was whitespace-only → fell through to author.
	if r.Source != SourceAuthorEnv || r.Slug != "spaced" {
		t.Errorf("got %+v, want author-env trimmed slug", r)
	}
}

func TestSave_RoundTrip(t *testing.T) {
	path := withTempIdentity(t)
	if err := Save("alice"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	withEnv(t, map[string]string{EnvAgent: "", EnvAuthor: ""})
	r, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve after Save: %v", err)
	}
	if r.Slug != "alice" || r.Source != SourceConfigFile {
		t.Errorf("got %+v", r)
	}
	// Sanity-check the file shape: trailing newline, exact content.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alice\n" {
		t.Errorf("file = %q, want %q", string(got), "alice\n")
	}
}

func TestSave_RejectsInvalidSlug(t *testing.T) {
	withTempIdentity(t)
	cases := []string{"", " ", "has space", "with/slash", "../traversal"}
	for _, s := range cases {
		if err := Save(s); err == nil {
			t.Errorf("Save(%q) = nil, want error", s)
		}
	}
}

func TestResolved_String_Shapes(t *testing.T) {
	withEnv(t, map[string]string{EnvModel: ""})
	if got := (Resolved{Slug: "alice"}).String(); got != "human:alice" {
		t.Errorf("human shape = %q", got)
	}
	if got := (Resolved{Slug: "claude", IsAgent: true}).String(); got != "agent:claude" {
		t.Errorf("agent (no model) shape = %q", got)
	}
	withEnv(t, map[string]string{EnvModel: "sonnet-4-6"})
	if got := (Resolved{Slug: "claude", IsAgent: true}).String(); got != "agent:claude/sonnet-4-6" {
		t.Errorf("agent (with model) shape = %q", got)
	}
}

func TestValidateSlug(t *testing.T) {
	good := []string{"alice", "alice_bob", "alice-bob", "Alice123"}
	for _, s := range good {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"", "a b", "a/b", "..", "a.b"}
	for _, s := range bad {
		if err := ValidateSlug(s); err == nil {
			t.Errorf("ValidateSlug(%q) = nil, want error", s)
		}
	}
}
