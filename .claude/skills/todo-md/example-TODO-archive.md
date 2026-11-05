# TODO archive

> **What this file is:** append-only history of completed work moved out of
> `TODO.md` by a periodic GC sweep (see `.claude/skills/todo-md/gc.md` —
> feature PRs never write here, so this file never conflicts). Each item
> lands at the end of the file as one line, prefixed with its ancestor
> chain (`- [x] A > B > **C**: ...`), so a grep hit names the whole path.
> Wording and links are kept verbatim; only the layout is normalized.
>
> **Reading this file:** do not read it whole — `grep` for a symbol or
> feature name; the matching line is the whole entry.

- [x] **First completed thing** ([#1](https://example.com)): wrapped lines in TODO.md are joined into one line at GC time.
- [x] Partially-done work > **Completed child**: the chain names the parent by its title only (no `[ ]`/`[-]` marker — open-state greps stay clean).
- [x] Partially-done work > Completed child > **Grandchild**: deeper nesting extends the chain.
- Partially-done work > Done when: notes without a checkbox get a chain too.
