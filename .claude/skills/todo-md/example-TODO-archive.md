# TODO archive

> **What this file is:** append-only history of completed work moved out of
> `TODO.md` by a periodic GC sweep (see `.claude/skills/todo-md/SKILL.md` —
> feature PRs never write here, so this file never conflicts). Entries land
> **verbatim** at the end of the file as `- [x]` lines; nested items get a
> `> parent: <title>` context line first. Entries are never rewritten;
> links into docs, issues, and PRs are preserved.
>
> **Reading this file:** do not read it whole — `grep` for a symbol or
> feature name, then read only the surrounding lines (`rg -C` / read with
> an offset).

- [x] **First completed thing** ([#1](https://example.com)): kept verbatim
  from TODO.md at GC time.
> parent: Partially-done work
- [x] **Completed child**: context line above records which parent it came
  from (parent title only, no `[ ]`/`[-]` marker — open-state greps stay
  clean). Deeper nesting collapses into one line: `> parent: A › B`.
