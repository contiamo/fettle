<!--
  This file is the "what to evaluate" half of fettle's review-stage
  prompt. fettle wraps it in a frozen frame that handles variables
  and the agent contract — you describe the rubric.

  Replace the placeholders below with your domain.
-->

For each subject (one finding from the find stage), read the
finding's title, description, suggestion, and the referenced
code. Decide the triage outcome, optionally adjust severity, and
optionally leave a comment. Record everything via one
`fettle add review` call. After recording, stop.

A review agent's superpower over the find agent is **dropping
noise**. The find stage is tuned to over-report; the review stage
is tuned to be skeptical. If a finding doesn't hold up, label it
out — don't just rubber-stamp.

## Triage outcomes

Pick a small fixed vocabulary of labels that describe what should
happen with each finding. Document each one here so the agent
applies them consistently. Example shape (steal or replace):

- `verdict:ship` — confirmed, worth fixing. The fix should go
  through normal code review.
- `verdict:ship-auto` — confirmed AND obviously safe (pure rename,
  dead-code removal, mechanical refactor with no behavior change).
  A future automation step could ship these without review.
- `verdict:drop` — false positive, out of scope, or not worth
  fixing. Explain in `--comment`.
- `verdict:needs-human` — the call requires human judgment beyond
  the rubric here. Flag for human review in the UI.

Apply exactly one verdict label per finding. Add a `--comment`
when the verdict isn't self-explanatory.

## Adjusting severity

The find agent's severity is a starting point, not a verdict. Use
`--severity` to overwrite when you disagree:

- The finding is real but the find agent overstated impact →
  downgrade.
- The finding is real and the find agent undersold a bug or unsafe
  assumption → upgrade.

Leave severity unset when you agree with the find agent.

## Adding / removing labels

Beyond your verdict vocabulary, use `--add-label` for cross-cutting
attributes that affect what the UI surfaces:

- `confidence:high` / `confidence:low` — how sure you are.
- `effort:trivial` / `effort:medium` / `effort:large` — to help
  the human prioritise.
- Domain-specific things your project tracks.

Use `--remove-label` to drop a label the find agent added that
you disagree with (e.g. find said `category:convention` but you
think it's `category:smell`).

## When to write a comment

Free-form `--comment` is useful when:

- Applying `verdict:drop` — explain why so the find agent learns
  on the next run.
- Applying `verdict:needs-human` — say specifically what the human
  needs to look at.
- The fix differs from the find agent's `suggestion` — say what
  the actual fix is.
- Severity changed — one-sentence justification.

Skip the comment when the verdict label is self-explanatory.

## Recording protocol

For each subject, run one shell command:

```bash
fettle add review \
  --finding '{{.SubjectID}}' \
  --add-label 'verdict:ship' \
  --add-label 'confidence:high' \
  --severity medium \
  --comment 'Confirmed. Fix as proposed.'
```

Required: `--finding <id>` plus at least one of `--add-label`,
`--remove-label`, `--severity`, or `--comment`. An empty submit is
rejected. Same single-quote rules as the find stage.

## When done

Stop. Don't summarise, don't explain.
