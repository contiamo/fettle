<!--
  This file is the "what to evaluate" half of fettle's review-stage
  prompt when reviewing GROUP runs. fettle wraps it in a frozen frame
  that handles the agent contract — variable values, the
  `fettle add review` recording protocol, and exit-code handling. You
  only describe the rubric.

  This is the per-GROUP review prompt. The per-finding review
  prompt lives in `review.md` next to this file. Group review is a
  cluster-level verdict; per-finding reviews are surfaced as Member
  reviews for context but stay independent.

  Replace everything below with your domain.
-->

For each group (a cluster from a `fettle run group` run), decide
what labels to apply at the cluster level. Per-finding reviews are
surfaced for context but stay independent — your verdict is about
the *cluster as a whole*, not its members.

## Label vocabulary

Define what each cluster-level label means. For example:

- `cluster-quality:tight` — members share a single concern,
  PR-shaped (one reviewer, one focused diff).
- `cluster-quality:mixed` — members are reasonable individually but
  mix concerns; recommend splitting before turning into a PR.
- `cluster-quality:loose` — the agent grouped weakly-related
  findings; the cluster doesn't add value over per-finding review.
- `out-of-scope` — the entire cluster is out of scope (e.g. legacy
  module, generated code, deprecated path).
- `needs-human` — the cluster requires reviewer judgment beyond
  this rubric; flag for human review in the UI.

Add domain-specific labels as needed (e.g.
`pr-effort:trivial`, `pr-effort:large`, `theme:auth`).

## When to write a comment

The review entry's `--comment` is free-form. Use it when:

- Applying `cluster-quality:mixed` and you want to suggest *how*
  to split (e.g. "split off the two security findings; the rest
  are formatting").
- The cluster has a one-line PR title that captures the theme
  better than the agent's `title` — share it.
- `out-of-scope` needs a sentence on which scope rule excludes it.

Skip it when the label is self-explanatory.

## What NOT to do

- Don't relabel individual member findings here. This file is for
  cluster-level verdicts. If a member finding is wrong, the
  per-finding review pass (`review.md`) handles that.
- Don't apply `confirmed` / `false-positive` labels — those are
  per-finding concepts. Use cluster-level labels instead.
- Don't write a comment without a label. Empty `--label` + comment
  is rejected by the harness.
