package walk

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWalk(t *testing.T) {
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

	got, err := Walk(root, []string{"**/*.go"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}

	var rel []string
	for _, p := range got {
		r, _ := filepath.Rel(root, p)
		rel = append(rel, filepath.ToSlash(r))
	}
	slices.Sort(rel)

	want := []string{"internal/foo/bar.go", "internal/foo/bar_test.go", "main.go"}
	if !slices.Equal(rel, want) {
		t.Fatalf("got %v, want %v", rel, want)
	}
}
