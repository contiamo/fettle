package walk

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestWalkFS exercises the plain-filesystem walker: `.git/` is the
// only hard skip, everything else flows through include/exclude.
func TestWalkFS(t *testing.T) {
	root := t.TempDir()
	mkfile := func(rel string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("main.go")
	mkfile("internal/foo/bar.go")
	mkfile("internal/foo/bar_test.go")
	mkfile("vendor/lib/lib.go")
	mkfile("docs/README.md")
	mkfile("node_modules/x/y.js")
	mkfile(".git/HEAD")

	got, err := WalkFS(root, []string{"**/*.go"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"internal/foo/bar.go",
		"internal/foo/bar_test.go",
		"main.go",
		// node_modules/ is NOT filtered here — fs walker only skips
		// .git/. The user's exclude or git walker is responsible for
		// everything else.
	}; !slices.Equal(relSlice(t, root, got), want) {
		t.Fatalf("got %v, want %v", relSlice(t, root, got), want)
	}
}

// TestWalkGit verifies that the git walker honours .gitignore (no
// extra --exclude needed for ignored dirs) and respects user
// include/exclude on top of git's view of the working tree.
func TestWalkGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	mkfile := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkfile(".gitignore", "node_modules/\nvendor/\n*.generated.go\n")
	mkfile("main.go", "x")
	mkfile("internal/foo/bar.go", "x")
	mkfile("internal/foo/bar_test.go", "x")
	mkfile("internal/foo/bar.generated.go", "x")
	mkfile("vendor/lib/lib.go", "x")
	mkfile("node_modules/x/y.js", "x")
	mkfile("docs/README.md", "x")

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "-A"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// No --exclude needed: gitignore handles vendor/, node_modules/,
	// and *.generated.go.
	got, err := WalkGit(root, []string{"**/*.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/foo/bar.go",
		"internal/foo/bar_test.go",
		"main.go",
	}
	if !slices.Equal(relSlice(t, root, got), want) {
		t.Fatalf("got %v, want %v", relSlice(t, root, got), want)
	}
}

// TestWalkGit_notGitRepo confirms the explicit error when a caller
// asks for the git walker on a directory that isn't a git repo.
// Hard-error rather than silent fallback so config drift surfaces.
func TestWalkGit_notGitRepo(t *testing.T) {
	if _, err := WalkGit(t.TempDir(), []string{"**/*"}, nil); err == nil {
		t.Errorf("WalkGit on non-git dir: want error, got nil")
	}
}

func relSlice(t *testing.T, root string, abs []string) []string {
	t.Helper()
	out := make([]string, 0, len(abs))
	for _, p := range abs {
		r, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(r))
	}
	slices.Sort(out)
	return out
}
