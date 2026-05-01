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
	// Permission posture:
	//   - No --dangerously-skip-permissions. Default permission mode is in
	//     effect, so anything outside the explicit allowlist is denied (or
	//     blocked by claude's sandbox if it would mutate state outside cwd).
	//   - Bash is restricted to `fettle *` — the only shell command the
	//     find/review/group prompts ever need is `fettle find add` (and
	//     future fettle subcommands). The agent cannot run arbitrary shell
	//     commands, even read-only ones, so the agent's blast radius is
	//     bounded by what fettle's own CLI lets it do.
	args := []string{
		"-p", prompt,
		"--allowed-tools", "Read,Grep,Glob,Bash(fettle *)",
		"--output-format", "json",
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = buildEnv(spec)
	return runCmd(ctx, cmd, "")
}
