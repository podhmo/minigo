# Doc comment policy

This policy decides what belongs in a Go doc comment — the text `go doc`
and pkg.go.dev show — in this repository. Apply it when writing new
exported API and when reviewing existing comments.

The goal: a reader who only runs `go doc` can find the entry point, learn
how to use it, and follow links to the next thing they need. Everything
else (how it works inside, why it was built this way, history) lives
outside doc comments.

## 1. Audience

A doc comment is written for a **caller** of the identifier, not for a
maintainer of its implementation. Ask: "would someone calling this need
to know this to use it correctly?" If not, it does not belong in the doc
comment.

## 2. Package documentation

- Exactly **one** file per package carries the package doc comment —
  `doc.go` if the package has one, else the file named after the
  package. It starts with `Package <name>`.
- Every other file's header comment must be **detached** from the
  `package` clause (separate it with a blank line), otherwise `go doc`
  concatenates it into the package doc. Detaching keeps maintainer notes
  in the source without showing them to callers.
- The package doc is a **map**, in this order:
  1. One sentence: what the package is for.
  2. Where to start: the entry-point identifiers, as doc links
     (`[NewEngine]`, `[Engine.Run]`), ideally with a short usage snippet.
  3. The main concepts / types and how they relate, each as a doc link.
  4. Pointers to related packages (`[github.com/podhmo/minigo/runtime]`)
     and to design docs, if a reader needs them to use the package.
- Keep it short. Design rationale goes to `docs/`, not the package doc.

## 3. Identifier documentation

### Keep (this is what callers need)

- **Contract**: what it returns, what it does to its arguments and to
  shared state, what errors it reports, nil/zero-value behavior.
- **When to use it vs. its siblings**: `RunFile is Run for a single
  file` is valuable — it places the identifier on the map.
- **Observable semantics that differ from Go or from the obvious
  guess**: e.g. a REPL `:=` reusing an existing global, or arguments
  crossing the host boundary by copy.
- **Navigation**: doc links (`[Engine.Call]`, `[WithHostPolicy]`,
  `[runtime.Value]`) to the identifiers a caller needs next.
- **Small examples** of input syntax when the input is a mini-language
  (meta commands, ref strings, DSL call shapes).

### Remove

- **Restating the name.** `WithResolver swaps the resolver` adds
  nothing to `func WithResolver(r resolve.Resolver)`. If the signature
  and name say it all, delete the comment entirely rather than keep a
  tautology. (A tautological one-liner is acceptable only if the type
  would otherwise look undocumented in a list where every sibling has
  real docs — prefer improving it over keeping it.)
- **Implementation details**: which internal function, field, cache, or
  opcode is used (`e.pkgs`, `goNative`, `OpSpecialCall`, `OP_EVAL_AST`),
  how the algorithm works, which file does what.
- **History and migration notes**: "for now", "a later phase", "bridge
  for the X migration", "previously ...".
- **Internal references**: plan section numbers (`plan §16`), TODO
  pointers, issue numbers. Link a design doc only if a caller needs it.
- **Justifications of code-level choices** (`Value is an any alias, so
  the case must be explicit`). These are code comments — move them next
  to the code they explain.

### Move, don't lose

If removed text is still useful to maintainers, move it to a plain
comment inside the function body or next to the relevant code, or to a
detached file header. Delete it only when it is redundant with the code.

## 4. Style

- Start with the identifier name, as gofmt/`go vet` conventions expect.
- First sentence stands alone: it is what `go doc -short` and
  pkg.go.dev listings show.
- Use Go doc links (`[Name]`, `[pkg.Name]`, `[Type.Method]`) instead of
  backticks when referring to identifiers, so readers can follow them.
- Use backticks only for code fragments and literal syntax.
- Prefer one short paragraph. Use a list when enumerating cases.
- Grouped constants: one comment on the group, plus a trailing comment
  per constant only where the name is not self-explanatory.
- Unexported identifiers are out of scope of this policy.

## 5. Decision checklist

For each sentence in an exported doc comment:

1. Does it restate the name/signature? → delete.
2. Does it describe *how* rather than *what*? → move to a code comment.
3. Does it mention an unexported identifier, internal file, opcode,
   plan section, or migration state? → rewrite for callers or move.
4. Does it change how a caller would call this, or what they should
   expect back? → keep.
5. Does it point to a related identifier? → keep, as a doc link.

For each package:

1. Does `go doc ./pkg` print exactly one package doc, starting with
   `Package <name>`? If not, detach the stray file headers.
2. Can a reader find the entry point from the package doc alone?

## 6. Verify with `go doc`

Judge navigation on the rendered output, not on the source:

- Read `go doc ./pkg` (the map) and `go doc -all ./pkg` (every entry).
  Starting from the package doc, every exported identifier a caller
  needs should be reachable by following names mentioned in the text.
- For each type, check that its doc says where values come from
  (constructor, method returning it) and where they go next.
- `go doc` strips the brackets of doc links it resolves. A `[Name]` that
  still shows brackets is a broken link — fix the name, or drop the
  brackets. `go doc -all ./pkg | grep -n '\[[A-Za-z*][A-Za-z0-9_./*]*\]'`
  lists candidates (ignore `map[K]V` and similar type syntax). A link
  followed directly by letters (`[Value]s`) does not resolve.
