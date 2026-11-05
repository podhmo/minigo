---
name: todo-md
description: Maintaining minigo's task tracker (TODO.md + TODO-archive.md). Use whenever you add a task, complete or partially complete one, re-apply TODO.md changes across merges/rebases, or run the GC sweep (`make todo-gc`).
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

In the PR that lands the work, mark the entry `- [x]` and leave it —
deleting it outright is also fine. Completed children of a still-open `[-]`
parent are marked the same way; the parent stays until its last open child
resolves.

**Do not edit `TODO-archive.md` in a feature PR.** Consolidation is
asynchronous so the archive can never conflict between branches.

## GC: consolidating completed entries

When `[x]` entries accumulate, any session may run `make todo-gc` (no fixed
schedule). Only GC writes to `TODO-archive.md`. Read [gc.md](gc.md) only
when running or changing the sweep.

## Reading the archive

Never read it whole. `rg <symbol>` returns the matching item with its
ancestor chain on the same line; read neighbouring lines (`rg -C 3`) only
to see its siblings.

## Porting this scheme to another repo

Copy this whole directory to `.claude/skills/todo-md/` — it is
self-contained: the rules (this file and [gc.md](gc.md)), the GC tool
([todo-gc/](todo-gc/main.go), its own Go module with no dependencies), and
reference starters [example-TODO.md](example-TODO.md) /
[example-TODO-archive.md](example-TODO-archive.md). The starters ship under
`example-*` names so nothing reads them as the live files day to day. Then
rename `minigo` in the `description` above and optionally add a
`todo-gc` make target that runs the command in [gc.md](gc.md).

See [why.md](why.md) for the background of this scheme.
