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

Run whenever `[x]` entries accumulate in `TODO.md` — any session may do it
(e.g. at the end of a work round); there is no fixed schedule. Run it with
the tool, not by hand:

```sh
make todo-gc   # = go -C .claude/skills/todo-md/todo-gc run . -w ../../../..
```

Without `-w` (`go -C .claude/skills/todo-md/todo-gc run . ../../../..`) it
only reports, and exits 1 when a file would change. The tool lives in this
skill directory as its own dependency-free Go module,
[todo-gc/](todo-gc/main.go); edit it rather than working around it when the
rule changes (`go -C .claude/skills/todo-md/todo-gc test ./...`). It does
the following:

1. Delete each `[x]` entry from `TODO.md`. An `[x]` entry that still has an
   open child is left in place with a warning — fix the entry by hand.
2. Append it to the **end** of `TODO-archive.md` as **one physical line per
   item**, so `rg <word>` on any word of any item returns a line that also
   names every ancestor:
   - Start the line with `- [x] `, then the ancestor chain, then the item:
     `- [x] A > B > **C**: ...`. Ancestors are named by their bold title
     (else the text before the first colon, else the whole text), with no
     `[ ]`/`[-]` marker. A top-level item has no chain. A drifted parent
     title is reconciled by its current text at GC time.
   - Join the item's wrapped lines into that one line (a space, except
     between Japanese characters). Keep the wording and links verbatim.
   - Never archive a subtree. A completed parent with `[x]` children goes
     first without them; each child, grandchild, … follows as its own
     line, in document order.
   - Plain `- ` notes without a checkbox (`完了の条件:`, `決めること:`)
     under an archived item get the same treatment without the checkbox:
     `- A > 完了の条件: ...`.
3. Normalize the rest of the archive into the same form (idempotent), so
   older multi-line entries and `> parent:` context lines never come back.

For example:

```markdown
<!-- TODO.md -->
- [x] **A**: one
  two.
  - [x] **B**: ...
  - [x] **C**: ...
    - [x] **D**: ...
  - 完了の条件: ...

<!-- appended to TODO-archive.md -->
- [x] **A**: one two.
- [x] A > **B**: ...
- [x] A > **C**: ...
- [x] A > C > **D**: ...
- A > 完了の条件: ...
```

Only GC writes to `TODO-archive.md`, so the file stays append-only and
conflict-free.

## Reading the archive

Never read it whole. `rg <symbol>` returns the matching item with its
ancestor chain on the same line; read neighbouring lines (`rg -C 3`) only
to see its siblings.

## Porting this scheme to another repo

Copy this whole directory to `.claude/skills/todo-md/` — it is
self-contained: the rules (this file), the GC tool
([todo-gc/](todo-gc/main.go), its own Go module with no dependencies), and
reference starters [example-TODO.md](example-TODO.md) /
[example-TODO-archive.md](example-TODO-archive.md). The starters ship under
`example-*` names so nothing reads them as the live files day to day. Then
rename `minigo` in the `description` above and optionally add a
`todo-gc` make target that runs the command above.

See [why.md](why.md) for the background of this scheme.
