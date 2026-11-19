# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

- **Should a parent's explicit `Bind` of a stdlib path propagate to `NewSession`?** A session builds its own lazy tables, and paths the session still holds pending skip the parent's copy — so `e.Bind("os", custom)` followed by `e.NewSession()` gives the session the default `os` table, not the custom one. This predates lazy install: under eager install the session bound stdlib itself at construction, so the parent's re-Bind never reached it either. But the `NewSession` doc says inherited host bindings "resolve identically", which arguably covers a custom re-Bind of a stdlib path. Fixing it needs the registry to track explicit-vs-default binds so the session prefers the parent's custom table. (Escalated from Devin Review on [#1150](https://github.com/podhmo/minigo/pull/1150).)

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
