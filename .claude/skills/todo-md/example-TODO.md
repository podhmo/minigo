# TODO

> **Note on updating this file:** upkeep rules live in
> [`.claude/skills/todo-md/SKILL.md`](.claude/skills/todo-md/SKILL.md) —
> follow them whenever you add or complete a task. Short version: this file
> lists actionable work only (`[ ]` / `[-]`); mark finished work `[x]` — a
> periodic GC sweep appends such entries to `TODO-archive.md`.

This file tracks actionable tasks only; completed work is swept to
`TODO-archive.md` by periodic GC.

## To Be Implemented

### Example feature area

- [ ] **Short actionable title** ([link to design doc or issue](#)): one
  line of context. Entries stay brief — detail lives in linked docs.
- [-] **Partially-done work** ([link](#)): a `[-]` parent stays open while
  it has open children.
  - [ ] **Open child task**: details and links go here.
  - [x] **Completed child**: marked but not yet swept — the next GC pass
    moves it to `TODO-archive.md` with a `> parent:` context line.
- [x] **Just-finished item**: feature PRs only mark `[x]`; they never edit
  `TODO-archive.md` themselves.
