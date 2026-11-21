# Open questions

Design questions that came up during implementation and need a human decision.

- **Should a parent's explicit `Bind` of a stdlib path propagate to `NewSession`?** A session builds its own lazy tables, and paths the session still holds pending skip the parent's copy — so `e.Bind("os", custom)` followed by `e.NewSession()` gives the session the default `os` table, not the custom one. This predates lazy install: under eager install the session bound stdlib itself at construction, so the parent's re-Bind never reached it either. But the `NewSession` doc says inherited host bindings "resolve identically", which arguably covers a custom re-Bind of a stdlib path. Fixing it needs the registry to track explicit-vs-default binds so the session prefers the parent's custom table. (Escalated from Devin Review on [#1150](https://github.com/podhmo/minigo/pull/1150).)
