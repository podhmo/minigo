// Package compile lowers Go functions, package initializers and
// expressions to [bytecode.Chunk] values for the minigo stack VM.
//
// Every Go program compiles. A construct outside the supported subset
// compiles to a [bytecode.OpTrap] that fails at run time, with its
// position, only if execution reaches it.
//
// Only locals and upvalues are resolved at compile time. Any other name
// compiles to [bytecode.OpGlobal] or [bytecode.OpSetGlobal] and is looked
// up at run time (file imports, then package globals, then builtins),
// which keeps imports lazy.
package compile

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/printer"
	"go/token"
	"strings"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
)

// namedBoolTyp returns t's type expression when it is a typed operand
// of a declared (non-predeclared) type — a named bool in a logical op —
// and nil otherwise.
func (c *compiler) namedBoolTyp(t opTyp) ast.Expr {
	if t.te == nil || t.untyped || t.iface || t.nilOperand {
		return nil
	}
	ni, ok := c.namedIdentOf(t.te)
	if !ok {
		return nil
	}
	if id, isStr := ni.id.(string); isStr && strings.HasPrefix(id, "predecl:") {
		return nil
	}
	return t.te
}

// logicalNamedTyp returns the declared type a `&&`/`||` expression
// takes from a named bool operand (`type B bool; v && true` is a B), or
// nil when both operands are plain bool or untyped.
func (c *compiler) logicalNamedTyp(x *ast.BinaryExpr) ast.Expr {
	if te := c.namedBoolTyp(c.staticOpTyp(x.X)); te != nil {
		return te
	}
	return c.namedBoolTyp(c.staticOpTyp(x.Y))
}

// nonIntBasicTyp reports whether a type expression's underlying type
// is a float, complex or string basic type — one a non-constant
// shift's untyped left operand cannot adopt.
func (c *compiler) nonIntBasicTyp(te ast.Expr) bool {
	id, ok := c.unfoldTypExpr(te, 8).(*ast.Ident)
	if !ok {
		return false
	}
	if id == ast.Unparen(te) && c.declared(id.Name) && !predeclaredTypeNames[id.Name] {
		return false
	}
	switch id.Name {
	case "float32", "float64", "complex64", "complex128", "string":
		return true
	}
	return false
}

// nonConstShiftOperand finds, inside an untyped operand, a shift whose
// left operand is an untyped constant and whose count is not constant
// — `1<<n` in `f + 1<<n*2` — and returns that left operand; nil when
// there is none. It descends only through the arithmetic that keeps
// the expression untyped (parens, unary + - ^, binary non-comparison
// ops), the places the outer operand's type flows into.
func (c *compiler) nonConstShiftOperand(e ast.Expr, depth int) ast.Expr {
	if depth <= 0 {
		return nil
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.UnaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.XOR:
			return c.nonConstShiftOperand(x.X, depth-1)
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.SHL, token.SHR:
			if c.staticOpTyp(x.X).untyped && !c.isConstExpr(x.X) {
				return c.nonConstShiftOperand(x.X, depth-1)
			}
			if c.staticOpTyp(x.X).untyped && c.certainlyNonConst(x.Y) {
				// only a count proven non-constant: an unresolved
				// constant count must not reject a valid program.
				return x.X
			}
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
			token.AND, token.OR, token.XOR, token.AND_NOT:
			if c.staticOpTyp(x.X).untyped {
				if l := c.nonConstShiftOperand(x.X, depth-1); l != nil {
					return l
				}
			}
			if c.staticOpTyp(x.Y).untyped {
				return c.nonConstShiftOperand(x.Y, depth-1)
			}
		}
	}
	return nil
}

// trapOpGate emits the operand-gate trap in gc's wording for the
// context: `invalid case s in switch on d` inside a switch clause,
// `invalid operation` everywhere else.
func (c *compiler) trapOpGate(x, y ast.Expr, pos token.Pos, inSwitch bool, xName, yName string) {
	if inSwitch {
		c.trap(pos, "invalid case %s in switch on %s (mismatched types %s and %s)", c.exprSpell(x), c.exprSpell(y), xName, yName)
		return
	}
	c.trap(pos, "invalid operation: mismatched types %s and %s", xName, yName)
}

// exprSpell renders an operand's source spelling for gc-style case
// messages (`invalid case s in switch on d`). It falls back to the
// node's Go type when the package fileset is unavailable.
func (c *compiler) exprSpell(e ast.Expr) string {
	var buf bytes.Buffer
	if c.pkg == nil || c.pkg.Fset == nil || printer.Fprint(&buf, c.pkg.Fset, e) != nil {
		return fmt.Sprintf("%T", e)
	}
	return buf.String()
}

func (c *compiler) expr(e ast.Expr) {
	switch x := e.(type) {
	case *ast.BasicLit:
		v, err := literalValue(x)
		if err != nil {
			c.trap(x.Pos(), "bad literal: %s", err)
			return
		}
		c.emit(bytecode.OpConst, c.constIdx(v), 0, x.Pos())
	case *ast.Ident:
		switch x.Name {
		case "nil", "true", "false":
			// a declaration shadows the predeclared literal — Go lets
			// users redeclare every predeclared name (`const true = 31`).
			if c.declared(x.Name) {
				c.getRef(x.Name, x.Pos())
				return
			}
			switch x.Name {
			case "nil":
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
			case "true":
				c.emit(bytecode.OpConst, c.constIdx(&runtime.UConst{V: constant.MakeBool(true)}), 0, x.Pos())
			case "false":
				c.emit(bytecode.OpConst, c.constIdx(&runtime.UConst{V: constant.MakeBool(false)}), 0, x.Pos())
			}
		default:
			if dc, ok := c.deferCallTemps[x.Name]; ok {
				c.deferredCallOperand(dc, x.Pos())
				return
			}
			c.getRef(x.Name, x.Pos())
		}
	case *ast.SelectorExpr:
		c.boxReceiver(x.X, x.Sel.Name)
		c.selectorBase(x.X, x.Sel.Name)
		// the member's own position — Go reports a select failure at the
		// .Sel token, which matters when the callee wraps to the next
		// line (`v.\n\t\tA()` reports A's line, not v's).
		c.emit(bytecode.OpSelect, c.nameIdx(x.Sel.Name), 0, x.Sel.Pos())
	case *ast.IndexExpr:
		// OpInstantiate doubles as indexing: non-generic bases fall back to
		// an index lookup, so `a[i]` and `F[T]` share one encoding. A type
		// form arg (F[[]int]) compiles to a typedef; anything else stays a
		// value expr (ident type args resolve through getRef too).
		c.expr(x.X)
		if c.isTypeForm(x.Index) {
			c.typeExpr(x.Index)
		} else {
			c.expr(x.Index)
		}
		c.emit(bytecode.OpInstantiate, 1, 0, x.Lbrack)
	case *ast.SliceExpr:
		if ix, ok := indexOperand(x.X); ok && c.refableIndexBase(ix.X) {
			// a[i][:] binds the element's storage like &a[i] does —
			// an index REF keeps array elements' slices aliased to
			// the container's backing (OpIndex would read a copy).
			// B=2 falls back to the element value when the base has
			// no element storage (map elements are unaddressable in
			// Go, so m[k][:] still slices a copy).
			c.expr(ix.X)
			c.expr(ix.Index)
			c.emit(bytecode.OpIndexRef, 0, 2, ix.Lbrack)
		} else {
			c.expr(x.X)
		}
		if x.Low != nil {
			c.expr(x.Low)
		} else {
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
		}
		if x.High != nil {
			c.expr(x.High)
		} else {
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
		}
		if x.Slice3 {
			if x.Max != nil {
				c.expr(x.Max)
			} else {
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
			}
			c.emit(bytecode.OpSlice, 0, 1, x.Lbrack)
			return
		}
		c.emit(bytecode.OpSlice, 0, 0, x.Lbrack)
	case *ast.StarExpr:
		c.expr(x.X)
		c.emit(bytecode.OpDeref, 0, 0, x.Pos())
	case *ast.ParenExpr:
		c.expr(x.X)
	case *ast.UnaryExpr:
		c.unary(x)
	case *ast.BinaryExpr:
		c.binary(x)
	case *ast.CallExpr:
		c.call(x)
	case *ast.CompositeLit:
		c.compositeLit(x)
	case *ast.FuncLit:
		c.funcLit(x)
	case *ast.TypeAssertExpr:
		c.expr(x.X)
		if x.Type == nil {
			c.trap(x.Pos(), ".(type) outside type switch")
			return
		}
		c.staticTyp(x.X)
		c.typeExpr(x.Type)
		c.emit(bytecode.OpAssert, 0, 1, x.Pos())
	case *ast.IndexListExpr:
		// multi-index is only legal as generic instantiation F[T, U]
		c.expr(x.X)
		for _, i := range x.Indices {
			c.typeExpr(i)
		}
		c.emit(bytecode.OpInstantiate, len(x.Indices), 0, x.Lbrack)
	case *ast.Ellipsis:
		c.trap(x.Pos(), "bare ellipsis is not supported")
	case *ast.KeyValueExpr:
		c.trap(x.Pos(), "key:value outside composite literal")
	case *ast.InterfaceType, *ast.StructType, *ast.FuncType,
		*ast.ArrayType, *ast.MapType, *ast.ChanType:
		// a type expression in value position — `interface{ m() }.m`
		// (anonymous-interface method expression) compiles the typedef.
		c.typeExpr(x)
	default:
		c.trap(e.Pos(), "unsupported expression %T", e)
	}
}

func (c *compiler) unary(x *ast.UnaryExpr) {
	switch x.Op {
	case token.AND: // &x
		switch t := x.X.(type) {
		case *ast.Ident:
			c.refRef(t.Name, t.Pos())
		case *ast.CompositeLit:
			c.compositeLit(t)
			c.emit(bytecode.OpBox, 0, 0, x.Pos())
		case *ast.SelectorExpr:
			// `&x.f` — the FieldRef the operand ref builds keeps the
			// base's cell alive; `&x[i]` takes the index-ref branch and
			// reads the container by value instead.
			if id := rootIdent(t.X); id != nil {
				c.fs.boxName(id.Name, "fieldref")
			}
			c.refTargetBase(t.X, false, true)
			c.emit(bytecode.OpFieldRef, c.nameIdx(t.Sel.Name), 0, t.Pos())
		case *ast.IndexExpr:
			c.aliasBase(t.X)
			c.expr(t.Index)
			c.emit(bytecode.OpIndexRef, 0, 0, t.Lbrack)
		case *ast.StarExpr:
			// &*p is p — the address-of and the dereference cancel,
			// but the dereference's nil check still fires (Go panics
			// on &*p when p is nil).
			c.expr(t.X)
			c.emit(bytecode.OpNilPtrCheck, 0, 0, x.Pos())
		case *ast.ParenExpr:
			// &(expr) recurses on the unwrapped operand.
			c.unary(&ast.UnaryExpr{OpPos: x.OpPos, Op: x.Op, X: t.X})
		default:
			c.trap(x.Pos(), "address-of %T is not supported", x.X)
		}
	case token.ADD:
		if c.foldUnaryConst(x) {
			return
		}
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnPos), 0, x.Pos())
	case token.SUB:
		if c.foldUnaryConst(x) {
			return
		}
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnNeg), 0, x.Pos())
	case token.NOT:
		if c.foldUnaryConst(x) {
			return
		}
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnNot), 0, x.Pos())
	case token.XOR:
		if c.foldUnaryConst(x) {
			return
		}
		c.expr(x.X)
		c.emit(bytecode.OpUnary, int(bytecode.UnXor), 0, x.Pos())
	case token.ARROW:
		c.expr(x.X)
		c.emit(bytecode.OpRecv, 0, 0, x.Pos())
	default:
		c.trap(x.Pos(), "unsupported unary %s", x.Op)
	}
}

func (c *compiler) binary(x *ast.BinaryExpr) {
	switch x.Op {
	case token.LAND, token.LOR:
		if c.opCmpGate(x.X, x.Y, x.Pos()) {
			return
		}
		jump := bytecode.OpJumpFalse
		if x.Op == token.LOR {
			jump = bytecode.OpJumpTrue
		}
		c.expr(x.X)
		c.emit(bytecode.OpDup, 0, 0, x.Pos())
		j := c.emit(jump, 0, 0, x.Pos())
		c.emit(bytecode.OpPop, 0, 0, x.Pos())
		c.expr(x.Y)
		c.patchA(j, len(c.ch.Code))
		if te := c.logicalNamedTyp(x); te != nil {
			// whichever operand decides the result, it is the named
			// bool type: `v && true` with `type B bool` is a B even
			// when the untyped true is what comes out.
			c.typeExpr(te)
			c.emit(bytecode.OpCoerceTop, 0, 0, x.Pos())
		}
	default:
		if c.foldConst(x) {
			return
		}
		op, ok := binOpOf(x.Op)
		if !ok {
			c.trap(x.Pos(), "unsupported binary %s", x.Op)
			return
		}
		// statically distinct named operands are a compile error in gc —
		// a run-time check can't see it once a value carries `any`.
		if op != bytecode.BinShl && op != bytecode.BinShr && c.opCmpGate(x.X, x.Y, x.Pos()) {
			return
		}
		if op == bytecode.BinShl || op == bytecode.BinShr {
			c.emitShift(op, x)
			return
		}
		c.binaryOperands(x.X, x.Y)
		if (op == bytecode.BinEql || op == bytecode.BinNeq) && (c.isIfaceExpr(x.X) || c.isIfaceExpr(x.Y)) {
			// an interface-typed operand compares (dynamic type, value)
			// pairs: `any(ch) == any(r)` is false for chan-int and
			// chan<-int values sharing one channel, and `any(A{}) ==
			// any(B{})` is false for look-alike declared array types —
			// a statically typed `ch == r` still resolves by
			// assignability and stays true.
			c.emit(bytecode.OpBinary, int(bytecode.BinEqlIface), 0, x.Pos())
			if op == bytecode.BinNeq {
				c.emit(bytecode.OpUnary, int(bytecode.UnNot), 0, x.Pos())
			}
			return
		}
		c.emit(bytecode.OpBinary, int(op), 0, x.Pos())
	}
}

// binaryOperands emits the operands of a plain binary op in Go's
// two-phase order — the same assembly hoistEagerOps runs for call
// arguments: the eager operations inside the operands (calls, channel
// receives, slice bounds, type assertions, map literals —
// hoistedArgCalls) materialize to scratch locals first in lexical
// order across both operands, then each operand's deferred reads run
// left-to-right at the operation.
// `int(s[i]) + f(r[j])` therefore reports r[j]'s panic — a pure left
// read defers past the right's call — and `Itoa(v)[i] + f(r[j])` calls
// Itoa first but still checks the index after r[j]. When a conditional
// (&&/||) inside an operand makes hoisting unsafe, the left read
// instead defers wholesale, the model's earlier approximation.
func (c *compiler) binaryOperands(x, y ast.Expr) {
	if subs, ok := c.hoistEagerOps([]ast.Expr{x, y}, "$bin"); ok {
		c.expr(subs[0])
		c.expr(subs[1])
		return
	}
	if c.pureOperand(x) && !c.pureOperand(y) {
		c.expr(y)
		c.expr(x)
		c.emit(bytecode.OpSwap, 0, 0, x.Pos())
		return
	}
	c.expr(x)
	c.expr(y)
}

// pureOperand reports whether an operand can defer its evaluation to the
// operation point — gc reads pure operands (variables, fields, indexes,
// literals) when the operator runs, not in operand order, so `c + inc()`
// sees inc()'s side effects in c. Real calls, sends/receives and the
// operations gc materializes eagerly at operand position — type
// assertions, slice bounds checks, composite literals — disqualify.
// A conversion is transparent here: `T(x)` defers its pure interior
// reads like the index/arithmetic op it wraps (unlike a real call).
func (c *compiler) pureOperand(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.FuncLit:
		return true
	case *ast.ParenExpr:
		return c.pureOperand(x.X)
	case *ast.StarExpr:
		return c.pureOperand(x.X)
	case *ast.UnaryExpr:
		return x.Op != token.ARROW && c.pureOperand(x.X)
	case *ast.BinaryExpr:
		// a conditional op is an ordering point of its own — its
		// operands run at its position, not at the enclosing op.
		return x.Op != token.LAND && x.Op != token.LOR &&
			c.pureOperand(x.X) && c.pureOperand(x.Y)
	case *ast.SelectorExpr:
		return c.pureOperand(x.X)
	case *ast.IndexExpr:
		return c.pureOperand(x.X) && c.pureOperand(x.Index)
	case *ast.IndexListExpr:
		if !c.pureOperand(x.X) {
			return false
		}
		for _, i := range x.Indices {
			if !c.pureOperand(i) {
				return false
			}
		}
		return true
	case *ast.CallExpr:
		if !c.conversionCall(x) {
			return false
		}
		for _, a := range x.Args {
			if !c.pureOperand(a) {
				return false
			}
		}
		return true
	case *ast.SliceExpr:
		return false // gc checks the bounds at operand position
	case *ast.TypeAssertExpr:
		return false // gc materializes the check at operand position
	case *ast.CompositeLit:
		// a map literal lowers to runtime makemap/mapassign calls —
		// materialized eagerly at operand position; a struct, array
		// or slice literal's interior reads defer like other pure
		// reads (S{v: r[i]} == f(r[j]) panics on r[j]).
		if c.mapLitType(x.Type, 8) {
			return false
		}
		for _, e := range x.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if !c.pureOperand(kv.Key) || !c.pureOperand(kv.Value) {
					return false
				}
			} else if !c.pureOperand(e) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// typeExpr emits a push of *runtime.TypeDef for a type expression.
func (c *compiler) typeExpr(e ast.Expr) {
	// under pkgScopeTyp the spelling belongs to a package decl: its
	// file's imports resolve names, and the use site contributes no
	// function-local types.
	file := c.file
	var localTypes map[string]*runtime.TypeDef
	if c.pkgScopeTyp != nil {
		file = c.pkgScopeTyp
	} else {
		localTypes = c.fs.localTypeDefs()
	}
	c.checkTypeExprMembers(e)
	switch t := e.(type) {
	case *ast.Ident:
		c.typeIdent(e, t.Name)
	case *ast.SelectorExpr:
		if c.pkgScopeTyp != nil {
			// pkg.Type spelled in the decl's file — the use site
			// may not import pkg at all.
			c.emit(bytecode.OpEvalFragment, c.constIdx(&bytecode.ASTFragment{Expr: t, File: file}), 0, e.Pos())
		} else {
			// pkg.Type: a selector cannot be shadowed by a local var
			c.expr(t)
		}
	case *ast.ArrayType:
		// Anon/Pkg/File let OpElemType resolve the element typedef later;
		// Binds carries the generic instantiation so `[]T` resolves T.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.MapType:
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.StarExpr:
		// *T is a real typedef now: `var p *int` yields a TypedNil,
		// `x.(*T)` asserts on pointer identity, `[]*T{{...}}` auto-takes &.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindPointer, Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.StructType:
		td := &runtime.TypeDef{Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}
		td.SetStructShape(t)
		c.emit(bytecode.OpConst, c.constIdx(td), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.FuncType:
		// the signature AST rides on the typedef so generalized inference
		// (Go 1.27) can unify it against a generic function's parameters.
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}
		td.SetInterfaceShape(t)
		c.emit(bytecode.OpConst, c.constIdx(td), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.ParenExpr:
		c.typeExpr(t.X)
	case *ast.IndexExpr:
		// generic instantiation T[Args]: args in type position
		c.expr(t.X)
		c.typeExpr(t.Index)
		c.emit(bytecode.OpInstantiate, 1, 0, t.Lbrack)
	case *ast.IndexListExpr:
		c.expr(t.X)
		for _, i := range t.Indices {
			c.typeExpr(i)
		}
		c.emit(bytecode.OpInstantiate, len(t.Indices), 0, t.Lbrack)
	case *ast.Ellipsis:
		// ...T binds as []T: a variadic param's declared type IS a slice,
		// so a missing rest coerces to TypedNil{slice}, not the elem zero
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: t.Pos(), Elt: t.Elt}, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	case *ast.ChanType:
		c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, e.Pos())
		c.emitLenFolds(t)
	default:
		c.trap(e.Pos(), "unsupported type expression %T", e)
	}
	// Inside an instantiated generic function the enclosing type args
	// are part of every local type's identity and of how embedded local
	// names spell — `type L0 int` in F[int] is `main.L0[int]`, and
	// `func(U[int])` spells `func(main.U[int;int]·3)`. OpLocalType folds
	// them onto the pushed typedef (a no-op outside instantiations).
	c.emit(bytecode.OpLocalType, 0, 0, e.Pos())
}

// checkTypeExprMembers traps on a package-qualified name in type
// position that an already-known package demonstrably lacks — gc
// rejects `var x *json.Nope` at compile time; the lazy OpConst typedef
// would silently mint a broken nilable. Unindexed packages still defer
// (their facts arrive with materialization) and Failed queries stay
// silent, so only a Missing answer traps.
func (c *compiler) checkTypeExprMembers(e ast.Expr) {
	checkList := func(l *ast.FieldList) {
		if l == nil {
			return
		}
		for _, f := range l.List {
			c.checkTypeExprMembers(f.Type)
		}
	}
	switch t := e.(type) {
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			// a.b.T — the qualifier is itself a member path.
			c.checkTypeExprMembers(t.X)
			return
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return
		}
		// Only a source-indexed package can demonstrate a member is
		// missing — bound packages expose types through their host
		// bindings instead (`exec.Cmd` is usable but never indexed),
		// so a Missing answer there stays lazy like before.
		res := c.askIndexed(ref)
		if res.Val == nil || res.Val.Index == nil {
			return
		}
		if c.selMember(ref, t.Sel.Name).Kind == semantics.Missing {
			c.trap(t.Pos(), "undefined: %s.%s", id.Name, t.Sel.Name)
		}
	case *ast.StarExpr:
		c.checkTypeExprMembers(t.X)
	case *ast.ParenExpr:
		c.checkTypeExprMembers(t.X)
	case *ast.Ellipsis:
		c.checkTypeExprMembers(t.Elt)
	case *ast.ArrayType:
		// t.Len is a value expression (`[n]T`, `[strconv.IntSize]byte`),
		// not a type — its selectors are resolved as members at compile
		// time by the operand path, not here.
		c.checkTypeExprMembers(t.Elt)
	case *ast.MapType:
		c.checkTypeExprMembers(t.Key)
		c.checkTypeExprMembers(t.Value)
	case *ast.ChanType:
		c.checkTypeExprMembers(t.Value)
	case *ast.FuncType:
		checkList(t.Params)
		checkList(t.Results)
	case *ast.StructType:
		checkList(t.Fields)
	case *ast.InterfaceType:
		checkList(t.Methods)
	case *ast.IndexExpr:
		c.checkTypeExprMembers(t.X)
		c.checkTypeExprMembers(t.Index)
	case *ast.IndexListExpr:
		c.checkTypeExprMembers(t.X)
		for _, i := range t.Indices {
			c.checkTypeExprMembers(i)
		}
	}
}
