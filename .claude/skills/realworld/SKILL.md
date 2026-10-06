---
name: realworld
description: Exploration loop that runs sync-tool scripts against pinned real-world Go codebases (grafana, plugins, …) to find where minigo falls short of its pitch — cheap, lazy analysis of legacy code. Use to re-run the harness after fixes (`/realworld run`), to add a task or target (`/realworld new <task>`), or to triage a failing task into pinned cases and TODO entries. Harness lives in podhmo/minigo-usecasefuzz `realworld/`.
---

# realworld: sync tasks on legacy code → minimize → pin → report

difffuzz, casefuzz and minigo-usecasefuzz ask "is minigo correct Go?".
This loop asks the pitch question: **does a tool that reads a real legacy
codebase and cross-checks it against another artifact run cheaper than a
go/packages tool, and keep running when the module graph is incomplete?**
Background: `docs/sketch/ja/exploration-legacy-sync.md` (sections 2–4).

Evaluation axes, in order: works (healthy env) → works (incomplete env:
missing modules, offline) → fast (total build+start+run; pain threshold is
~3 min with a cold cache) → understandable (traps/tracebacks point at the
cause). Runtime speed by itself is a non-goal.

## Harness

`podhmo/minigo-usecasefuzz/realworld` (see its README):

```sh
MINIGO_DIR=<this checkout> ./run.sh [task ...]   # COLD=1 adds cold-cache oracle timing
```

Targets are pinned commits in `targets.tsv`; downloads are large (grafana
≈ 5 GB of modules) — keep `SRC_DIR` outside both repos. Never run
`go clean -cache` to measure; cold timings use a throwaway `GOCACHE`.

## `/realworld run`

1. Run the harness; record the table.
2. For each non-PASS task, triage per the next section.
3. Compare times with the previous round's report; a PASS that got slower
   is a finding too.
4. For a slow PASS, `PROFILE=1 ./run.sh <task>` writes cpu/allocs
   profiles and `out/<task>.prof.txt`. On macOS check `TRACE=1` before
   trusting a CPU profile dominated by `pthread_cond_*` (see the harness
   README). Record tuning targets in TODO.md with the task name.

## Triage a failing task

1. Read `out/<task>.got`. Locate the frame in minigo's traceback; if the
   traceback lost the origin (recover-then-repanic), vendor the involved
   GOROOT/third-party package into a scratch module and bisect.
2. Minimize to a `func main()` program that `go run`s. Classify with the
   difffuzz contract (SILENT = bug, TRAP = backlog, CRASH/HANG = bug).
3. Pin it as `testdata/difffuzz/<slug>/` with `PENDING` (see the casefuzz
   skill for the layout) and add a TODO.md entry per the todo-md skill —
   name the realworld task that surfaced it.
4. To keep exploring past a blocker, patch a scratch worktree
   (`git worktree add --detach /tmp/...`) and point `MINIGO_DIR` at it.
   Never commit those patches from the exploration; fixes go through the
   difffuzz fix loop.
5. If the task fell back to `go/parser` because `inspect` cannot see
   something (func bodies, free comments, const initializers, …), record
   the gap — those are design findings, not bugs.

## `/realworld new <task>`

- Pick a sync a real project needs: Go code ↔ another artifact (OpenAPI,
  manifests, frontend types, generated code, docs).
- Prefer `inspect` (surface only); keep it `go run`-able when possible so
  the oracle is free. A minigo-only task gets a hand-checked `want.txt`.
- Add a target row with a pinned commit if needed. Application code beats
  libraries; a target whose bundled artifacts are all in sync is still
  useful as a regression, but look for external plugins/consumers where
  drift actually exists.

## Report

Each round gets a short Japanese report in `docs/sketch/ja/` (or an
appended section in the latest one): the table, new findings, what moved
since last time.
