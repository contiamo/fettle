package agent

import (
	"context"
	"fmt"
	"os/exec"
)

// runClaude invokes the `claude` CLI with the prompt as an argument.
// Tools are restricted to read + write surfaces the find/review/group
// stages need.
func runClaude(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude CLI not found in PATH: %w", err)
	}
	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--allowed-tools", "Read,Grep,Glob,Write",
		"--output-format", "json",
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = spec.WorkDir
	return runCmd(ctx, cmd, "")
}
