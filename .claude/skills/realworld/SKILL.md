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
`run.sh` without arguments runs every task and fetches any target missing
from `SRC_DIR`, re-fetching a checkout at the wrong commit. When reusing a
shared or pre-fetched `SRC_DIR` you were asked not to modify, name the
tasks (`./run.sh grafana-openapi`) so only their targets are touched.

## `/realworld run`

1. Run the harness; record the table.
2. For each non-PASS task, triage per the next section.
3. Compare times with the previous round's report; a PASS that got slower
   is a finding too.
4. For a slow PASS, `PROFILE=1 ./run.sh <task>` writes cpu/allocs
   profiles and `out/<task>.prof.txt`. On macOS check `TRACE=1` before
   trusting a CPU profile dominated by `pthread_cond_*` (see the harness
   README). Record tuning targets in TODO.md with the task name, then
   follow "Tuning a slow task" below.

## Tuning a slow task

1. **Measure honestly.** Interleave the sides (A B A B A B, ≥3 runs each)
   with nothing else running. A background `go vet` once inflated a 5 s run
   to 25 s. Absolute times drift between machines and days, so compare
   sides measured in the same sitting, never against an old report's
   number.
2. **Locate.** Profile both sides and diff them:
   `go tool pprof -top -diff_base old.cpu.pprof new-binary new.cpu.pprof`
   (also `-sample_index=alloc_space` on the allocs profiles). If the
   growth sits in GC/scheduler frames (`gcBgMarkWorker`, `madvise`,
   `kevent`, `pthread_*`) rather than `VM.loop`, the cause is allocation:
   read the alloc diff, not the CPU top.
3. **A regression across commits** (for example, after rebasing onto a
   newer main): measure the endpoints, then bisect with your own loop over
   `git rev-list --first-parent`. Use one scratch worktree, checking out
   each commit and applying the needed patches as a diff. **Never run the
   harness under `git bisect run`.** It exports `GIT_DIR`, and any `git
   init`/`fetch` underneath then rewrites the caller's repository (this
   once set `core.bare=true` and wrote a stray `.git/shallow`). `run.sh`
   now unsets `GIT_*`, but other scripts don't. Steps can be non-monotonic
   when several commits each add a little; when the bisect boundary looks
   noisy, profile the endpoints instead.
4. **Fix** through a normal PR. Its body carries an interleaved
   before/after table (wall time and allocated bytes), difffuzz counts for
   the same seed before and after (`lang`, `text`, and `reflect` when
   types or interfaces are involved), and `make format/lint/test`. Leave
   the residual gap as TODO.md children under the hot-path entry.
5. **Clean up.** Remove scratch worktrees (`git worktree remove`). Never
   clear the global Go cache: `COLD=1` and any cold measurement use a
   throwaway `GOCACHE`.

PRs that depend on each other are stacked with `gh stack` (see the
gh-stack skill), not just base-chained. After the base branch moves, replay
each layer's own commits onto it (no merge commits) and re-measure the
whole stack.

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
