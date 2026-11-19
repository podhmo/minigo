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
