<!--
  This file is the "what to evaluate" half of fettle's review-stage
  prompt. fettle wraps it in a frozen frame that handles the agent
  contract — variable values, the `fettle add review` recording
  protocol, and exit-code handling. You only describe the rubric.

  Replace everything below with your domain.
-->

For each finding (from a find / merge / dedupe run), decide which
labels to apply and whether to write a comment. Group review uses
its own rubric in `review_group.md`.

## Label vocabulary

Define what each label means. For example:

- `confirmed` — the issue is real and worth fixing.
- `false-positive` — the upstream agent misread the code; nothing
  to fix.
- `out-of-scope` — real issue, but explicitly not in scope for this
  audit (legacy module, generated code, deprecated path).
- `needs-human` — the call requires reviewer judgment beyond the
  rubric here; flag for human review in the UI.

Add domain-specific labels as needed (e.g.
`security-impact:exploitable`, `effort:trivial`).

## When to write a comment

The review entry's `--comment` is free-form. Use it when:

- You're applying `false-positive` and want to explain why (the
  source agent will see it on the next run if the same issue
  resurfaces).
- The label needs context — `needs-human` is much more useful with
  a sentence on what specifically needs the reviewer's eye.
- The fix should differ from the upstream agent's `suggestion`.

Skip it when the label is self-explanatory.

## What NOT to do

- Don't relabel things that already have a confident label from
  another reviewer — pick your own opinion only if you disagree.
- Don't review out-of-scope subjects with `confirmed`. Use
  `out-of-scope` so downstream stages can drop them.
- Don't write a comment without a label. Empty `--label` + comment
  is rejected by the harness.
