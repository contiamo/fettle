package main

import (
	"fmt"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

// versionCmd mirrors `fettle --version` for users who reach for the
// subcommand form. Output shape matches cobra's default `--version`
// output (`fettle version <semver>`) so the two are interchangeable
// in scripts.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the fettle version",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Printf("fettle version %s\n", project.Version)
		return err
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
