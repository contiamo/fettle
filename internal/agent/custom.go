package agent

import (
	"context"
	"fmt"
	"os/exec"
)

// runCustom invokes a user-provided agent script. The contract:
//
//   - stdin:  the fully composed prompt
//   - env:    FETTLE_RUN     absolute path to the active run folder
//             FETTLE_AGENT   the configured agent name
//             FETTLE_MODEL   the configured model, when set
//             FETTLE_EFFORT  the configured reasoning effort, when set
//             PATH           prepended with fettle's binary dir, so the
//                            script can `fettle find add` directly
//   - cwd:    spec.WorkDir (typically the target repo root)
//   - args:   none — fettle never adds positional arguments. Bake any
//             flags you need into a wrapper.
//   - exit:   0 on success; non-zero is treated as an agent failure and
//             the run records status=error for the file
//
// fettle does not interpret the script's stdout/stderr beyond capturing
// it for the per-file raw/<hash>.log. The script is responsible for
// invoking its underlying agent however it wants — claude with custom
// flags, a wrapped codex, a shell-only flow, anything.
func runCustom(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if _, err := exec.LookPath(spec.Script); err != nil {
		return nil, fmt.Errorf("agent script %q not executable: %w", spec.Script, err)
	}
	cmd := exec.CommandContext(ctx, spec.Script)
	cmd.Dir = spec.WorkDir
	cmd.Env = buildEnv(spec)
	return runCmd(ctx, cmd, prompt)
}
