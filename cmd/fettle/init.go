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
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		target := initFlags.target
		if target == "" {
			target = wd
		}
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("resolve target: %w", err)
		}
		cfg := project.NewConfig(absTarget, initFlags.agent, initFlags.model)
		if err := project.Init(wd, cfg); err != nil {
			return err
		}
		fmt.Printf("Initialized fettle project in %s\n", wd)
		fmt.Println("Edit instructions/find.md to describe what to look for, then run `fettle find`.")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initFlags.target, "target", "", "target repository path (default: current directory)")
	initCmd.Flags().StringVar(&initFlags.agent, "agent", "claude", "default agent: claude, codex, or gemini")
	initCmd.Flags().StringVar(&initFlags.model, "model", "sonnet", "default model")
	rootCmd.AddCommand(initCmd)
}
