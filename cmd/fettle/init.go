package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

var initFlags struct {
	target  string
	agent   string
	model   string
	walker  string
	include []string
	exclude []string
}

var initCmd = &cobra.Command{
	Use:   "init <path>",
	Short: "Create a new fettle project at <path>",
	Long: `init creates a fettle project at the path you name. The path
is either an existing directory (must be empty) or a new directory
whose parent already exists — fettle uses mkdir, not mkdir -p, so a
typo can't silently create a chain of nested directories.

  fettle init .                  # init in the current directory (must be empty)
  fettle init foobar             # create ./foobar and init
  fettle init ../audits/api      # ../audits must already exist

The project directory's name is up to you. Subsequent commands find
it by upward-walking from cwd looking for fettle.json, or by the
--project-dir flag / $FETTLE_PROJECT_DIR env override.

At least one --include glob is required — there's no
project-independent default that would do the right thing, and a
permissive one (` + "`**/*`" + `) would pull in lockfiles, vendored
dependencies, generated code, and binary blobs. Pass globs that match
the files you actually want scanned:

  fettle init audits --include '**/*.go'
  fettle init audits --include 'src/**/*.{ts,tsx}' --include '**/*.css'

With the default --walker git, anything in .gitignore is dropped on
top of your --exclude patterns. Use --walker fs for non-git targets
or when you want to scan ignored files explicitly.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch initFlags.agent {
		case "claude", "codex":
			// supported
		default:
			return fmt.Errorf("unsupported agent %q (supported: claude, codex)", initFlags.agent)
		}
		switch initFlags.walker {
		case project.WalkerGit, project.WalkerFS:
			// supported
		default:
			return fmt.Errorf("unsupported walker %q (supported: git, fs)", initFlags.walker)
		}
		if len(initFlags.include) == 0 {
			return fmt.Errorf("at least one --include glob is required (e.g. --include '**/*.go'); see `fettle init --help`")
		}

		projectDir, err := resolveInitTarget(args[0])
		if err != nil {
			return err
		}

		target := initFlags.target
		if target == "" {
			target = projectDir
		}
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("resolve --target: %w", err)
		}

		cfg := project.NewConfig(absTarget, initFlags.agent, initFlags.model, initFlags.walker, initFlags.include, initFlags.exclude)
		if err := project.Init(projectDir, cfg); err != nil {
			return err
		}
		fmt.Printf("Initialized fettle project in %s\n", projectDir)
		fmt.Println("Edit instructions/find.md to describe what to look for, then run `fettle run find`.")
		return nil
	},
}

// resolveInitTarget validates the positional <path> arg and returns
// the absolute path the project should live at. The rules:
//
//   - The path resolves to an absolute path against cwd.
//   - The path's parent directory must already exist. fettle uses
//     `mkdir`, not `mkdir -p`, so a typo can't silently create a chain
//     of nested directories.
//   - If the target itself doesn't exist, fettle creates it.
//   - If the target exists, it must be a directory and it must be
//     empty (zero entries, including hidden files).
//   - If the target already contains a fettle.json, init refuses —
//     re-running `fettle init` is a deliberate no-op rather than a
//     silent overwrite.
//
// On success the directory exists and is ready for project.Init to
// populate; on error nothing has been created.
func resolveInitTarget(arg string) (string, error) {
	if arg == "" {
		return "", fmt.Errorf("init: <path> argument is required")
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("resolve init path %q: %w", arg, err)
	}

	parent := filepath.Dir(abs)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("parent directory %s does not exist (fettle init won't create nested missing directories — create them yourself or pick a different path)", parent)
		}
		return "", fmt.Errorf("stat %s: %w", parent, err)
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("parent %s is not a directory", parent)
	}

	info, err := os.Stat(abs)
	switch {
	case err == nil:
		if !info.IsDir() {
			return "", fmt.Errorf("%s exists but is not a directory", abs)
		}
		// Refuse to init in a directory that's already a fettle
		// project — re-running init shouldn't surprise-overwrite the
		// existing config.
		if _, err := os.Stat(filepath.Join(abs, project.ConfigName)); err == nil {
			return "", fmt.Errorf("%s already contains a %s (already a fettle project)", abs, project.ConfigName)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", filepath.Join(abs, project.ConfigName), err)
		}
		// Non-empty existing dir is rejected — too easy to clutter a
		// random folder with runs/ and instructions/ stubs by mistake.
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", abs, err)
		}
		if len(entries) > 0 {
			return "", fmt.Errorf("%s already exists and is not empty (pick an empty directory or a new path)", abs)
		}
		return abs, nil
	case errors.Is(err, fs.ErrNotExist):
		// Create the single leaf directory. The parent existed
		// (checked above) so this is one mkdir, not mkdir -p.
		if err := os.Mkdir(abs, 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", abs, err)
		}
		return abs, nil
	default:
		return "", fmt.Errorf("stat %s: %w", abs, err)
	}
}

func init() {
	initCmd.Flags().StringVar(&initFlags.target, "target", "", "target repository path (default: the project directory)")
	initCmd.Flags().StringVar(&initFlags.agent, "agent", "claude", "default agent: claude or codex")
	initCmd.Flags().StringVar(&initFlags.model, "model", "", "default model (empty = agent CLI default)")
	initCmd.Flags().StringVar(&initFlags.walker, "walker", project.WalkerGit, "file enumerator: git (honour .gitignore) or fs (raw filesystem walk)")
	initCmd.Flags().StringArrayVar(&initFlags.include, "include", nil, "doublestar glob for files to scan (required, repeatable)")
	initCmd.Flags().StringArrayVar(&initFlags.exclude, "exclude", nil, "doublestar glob for files to skip (repeatable)")
	initCmd.GroupID = groupProject
	rootCmd.AddCommand(initCmd)
}
