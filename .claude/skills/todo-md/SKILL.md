---
name: todo-md
description: Maintaining minigo's task tracker (TODO.md — open work only). Use whenever you add a task, complete or partially complete one, or re-apply TODO.md changes across merges/rebases.
---

# TODO.md upkeep

## Files

- `TODO.md` — the whole tracker: open work only (`[ ]` / `[-]`).

## Adding a task

Append `- [ ]` under `## To Be Implemented`: a bold title, 1–3 sentences of
what remains, and links (`docs/sketch/*` docs, issues/PRs). Bugs found
mid-task get an entry too. Fix narratives and status logs belong in the
sketch doc or the PR body — never in this file.

## Completing a task

Delete the entry's line in the same PR that lands the work. Completed
children of a still-open `[-]` parent delete the same way; the parent stays
while open work remains.

## Tracing completed work

`git log -p TODO.md` or `git log -S <symbol>` for entry history; linked
`docs/sketch/*` reports and the PRs that landed the work carry the detail.

See [why.md](why.md) for the background of this scheme.
