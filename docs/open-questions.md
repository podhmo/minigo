# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

## M4-4: do `runtime.HostNamedIs` / `runtime.HostTwinOf` count as removable twin helpers?

The M4-4 item (docs/next/ja/TODO.md) lists `hostTwinIs`, `hostNilTwin`,
`sameSpelledTwin`, `HostNamedIs`/`HostTwinOf`, `sourceTwinOf` as helpers
to delete, one per PR. Four are gone (PRs #1170–#1172). The last pair
still exists in `runtime/value.go` and is load-bearing:

- `HostNamedIs(rt, td)` — `rt.PkgPath()+rt.Name()` compare against a
  source typedef. Used at bind (minigo.go), ffi_marshal, and the scalar
  unbox twin check (runtime/value.go).
- `HostTwinOf(rt, td)` — source twin OR `HostScalar` box; still the
  documented fallback for typedefs the intern table cannot decide
  (vm/assert.go:1238). `intrinsics_errors.go:320` documents a spelling
  case the name+path compare intentionally absorbs.

Question: is the item satisfied by keeping them as the residual
reflect.Type↔typedef fallback (mark M4-4 `[x]`), or should the intern
table be extended to cover un-interned/stamp-less typedefs so they can
also go? The residual is small but real; removal means either widening
intern coverage or accepting edge-case behavior changes.
