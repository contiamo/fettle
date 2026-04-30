package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestRun_unknownAgent(t *testing.T) {
	_, err := Run(context.Background(), Spec{Name: "nonsense"}, "hi")
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}

func TestRun_customCommand(t *testing.T) {
	// cat reads stdin and echoes it — perfect stand-in for an agent whose
	// only job is to receive the prompt and produce output.
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	const marker = "hello-from-prompt-9f3c"
	res, err := Run(context.Background(), Spec{
		Script: cat,
		// Name is intentionally something that would error in the
		// switch fallback; the Command-takes-precedence rule must fire
		// before the switch.
		Name: "definitely-not-claude",
	}, marker)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(res.Output), marker) {
		t.Fatalf("output didn't echo prompt: got %q", res.Output)
	}
}

func TestRun_customCommand_missingBinary(t *testing.T) {
	_, err := Run(context.Background(), Spec{
		Script: "/nonsense/path/never/exists/agent.sh",
	}, "hi")
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("want not-executable error, got %v", err)
	}
}
