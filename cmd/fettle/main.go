// Command fettle is the CLI for the fettle audit harness.
//
// See FETTLE.md at the repo root for the design.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// Command is one fettle subcommand. Each owns its own flagset; main routes
// argv[1] to the matching command and hands the rest to Run.
type Command interface {
	Name() string
	Synopsis() string
	Run(args []string) error
}

var commands = []Command{
	&initCmd{},
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage(os.Stdout)
		return
	}
	for _, c := range commands {
		if c.Name() == name {
			err := c.Run(os.Args[2:])
			if err == nil {
				return
			}
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			fmt.Fprintln(os.Stderr, "fettle: "+err.Error())
			os.Exit(1)
		}
	}
	fmt.Fprintf(os.Stderr, "fettle: unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(2)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: fettle <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.Name(), c.Synopsis())
	}
}
