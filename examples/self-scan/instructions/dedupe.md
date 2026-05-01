<!--
  This file is the "domain rubric" half of fettle's dedupe-stage
  prompt. fettle wraps it in a frozen frame that handles the agent
  contract — receiving FINDINGS_JSON with from_run + review state,
  the `fettle find add --canonical-of` recording protocol, and
  exit-code handling. You only describe the merging judgment.

  Replace everything below with your domain.
-->

You are consolidating findings from multiple agents that scanned
the same code. Decide which findings describe the same underlying
issue and synthesize one canonical finding per cluster; pass through
unique findings as canonical singletons.

## Equivalence criteria

Two findings describe the same underlying issue when:

- They anchor at the same `(file, line)` (or close — within a few
  lines, when the title supports the same root cause).
- Their `title` + `description` describe the same problem, even if
  worded differently.
- Their fixes (`suggestion`) agree on direction.

Findings on different files, or at the same file with materially
different concerns, are NOT equivalent — they're separate canonical
findings.

## Severity reconciliation

When source findings disagree on severity, pick the higher one if
the underlying concern is the same. If they disagree because they
describe genuinely different concerns, that's a sign they shouldn't
have been clustered.

## Labels

Union `--label` across cluster members. Add a `consensus:N` label
where N is the number of distinct source runs that flagged it
(e.g. `consensus:2` when both agents agreed). Skip the consensus
label for singletons.

## Title and description

Write a fresh title and description that capture the consensus view
— don't blindly copy one source. The canonical finding speaks for
the whole cluster.

## What NOT to do

- Don't include sources labeled `false-positive` / `out-of-scope` /
  `needs-human` by any reviewer in any input run. Treat them as
  rejected and skip the cluster (or skip the rejected member if
  others remain valid).
- Don't fabricate findings that weren't in any input run.
- Don't relitigate review decisions; the upstream review stage
  already encoded reviewer judgment in labels.
