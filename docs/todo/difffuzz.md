# TODO — difffuzz & `$GOROOT/test` corpus

The `tools/difffuzz` harness (generated probes, shrinking, verdicts) and the `$GOROOT/test` corpus sweep.

## Open

- [-] **difffuzz harness** (`tools/difffuzz`: generated probes + batched shrinking + `$GOROOT/test` corpus, verdicts per minigo's trap contract; [docs/sketch/ja/difffuzz-harness.md](../sketch/ja/difffuzz-harness.md); fix loop: `.claude/skills/difffuzz`). Landed: text/num domains, metamorphic contexts, symptom-preserving shrink, `-mask`, `-emit` → `testdata/difffuzz` xfail regressions (`TestDiffRegressions`). Per-domain findings are filed under their owning topics — language/fmt in [vm-language.md](vm-language.md), reflect in [reflect-facade.md](reflect-facade.md), stdlib boundary in [stdlib-boundary.md](stdlib-boundary.md).

- [ ] **`$GOROOT/test` corpus sweep**: every corpus file picked so far PASSes byte-identical — pick the next batch of SILENT/CRASH/HANG programs on the next hunt. Remaining items are all boundary-class: the GC-finalizer family (`SetFinalizer`/`MemStats` bound but no real GC), `peano.go` VM frame limit, `unsafe.Pointer`/`unsafe.String`/`unsafe.Offsetof`/`runtime.FuncForPC` traps (pointer model + host PC), `linkmain_run.go` nondeterministic tmpdir, the proc-kill observability gap (a goroutine panic poisoning the proc surfaces only as `all goroutines are asleep` when the root call is parked in a host `WaitGroup.Wait`), and the throughput HANGs (copy/divmod/init1/heapsampling/winbatch — all exit 0 with longer timeouts). Per-file fix narratives: git history + [docs/sketch/ja/difffuzz-harness.md](../sketch/ja/difffuzz-harness.md).

## Done

- [x] Harness: a usecasefuzz-style skill for hand-written scenario programs, sharing difffuzz's verdict contract and `testdata/difffuzz` regression format — `.claude/skills/casefuzz` covers realistic `func main()` scenarios a generator cannot write (same PASS/TRAP/SILENT/CRASH/HANG contract, `{main.go,want.stdout[,PENDING]}` pinned dirs, `TestDiffRegressions` enforcement); first pinned scenario: `text_pass_jsonlines` (JSONL decode → filter → sort → re-emit).
