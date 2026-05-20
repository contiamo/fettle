package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/contiamo/fettle/internal/skill"
	"github.com/spf13/cobra"
)

var installSkillFlags struct {
	scope  string
	output string
	force  bool
	list   bool
}

var installSkillCmd = &cobra.Command{
	Use:   "install-skill [agent]",
	Short: "Install fettle's bundled agent skill into a coding agent's local skill directory",
	Long: `install-skill writes fettle's bundled skill for the named agent
into that agent's local skill directory so a coding agent can drive
fettle end-to-end. The skill bundle is embedded in this binary —
no network access is needed, and the bundle is version-locked to
the fettle binary that wrote it.

  fettle install-skill claude-code
      Write the Claude Code bundle to $HOME/.claude/skills/fettle/
      (user scope — available across every repo).

  fettle install-skill claude-code --scope project
      Write to ./.claude/skills/fettle/ in the cwd (project scope —
      check into the repo to share the bundle with teammates locked
      to your fettle version).

  fettle install-skill claude-code --output /tmp/skill
      Write to an explicit location, ignoring --scope (for testing).

  fettle install-skill claude-code --force
      Overwrite an existing installation. Without --force, an existing
      destination is left untouched to protect user-edited copies.

  fettle install-skill --list
      Print the agents this binary ships a bundle for.

For --scope user (default): if the agent's home directory doesn't
exist (e.g. $HOME/.claude/ for Claude Code), install-skill refuses
with a clear error — the agent almost certainly isn't installed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstallSkill,
}

func runInstallSkill(cmd *cobra.Command, args []string) error {
	if installSkillFlags.list {
		for _, a := range skill.Agents() {
			fmt.Println(string(a))
		}
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("agent is required (e.g. `fettle install-skill claude-code`); see --list for supported agents")
	}
	agent := skill.Agent(args[0])
	if !isKnownAgent(agent) {
		return fmt.Errorf("unknown agent %q (supported: %s)", args[0], joinAgents(skill.Agents()))
	}
	scope := skill.Scope(installSkillFlags.scope)
	if !isKnownScope(scope) {
		return fmt.Errorf("unknown scope %q (supported: %s)", installSkillFlags.scope, joinScopes(skill.Scopes()))
	}

	dest, err := skill.Install(agent, scope, installSkillFlags.output, installSkillFlags.force)
	if err != nil {
		switch {
		case errors.Is(err, skill.ErrAgentMissingHome):
			return fmt.Errorf("%w — is the agent installed?", err)
		case errors.Is(err, skill.ErrAlreadyInstalled):
			return fmt.Errorf("%w (pass --force to overwrite)", err)
		default:
			return err
		}
	}

	fmt.Printf("Installed fettle skill for %s (%s scope) at %s\n", agent, scope, dest)
	fmt.Println("The agent can now follow the skill — point your coding agent at it and ask for fettle help.")
	return nil
}

func isKnownAgent(a skill.Agent) bool {
	return slices.Contains(skill.Agents(), a)
}

func isKnownScope(s skill.Scope) bool {
	return slices.Contains(skill.Scopes(), s)
}

func joinAgents(agents []skill.Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}

func joinScopes(scopes []skill.Scope) string {
	names := make([]string, len(scopes))
	for i, s := range scopes {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

func init() {
	installSkillCmd.Flags().StringVar(&installSkillFlags.scope, "scope", string(skill.ScopeUser), "install scope: 'user' (~/.claude/skills/, available everywhere) or 'project' (./.claude/skills/, local to cwd)")
	installSkillCmd.Flags().StringVar(&installSkillFlags.output, "output", "", "explicit install destination, overrides --scope (for testing)")
	installSkillCmd.Flags().BoolVar(&installSkillFlags.force, "force", false, "overwrite an existing installation")
	installSkillCmd.Flags().BoolVar(&installSkillFlags.list, "list", false, "list the agents this binary ships a skill for")
	installSkillCmd.GroupID = groupProject
	rootCmd.AddCommand(installSkillCmd)
}
