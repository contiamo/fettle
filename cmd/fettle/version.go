package main

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/contiamo/fettle/internal/project"
	"github.com/spf13/cobra"
)

// versionCmd mirrors `fettle --version` for users who reach for the
// subcommand form. Both routes share rootCmd.Version (set in main's
// init) so output is identical.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the fettle version",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Match cobra's default --version template exactly:
		// "<name> version <version>\n".
		_, err := fmt.Printf("%s version %s\n", rootCmd.Use, rootCmd.Version)
		return err
	},
}

// versionDetails returns the multi-line value for rootCmd.Version,
// rendered by cobra's default template as `fettle version <details>`.
// It pulls runtime build info (Go's module version, vcs commit /
// dirty / time) so installed binaries report their real provenance:
//
//   - `go install …@v0.2.0` or `@latest` (with tags) → "v0.2.0"
//   - `go install …@main` (or any non-tagged ref)   → pseudo-version
//     like "v0.0.0-20260520143000-abc123def456"
//   - local `go install` from a checkout            → "<fallback> (devel)"
//     with vcs.revision / vcs.modified appended when available.
//
// Falls back to project.Version (the hardcoded const) only when
// debug.ReadBuildInfo can't tell us anything — exceedingly rare.
func versionDetails() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return project.Version
	}

	version := info.Main.Version
	if version == "" || version == "(devel)" {
		version = project.Version + " (devel)"
	}

	var commit, modified, vcsTime string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			modified = s.Value
		case "vcs.time":
			vcsTime = s.Value
		}
	}

	var b strings.Builder
	b.WriteString(version)
	if commit != "" {
		state := "clean"
		if modified == "true" {
			state = "dirty"
		}
		short := commit
		if len(short) > 10 {
			short = short[:10]
		}
		fmt.Fprintf(&b, "\ncommit: %s (%s)", short, state)
		if vcsTime != "" {
			fmt.Fprintf(&b, " %s", vcsTime)
		}
	}
	fmt.Fprintf(&b, "\ngo: %s", info.GoVersion)
	return b.String()
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
