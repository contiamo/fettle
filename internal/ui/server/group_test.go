package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/contiamo/fettle/internal/schema"
)

// TestResolveGroupMembers_resolvesAndPreservesOrder seeds an input
// run with three findings and asks for two of them out of order.
// The returned Members slice must match the request order, not the
// findings.jsonl order.
func TestResolveGroupMembers_resolvesAndPreservesOrder(t *testing.T) {
	projectDir := t.TempDir()
	inputRel := "runs/find_input"
	inputAbs := filepath.Join(projectDir, inputRel)
	if err := os.MkdirAll(inputAbs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunFolder(t, inputAbs, "find", `{"id":"a","file":"x.go","line":1,"title":"A"}
{"id":"b","file":"y.go","line":2,"title":"B"}
{"id":"c","file":"z.go","line":3,"title":"C"}
`)

	members, runName, missing := resolveGroupMembers(projectDir, inputRel, []string{"c", "a"})
	if runName != "find_input" {
		t.Errorf("runName = %q, want %q", runName, "find_input")
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	if members[0].ID != "c" || members[1].ID != "a" {
		t.Errorf("ids = [%s, %s], want [c, a]", members[0].ID, members[1].ID)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}

// TestResolveGroupMembers_missing — ids that don't resolve land in
// the missing slice in request order; found ones still come back in
// Members.
func TestResolveGroupMembers_missing(t *testing.T) {
	projectDir := t.TempDir()
	inputRel := "runs/find_input"
	inputAbs := filepath.Join(projectDir, inputRel)
	if err := os.MkdirAll(inputAbs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunFolder(t, inputAbs, "find", `{"id":"a","file":"x.go","line":1,"title":"A"}
`)

	members, _, missing := resolveGroupMembers(projectDir, inputRel, []string{"a", "ghost", "another-ghost"})
	if len(members) != 1 || members[0].ID != "a" {
		t.Errorf("members = %v, want one with id=a", members)
	}
	if len(missing) != 2 || missing[0] != "ghost" || missing[1] != "another-ghost" {
		t.Errorf("missing = %v, want [ghost, another-ghost]", missing)
	}
}

// TestResolveGroupMembers_inputRunGone — a deleted/missing input run
// reports every requested id as missing. The page degrades to a
// "missing" list rather than 500.
func TestResolveGroupMembers_inputRunGone(t *testing.T) {
	projectDir := t.TempDir()
	members, runName, missing := resolveGroupMembers(projectDir, "runs/never-existed", []string{"a", "b"})
	if len(members) != 0 {
		t.Errorf("got %d members, want 0", len(members))
	}
	if runName != "never-existed" {
		t.Errorf("runName = %q, want never-existed", runName)
	}
	if len(missing) != 2 || missing[0] != "a" || missing[1] != "b" {
		t.Errorf("missing = %v, want [a, b]", missing)
	}
}

// TestResolveGroupMembers_emptyInputRun — manifest with empty
// input_run (shouldn't happen in practice, but harmless to guard).
func TestResolveGroupMembers_emptyInputRun(t *testing.T) {
	members, runName, missing := resolveGroupMembers(t.TempDir(), "", []string{"a"})
	if len(members) != 0 || runName != "" || len(missing) != 1 {
		t.Errorf("emptyInputRun: members=%d, runName=%q, missing=%v", len(members), runName, missing)
	}
}

// TestResolveGroupMembers_invalidInputRun — manifests with malformed
// input_run paths must never drive filepath.Join outside the project
// root, even before run.Open gets a chance to fail. The function
// returns all-as-missing with an empty inputRunName, signalling the
// template to render the missing list without member-detail links.
func TestResolveGroupMembers_invalidInputRun(t *testing.T) {
	projectDir := t.TempDir()
	cases := []struct {
		name     string
		inputRun string
	}{
		{"parent_traversal", "runs/../../etc"},
		{"parent_only", "../../etc"},
		{"absolute", "/etc/passwd"},
		{"abs_runs", "/runs/find_x"},
		{"wrong_prefix", "other-folder/find_x"},
		{"runs_with_subdir", "runs/find_x/sub"},
		{"runs_with_dot", "runs/."},
		{"runs_with_doubledot", "runs/.."},
		{"empty_name", "runs/"},
		{"trailing_slash", "runs/find_x/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			members, runName, missing := resolveGroupMembers(projectDir, tc.inputRun, []string{"a", "b"})
			if len(members) != 0 {
				t.Errorf("got %d members, want 0", len(members))
			}
			if runName != "" {
				t.Errorf("runName = %q, want empty (signals invalid input)", runName)
			}
			if len(missing) != 2 {
				t.Errorf("missing = %v, want both ids reported", missing)
			}
		})
	}
}

// writeRunFolder writes a minimal run.json + findings.jsonl pair at
// runDir so resolveGroupMembers can open the run cleanly via run.Open.
func writeRunFolder(t *testing.T, runDir, stage, findingsJSONL string) {
	t.Helper()
	manifest := `{"name":"` + filepath.Base(runDir) + `","stage":"` + stage + `","fettle_version":"0.1.0","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "findings.jsonl"), []byte(findingsJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
}

// silence the unused import warning on schema if a future test needs
// to construct findings directly. members carries schema.Finding so
// the package imports it transitively, but explicit import keeps the
// test file's intent clear.
var _ schema.Finding