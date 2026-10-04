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

In the PR that lands the work, mark the entry `- [x]` and leave it —
deleting it outright is also fine. Completed children of a still-open `[-]`
parent are marked the same way; the parent stays until its last open child
resolves.

**Do not edit `TODO-archive.md` in a feature PR.** Consolidation is
asynchronous so the archive can never conflict between branches.

## GC: consolidating completed entries

Run whenever `[x]` entries accumulate in `TODO.md` — any session may do it
(e.g. at the end of a work round); there is no fixed schedule. For each
`[x]` entry in `TODO.md`:

1. Delete it from `TODO.md`.
2. Append it to the **end** of `TODO-archive.md` verbatim, keeping the
   `- [x]` marker. If it was nested under a parent item, prepend one context
   line `> parent: <parent's title text>` (the parent's own text, *without*
   its `[ ]`/`[-]` marker) so the line is self-contained. Deeper nesting:
   collapse the ancestor chain into that one line (`> parent: A › B › …`).
   If the parent's title has drifted, use its current text — the GC pass is
   where such drift gets reconciled.

Only GC writes to `TODO-archive.md`, so the file stays append-only and
conflict-free.

## Reading the archive

Never read it whole. `rg <symbol>` to find the entry, then read the
surrounding window (`rg -C 20`, or an offset read).

## Porting this scheme to another repo

Copy this skill directory wholesale, then seed the tracker from the
bundled starters — [example-TODO.md](example-TODO.md) and
[example-TODO-archive.md](example-TODO-archive.md) — which carry the
header notices and a minimal worked example (open/`[-]`/`[x]` shapes plus
`> parent:` archive lines).

See [why.md](why.md) for the background of this scheme.
