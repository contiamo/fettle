package agent

import (
	"context"
	"fmt"
	"os/exec"
)

// runCodex invokes the `codex` CLI in non-interactive mode. Prompt comes
// in via stdin; the agent writes its findings file directly.
func runCodex(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, fmt.Errorf("codex CLI not found in PATH: %w", err)
	}
	args := []string{
		"exec",
		"--json",
		"--full-auto",
		"--ephemeral",
	}
	if spec.WorkDir != "" {
		args = append(args, "--cd", spec.WorkDir)
	}
	for _, d := range spec.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+spec.Effort)
	}
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = spec.WorkDir
	return runCmd(ctx, cmd, prompt)
}
