---
name: todo-md
description: Maintaining minigo's task tracker (TODO.md + TODO-archive.md). Use whenever you add a task, complete or partially complete one, or re-apply TODO.md changes across merges/rebases.
---

# TODO.md upkeep

## Files

- `TODO.md` — actionable work only: `[ ]` open, `[-]` partially done.
- `TODO-archive.md` — append-only flat log of completed entries.

## Adding a task

Append `- [ ]` under `## To Be Implemented`: a bold title, 1–3 sentences of
what remains, and links (`docs/sketch/*` docs, issues/PRs). Bugs found
mid-task get an entry too. Never write done-work narratives here.

## Completing a task

In the same PR that lands the work:

1. Delete the entry from `TODO.md`.
2. Append it to the **end** of `TODO-archive.md` as a `- [x]` line, verbatim.
   If it was nested under a parent item, prepend one context line
   `> parent: <parent's title text>` (the parent's own text, *without* its
   `[ ]`/`[-]` marker) so the line is self-contained. Deeper nesting:
   collapse the ancestor chain into that one line (`> parent: A › B › …`).
3. Completed children of a still-open `[-]` parent move the same way; the
   parent stays in `TODO.md` until its last open child resolves.

## Reading the archive

Never read it whole. `rg <symbol>` to find the entry, then read the
surrounding window (`rg -C 20`, or an offset read).

See [why.md](why.md) for the background of this scheme.
