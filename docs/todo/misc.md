# TODO — Misc

Cross-cutting rounds that do not belong to a single subsystem.

## Open

Nothing open.

## Done

- [x] **`TestHostParkLeak` was timing-flaky on CI** (`concurrency_test.go`): fixed — the assertion was a net `runtime.NumGoroutine` delta, so any unrelated goroutine death inside the poll window masked the leaked +1 permanently (reproduced locally by parking a transient goroutine that dies mid-run); the rewritten test drops counting and has the spawned goroutine prove its own survival — a test-bound `parkprobe` builtin signals that the goroutine reached the park point, after which the script parks on a real `sync.WaitGroup` as before (`testdata/hostpark`).

- [x] **Windows verification round** (`make test` on Windows Server 2022 + all four fuzz harnesses re-run; [docs/sketch/ja/fuzz-windows.md](../sketch/ja/fuzz-windows.md)): `make test` green; usecasefuzz/langfuzz/concfuzz/convfuzz all reproduce Linux verdicts. Windows fixes — `find`/CRLF/`filepath.ToSlash` for display paths, `os.SameFile` for exec-dir matching, `cmd /c` for shell builtins in testdata, `.gitattributes` (`eol=lf`). Interpreter bugs found along the way (platform-independent): conversion-produced sized ints lost their type tag (`-uint8(5)` → -5) now `Named`-wrapped, constant expressions fold via `go/constant` (`1<<100>>50` = 2^50; unrepresentable consts and `x/0` trap at compile), `%T` prints `int32`/`uint8` for `rune`/`byte` aliases.

- [x] **Conformance harness**: `conformance_test.go` runs the shared-subset corpus under both engines and diffs normalized results (known v1 divergences documented).
