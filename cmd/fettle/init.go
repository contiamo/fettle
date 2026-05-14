package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

var initFlags struct {
	target  string
	agent   string
	model   string
	include []string
	exclude []string
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a new fettle project in the current directory",
	Long: `init creates .fettle/ in the current directory with a stub
instructions/ tree and a config.json scoped to the patterns you pass.

At least one --include glob is required — there's no
project-independent default that would do the right thing, and a
permissive one (` + "`**/*`" + `) would pull in lockfiles, vendored
dependencies, generated code, and binary blobs. Pass the globs that
match the files you actually want scanned:

  fettle init --include '**/*.go'
  fettle init --include 'src/**/*.{ts,tsx}' --include '**/*.css'
  fettle init --include '**/*.py' --exclude 'tests/**'

The walker hard-skips ` + "`.git/` / `.hg/` / `.svn/` / `node_modules/`" + `
regardless of globs, so you don't need to exclude those.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		switch initFlags.agent {
		case "claude", "codex":
			// supported
		default:
			return fmt.Errorf("unsupported agent %q (supported: claude, codex)", initFlags.agent)
		}
		if len(initFlags.include) == 0 {
			return fmt.Errorf("at least one --include glob is required (e.g. --include '**/*.go'); see `fettle init --help`")
		}
		dir, err := projectDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create project dir: %w", err)
		}
		target := initFlags.target
		if target == "" {
			target = dir
		}
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		cfg := project.NewConfig(absTarget, initFlags.agent, initFlags.model, initFlags.include, initFlags.exclude)
		if err := project.Init(dir, cfg); err != nil {
			return err
		}
		fmt.Printf("Initialized fettle project in %s\n", dir)
		fmt.Println("Edit .fettle/instructions/find.md to describe what to look for, then run `fettle run find`.")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initFlags.target, "target", "", "target repository path (default: current directory)")
	initCmd.Flags().StringVar(&initFlags.agent, "agent", "claude", "default agent: claude or codex")
	initCmd.Flags().StringVar(&initFlags.model, "model", "", "default model (empty = agent CLI default)")
	initCmd.Flags().StringArrayVar(&initFlags.include, "include", nil, "doublestar glob for files to scan (required, repeatable)")
	initCmd.Flags().StringArrayVar(&initFlags.exclude, "exclude", nil, "doublestar glob for files to skip (repeatable)")
	initCmd.GroupID = groupProject
	rootCmd.AddCommand(initCmd)
}
