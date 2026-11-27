package bytecode

import (
	"go/ast"
	"go/token"
	"sync/atomic"

	"github.com/podhmo/minigo/syntax"
)

// GlobalSite is one instruction's resolution cache — OpGlobal guards its
// entry on the package's Globals generation, OpSelect/OpSelectCall on the
// receiver's TypeID and the method-set epoch. The VM owns the stored
// value's type; Cache is shared by every VM (goroutine) running the
// chunk. A global entry stays valid while the env generation is
// unchanged: code that changes name resolution without setting a global
// must call [github.com/podhmo/minigo/runtime.Env.Touch].
type GlobalSite struct {
	Cache atomic.Value
}

// CallSite is one deferred fact (M4-7): a type expression the compiler
// could not resolve under IndexedOnly. The VM resolves it on first
// execution — under AllowIndex, through the engine hook — and caches the
// outcome on the site, so an unexecuted instruction never asks and a
// resolved one never asks twice. bytecode cannot name runtime types:
// Ctx is whatever resolution context the chunk's producer stashed (the
// compiler stores a context typedef), and Cache holds a producer-side
// result box.
type CallSite struct {
	// Expr is the type expression in question — `pkg.T`, `[]L`, a
	// composite-literal element — in the declaring file's syntax.
	Expr ast.Expr
	// File is the declaring file, whose imports resolve qualifiers.
	File *syntax.File
	// Pos is the source position traps report at.
	Pos token.Pos
	// Ctx is opaque producer context for the resolver (package,
	// type-parameter binds); nil when the frame's own context applies.
	Ctx any
	// Consts maps the local declarations in scope where Expr was
	// spelled: true for a `const`-bound name, false for a non-const
	// one (a local var or type shadows a package const of the same
	// name, so presence alone cannot answer "is this identifier a
	// constant"). Nil when the site has no local scope.
	Consts map[string]bool
	// Depth peels Expr's resolved typedef through its element typedefs
	// (a `[]T` literal's elements sit one peel below). Key then takes
	// the map key typedef, and After peels further element typedefs —
	// together they spell which container a deferred literal-key
	// question asks about.
	Depth int
	Key   bool
	After int
	// Args carries a call site's per-argument static-typedef recipes —
	// what argStatics used to emit as operand ops (P4-4a). nil for a
	// pure deferred-fact site.
	Args []ArgHint
	// Sym and Quote index [Chunk.Consts] for a special-form call site:
	// the *runtime.SymbolID and the *runtime.QuotedCall. -1 when the
	// site is not a special form.
	Sym   int
	Quote int
	// Cache stores the resolution result, written once and read by
	// every goroutine running the chunk.
	Cache atomic.Value
}

// ArgHintKind is one static-typedef recipe's source — the compile-time
// answer an argStatics push used to evaluate per call.
type ArgHintKind byte

const (
	// HintNil: no static type (the old OpNil push).
	HintNil ArgHintKind = iota
	// HintTyp: a typedef constant — A indexes Chunk.Consts (*TypeDef).
	HintTyp
	// HintLocalTyp: the caller local slot's declared typedef — A is the
	// slot (the old OpLocalTyp).
	HintLocalTyp
	// HintUpvalTyp: the caller upval cell's stamp — A is the index (the
	// old OpUpvalTyp).
	HintUpvalTyp
	// HintGlobalTyp: the package global cell's stamp — A indexes
	// Chunk.Consts for the name (the old OpGlobalTyp).
	HintGlobalTyp
	// HintDeferredTyp: a deferred type-expression site — A indexes
	// Chunk.CallSites (the old OpDeferredTyp/typeExpr run).
	HintDeferredTyp
	// HintDeferredValueTyp: a deferred value-typedef site — A indexes
	// Chunk.CallSites (the old OpDeferredValueTyp).
	HintDeferredValueTyp
)

// ArgHint is one argument's static-typedef recipe at a call site. The VM
// resolves it against the *calling* frame — locals, upvals and globals
// read the caller's cells, deferred sites resolve and cache once.
type ArgHint struct {
	Kind ArgHintKind
	A    int32
}
