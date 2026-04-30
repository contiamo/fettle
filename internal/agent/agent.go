// Package agent wraps the CLI invocations for the LLM agents fettle drives.
//
// Each invocation takes a fully substituted prompt and writes its findings
// to a file path embedded in that prompt — the agent package never reads or
// parses results, only runs the CLI and returns the combined output for
// raw-log capture.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Spec identifies which agent to invoke and how.
type Spec struct {
	Name    string        // "claude" | "codex"
	Model   string        // model alias or id; empty uses the CLI's default
	Effort  string        // codex reasoning effort (low|medium|high|xhigh|max); ignored for other agents
	WorkDir string        // process CWD (typically the target repo root)
	AddDirs []string      // additional dirs the agent may write to (codex sandbox)
	Timeout time.Duration // per-invocation timeout; 0 = no override
}

// Result captures the raw outcome of one agent invocation.
type Result struct {
	Output   []byte        // combined stdout+stderr; suitable for the run's raw/ dir
	Duration time.Duration // wall-clock
}

// Run invokes the named agent CLI with the given prompt. Variable
// substitution (TARGET_FILE, OUTPUT_PATH, etc.) must already be done by
// the caller. Output is always returned, even on error, so the harness
// can persist the raw log regardless of outcome.
func Run(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	switch spec.Name {
	case "claude":
		return runClaude(ctx, spec, prompt)
	case "codex":
		return runCodex(ctx, spec, prompt)
	default:
		return nil, fmt.Errorf("unknown agent %q (supported: claude, codex)", spec.Name)
	}
}

// runCmd executes a prepared command, capturing combined output. If
// stdinPrompt is non-empty, it's piped to the process; otherwise the
// process inherits an empty stdin.
func runCmd(ctx context.Context, cmd *exec.Cmd, stdinPrompt string) (*Result, error) {
	if stdinPrompt != "" {
		cmd.Stdin = strings.NewReader(stdinPrompt)
	} else {
		cmd.Stdin = nilReader
	}
	start := time.Now()
	out, err := cmd.CombinedOutput()
	res := &Result{Output: out, Duration: time.Since(start)}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return res, fmt.Errorf("%s timed out after %s", cmd.Path, res.Duration.Round(time.Second))
	}
	if err != nil {
		return res, fmt.Errorf("%s failed: %w", cmd.Path, err)
	}
	return res, nil
}

// nilReader is an explicit empty stdin so CLI prompts that read from stdin
// don't block forever waiting on the parent's terminal.
var nilReader io.Reader = strings.NewReader("")
