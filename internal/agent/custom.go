package agent

import (
	"context"
	"fmt"
	"os/exec"
)

// runCustom invokes a user-provided agent script. The contract a
// script can rely on (stdin, env, cwd, args, exit) is documented in
// CustomScriptDoc, which is also embedded in `fettle find --help`.
//
// fettle does not interpret the script's stdout/stderr beyond
// capturing it for the per-file raw/<hash>.log.
func runCustom(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if _, err := exec.LookPath(spec.Script); err != nil {
		return nil, fmt.Errorf("agent script %q not executable: %w", spec.Script, err)
	}
	cmd := exec.CommandContext(ctx, spec.Script)
	cmd.Dir = spec.WorkDir
	cmd.Env = buildEnv(spec)
	return runCmd(ctx, cmd, prompt)
}
