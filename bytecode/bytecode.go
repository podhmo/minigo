// Package bytecode defines the instruction set of the minigo stack VM and
// the compiled unit it runs.
//
// A [Chunk] holds the code for one function, package initializer or
// expression: a sequence of [Instruction] values plus constants, slot counts
// and [UpvalDesc] entries. [github.com/podhmo/minigo/compile] produces
// chunks and [github.com/podhmo/minigo/vm] runs them.
package bytecode

import (
	"fmt"
	"go/ast"
	"go/token"
	"sync/atomic"

	"github.com/podhmo/minigo/syntax"
)

// Op is a VM instruction kind. The comment on each constant gives the
// meaning of the instruction's A, B and C operands and its stack effect.
type Op uint8

const (
	OpNop Op = iota

	// values / stack
	OpConst    // push Consts[A]
	OpNil      // push Nil
	OpDup      // push a copy of stack top
	OpSwap     // swap top two stack slots
	OpRot3     // rotate top three: a,b,c -> b,c,a
	OpPop      // discard top
	OpNewLocal // pop -> new cell at slot A (variable declaration); B=1 makes it ReadOnly (local const)
	OpRenewVar // replace cell at slot A with a fresh cell (per-iteration loop var)
	OpLocal    // push cell(slot A).Elem
	OpLocalTyp // push the declared typedef stamped on cell(slot A) (NIL when untyped)
	OpSetLocal // pop -> cell(slot A).Elem
	OpLocalRef // push the cell at slot A itself (address-of); B=1 when the ref can be published, 0 for a transient store target
	OpUpval    // push cell(upval A).Elem
	OpUpvalTyp // push the declared typedef stamped on upval cell A (NIL when untyped)
	OpSetUpval // pop -> cell(upval A).Elem
	OpUpvalRef // push the upval cell at index A itself (multi-assign target)

	// global / package scope
	OpGlobal    // push resolve(name Consts[A]): file imports -> pkg env -> builtins; C indexes Chunk.Sites
	OpGlobalTyp // push the declared typedef stamped on the package cell for name Consts[A] (NIL when untyped)
	OpNewGlobal // pop -> pkg.Globals[name] = &Cell{v}; B=1 binds a ReadOnly cell (const decl)
	OpSetGlobal // pop -> pkg.Globals[name] (cell-aware store)
	OpGlobalRef // push the package cell for name (address-of a package var)

	// composite access
	OpSelect   // A: const idx of field/method name; pop base -> push base.name; C indexes Chunk.Sites
	OpSetField // A: const idx of name; pop value, pop base -> base.name = value
	OpIndex    // pop index, pop base -> push base[index]; B=1: container operand of a nested store or address-of (a boxed host map/slice element stays boxed over its storage)
	OpIndexOK  // pop index, pop base -> push Tuple{value, ok} (comma-ok map access)
	OpSetIndex // pop value, pop index, pop base -> base[index] = value
	OpSlice    // pop hi, pop lo, pop base -> base[lo:hi] (Nil bounds = absent); B=1: pop max first -> base[lo:hi:max]
	OpDeref    // pop cell -> push cell.Elem
	OpSetInd   // pop value, pop cell -> cell.Elem = value (*p = v)
	OpSetRefs  // A: n targets; B=0: pop A values then A refs; B=1: pop A refs then A values (range: refs evaluate after the iter pair pushes). Stores ref_i <- val_i left-to-right.
	OpBox      // pop value -> push &Cell{value} (address-of composite literal)

	// calls and literals
	OpCall          // A: argc; B: 1 = last arg is a spread slice (f(xs...)); pop args, pop callee -> call -> push result(s)
	OpDefer         // A: argc; B: spread flag; pop args, pop callee -> register on frame defer list
	OpGo            // A: argc; B: spread flag; pop args, pop callee -> spawn a goroutine in the current process
	OpPack          // pop A values -> push Tuple
	OpUnpack        // pop Tuple -> push A values (multi-assign)
	OpRejectTuple   // peek: a result Tuple on top means a multi-valued call fed a single-value argument — trap
	OpMakeComposite // A: nelems, B: flags(1=kv pairs); pop elems, pop *TypeDef -> push composite
	OpMakeClosure   // A: const idx of *Function; captures per fn.UpvalDescs -> push Closure
	OpEvalFragment  // A: const idx of *ASTFragment; compile fragment at run time -> push result

	// arithmetic
	OpBinary // A: BinOp; B: flags (ShiftConstCount, ShiftTypedLeft, ShiftNonConstCount)
	OpUnary  // A: UnOp

	// control
	OpJump       // ip = A
	OpLenIdxFold // stack [callee, base]; if base's elem type is an array replace both with len(elem) and ip = A (skips index+index-op+call); else fall through
	// OpLenDerefFold folds len(*x)/cap(*x): Go never evaluates the deref
	// of a *[N]T operand, so the call is the constant N. The stack is
	// [callee, x-tier]; a hit replaces both with N and jumps to A,
	// skipping the emitted operand/deref/call run. B=0's x is the
	// operand's declared typedef (a miss pops it so the pointer expr
	// still evaluates); B=1's x is the evaluated pointer itself (a miss
	// keeps it for OpDeref).
	OpLenDerefFold
	OpJumpFalse // pop cond; if !truthy ip = A
	OpJumpTrue  // pop cond; if truthy ip = A
	OpIter      // pop value -> push *Iterator (range over slice/map/int/string)
	// C: nvars in the low two bits; bit 2 set when a non-blank element
	// var binds each iteration's value (reading the element is a real
	// dereference on a nil *[N]T; a `_` binding never reads).
	OpRangeNext // A: exit ip; B: iterator local slot; C: nvars; pushes C values or exits

	// channels — real blocking semantics on host channels
	OpSend    // pop value, pop chan -> blocking send (park until received/closed-abort)
	OpRecv    // pop chan -> blocking receive; closed chan -> element zero
	OpRecvOK  // pop chan -> push Tuple{value, ok}
	OpSelArm  // A: nrecv; B: 1=send — pop chan (+send val) -> push *runtime.SelArm
	OpSelWait // A: ncases; B: 1=has default — pop A arms, reflect.Select, dispatch via the A(+1) OpJump table that follows

	// failure / flow
	OpPanic  // pop value -> unwind with *Panic
	OpTrap   // unwind with *Trap{reason Consts[A]} — never catchable
	OpReturn // A: nresults — pop n -> tear down frame

	// references (address-of on field/index expressions)
	OpDup2        // duplicate top two slots: a,b -> a,b,a,b
	OpFieldRef    // A: name const; pop base -> push *FieldRef{base, name} (&s.f)
	OpIndexRef    // pop key, pop base -> push *IndexRef{base, key} (&s[i]); B=1: store target (map bases allowed)
	OpNilPtrCheck // pop v -> panic on a nil pointer value; push v (&*p is still a dereference)
	OpDerefRef    // pop ref -> push *DerefRef{ref} — the location the ref's value points at (`*p` store target)

	// types / interfaces / generics
	OpAssert        // pop typedef, pop value -> push asserted value (script panic on mismatch); B=1: a static-typedef operand sits between value and typedef
	OpAssertOK      // pop typedef, pop value -> push Tuple{value, ok} (comma-ok assert)
	OpInstantiate   // A: ntypeargs; pop type args, pop generic -> push specialized value; C indexes Chunk.Sites
	OpElemType      // pop typedef -> push element typedef ([]T->T, map[K]V->V, chan T->T, *T->T)
	OpElemTypeOrNil // OpElemType that pushes NIL instead of trapping when the operand or its element type is unknown (fold probes only)

	// declared-type coercion: emitted wherever the language attaches a
	// declared type to a binding (var x T, parameters, named results,
	// return values). Pops a typedef and applies Go's zero-value /
	// interface-boxing rules to the bound value.
	OpCoerce       // A: local slot; pop typedef -> coerce cell(slot).Elem
	OpCoerceTop    // pop typedef -> coerce stack top in place (return values)
	OpCoerceN      // A: count; pop A typedefs + value -> element-wise coerce for *Tuple
	OpCoerceGlobal // A: const idx of name; pop typedef -> coerce package-global cell

	// special forms (quoted Go)
	OpSpecialCall // A: const idx *SymbolID; B: const idx *QuotedCall — quoted args, handler fires at run time

	// types / interfaces / generics (cont.)
	OpFoldArrayLen // A: const idx of the *ast.ArrayType; pop len value, pop typedef -> publish the len as that node's folded length -> push typedef
	OpLocalType    // pop typedef -> push typedef re-parameterized by the enclosing generic function's resolved type args

	// OpSelectCall is OpSelect in callee position (`i.M()`, and the
	// `defer`/`go` variants): the bound method is flagged Direct so a nil
	// *T receiver under a value method panics like Go's devirtualized
	// call — a plain nil dereference — where a lazily bound method value
	// reports the "value method ... called using nil" wrapper text.
	OpSelectCall // A: const idx of field/method name; pop base -> push base.name; C indexes Chunk.Sites

	// OpLenCallFold folds len(x)/cap(x): the stack is [callee, operand];
	// when the callee still is the predeclared len/cap builtin (B: 0=len,
	// 1=cap) the operand's slot becomes the call's single argument, both
	// slots collapse into the result, and control jumps to A, skipping
	// the emitted static+OpCall run. A miss — the name rebound after
	// compile, say — falls through to the ordinary call.
	OpLenCallFold

	// OpDeferredTyp resolves one deferred fact: A indexes
	// [Chunk.CallSites]. On first execution the VM answers the site's
	// type question through the engine hook under AllowIndex and caches
	// the result on the site; Missing/Failed trap. An unexecuted
	// instruction's site is never consulted.
	OpDeferredTyp // A: CallSites index -> push resolved *TypeDef

	// OpDeferredValueTyp is OpDeferredTyp over the value-typedef
	// question (M4-8): the site's expression is a VALUE (a deferred
	// conversion like `time.Duration(x)`), and the pushed typedef is
	// the member type it resolves to. unsafe.Sizeof/Alignof use it to
	// size a deferred operand without evaluating it.
	OpDeferredValueTyp // A: CallSites index -> push resolved *TypeDef

	// OpJumpIfTypedef pops the stack top and jumps (A) when it is a
	// *runtime.TypeDef. M4-8's undecidable member call (`p.T(x)` under
	// IndexedOnly — conversion or call?) emits two paths and picks on
	// the member's runtime shape: TypeDef keeps the conversion's
	// deferred-operand timing, anything else runs the call eagerly.
	OpJumpIfTypedef // A: jump target

	// OpLitKey is the deferred form of a composite literal's ident key
	// (M4-7b): the compile side could not decide whether `T{K: v}`'s K
	// is a field name or a key expression because T's shape was a
	// deferred fact. The site (B indexes [Chunk.CallSites]) resolves the
	// literal's declared type — when it answers a map or array the key
	// expression instructions that follow run; otherwise the field name
	// (consts[C]) is pushed and control jumps to A.
	OpLitKey // A: else ip; B: CallSites index; C: field-name const

	// OpConstExpr is the deferred form of a constant-expression verdict
	// (M4-7c): the compile side could not decide whether the site's
	// expression is constant because a qualified package member was a
	// deferred fact. The VM asks the engine hook once and caches the
	// answer on the site; a false answer jumps to A (the non-constant
	// emission), a true one falls through to the constant emission.
	OpConstExpr // A: else ip; B: CallSites index

	// OpTypGate is the deferred form of the compile-time compare gate
	// (M4-7c): one compare operand's static type was a deferred fact.
	// A and B index [Chunk.CallSites]; C bit0 marks site A a value
	// expression (resolved through the DeferredValueTyp hook), bit1 the
	// same for site B — cleared bits resolve through DeferredType. Both
	// sides resolving to distinct named typedefs traps
	// "mismatched types X and Y" in source order; anything else runs on.
	OpTypGate // A: CallSites index; B: CallSites index; C: value-site mask
)

// ShiftConstCount is an [OpBinary] B flag on << and >>: the count is
// statically a constant expression, so an untyped-constant left operand
// folds to an untyped constant instead of taking the context's type.
const ShiftConstCount = 1

// ShiftTypedLeft is an [OpBinary] B flag on << and >>: the left operand
// is statically typed (`int(1) << n`), so even when it arrives as a
// bare constant payload it is no untyped constant to adopt the
// context's type.
const ShiftTypedLeft = 2

// ShiftNonConstCount is an [OpBinary] B flag on << and >>: the count is
// statically known NOT to be a constant (a variable, a call, a field
// read). Unflagged counts that the compiler could not prove constant
// stay "maybe".
const ShiftNonConstCount = 4

// BinOp is the operator of an [OpBinary] instruction, carried in operand A.
type BinOp uint8

const (
	BinAdd      BinOp = iota // +
	BinSub                   // -
	BinMul                   // *
	BinQuo                   // /
	BinRem                   // %
	BinAnd                   // &
	BinOr                    // |
	BinXor                   // ^
	BinAndNot                // &^
	BinShl                   // <<
	BinShr                   // >>
	BinLAnd                  // &&
	BinLOr                   // ||
	BinEql                   // ==
	BinNeq                   // !=
	BinLss                   // <
	BinLeq                   // <=
	BinGtr                   // >
	BinGeq                   // >=
	BinEqlIface              // == against an interface-typed switch tag
)

var binOpSyms = [...]string{
	BinAdd: "+", BinSub: "-", BinMul: "*", BinQuo: "/", BinRem: "%",
	BinAnd: "&", BinOr: "|", BinXor: "^", BinAndNot: "&^",
	BinShl: "<<", BinShr: ">>", BinLAnd: "&&", BinLOr: "||",
	BinEql: "==", BinNeq: "!=", BinLss: "<", BinLeq: "<=",
	BinEqlIface: "==",
	BinGtr:      ">", BinGeq: ">=",
}

// String renders the operator's source spelling, for error messages.
func (b BinOp) String() string {
	if int(b) < len(binOpSyms) && binOpSyms[b] != "" {
		return binOpSyms[b]
	}
	return fmt.Sprintf("BinOp(%d)", int(b))
}

// UnOp is the operator of an [OpUnary] instruction, carried in operand A.
type UnOp uint8

const (
	UnPos UnOp = iota // +x
	UnNeg             // -x
	UnNot             // !x
	UnXor             // ^x
)

// String renders the operator's source spelling, for error messages.
func (u UnOp) String() string {
	switch u {
	case UnPos:
		return "+"
	case UnNeg:
		return "-"
	case UnNot:
		return "!"
	case UnXor:
		return "^"
	}
	return fmt.Sprintf("UnOp(%d)", int(u))
}

// Instruction is one fixed-width bytecode instruction. The operands'
// meaning depends on [Op].
type Instruction struct {
	Op   Op
	A, B int32
	C    int32 // third operand; -1 when unused
	Pos  token.Pos
}

// UpvalDesc describes how one free variable of a function is supplied when
// [OpMakeClosure] builds its closure: Index is a slot in the parent frame's
// locals, or in the parent's upvalues when FromParentUpval is set.
type UpvalDesc struct {
	FromParentUpval bool
	Index           int
}

// ASTFragment is an AST expression kept uncompiled as a chunk constant.
// [OpEvalFragment] compiles it on first execution (memoized in Cache) and
// pushes its value; the
// expression sees package globals, File's imports and builtins, but not
// the enclosing function's locals.
type ASTFragment struct {
	Expr ast.Expr
	File *syntax.File
	// Cache memoizes the fragment's compiled chunk across executions:
	// the same site compiles identically every time (P4-6). Written
	// once and read by every goroutine running the chunk.
	Cache atomic.Value
}

// Chunk is compiled code for one function, package initializer or
// expression.
type Chunk struct {
	Name       string
	Code       []Instruction
	Consts     []any
	NLocals    int // number of cell slots
	NParams    int // parameter slots (first N of NLocals; methods: slot 0 = receiver)
	NResults   int
	NamedSlots []int // local slots of named results, for bare `return`; nil when none
	Upvals     []UpvalDesc
	IsVararg   bool
	// Sites are the per-instruction caches of OpGlobal, OpSelect and
	// OpSelectCall (indexed by the instruction's C operand).
	Sites []*GlobalSite

	// CallSites are the deferred-fact records of OpDeferredTyp (indexed
	// by the instruction's A operand). One record per emitted question.
	CallSites []*CallSite

	// Escapes is the VM's memo of whether the chunk's local cells can
	// outlive their frame: 0 uncomputed, 1 frame-bound (the chunk holds
	// no OpLocalRef and no OpMakeClosure that captures a local slot —
	// such frames may recycle their cell block), 2 cells may escape.
	// The vm package computes and stores it; concurrent readers race
	// benignly on the atomic.
	Escapes atomic.Int32

	// Boxed is the compile-time slot classification (P4-2), parallel to
	// NLocals: Boxed[i] marks a local whose cell can outlive its frame —
	// its address is taken (&x, &x.f), a closure captures it, a special
	// form snapshots it, or a select may bind it through a pointer
	// receiver. Unclassified chunks (hand-built code) leave it nil and
	// fall back to the opcode scan in the VM.
	Boxed []bool
}

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
	// HintLocalTypElem / HintUpvalTypElem / HintGlobalTypElem: the Elem
	// of the cell's declared typedef — a `*p` call argument's static
	// type is the pointee (`Tpl(*t)` needs T, not *T).
	HintLocalTypElem
	HintUpvalTypElem
	HintGlobalTypElem
)

// ArgHint is one argument's static-typedef recipe at a call site. The VM
// resolves it against the *calling* frame — locals, upvals and globals
// read the caller's cells, deferred sites resolve and cache once.
type ArgHint struct {
	Kind ArgHintKind
	A    int32
}
