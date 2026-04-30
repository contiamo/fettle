<!--
  This file is the "what to look for" half of fettle's find-stage prompt.
  fettle wraps it in a frozen frame that handles variable values, the
  `fettle finding add` recording protocol, exit-code handling, and output
  discipline. You don't need to repeat any of that here — just describe
  the analysis you want.

  Replace everything below with your domain.
-->

Identify issues in the file under analysis. Replace this paragraph with
the patterns you actually want fettle to find — convention violations,
security smells, missing docs, license-header gaps, anything you can
state in concrete terms.

Be specific in your criteria; vague advice produces noisy findings.

## Severity guidance

If you tell the agent to use `--severity`, name the scale here. For
example:

- `high` — bug, swallowed error with concrete impact, unsafe assumption
- `medium` — solid improvement; idiomatic gap with material reader cost
- `low` — nice-to-have

## Labels

If you use `--label`, document the conventions here. For example:

- Always include one `category:<word>` tag from the set you define.
- Add `confidence:high` / `confidence:low` when uncertain.

## What NOT to flag

List what's out of scope, so the agent doesn't churn:

- Stylistic preferences not documented here.
- Imagined future-proofing.
- Generated files (skip them with zero findings).
