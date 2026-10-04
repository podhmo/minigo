---
name: casefuzz
description: Hand-written scenario programs for minigo, diffed against the go toolchain with difffuzz's verdict contract and pinned in testdata/difffuzz. Use to add a realistic use-case program (`/casefuzz new [slug]`) or to classify and pin a scenario that diverges. Generated probes live in tools/difffuzz; this skill is for the programs a generator cannot write.
---

# casefuzz: hand-written scenarios → pin → fix → PR

`tools/difffuzz gen` probes single expressions. It cannot write the
programs minigo actually runs: a JSON-lines pipeline, a log parser, a
`{{var}}` template, a reflect-driven config mapper. Those are written by
hand, in the style of the `minigo-usecasefuzz` corpus — one realistic
`func main()` per scenario, exercising a use case end to end.

Same contract as difffuzz (see `.claude/skills/difffuzz/SKILL.md` and
`tools/difffuzz/README.md`):

- PASS — same output as `go run`.
- TRAP — `runtime trap:` after a matching prefix. Not a bug — backlog.
- SILENT — different output, no trap. **The bug.** Same symptoms as
  difffuzz: `value`, `type`, `missing-panic`, `spurious-panic`,
  `panic-message`, `unrecovered-panic`, `panic-became-trap`.
- CRASH / HANG — the interpreter itself failed. Bugs.

Cases live in `testdata/difffuzz/<slug>/` alongside generated pins —
`main.go` + `want.stdout` (+ `PENDING` while divergent). The root
`TestDiffRegressions` runs every case and fails when a PENDING case
starts passing, so a fix is never left unpinned.

## `/casefuzz new [slug]`

1. Write the scenario as a standalone dir `testdata/difffuzz/<slug>/`
   with a single `main.go` (`package main`, `func main()`). Name the
   slug after the feature area it exercises (`text_*`, `reflect_*`,
   `json*`, ...). Keep it realistic — the point is coverage a probe
   cannot express: multi-step pipelines, real stdlib call shapes,
   error paths a user would hit.
2. Output rules — the diff is byte-exact, so the program must be
   deterministic under both engines:
   - print results, don't `panic` on success (self-checking
     `panic("FAIL")` is fine — a Go-style self-check failure reads as
     a SILENT `panic` line),
   - no map iteration order (sort keys first), no random/timing/
     goroutine-order-dependent output, no pointer addresses (`%p`,
     `%#v` on pointer values — the harness mask does not run here),
   - no reading files outside the case dir, no network, no `os/exec`
     on the go toolchain.
3. Capture the oracle: `go run ./testdata/difffuzz/<slug> > want.stdout`
   from the repo root (the same path `TestDiffRegressions` passes to
   the engine). Verify the program compiles with plain `go build`
   first.
4. Classify: `go run ./cmd/minigo run ./testdata/difffuzz/<slug>` and
   diff against `want.stdout`. PASS → commit as-is (no PENDING).
   SILENT/CRASH → `touch PENDING`, file a TODO.md entry under the
   difffuzz section with the root-cause hypothesis, then fix per
   `/difffuzz fix`. TRAP → decide: boundary (unsafe, GC, cgo — usually
   not worth pinning) or implementation backlog (pin with PENDING and
   a TODO note saying which unbound surface it needs).
5. `go test -run TestDiffRegressions .` — PENDING cases skip, pinned
   PASS cases become required tests.

## Notes

- Prefer one scenario per use case; a second program that exercises the
  same surface with the same verdict adds noise, not coverage. Fold
  extra sub-scenarios into one `main.go` printing several lines.
- A scenario that only fails through `unsafe.Pointer`/GC fidelity is
  not pinnable — those divergences are boundary-class (tracked in
  TODO.md / issue #40), not bugs with seeds.
- When a fixed scenario starts passing, `TestDiffRegressions` tells you
  to delete `PENDING` — do it in the same PR as the fix.
