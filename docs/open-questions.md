# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

## M4-4: do `runtime.HostNamedIs` / `runtime.HostTwinOf` count as removable twin helpers? (resolved)

Resolved 2026-11-19 (user): both stay as the residual reflect.Type↔typedef
fallback — a two-stage capability injection to remove them was judged
too complex to be worth it. Their removal is tracked as a separate
task in docs/next/ja/TODO.md ("intern 表を拡張して HostNamedIs/HostTwinOf
を除去する"), to revisit once the intern table covers un-interned and
stamp-less typedefs.

## src-netip: can a bound-mode-only stdlib package ever run under `--src`?

`ModeSource` loading of `net/netip` traps at package init:
`unique.Make(addrDetail{})` (netip.go:69) calls `unique.Make[T]` — a
generic builtin whose `T` is the unexported `addrDetail` — which lowers
to `abi.TypeFor` (a compiler intrinsic) plus `unsafe.Pointer(&a)`
reinterpretation. None of that is bindable or source-interpretable:
the intrinsic has no body to run, and the unsafe aliasing defeats the
interpreter's value model.

Options:

- Give the interpreter its own intrinsics for `unique.Make`/`abi.TypeFor`
  (a per-package escape hatch, like asmimpl but for generics).
- Declare `net/netip` bound-only by policy and document that `--src`
  for it is unsupported (the bound path already works).
- A general `unsafe.Pointer` story for source mode — much larger scope;
  pointer-reinterpret casts have no interpreter model.

Decision: needs podhmo. `--src net/netip` reproduces at
`unique.Make(addrDetail{})` — bound `netip` stays the parity path.
