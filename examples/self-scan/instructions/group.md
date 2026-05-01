<!--
  This file is the "domain rubric" half of fettle's group-stage
  prompt. fettle wraps it in a frozen frame that handles the agent
  contract — receiving FINDINGS_JSON and REVIEWS_JSON, the
  `fettle add group` recording protocol, and exit-code handling.
  You only describe the clustering judgment.

  Replace everything below with your domain.
-->

You are clustering findings into PR-sized batches. Each group you emit
should be small enough that one reviewer can land it as a single PR.

## Clustering signals

In rough priority order, prefer to cluster findings that share:

- **The same file or sibling files in one package/module.** A PR
  scoped to a single module is the easiest to review.
- **The same root cause or fix shape.** Findings that all want the
  same kind of edit (e.g. "wrap in context.WithTimeout", "rename
  field to snake_case") cluster cleanly even across files.
- **The same `category:` label.** If your `find.md` emits category
  labels, prefer same-category clusters.
- **Overlapping `references[]`.** Findings that point at the same
  secondary site likely describe the same underlying issue.

Findings that share none of these are typically separate groups
(possibly singletons).

## Sizing

Aim for 3–10 findings per group. A homogeneous sweep (e.g. "fix the
same lint across 30 files") can go larger if the diff per finding is
small. A group with 1 finding is fine; don't pad.

## Title and summary

`--title` is what shows up in the run picker — make it scan well at a
glance. `--summary` is one paragraph: what these findings have in
common, and what landing the group looks like.

## Labels

Add `effort:small|medium|large` based on rough diff size, and any
domain labels that would help a reviewer triage. Skip labels you
already see in member findings — those propagate naturally via
display.

## What NOT to do

- Don't include findings whose review state marks them rejected
  (`false-positive`, `out-of-scope`, `needs-human`). The frame's
  filter rule covers this; mention here only if your project uses
  different rejection labels.
- Don't fabricate findings or duplicate them across groups.
