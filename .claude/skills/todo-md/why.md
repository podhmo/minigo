# Why the task tracker is organized this way

`TODO.md` grew to ~135KB (241 lines): ~179 completed `[x]` items (~99KB)
accumulated under one `## To Be Implemented` section because the old
AGENTS.md discipline never moved anything out and entries carried full fix
narratives. It stopped working as a task tracker — nobody, human or LLM,
could read it whole.

Restructured on 2026-10-04 under
[issue #386](https://github.com/podhmo/minigo/issues/386). Three alternative
schemes were implemented and compared (PRs
[#384](https://github.com/podhmo/minigo/pull/384),
[#385](https://github.com/podhmo/minigo/pull/385),
[#387](https://github.com/podhmo/minigo/pull/387)); the comparison report is
attached to the issue comments. This repo adopted the open-only scheme (TODO.md lists open work; history lives in git and sketch docs).

The principle that survives every scheme: **the tracker holds actionable
work only — detail lives in linked docs, archives, or git history, never
inline.**
