package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeProjectWithRuns(t *testing.T, names ...string) string {
	t.Helper()
	project := t.TempDir()
	runsDir := filepath.Join(project, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(runsDir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

// findProject returns a findProject func that yields a fixed
// project dir — used by tests that exercise the slug-lookup
// branches.
func findProject(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

// findProjectErrs returns a findProject func that always errors —
// used by tests that exercise the absolute / relative branches, to
// prove they don't trigger project discovery.
func findProjectErrs() func() (string, error) {
	return func() (string, error) {
		return "", fmt.Errorf("findProject should not have been called")
	}
}

func TestLookupRun(t *testing.T) {
	project := makeProjectWithRuns(t,
		"run_3cdf6f_20260519T110354Z",
		"run_abc123_20260520T090000Z",
		"run_3cdf61_20260518T080000Z", // shares a prefix with 3cdf6f
		"not-a-run-dir",
	)
	runsDir := filepath.Join(project, "runs")

	cases := []struct {
		name         string
		ref          string
		wantPath     string
		wantErr      string // substring; "" means no error
		wantNotFound bool
	}{
		{
			name:     "exact slug",
			ref:      "3cdf6f",
			wantPath: filepath.Join(runsDir, "run_3cdf6f_20260519T110354Z"),
		},
		{
			name:     "full run name",
			ref:      "run_abc123_20260520T090000Z",
			wantPath: filepath.Join(runsDir, "run_abc123_20260520T090000Z"),
		},
		{
			name:     "prefix match (unique)",
			ref:      "abc",
			wantPath: filepath.Join(runsDir, "run_abc123_20260520T090000Z"),
		},
		{
			name:    "ambiguous prefix",
			ref:     "3cdf6",
			wantErr: "ambiguous",
		},
		{
			name:         "no match",
			ref:          "nope01",
			wantNotFound: true,
		},
		{
			name:    "empty ref",
			ref:     "",
			wantErr: "empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LookupRun(tc.ref, findProject(project))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got path %q", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q doesn't contain %q", err, tc.wantErr)
				}
				return
			}
			if tc.wantNotFound {
				if err == nil {
					t.Fatalf("want ErrRunNotFound, got path %q", got)
				}
				if !errors.Is(err, ErrRunNotFound) {
					t.Errorf("want errors.Is(err, ErrRunNotFound); got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantPath {
				t.Errorf("got %q, want %q", got, tc.wantPath)
			}
		})
	}
}

func TestLookupRun_AbsolutePath_SkipsProjectDiscovery(t *testing.T) {
	// Absolute paths shouldn't trigger findProject; passing an
	// always-erroring discoverer confirms that.
	abs := "/tmp/nonexistent/run_3cdf6f_20260519T110354Z"
	got, err := LookupRun(abs, findProjectErrs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != abs {
		t.Errorf("got %q, want %q", got, abs)
	}
}

func TestLookupRun_RelativePath_SkipsProjectDiscovery(t *testing.T) {
	// Ref containing a separator → treated as cwd-relative path,
	// no project discovery. Same assertion mechanism as the
	// absolute-path test.
	got, err := LookupRun("some/path", findProjectErrs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want absolute path", got)
	}
}

func TestLookupRun_ProjectDiscoveryError(t *testing.T) {
	// When the lookup needs project discovery but it fails, the
	// error propagates with context.
	_, err := LookupRun("3cdf6f", findProjectErrs())
	if err == nil {
		t.Fatal("want error from findProject, got nil")
	}
	if !strings.Contains(err.Error(), "findProject should not have been called") {
		// Misleading name, but the function does get called here —
		// only the absolute / relative branches skip it.
		t.Errorf("error %q doesn't contain the discoverer's error", err)
	}
}
