# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

- **Should a parent's explicit `Bind` of a stdlib path propagate to `NewSession`?** A session builds its own lazy tables, and paths the session still holds pending skip the parent's copy — so `e.Bind("os", custom)` followed by `e.NewSession()` gives the session the default `os` table, not the custom one. This predates lazy install: under eager install the session bound stdlib itself at construction, so the parent's re-Bind never reached it either. But the `NewSession` doc says inherited host bindings "resolve identically", which arguably covers a custom re-Bind of a stdlib path. Fixing it needs the registry to track explicit-vs-default binds so the session prefers the parent's custom table. (Escalated from Devin Review on [#1150](https://github.com/podhmo/minigo/pull/1150).)

## M4-4: do `runtime.HostNamedIs` / `runtime.HostTwinOf` count as removable twin helpers? (resolved)

Resolved 2026-11-19 (user): both stay as the residual reflect.Type↔typedef
fallback — a two-stage capability injection to remove them was judged
too complex to be worth it. Their removal is tracked as a separate
task in docs/next/ja/TODO.md ("intern 表を拡張して HostNamedIs/HostTwinOf
を除去する"), to revisit once the intern table covers un-interned and
stamp-less typedefs.
