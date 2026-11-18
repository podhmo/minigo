package runtime

// TypeID is the identity key a session's intern table maps to one
// canonical *TypeDef: two typedefs with the same TypeID name the same
// type, and every synthesis path that would mint a fresh descriptor for
// it returns the interned one instead. The key is a canonical string —
// comparable, hashable and readable in diagnostics.
//
// The components (joined with ':' and ';' separators):
//   - a source-declared type keys on ("src", declaring package path,
//     the parse's *syntax.File — the source version — declaration
//     position, resolved type arguments)
//   - a host-bound type keys on ("host", the REPRESENTED type's
//     (PkgPath, Name), type arguments) — never the Go type of the
//     carrier: atomicBox carries both atomic.Value and atomic.Pointer,
//     and timerChanBox/afterFuncTimer both stand for *time.Timer
//   - an anonymous composite keys on ("anon", kind, the canonical
//     element/underlying spelling)
type TypeID string
