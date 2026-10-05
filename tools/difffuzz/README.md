# difffuzz

Differential harness for minigo with the go toolchain as the oracle.
Report (ja): [docs/sketch/ja/difffuzz-harness.md](../../docs/sketch/ja/difffuzz-harness.md).
Fix-loop skill: [.claude/skills/difffuzz/SKILL.md](../../.claude/skills/difffuzz/SKILL.md).

```
make difffuzz                                   # text domain, random seed
make difffuzz DIFFFUZZ_ARGS="-domain num -seed 3 -mask '^\S+ '"
make difffuzz-corpus                            # every `// run` test in $GOROOT/test
go -C ./tools/difffuzz run ./ gen -h            # all flags
```

## Verdicts

minigo promises "valid Go in; Go's behavior out, or a loud trap". Verdicts
encode that contract, not "equal to go":

| verdict | meaning | bug? |
|---|---|---|
| PASS | same output as go | |
| TRAP | `runtime trap:` after a matching output prefix | no (backlog) |
| SILENT | different output without a trap | **yes** |
| CRASH | the interpreter itself panicked | **yes** |
| HANG | timeout where go finished | **yes** |
| SKIP | gc rejected the program | generator noise |

SILENT carries a symptom: `value`, `type` (only `%T` differs),
`missing-panic`, `spurious-panic`, `panic-message`, `unrecovered-panic`
(a script panic escaped `recover`), `panic-became-trap` (a recoverable Go
panic surfaced as an unrecoverable trap, e.g. inside a `strings.Map`
callback).

## `gen`: generated probes

Each program holds ~200 independent *probes*. A probe is one typed
expression, evaluated inside its own closure with a `recover`, printed as
`<id>: %T %v` (or `<id>: panic: …`). Consequences:

- go runs once per program; probes are compared line by line.
- when minigo stops early (trap/crash/hang), the probe it stopped at is
  charged and minigo resumes from the next probe — a trap costs one
  re-run, not the batch.
- reduction is batched: all single-step shrink candidates of a probe go
  into one program sorted by size, so the first diverging line *is* the
  smallest candidate. Shrinking keeps the verdict **and** symptom, so a
  dominant bug (e.g. wrong `%T`) cannot absorb a wrong-value finding.

Domains:

- `text` (default) — minigo's main use: strings/strconv/fmt verbs, unicode,
  utf8, `[]string`/`[]byte`/`[]rune`/`map[string]int`, regexp, sort/slices/
  maps, plus statement shapes (range over string, word count, Builder,
  switch) as IIFE templates. Table-driven: add a row to `textSigs`.
- `num` — sized/named ints, floats, shifts, conversions, compound
  assignment and `++/--`.
- `lang` — the language core beyond string helpers: user structs with
  value/pointer methods, method values/expressions, embedding/promotion,
  interfaces (typed nils, assertions, type switches, fmt's Stringer/error
  dispatch), control flow (labels, goto, fallthrough, defer/recover,
  per-iteration loop vars, range-over-int/func, channels, goroutines) and
  user-defined generics. Shares the text domain's signature machinery;
  rows live in `langSigs`, the prelude types/generics in `langDecls`
  (`tools/difffuzz/lang.go`).

Metamorphic contexts re-evaluate the same expression through a generic
`id[T]`, a struct field, a closure call and a deferred assignment.

Validity: generator rules avoid most compile errors, and gc is the final
filter (`-gcflags=-e`; rejected probes become SKIP and the program is
rebuilt). `TestGeneratedProgramsCompile` asserts the rules alone suffice
for generated probes and every shrink candidate.

Triage aids:

- grouping by fingerprint (verdict, symptom, types, context, root op)
  before shrinking; `-per-bucket` members are shrunk per group;
- dedup by reduced shape (sized ints collapsed to signedness classes);
- folding: a finding whose reduced expression contains another finding's
  reduced expression is listed as `derived` under it;
- `-mask REGEXP` (repeatable) hides known divergences before comparing,
  e.g. `-mask '^\S+ '` compares values only;
- traps are bucketed by signature and one example per signature is shrunk.

`-emit DIR` writes each reduced bug as `DIR/<slug>/{main.go,want.stdout,PENDING}`.
Copied into `testdata/difffuzz/`, the root `TestDiffRegressions` runs it:
a `PENDING` case must still diverge (skipped); once minigo matches go the
test fails until `PENDING` is deleted, so fixes get pinned automatically.

## `corpus`: whole programs

`corpus [-goroot-tests] [dir-or-file…]` runs single-file programs under
both and reports verdicts plus TRAP/CRASH buckets ranked by how many
programs they block. `$GOROOT/test` `// run` files are mostly
self-checking (they `panic` on a wrong result), so a SILENT with a minigo
`panic:` usually means a failed self-check.
