# Doc comment policy

Go doc comments serve callers. Keep enough to find an entry point, use it
correctly and understand its results. Implementation rationale and history
belong in source comments or `docs/`.

## Package documentation

Keep one package doc comment, conventionally in `doc.go` or the package's
main source file. Start with `Package <name>` for a library or
`Command <name>` for a command. Detach other file headers from the package
clause with a blank line so `go doc` does not concatenate them.

Make the overview a short map:

- State the purpose and link the entry point, such as [NewEngine] or
  [Engine.Run]. Add a snippet only when it clarifies how to start.
- Connect the main types and operations. Link the next useful API or
  related package, not every exported name: `go doc` already lists them.
- Explain package-wide usage restrictions once. Put detailed contracts
  on the relevant identifiers rather than repeating them in the map.

Use existing comment locations when practical; filenames and a fixed
number of paragraphs are not quality goals.

## Identifier documentation

Keep information the name and signature cannot express:

- Effects on arguments and shared state, copying and aliasing, ordering,
  initialization, concurrency restrictions and required cleanup.
- Errors, panics, nil and `ok == false` cases, defaults, argument syntax
  and result representations that callers must understand.
- Differences from Go or a likely expectation, such as REPL `:=` reusing
  a global, or host arguments crossing the interpreter boundary by copy.
- A useful comparison or onward link, such as `RunFile` versus `Run`.
  Avoid repeating the same navigation on every member.
- Short examples for mini-languages, ref strings and DSL call shapes.

Remove name/signature restatements, duplicated contracts and filler added
only to make comment coverage uniform. An obvious identifier may have no
comment. Do not copy familiar standard-library docs onto wrapper methods;
document the shared API once and explain differences where they occur.

Move useful maintainer information to a detached header, a comment inside
the function or beside the relevant code. This includes internal call
paths, algorithms, file organization, design justifications, TODOs, issue
numbers and migration history. Delete it only when redundant with code or
other retained documentation.

Judge details by the caller of that package. Bytecode operands, hook
requirements, representation invariants and cache invalidation are public
contracts for low-level consumers; technical vocabulary alone is not a
reason to remove them. Exported field and interface-method comments follow
the same rules as top-level declarations.

## Style

- When a comment is needed, start with the identifier name and make the
  first sentence useful on its own.
- Prefer one short paragraph; use a list for distinct cases. Retain a
  longer explanation when shortening it would hide a contract.
- Use Go doc links (`[Name]`, `[pkg.Name]`, `[Type.Method]`) for useful
  navigation. Code fragments and literal syntax are not doc links.
- For constants, explain the group and only add member comments where
  names leave behavior unclear.

Unexported identifiers are outside this policy. Example fixtures,
generated code and source used as inspection input are not caller
libraries: do not add navigation boilerplate or rewrite comments consumed
by generators merely to satisfy this format.

## Verify with go doc

Read both `go doc ./pkg` and `go doc -all ./pkg`, including exported fields
and interface methods. For nested modules, run inside the owning module.

Check that there is one package overview and that a representative caller
can move from its entry point to the relevant type and operation. Use the
generated API listing too. Explain where a type comes from or goes next
only when the signature, constructor listing or map leaves it unclear.

Resolved links lose their brackets in `go doc`. Inspect remaining bracket
matches for broken links, including links followed by letters (`[Value]s`).
Generic arguments, map types, indexing and optional command arguments also
use brackets and are not broken links. Fix invalid names rather than
adding links solely for coverage.

Review each sentence for caller value; word counts and comment coverage
alone do not establish quality.
