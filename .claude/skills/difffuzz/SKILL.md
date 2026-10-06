---
name: difffuzz
description: Differential testing loop for minigo against the go toolchain. Use to hunt for new divergences (`/difffuzz hunt`), to fix a known one end-to-end and open a PR (`/difffuzz fix [slug]`), or to extend the generator (`/difffuzz extend <area>`). Harness lives in tools/difffuzz; regression cases in testdata/difffuzz.
---

# difffuzz: hunt → pin → fix → PR

minigo's contract is "valid Go in, Go's behavior out — or a loud `runtime
trap`". The harness (`tools/difffuzz`, read its README.md first) checks
exactly that against the go toolchain:

- PASS — same output as go
- TRAP — minigo stopped with `runtime trap:` after a matching prefix.
  Acceptable by design (unimplemented surface). Not a bug, but a backlog item.
- SILENT — different output with no trap (`value`, `type`, `missing-panic`,
  `spurious-panic`, `panic-message`, `unrecovered-panic`,
  `panic-became-trap`). **These are the bugs.**
- CRASH / HANG — the interpreter itself panicked / timed out. Bugs.

Priorities (the project's main use is text processing / LL-style scripting):
text-domain `value` and `panic-became-trap` > `missing-panic`/`spurious-panic`
> `panic-message` > `type` (e.g. `%T` printing `rune` for `int32`) > num-domain.
Numeric/binary work is lower priority unless it changes values in ordinary
scripts (e.g. `var f float64 = 3; f/2`).

Follow AGENTS.md throughout (make format / make lint / make test, English
code and commits, Japanese replies to the user, TODO.md upkeep).

## `/difffuzz hunt [domain] [seed]`

1. Run the generator (default domain `text`; also `num`, `reflect`, `lang`); use a fresh seed and record it:
   ```
   go -C ./tools/difffuzz run ./ gen -domain text -seed <N> -batches 16 -per-bucket 1 \
     -emit /tmp/difffuzz-emit -out /tmp/difffuzz-<N>.md
   ```
   Add `-mask '^\S+ '` to compare values only (hides all `%T` divergences);
   add more `-mask` regexps to hide bugs already pinned in testdata/difffuzz,
   so that a dominant known bug stops absorbing findings.
2. Optionally run the whole-program corpus (`make difffuzz-corpus`) — GOROOT's
   `// run` tests. Most TRAPs there are out of scope (unsafe, GC, runtime
   internals); look at SILENT/CRASH/HANG only. `-goroot-tests` covers only
   `$GOROOT/test/*.go`: the subdirectories (`typeparam`, `fixedbugs`,
   `interface`, `chan`, `syntax`, `ken`, `abi`, `stress`, `simd`, `dwarf`)
   are a separate, still-fertile sweep — pass each as a positional dir to
   `corpus` (procedure, gotchas and pilot yields in the README's `corpus`
   section). `fixedbugs` is mostly `// errorcheck` noise (SKIP).
3. Triage the report. For each finding, decide: real bug, by-design trap,
   or generator mistake (a gc compile error is filtered automatically; a
   probe whose go output is implementation-defined is a generator bug —
   fix the generator, not minigo).
4. Curate: copy one case per root cause from `/tmp/difffuzz-emit` into
   `testdata/difffuzz/` (keep the emitted `PENDING`). Skip duplicates of
   cases already there. `go test -run TestDiffRegressions .` must pass
   (pending cases skip).
5. Add a TODO.md entry per root cause under the difffuzz section and report
   to the user: seed, counts per verdict, new root causes with one-line
   repros, and which traps dominate (by count) as implementation backlog.

## `/difffuzz fix [slug]`

1. Pick a case: the given slug, or the highest-priority `PENDING` under
   `testdata/difffuzz/` (see priorities above). Read its `PENDING` and the
   single `try(0, ...)` line at the bottom of its `main.go`.
2. Reproduce both sides:
   ```
   go run ./testdata/difffuzz/<slug>            # oracle
   go run ./cmd/minigo run ./testdata/difffuzz/<slug>
   ```
   Then reduce further by hand in a scratch dir if useful — but the probe is
   already greedily minimal, so the bug usually sits in the outermost call.
3. Find the root cause in minigo (vm/, runtime/, compile/, intrinsics.go,
   dispatch.go …). Fix the cause, not the symptom: if the bug is "nil slice
   prints `<nil>`", fix the formatting path for every verb, not only `%v`.
4. `go test -run TestDiffRegressions .` now fails with "now matches go:
   delete …/PENDING". Delete the PENDING file. Check whether other PENDING
   cases were fixed by the same change (the test tells you) and unpin them
   too.
5. Guard against regressions with the harness, same seed before/after:
   ```
   make difffuzz DIFFFUZZ_ARGS="-domain text -seed <N> -batches 16 -per-bucket 1"
   ```
   SILENT must not grow, and no new root cause may appear.
6. `make format && make lint && make test`. Update TODO.md (check the item).
7. Branch, commit (English message, e.g. `fmt: print nil slices as [] (difffuzz text_value_05d448e7)`),
   push, and open a PR with `gh pr create`. The PR body lists: the case
   slug(s), root cause, fix, and before/after harness counts for the seed.
   One root cause per PR keeps review small; batch only trivially-related
   cases. A fix that needs another unmerged fix is stacked on it with
   `gh stack` (gh-stack skill); an unrelated fix branches from main.

## `/difffuzz extend <area>`

Grow the generator where the project's real usage is:

- Text domain: add rows to `textSigs` in `tools/difffuzz/text.go`
  (`sig(name, ret, template, args...)`, `$1..$n` placeholders). Statement
  shapes (loops, switch, maps, closures) go in as IIFE templates. Prefer
  surface that scripts actually use (strings/strconv/fmt verbs, maps,
  sort/slices, regexp, encoding/json, path/filepath, time formatting).
- Lang domain (`-domain lang`): add rows to `langSigs` in
  `tools/difffuzz/lang.go`; prelude types, methods and generic helpers go in
  `langDecls` (fmt-only — it is emitted into every program). Templates must
  never mutate package-level values (minigo re-runs from the next probe
  with fresh globals after a trap) — copy into a local first.
- New types: add a `*Typ` with edge-case `Values` (include nil/empty and
  non-ASCII) to `textTypes`.
- New metamorphic carriers: add a context to `Probe.Body` + `metaCtxs`.
- Then `go test ./tools/difffuzz/` — `TestGeneratedProgramsCompile` proves
  every generated probe and every shrink candidate builds under gc. Keep
  it green; templates must not depend on map iteration order or other
  unspecified behavior (sort keys first).
