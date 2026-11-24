# AI-assisted CI investigation: verified exercise

## Intent and safe inputs

On 2026-11-24, manually replay a real historical failure through the existing
metadata-only AI triage workflow, then compare its hypotheses with the original
job evidence. No new failure is injected into the default branch. The model
receives failed job/step names, not repository files, logs, artifacts or secrets.
The model has no tools and cannot apply a fix.

## Observed result

- [Original failed run](https://github.com/ChuDucAnh242002/di-agent/actions/runs/45057512373):
  `kind Helm smoke test (di-agent-system chart, demo)`, step
  `Install PostgreSQL and load the replay fixture`.
- [Live AI replay](https://github.com/ChuDucAnh242002/di-agent/actions/runs/45371815108):
  collection, pinned CLI installation, model diagnosis and publication all
  completed successfully. This was not a skipped triage job or a mock response.
- The model explicitly reported **insufficient evidence**, suggested reviewing
  PostgreSQL startup and fixture-loading logs, and did not claim a confirmed fix.
- Independent log inspection found `psql` failed with
  `FATAL: the database system is shutting down`. This supports checking database
  lifecycle/readiness; metadata alone did not establish the underlying cause.
- The [historical exercise issue](https://github.com/ChuDucAnh242002/di-agent/issues/17)
  was marked as a replay and closed, not left as a current main-branch incident.
  The latest observed main CI run
  [45256783504](https://github.com/ChuDucAnh242002/di-agent/actions/runs/45256783504)
  had passed. No source fix or automated remediation is claimed by this exercise.

## Reproduce after merging the preview improvement

Run **AI CI triage** manually with a failed same-repository CI `run_id`.
Leave **publish_report** off: the diagnosis appears in the Actions summary,
even for a closed historical PR, without writing an incident or PR comment.
Automatic failure triage retains its normal deduplicated publication behavior.
Enable publication only when deliberately requesting a current review handoff.

The preview regression test verifies summary creation with no GitHub write API
available. Existing prompt, output-validation, isolation, fork and deduplication
tests remain in place.

## Human review still required

A person must inspect the original logs, evaluate whether hypotheses are useful,
approve any proposed patch, and verify its tests and outcome. This document
records observed AI execution and an independent evidence check; it does not
claim human approval, production recovery, or measured AI accuracy.
