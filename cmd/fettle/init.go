package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/contiamo/fettle/internal/project"
)

type initCmd struct{}

func (initCmd) Name() string     { return "init" }
func (initCmd) Synopsis() string { return "Create a new fettle project in the current directory" }

func (initCmd) Run(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	target := fs.String("target", "", "target repository path (default: current directory)")
	agent := fs.String("agent", "claude", "default agent: claude, codex, or gemini")
	model := fs.String("model", "sonnet", "default model")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: fettle init [flags]")
		fmt.Fprintln(fs.Output())
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if *target == "" {
		*target = wd
	}
	absTarget, err := filepath.Abs(*target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	cfg := project.NewConfig(absTarget, *agent, *model)
	if err := project.Init(wd, cfg); err != nil {
		return err
	}
	fmt.Printf("Initialized fettle project in %s\n", wd)
	fmt.Println("Edit instructions/find.md to describe what to look for, then run `fettle find`.")
	return nil
}
