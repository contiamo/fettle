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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Spec identifies which agent to invoke and how.
//
// Dispatch order: if Script is set, Run executes that as a custom
// agent script (stdin = prompt). Otherwise it dispatches by Name to
// the built-in claude or codex implementations.
type Spec struct {
	Name    string        // "claude" | "codex" (built-in) — also used for created_by stamping when Script is set
	Model   string        // model alias or id; empty uses the CLI's default
	Effort  string        // reasoning effort (low|medium|high|xhigh|max); claude uses --effort, codex via -c model_reasoning_effort=
	WorkDir string        // process CWD (typically the target repo root)
	AddDirs []string      // additional dirs the agent may write to (codex sandbox; ignored for custom scripts)
	Timeout time.Duration // per-invocation timeout; 0 = no override
	Env     []string      // extra "KEY=VALUE" entries; appended after os.Environ() so they win on key conflict
	Script  string        // optional path to a custom agent script; takes precedence over Name when set
	Args    []string      // optional args to pass to Script before fettle's own additions
}

// Result captures the raw outcome of one agent invocation.
type Result struct {
	Output   []byte        // combined stdout+stderr; suitable for the run's raw/ dir
	Duration time.Duration // wall-clock
}

// Run invokes the agent with the given prompt. The caller has already
// composed the prompt (template substitution, frame, etc.). Output is
// always returned, even on error, so the harness can persist the raw
// log regardless of outcome.
func Run(ctx context.Context, spec Spec, prompt string) (*Result, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	if spec.Script != "" {
		return runCustom(ctx, spec, prompt)
	}
	switch spec.Name {
	case "claude":
		return runClaude(ctx, spec, prompt)
	case "codex":
		return runCodex(ctx, spec, prompt)
	default:
		return nil, fmt.Errorf("unknown agent %q (supported: claude, codex; or set agent.script for a custom script)", spec.Name)
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

// buildEnv constructs the env for an agent subprocess: os.Environ() with
// PATH prepended by the running fettle binary's directory (so the agent
// can shell out to `fettle finding add` and find this exact build), plus
// spec.Env appended so caller-supplied vars win on key conflict.
func buildEnv(spec Spec) []string {
	env := os.Environ()
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		existing := os.Getenv("PATH")
		env = append(env, "PATH="+dir+string(os.PathListSeparator)+existing)
	}
	env = append(env, spec.Env...)
	return env
}
