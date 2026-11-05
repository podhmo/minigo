# GC: consolidating completed entries

Read this only when running or changing the GC sweep; day-to-day TODO.md
upkeep is in [SKILL.md](SKILL.md).

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
