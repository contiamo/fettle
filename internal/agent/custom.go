package agent

import (
	"context"
	"fmt"
	"os/exec"
)

func runCustom(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if _, err := exec.LookPath(spec.Script); err != nil {
		return nil, fmt.Errorf("agent script %q not executable: %w", spec.Script, err)
	}
	cmd := exec.CommandContext(ctx, spec.Script)
	cmd.Dir = spec.WorkDir
	cmd.Env = buildEnv(spec)
	return runCmd(ctx, cmd, prompt)
}
