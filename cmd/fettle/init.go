package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

var initFlags struct {
	target string
	agent  string
	model  string
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a new fettle project in the current directory",
	RunE: func(cmd *cobra.Command, args []string) error {
		switch initFlags.agent {
		case "claude", "codex":
			// supported
		default:
			return fmt.Errorf("unsupported agent %q (supported: claude, codex)", initFlags.agent)
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
		cfg := project.NewConfig(absTarget, initFlags.agent, initFlags.model)
		if err := project.Init(dir, cfg); err != nil {
			return err
		}
		fmt.Printf("Initialized fettle project in %s\n", dir)
		fmt.Println("Edit instructions/find.md to describe what to look for, then run `fettle find`.")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initFlags.target, "target", "", "target repository path (default: current directory)")
	initCmd.Flags().StringVar(&initFlags.agent, "agent", "claude", "default agent: claude or codex")
	initCmd.Flags().StringVar(&initFlags.model, "model", "", "default model (empty = agent CLI default)")
	rootCmd.AddCommand(initCmd)
}
