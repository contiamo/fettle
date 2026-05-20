# Set up fettle (agent-driven)

**This file is written for a coding agent to read on the user's behalf.** If you are a human, hand the URL of this file to your coding agent and ask it to follow these instructions.

Fettle is a file-oriented LLM audit harness: it runs an agent per file matching the user's globs and records findings to JSONL, with a local web UI for triage. See `FETTLE.md` in this repo for the full design.

## Instructions for the agent

1. **Install fettle** if it isn't already on PATH:

   ```bash
   go install github.com/contiamo/fettle/cmd/fettle@latest
   ```

   Confirm with `which fettle`. Requires Go 1.22+.

2. **Ask the user whether to install the skill globally or just for this project** (recommend global unless they have a reason to scope it). Then run the matching command:

   ```bash
   fettle install-skill claude-code                    # global: ~/.claude/skills/fettle/ — recommended
   fettle install-skill claude-code --scope project    # project-local: ./.claude/skills/fettle/
   ```

   - **Global (recommended):** available across every repo, no extra setup per project. The right default for solo use.
   - **Project-local:** writes `.claude/skills/fettle/` under the cwd. Useful when the team wants the skill checked into the repo so everyone is locked to the same fettle version.

   The skill is embedded in the fettle binary, so it's always in sync with the installed version. Re-run with `--force` to upgrade after a `go install`. List other supported agents with `fettle install-skill --list`.

3. **Follow the now-installed skill.** Re-read your skill list (the fettle skill should now appear) and follow its instructions to walk the user through setup, prompt tailoring, smoke testing, the real find run, and launching the UI.

That's it. The skill carries the actual workflow; this file is only the bootstrap.
