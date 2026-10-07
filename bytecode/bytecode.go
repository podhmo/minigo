// Package bytecode defines the instruction set of the minigo stack VM and
// the compiled unit (Chunk) produced per function.
package bytecode

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/syntax"
)

// Op is a VM instruction kind.
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
	OpLocalRef // push the cell at slot A itself (address-of)
	OpUpval    // push cell(upval A).Elem
	OpUpvalTyp // push the declared typedef stamped on upval cell A (NIL when untyped)
	OpSetUpval // pop -> cell(upval A).Elem
	OpUpvalRef // push the upval cell at index A itself (multi-assign target)

	// global / package scope
	OpGlobal    // push resolve(name Consts[A]): file imports -> pkg env -> builtins
	OpGlobalTyp // push the declared typedef stamped on the package cell for name Consts[A] (NIL when untyped)
	OpNewGlobal // pop -> pkg.Globals[name] = &Cell{v}; B=1 binds a ReadOnly cell (const decl)
	OpSetGlobal // pop -> pkg.Globals[name] (cell-aware store)
	OpGlobalRef // push the package cell for name (address-of a package var)

	// composite access
	OpSelect   // A: const idx of field/method name; pop base -> push base.name
	OpSetField // A: const idx of name; pop value, pop base -> base.name = value
	OpIndex    // pop index, pop base -> push base[index]
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
	OpMakeComposite // A: nelems, B: flags(1=kv pairs); pop elems, pop *TypeDef -> push composite
	OpMakeClosure   // A: const idx of *Function; captures per fn.UpvalDescs -> push Closure
	OpEvalAST       // A: const idx of *ASTFragment; compile fragment at run time -> push result

	// arithmetic
	OpBinary // A: BinOp
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
	OpInstantiate   // A: ntypeargs; pop type args, pop generic -> push specialized value
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
	OpFoldArrayLen // pop len value, pop typedef -> fold len into the typedef's array AST -> push typedef
	OpLocalType    // pop typedef -> push typedef re-parameterized by the enclosing generic function's resolved type args

	// OpSelectCall is OpSelect in callee position (`i.M()`, and the
	// `defer`/`go` variants): the bound method is flagged Direct so a nil
	// *T receiver under a value method panics like Go's devirtualized
	// call — a plain nil dereference — where a lazily bound method value
	// reports the "value method ... called using nil" wrapper text.
	OpSelectCall // A: const idx of field/method name; pop base -> push base.name
)

// BinOp is an OpBinary sub-op.
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

// UnOp is an OpUnary sub-op.
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

// Instruction is one fixed-width bytecode instruction.
type Instruction struct {
	Op   Op
	A, B int32
	C    int32 // third operand; -1 when unused
	Pos  token.Pos
}

// UpvalDesc describes how one free variable of a function is supplied:
// either a slot index in the parent frame's locals or the parent's upvalues.
type UpvalDesc struct {
	FromParentUpval bool
	Index           int
}

// ASTFragment is a not-yet-compiled AST expression kept as a chunk constant;
// OpEvalAST compiles and runs it under the VM at run time (the OP_EVAL_AST
// migration bridge — the basis for special-form partial evaluation).
type ASTFragment struct {
	Expr ast.Expr
	File *syntax.File
}

// Chunk is compiled code for one function.
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
}
