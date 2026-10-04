# TODO — usecase-fuzz corpus

Residuals from the minigo-usecasefuzz realistic-program corpus.

## Open

- [-] **Use-case-fuzz leftovers** (deferred; [docs/sketch/ja/fuzz-usecase.md](../sketch/ja/fuzz-usecase.md) §2-3): latest corpus state — 0 DIFF / 0 TRAP / 1 ACCEPT / 1 REJECT in minigo-usecasefuzz; lim-http and lim-xml PASS end-to-end, lim-cgo stays REJECT (cgo unsupported — expected), `inspectuse` stays ACCEPT. Re-run the corpus after each stdlib-boundary round. Completed children moved to [stdlib-boundary.md](stdlib-boundary.md) and [reflect-facade.md](reflect-facade.md); the still-open `reflect-facade leftovers` child is in [reflect-facade.md](reflect-facade.md)'s Open section.

## Done

- [x] **Use-case fuzz round** (28 realistic programs diffed against `go run` as oracle — text/scripting bias; [docs/sketch/ja/fuzz-usecase.md](../sketch/ja/fuzz-usecase.md)): fixed — `json.Unmarshal(data, &v)` two-arg form with tag-driven `jsonShape` decode (+ `VMCaller.ElemZero`); `json:` tags honored on marshal AND unmarshal (`StructFieldTags` populated at all four typedef sites); struct marshal emits fields in declaration order (`orderedObject`) with `omitempty`; `errors.As` works on script error types (`hostErrOf`/`scriptError` chain walk, pointer-valued targets via inner Cell, interface targets match any error); `errors.Join` skips nil; `strings.Builder`/`NewReplacer`/`TrimFunc`/`IndexFunc`/`Map` bound (runePred callback adapter); `fmt.Fprint*` bound with `asWriter` unwrapping `&b`; `time.ParseDuration` + duration methods/arithmetic; uint64 literals >MaxInt64 box as `GoValue{uint64}` with wrap-around arithmetic and `uint64(x)`/`%x` unsigned rendering; nil `[]string`/`[]byte` host results become `TypedNil` (`m == nil` works); unnamed host slices/arrays unbox element-wise (regexp `[]int` results indexable); `os.Args` is a variable (flag's init reads it); function-local `type` decls resolve element types at runtime (`localElemTypedef` fallback at all `ElemOf` sites); named-const indices in `[]T{Const: v}` literals; map output sorts keys (fmtsort); `os.DirEntry`/`FileInfo` marker interfaces for `filepath.WalkDir` callbacks.
