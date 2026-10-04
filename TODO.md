# TODO

> **Note on updating this file:**
> -   Do not move individual tasks to the "Implemented" section.
> -   A whole feature section should only be moved to "Implemented" when all of its sub-tasks are complete.
> -   For partially completed features, use checkboxes (`[x]` for complete, `[-]` for partially complete). A feature is considered partially complete if it has been implemented but has associated tests that are currently disabled.
> -   For partially completed features, use checkboxes (`[x]`) to mark completed sub-tasks.

This file tracks implemented features and immediate, concrete tasks.
It was seeded from `podhmo/go-scan`'s TODO.md (the `minigo2` section) at
migration; history above that point lives in the source repository.

## To Be Implemented

### `minigo`: Stack-VM Interpreter (redesign of go-scan's tree-walking `minigo`) ([docs/sketch/plan-minigo-vm.md](./docs/sketch/plan-minigo-vm.md))

Skeleton landed: lazy per-package loading, per-function compile, struct/method/closure/multi-return. Unsupported constructs compile to `OpTrap` instead of failing.

- [x] **`defer` / `recover` semantics**: defers run LIFO through VM frame teardown on both normal return and panic unwind; `recover()` is visible only inside directly called deferred functions and can mutate named return values. Script `*Panic` is recoverable; `*Trap` is not.
- [x] **Real concurrency semantics**: real host goroutines sharing a per-run process; channels support blocking send/recv, buffered capacity, and close notification; `select` chooses randomly among ready cases and handles blocking/defaults; process terminates cleanly on root return or unrecovered goroutine panic; `sync` types (`WaitGroup`, `Mutex`, `RWMutex`, `Once`) bound as host types.
- [x] **Concurrency fuzz fixes**: func-typed host params (e.g. `sync.Once.Do`) adapt via `reflect.MakeFunc` on the caller's VM; empty-interface channel sends preserve pointer identity and pass slices/maps; `time.Duration` arithmetic normalization; empty `default:` in select no longer deadlocks; proc-exit unwind drains remaining defers; bound-intrinsic callbacks run on the calling VM; `for ... range` loop variables are per-iteration (Go 1.22); concurrent `Engine.Call`/`EvalExpr` run on isolated VMs.
- [x] **`sync` package support**: bindings for `sync.Map`, `Pool`, `Cond`, `Locker`, `NewCond`, `OnceFunc`, `OnceValue`, `OnceValues`; GoValue-boxed functions are callable (`p.New()`); keyed host composite literals (`&sync.Pool{New: f}`) supported via `initHostLiteral`; foreign-goroutine `VM.Call` routes through `Spawn` + `Task.Wait`; embedded host-typed fields promote methods and fields; type assertions `x.(*T)` match boxed native types.
- [x] **`sync` package semantics alignment**: `time.AfterFunc` runs callbacks via `vc.Spawn` (cancelled upon process termination); ambiguous promoted members across host/script trap at BFS depth with cycle detection for recursive embeds; defined types over host types (`type MyMutex sync.Mutex`) promote fields only, not methods; nil embedded pointer receivers follow Go semantics (pointer-receiver methods callable on nil, value-receiver/field access panics on dereference).
- [x] **Promoted-method resolution**: replaced DFS with BFS `promotedMember` resolution so shallower methods beat deeper ones, same-depth collisions trap as ambiguous, and recursive embeds terminate safely without stack overflow.
- [x] **Language-surface semantics alignment**: elided-key struct literals in maps compile against `MapType.Key`; package-var initialization ordered via dependency-driven DFS; structural type tags for map keys (anonymous structs/arrays match structurally; unhashable slice keys panic); array slicing `a[:]` retains cap and sharing; exact-width masking for sized-int operations; slice-to-array conversions (`[N]T(s)`, `(*[N]T)(s)`); function-local `type` declarations participate in generic inference; `fmt.Errorf` `%w` builds unwrap chains; scalar verbs (`%q`, `%s`, `%x`) format byte/rune slices correctly; `print`/`println` write to stderr.
- [x] **Numeric and constant semantics**: untyped constants retained as `runtime.UConst` with lazy `go/constant` folding and overflow checks on concrete binding; char literals preserve rune typing; `complex64`/`complex128` support and builtins (`complex`, `real`, `imag`); three-index slicing `s[a:b:c]`; assertion failures report static interface types; float32 narrowing at VM boundaries.
- [x] **Use-case stdlib support**: `json.Unmarshal`/`Marshal` honoring struct tags, declaration order, and `omitempty`; `errors.As` and `errors.Join`; `strings.Builder`, `NewReplacer`, `TrimFunc`, `IndexFunc`, `Map`; sized-int host return values retain declared widths; uint64 literals >MaxInt64 boxed correctly; map formatting sorts keys; `os.DirEntry`/`FileInfo` interfaces for `filepath.WalkDir`.
- [-] **Use-case-fuzz leftovers**:
  - [x] `sync.Pool` / `sync.Map` bound; `io` loads from GOROOT source.
  - [x] Host-type composite literals with fields (`sync.Pool{New: fn}`) via `initHostLiteral`.
  - [x] `flag` package pointer-to-named conversion (`(*stringValue)(p)`).
  - [x] Unsigned shift (`<<`, `>>`) and arithmetic operations on unsigned types.
  - [x] Package bindings for `crypto/sha256`, `encoding/csv`, `bufio`, `text/template`, `io` (singleton EOF identity), and `time` layouts/types.
  - [x] `net/http` interpreted from GOROOT source: leaf syscall/unsafe binds, vendor directory resolution, `//go:linkname` directives resolved via engine cache, script I/O adaptation (`asReaderVM`), `context.CancelFunc` deferred execution.
  - [x] Native reflect facade (`minireflect`) for `gopkg.in/yaml.v3`, `BurntSushi/toml`, `text/template`, and `net/url` to bypass `unsafe.Pointer` in `internal/abi.TypeOf`.
  - [x] `fmt` unwraps facade `reflect.Value` instances prior to formatting.
  - [x] `reflect.TypeAssert[T]` bound (Go 1.25 generic builtin).
  - [ ] **reflect-facade leftovers**: decide whether `net/url` defaults to source interpretation; same-depth embedded-field ambiguity resolution (currently first-wins); shared stub layer for `internal/*` packages if required.
  - [x] **reflect-facade review fixes**:
    - Addressability: `CanAddr`/`CanSet` gated by parent reference; slice elements addressable; `FieldRef.Base` tracking across replacements; unaddressable array slicing checks.
    - Type identity: Canonical `anon:` keys for synthesized composite types (`ArrayOf`, `MapOf`, `SliceOf`, `ChanOf`, `PtrTo`); alias identity folding (`byte`→`uint8`); local/generic type disambiguation.
    - Validation: `MakeSlice`/`ArrayOf` negative and capacity bounds; `IsZero` validation; `SetInt`/`SetUint` width truncation; kind checks on scalar accessors/setters; `AssignableTo` verification on `Set`/`SetMapIndex`.
    - Copy semantics: Struct values copied element-wise on `Copy`, `Append`, `SetMapIndex`, and channel `Send`/`Recv`.
    - Call semantics: Void functions return empty slices; `CallSlice` variadic tail expansion.
    - Relations & conversions: Deep comparability for structs/arrays; named-to-unnamed assignability; conversion legality gates.
    - Member model: Method sets filter by receiver indirection and export status; method metadata includes signatures; `FieldByName` traverses embedded pointers; shallowest BFS resolution for promoted fields.
    - Minireflect edges: `Indirect` dereferences once; `Bytes` returns shared slice view; `MapIter` skips deleted keys.
- [-] **difffuzz harness**:
  - [x] Formatting alignments: nil slice/map formatting (`<nil>` vs `[]`/`map[]`, `%#v`, `%T`, `%p`); multi-value tuple argument expansion in `fmt.Sprint`.
  - [x] Panic semantics: script callbacks to host re-raise script panics to preserve `recover()`; bounds error messages match Go's `runtime.boundsError`.
  - [x] Collections & typing: `slices.Clone`/`maps.Clone` typed-nil and typedef preservation; `delete(m, k)` removes keys from iteration order; `string(nilBytes)` produces empty string; typed variable declarations from untyped constants preserve target type.
  - [ ] **`$GOROOT/test` corpus sweep**: tracked boundary gaps remain — `unsafe.Pointer` value model (#40), GC fidelity (`SetFinalizer`/`ReadMemStats` approximations), `unsafe.String`/`unsafe.Offsetof`/`runtime.FuncForPC`, throughput limits.
  - [x] **reflect-domain fixes**:
    - Verbatim error messages for `reflect.Value`/`Type` panics.
    - Setter validation: addressability checked before kind gates.
    - `Value.Slice` string support and capacity-based bounds checks.
    - Bound `Value.SetCap` and `Value.Grow`.
    - Builtin `error` typedef equipped with AST interface methods for `Implements` checking.
    - Interface value formatting: `Error()` prioritized over `String()`.
    - AMD64 alignment and struct field offset calculations.
    - Read-only (`flagRO`) propagation across derived views.
  - [x] `io.Writer` script adaptation for `fmt.Fprintf` family (`asReaderVM`/`asWriterVM`).
  - [x] Refactor sweep: `runtime.Map` Insert/Delete/Clear API; fscope binding ladder; `runtime.TypIdentical` type identity; standard panic constructors.
- [x] **Windows verification round**: path normalization (CRLF, `filepath.ToSlash`), exec-dir matching via `os.SameFile`, sized-int conversion preservation, constant folding via `go/constant`.
- [x] **Interfaces**: duck-typed satisfaction over runtime method sets, dynamic method dispatch, type assertions/switches (`x.(T)`, `switch v := x.(type)`).
- [x] **Generics**: monomorphize-on-use (`OpInstantiate`), call-site type-argument inference, type constraint enforcement (`~T`, unions, `comparable`).
- [x] **Special forms (`SPECIAL_CALL`)**: compile-time quote evaluation, lazily resolved canonical symbol identity via `SpecialContext` without materializing imports.
- [x] **Full init-order analysis**: transitive dependency graph traversal through function bodies for package variables.
- [x] **Lvalues & references**: unified `FieldRef`/`IndexRef` cell views supporting `&s.f`, `x[i]++`, `s.f++`, `(*p)++`, and compound assignments.
- [x] **Control flow features**: spread calls (`f(args...)`), switch `fallthrough`, labeled loops/switches with `break`/`continue`/`goto`, and `goto` block-scoping validation.
- [x] **Execution modes & policy**: `LazyInit` mode for querying members without running var/const initializers; `AllowedRoots` path confinement; virtual cwd (`WithWorkingDir`/`os.Chdir`); host policy filtering for bindings.
- [x] **Go 1.26/1.27 language deltas**: `new(expr)` initializers, self-referential type constraints, generic methods, generic function-type inference, promoted-field composite literal keys.
- [-] **Intrinsic coverage + element types**: intrinsics bound for fmt, errors, strings, strconv, sort, slices, maps, os, time, bytes, math, regexp, encoding/json, etc.; host structs box as `*runtime.GoValue` with reflective method dispatch.
  - [ ] Gaps: a member missing from a bound package traps `undefined` (bind map completely shadows source; no fallback to source); ordering helpers only sort int64/float64/string natively.
- [x] **`[]byte` conversions & typed zeros**: conversions between strings, byte slices, and rune slices; named type tagging (`Named`); zero-value coercion via `OpCoerce`/`OpCoerceGlobal`; typed nils in interface slots.
- [x] **Package objects / symbol introspection (`minigo.dev/inspect`)**: 3-layer API (index, syntax, value); `Implementers` index-level subtype lookup; `MethodSet` with BFS promotion and receiver filtering; `TypeFields` for composite type elements; `EnumMembers`, `IsAlias`, `DeclType`, structured `Position`, and askable `SymbolID`.
- [x] **inspect: `SourceOf`**: locator bypassing bound-stdlib shadows to parse and inspect real GOROOT source declarations.
- [x] **REPL & CLI tools**: persistent REPL session with `:cd`/`:ls`/`:pin` monkey-patching; multi-line input handling; directory imports; `minigo vet` static intrinsic checker; `gen-intrinsics` code generator.
- [x] **Range-over-func iterators**: Go 1.23 `for x := range f` over `iter.Seq`/`iter.Seq2` producer functions via `OpRangeNext` and bounded frames.
- [x] **Tracebacks & error reporting**: structured stack traces with source line formatting, Go host stack preservation in `*runtime.Panic`, generic instantiation names, callee compile-error positions, and arity checking.
- [x] **`[]byte` boundary marshal**: fast-path byte slice marshaling without per-byte reflect boxing; `io.ReadFull` write-back via `borrowBytes`.
- [x] **`convert-define` toolchain**: DSL interpretation via `OpSpecialCall`; inspection via `minigo.dev/inspect`; stmt/expr unification; generic API support; provenance tracking for DSL errors; validation for `-tags`, missing imports, and type mismatches.
- [ ] **`convert-define`: element-wise conversion through generic instantiations**: type-argument substitution for parametric specs (`type SrcList List[int]`).
- [ ] **Script-side traversal of function bodies**: expose function AST bodies in `minigo.dev/inspect` for linters, static analysis, and script-side codegen.
- [ ] **`go.mod` language version (`-lang`) is not enforced**: decide whether to gate newer-than-module language features or document as designed.
- [ ] **`map[string]*[3]int` interior writes still trap**: interior pointer dereference through map-element indexing (`pa["a"][1] = 9`) traps on `IndexRef` lvalue check.
- [ ] **`map[string]struct{F []int} m[k].F[i] = v` traps**: writing to reference-shaped field backing of a copy struct map element traps uniformly before checking field kind.
