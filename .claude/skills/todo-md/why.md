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
attached to the issue comments. This repo adopted the single-archive scheme (TODO.md + TODO-archive.md).

The principle that survives every scheme: **the tracker holds actionable
work only — detail lives in linked docs, archives, or git history, never
inline.**

One deliberate looseness: consolidation is *asynchronous GC*, not part of
the completing PR. Feature branches just mark `[x]` (or delete); a periodic
sweep appends stragglers to the archive. That keeps `TODO-archive.md`
conflict-free across parallel branches and moves every judgment call
(parent renames, ancestor chains) into one place — the GC pass.

The archive is **always greppable**: every item is one physical line that
starts with its ancestor chain (`- [x] A > B > **C**: ...`). The earlier
verbatim layout kept wrapped lines, nested children and `> parent:` context
lines, so an `rg` hit was often a fragment whose owner sat dozens of lines
above. With one line per item the hit itself is the whole entry, path
included. Because the rule is mechanical, it is enforced by a tool
(`todo-gc/`) rather than by hand, and the tool lives inside this skill
directory so the scheme can be copied to another repo as one folder. The
one-line rule was worked out in
[podhmo/html-sandbox](https://github.com/podhmo/html-sandbox) and ported
back here.
