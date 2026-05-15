package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contiamo/fettle/internal/project"
)

func TestResolveInitTarget_CreatesNewDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "foobar")

	got, err := resolveInitTarget(target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("target not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("target is not a directory")
	}
}

func TestResolveInitTarget_AcceptsEmptyExistingDirectory(t *testing.T) {
	target := t.TempDir() // already empty
	got, err := resolveInitTarget(target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}
}

func TestResolveInitTarget_RejectsNonEmptyDirectory(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveInitTarget(target)
	if err == nil {
		t.Fatal("want error for non-empty dir, got nil")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error %q should mention 'not empty'", err)
	}
}

func TestResolveInitTarget_RejectsExistingFettleProject(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, project.ConfigName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveInitTarget(target)
	if err == nil {
		t.Fatal("want error for already-init'd dir, got nil")
	}
	if !strings.Contains(err.Error(), project.ConfigName) {
		t.Errorf("error %q should reference %s", err, project.ConfigName)
	}
}

func TestResolveInitTarget_RejectsNonExistentParent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "does-not-exist", "foobar")
	_, err := resolveInitTarget(target)
	if err == nil {
		t.Fatal("want error for nested missing parent, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error %q should mention 'does not exist'", err)
	}
}

func TestResolveInitTarget_RejectsTargetThatIsAFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "regular-file")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveInitTarget(target)
	if err == nil {
		t.Fatal("want error for target=file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error %q should mention 'not a directory'", err)
	}
}

func TestResolveInitTarget_DotResolvesToCwd(t *testing.T) {
	// `fettle init .` should resolve to the current working
	// directory. We can't actually chdir into a tempdir in a test
	// safely (other tests run in parallel), so we approximate by
	// asking for "." and asserting it resolved to an absolute path
	// matching the same dir os.Getwd would return.
	got, err := resolveInitTarget(".")
	// We expect either:
	// - success (if cwd is empty — unlikely in the repo) or
	// - the "not empty" rejection.
	// Either way, the path it resolved must be the cwd.
	cwd, _ := os.Getwd()
	if err != nil {
		// On the repo working dir we'll hit "already a fettle
		// project" or "not empty" — both fine. Confirm the cwd
		// appears in the error message.
		if !strings.Contains(err.Error(), cwd) {
			t.Errorf("error %q should reference cwd %q", err, cwd)
		}
		return
	}
	if got != cwd {
		t.Errorf("got %q, want %q", got, cwd)
	}
}
