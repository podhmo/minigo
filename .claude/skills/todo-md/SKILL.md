---
name: todo-md
description: Maintaining minigo's task tracker (TODO.md index + docs/todo/ topic files). Use whenever you add a task, complete or partially complete one, or re-apply TODO.md changes across merges/rebases.
---

# TODO.md upkeep

## Files

- `TODO.md` — index only: the rules note plus a table of topic files.
- `docs/todo/<topic>.md` — the actual tracker per feature area: `## Open`
  then `## Done`.

## Adding a task

Append `- [ ]` under the matching topic file's `## Open`: a bold title, 1–3
sentences of what remains, and links. No fitting topic: create
`docs/todo/<name>.md` (Open then Done sections) and add a row to the TODO.md
table. Either way, keep the topic's Open count in the table current.

## Completing a task

In the same PR that lands the work, move the entry to the same file's
`## Done` section as `- [x]` — verbatim; done entries are not grown after
the move. Completed children of a `[-]` parent move individually; the parent
stays in Open while work remains. Update the topic's Open count in TODO.md.

## Reading history

Topic files are the per-feature log. `rg <symbol> docs/todo/` finds entries;
read one file's `## Done` for context.

See [why.md](why.md) for the background of this scheme.
