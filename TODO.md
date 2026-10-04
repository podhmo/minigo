# TODO

> **Note on updating this file:**
> -   This file is an index only — task entries live in the topic files under `docs/todo/`. Add new tasks to the `## Open` section of the matching `docs/todo/<topic>.md` as `- [ ]` (or `- [-]` when partially done). If no topic fits, create a new topic file and add a row to the table below.
> -   Keep open entries compact: a bold title, 1–3 sentences of what remains, and links to `docs/sketch/` reports or issues/PRs for detail. Do not write investigation narratives here — they belong in `docs/sketch/`.
> -   When a task is done, move its entry to the same file's `## Done` section and mark it `[x]`. Done entries may keep their full text as history; do not grow them after the move.
> -   Update the topic's row below whenever its open count changes or a topic file is added/removed.

This file is the index of the project's task tracker; task entries live in
the topic files under `docs/todo/`.

It was seeded from `podhmo/go-scan`'s TODO.md (the `minigo2` section) at
migration; history above that point lives in the source repository.

## Topics

| Topic | Open | Status |
|-------|-----:|--------|
| [vm-language](docs/todo/vm-language.md) | 0 | VM core & language semantics — no open work |
| [stdlib-boundary](docs/todo/stdlib-boundary.md) | 1 | intrinsics bound per-package; coverage gaps remain |
| [reflect-facade](docs/todo/reflect-facade.md) | 1 | leftover decisions (net/url default, embed ambiguity) |
| [inspect](docs/todo/inspect.md) | 1 | function-body traversal unimplemented |
| [convert-define](docs/todo/convert-define.md) | 1 | generic-instantiation element conversion open |
| [difffuzz](docs/todo/difffuzz.md) | 2 | corpus sweep continuing; boundary-class items remain |
| [usecase-fuzz](docs/todo/usecase-fuzz.md) | 1 | corpus clean; deferred residuals |
| [tooling](docs/todo/tooling.md) | 0 | REPL/CLI/examples — no open work |
| [misc](docs/todo/misc.md) | 0 | cross-cutting rounds — no open work |
