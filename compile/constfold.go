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
	"go/ast"
	"go/constant"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// constRefsVarOrFunc reports whether any const spec's effective value or
// type expression names a package-level var or function — references a
// const-only init cannot serve (var cells stay unbound, and calling a
// function would be a side effect a constant never has). Type and
// method names are fine: typedef materialization and member selection
// run no initializers.
func constRefsVarOrFunc(ix *index.Index, reps []*index.Decl) bool {
	bad := false
	scan := func(e ast.Expr) {
		if e == nil || bad {
			return
		}
		ast.Inspect(e, func(n ast.Node) bool {
			if bad {
				return false
			}
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if _, ok := ix.Vars[id.Name]; ok {
				bad = true
				return false
			}
			if _, ok := ix.Funcs[id.Name]; ok {
				bad = true
				return false
			}
			return true
		})
	}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		vals := vs.Values
		if len(vals) == 0 {
			vals = d.Inherited
		}
		for _, e := range vals {
			scan(e)
		}
		t := vs.Type
		if t == nil {
			t = d.InheritedType
		}
		scan(t)
	}
	return bad
}

// constExpr emits a constant declaration's initializer: when the whole
// expression is a compile-time constant it stays a UConst so each use
// materializes for its own context (`const c = 1e3; var i int = c`
// binds int 1000 where storing float64(1000) would reject the int
// conversion). A non-constant expression compiles normally and traps
// wherever it must.
func (c *compiler) constExpr(e ast.Expr) {
	if cv, ok := constValue(e); ok {
		u := &runtime.UConst{V: cv}
		if hasCharLit(e) {
			u.Rune = true
		}
		c.emit(bytecode.OpConst, c.constIdx(u), 0, e.Pos())
		return
	}
	c.expr(e)
}

// typedConstExpr emits the value of a typed const decl `const c T = e`
// as the conversion T(e): Go's typed constant is the constant
// expression T(e), so the value keeps its constant payload under T's
// tag (`c + c` with `const c int8 = 100` is gc's overflow error, and a
// shift by `const k uint = 3` stays a constant count) instead of
// materializing into a plain T value the way a var's coerce does.
//
// int, float64, complex128, string and bool are left out (ok=false):
// the VM's canonical value of those types is the bare scalar, so the
// caller's coerce already yields the right typed value.
func (c *compiler) typedConstExpr(te, e ast.Expr) bool {
	if id, ok := c.unfoldTypExpr(te, 8).(*ast.Ident); ok && ast.Unparen(c.unaliasTypExpr(te, 8)) == ast.Expr(id) {
		switch id.Name {
		case "int", "float64", "complex128", "string", "bool":
			return false
		}
	}
	c.expr(&ast.CallExpr{Fun: te, Lparen: e.Pos(), Args: []ast.Expr{e}, Rparen: e.End()})
	return true
}

// constKindDte maps a constant's default type to the ident it binds —
// reusing UConst.DefaultName's mapping (the same table defaultLitTyp
// applies to literals) so a rune-kind const binds rune, not int.
func constKindDte(uc *runtime.UConst) ast.Expr {
	switch name := uc.DefaultName(); name {
	case "int32": // an untyped rune's default spells rune, like a CHAR literal
		return &ast.Ident{Name: "rune"}
	case "unknown":
		return nil
	default:
		return &ast.Ident{Name: name}
	}
}

// isConstExpr reports whether e is statically one of Go's constant
// expressions: literals, const-bound names (local, package-level or
// package-qualified, typed or not), operators and basic-type
// conversions over constants, and the builtins whose result is
// constant — len/cap of a constant string or of an array that calls
// nothing, unsafe.Sizeof/Alignof/Offsetof, and min/max/real/imag/
// complex over constants. It under-approximates: an unresolvable
// shape reports false, and the VM still treats an operand that
// arrives as an untyped constant as constant. The shift op reads it
// to tell `1 << T` (T a typed constant: an untyped-constant result)
// from `1 << n` (n a variable: the left operand takes the context's
// type).
func (c *compiler) isConstExpr(e ast.Expr) bool {
	return c.isConstExprDepth(e, 16)
}

// semantics: fact — pkg.C's constness decides shift-count constness
// (ShiftConstCount) and const-call inlining.
func (c *compiler) isConstExprDepth(e ast.Expr, depth int) bool {
	if depth <= 0 || e == nil {
		return false
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		if b := c.fs.lookupBinding(x.Name); b != nil {
			return b.constDecl || b.untypedConst
		}
		if _, isUp := c.fs.upmap[x.Name]; isUp {
			return false
		}
		if _, bound := c.binds[x.Name]; bound {
			return false
		}
		if c.pkg != nil && c.pkg.Index != nil {
			ix := c.pkg.Index
			if ix.Consts[x.Name] != nil {
				return true
			}
			if ix.Vars[x.Name] != nil || ix.Funcs[x.Name] != nil || ix.Types[x.Name] != nil {
				return false
			}
		}
		switch x.Name {
		case "true", "false", "iota":
			return true
		}
		return false
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok || c.fs.lookupBinding(id.Name) != nil {
			return false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return false
		}
		res := c.askIndexed(ref)
		if res.Kind == semantics.Deferred {
			c.sawDeferred = true
		}
		if res.Kind != semantics.Found {
			return false
		}
		p := res.Val
		if p.Index != nil && p.Index.Consts[x.Sel.Name] != nil {
			return true
		}
		if p.Globals != nil {
			if v, ok := p.Globals.Get(x.Sel.Name); ok {
				_, isU := v.(*runtime.UConst)
				return isU
			}
		}
		return false
	case *ast.UnaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return c.isConstExprDepth(x.X, depth-1)
		}
		return false
	case *ast.BinaryExpr:
		return c.isConstExprDepth(x.X, depth-1) && c.isConstExprDepth(x.Y, depth-1)
	case *ast.CallExpr:
		if x.Ellipsis.IsValid() {
			return false
		}
		if c.conversionCall(x) {
			if len(x.Args) != 1 {
				return false
			}
			// only a conversion to a basic type keeps a constant
			// (`[]byte("s")` does not).
			te := c.unfoldTypExpr(x.Fun, 8)
			id, ok := te.(*ast.Ident)
			if !ok || !predeclaredTypeNames[id.Name] || id.Name == "error" || id.Name == "any" {
				return false
			}
			if fid, isID := ast.Unparen(x.Fun).(*ast.Ident); isID && fid == id && c.declared(id.Name) {
				return false
			}
			return c.isConstExprDepth(x.Args[0], depth-1)
		}
		switch f := ast.Unparen(x.Fun).(type) {
		case *ast.Ident:
			if c.declared(f.Name) || len(x.Args) == 0 {
				return false
			}
			switch f.Name {
			case "len", "cap":
				if len(x.Args) != 1 {
					return false
				}
				arg := x.Args[0]
				if f.Name == "len" && c.isConstExprDepth(arg, depth-1) {
					return true // len of a constant string
				}
				if c.lenOperandCalls(arg) {
					return false
				}
				at, isArr := c.lenOperandArrayTyp(arg).(*ast.ArrayType)
				return isArr && at.Len != nil
			case "min", "max", "real", "imag", "complex":
				for _, a := range x.Args {
					if !c.isConstExprDepth(a, depth-1) {
						return false
					}
				}
				return true
			}
		case *ast.SelectorExpr:
			switch f.Sel.Name {
			case "Sizeof", "Alignof", "Offsetof":
			default:
				return false
			}
			id, ok := f.X.(*ast.Ident)
			if !ok || c.fs.lookupBinding(id.Name) != nil || len(x.Args) != 1 {
				return false
			}
			if len(c.binds) > 0 && c.mayInvolveTypeParam(x.Args[0]) {
				// inside a generic body an operand whose type
				// involves a type parameter has a size that is not
				// constant.
				return false
			}
			ref, isPkg := c.importRef(id.Name)
			return isPkg && ref.Path == "unsafe"
		}
	}
	return false
}

// certainlyNonConst reports whether e is statically known NOT to be a
// constant expression: it reads a variable, a field, an index or a
// pointer, receives, or calls a function. It is isConstExpr's
// conservative counterpart — an operand neither proves is unknown.
func (c *compiler) certainlyNonConst(e ast.Expr) bool {
	return c.certainlyNonConstDepth(e, 16)
}

// semantics: fact — decides ShiftNonConstCount and the left operand
// verdict for a non-const shift.
func (c *compiler) certainlyNonConstDepth(e ast.Expr, depth int) bool {
	if depth <= 0 || e == nil {
		return false
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if b := c.fs.lookupBinding(x.Name); b != nil {
			return !b.constDecl && !b.untypedConst && !b.typeDecl
		}
		if _, isUp := c.fs.upmap[x.Name]; isUp {
			return true
		}
		if _, bound := c.binds[x.Name]; bound {
			return false
		}
		if c.pkg != nil && c.pkg.Index != nil && c.pkg.Index.Vars[x.Name] != nil {
			return true
		}
		return false
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && c.fs.lookupBinding(id.Name) == nil {
			if ref, isPkg := c.importRef(id.Name); isPkg {
				res := c.askIndexed(ref)
				if res.Kind == semantics.Deferred {
					c.sawDeferred = true
				}
				return res.Kind == semantics.Found && res.Val.Index != nil && res.Val.Index.Vars[x.Sel.Name] != nil
			}
		}
		return true // a field read is never a constant
	case *ast.IndexExpr, *ast.SliceExpr, *ast.StarExpr, *ast.TypeAssertExpr:
		return true
	case *ast.UnaryExpr:
		if x.Op == token.ARROW {
			return true
		}
		return c.certainlyNonConstDepth(x.X, depth-1)
	case *ast.BinaryExpr:
		return c.certainlyNonConstDepth(x.X, depth-1) || c.certainlyNonConstDepth(x.Y, depth-1)
	case *ast.CallExpr:
		if c.conversionCall(x) {
			return len(x.Args) == 1 && c.certainlyNonConstDepth(x.Args[0], depth-1)
		}
		switch f := ast.Unparen(x.Fun).(type) {
		case *ast.Ident:
			if !c.declared(f.Name) {
				switch f.Name {
				case "len", "cap":
					if len(x.Args) != 1 || c.isConstExpr(x) {
						return false
					}
					te := c.lenOperandArrayTyp(x.Args[0])
					if te == nil {
						return false
					}
					at, isArr := te.(*ast.ArrayType)
					return !isArr || at.Len == nil || c.lenOperandCalls(x.Args[0])
				case "min", "max", "real", "imag", "complex":
					for _, a := range x.Args {
						if c.certainlyNonConstDepth(a, depth-1) {
							return true
						}
					}
					return false
				case "new", "make", "append", "recover":
					return true
				}
				if _, builtin := universeFuncs[f.Name]; builtin {
					return false
				}
			}
			return true
		case *ast.SelectorExpr:
			if id, ok := f.X.(*ast.Ident); ok && c.fs.lookupBinding(id.Name) == nil {
				if ref, isPkg := c.importRef(id.Name); isPkg && ref.Path == "unsafe" {
					return false
				}
			}
			return true
		}
		return true
	}
	return false
}

// foldConst evaluates a constant-only binary expression in go/constant's
// arbitrary-precision domain and emits one OpConst. Go computes constant
// arithmetic exactly — 1<<100>>50 is 2^50 — where evaluating the same
// expression at runtime in int64 would silently wrap to 0. An expression
// whose constant result minigo cannot represent traps at compile time
// (the same operation Go rejects). Returns false when any operand is not
// a constant, so the caller emits the usual binary ops.
func (c *compiler) foldConst(x *ast.BinaryExpr) bool {
	// division by zero is a compile error in Go when the whole
	// expression is constant — trap it like the compiler. With a
	// non-constant dividend the op divides at runtime instead: floats
	// yield ±Inf or NaN and ints panic (a recoverable *Panic, not a
	// compile-time *Trap).
	if x.Op == token.QUO || x.Op == token.REM {
		if rv, ok := constValue(x.Y); ok && (rv.Kind() == constant.Int || rv.Kind() == constant.Float) && constant.Sign(rv) == 0 {
			if _, ok := constValue(x.X); ok {
				c.trap(x.Pos(), "constant division by zero")
				return true
			}
		}
	}
	cv, ok := constValue(x)
	if !ok {
		return false
	}
	v, ok := constOperand(cv, hasCharLit(x.X) || hasCharLit(x.Y))
	if !ok {
		// Unknown kind: the operation is undefined for these operand
		// types ("a" + 1), which Go rejects at compile time.
		c.trap(x.Pos(), "invalid constant expression: %s %s %s", x.X, x.Op, x.Y)
		return true
	}
	c.emit(bytecode.OpConst, c.constIdx(v), 0, x.Pos())
	return true
}

// constOperand materializes a folded constant for OpConst emission:
// scalars emit bare (ints wrap >int64 in GoValue so formatting reads
// the box), values that only exist in the constant domain — wide ints,
// floats that don't fit float64, complex — stay boxed UConst until a
// materialization boundary decides. rune marks char-literal exprs
// (`var y = 'a' + 1` types y as int32, not int). ok is false when the
// kind is one Go rejects in constant expressions.
func constOperand(cv constant.Value, rune bool) (v any, ok bool) {
	switch cv.Kind() {
	case constant.Bool:
		// Bools stay boxed like ints: an unboxed bool is
		// indistinguishable from a `var b bool`, which would let a
		// named-type comparison (`flag == verbose`) answer instead of
		// trapping — Go rejects it as mismatched types.
		return &runtime.UConst{V: cv}, true
	case constant.String:
		// Same for strings: `tag == "json"` must reach the runtime as
		// an untyped const so it can adopt the operand's declared
		// string type, while `tag == s` (typed var) stays a reject.
		return &runtime.UConst{V: cv}, true
	case constant.Float:
		// Float constants stay boxed like wide ints: materializing to
		// float64 eagerly loses the constness later conversions need —
		// `var f float32 = -1e-50` would narrow an already-typed -1e-50
		// to -0, where Go's constant rounds to +0 ($GOROOT/test/
		// fixedbugs/issue12621.go). Boxed UConsts reach every materialize
		// boundary, which canonicalizes constant -0 to +0.
		return &runtime.UConst{V: cv}, true
	case constant.Complex:
		return &runtime.UConst{V: cv}, true
	case constant.Int:
		// Same rule as literalValue: every int constant stays untyped
		// until a materialization boundary, preserving the constness a
		// conversion or declared type needs for its representability
		// check (`int8(300)` must fail). Rune marks 'x'-derived values.
		return &runtime.UConst{V: cv, Rune: rune}, true
	}
	return nil, false
}

// foldUnaryConst applies a unary op to a constant operand at compile
// time, like gc — `-1e-10000` folds in the exact domain so materializing
// it reads +0, where the runtime path would negate an already-rounded
// literal and leak -0 ($GOROOT/test/fixedbugs/issue12621.go).
func (c *compiler) foldUnaryConst(x *ast.UnaryExpr) bool {
	switch x.Op {
	case token.ADD, token.SUB, token.XOR, token.NOT:
	default:
		return false
	}
	cv, ok := constValue(x)
	if !ok {
		return false
	}
	v, ok := constOperand(cv, hasCharLit(x.X))
	if !ok {
		c.trap(x.Pos(), "invalid constant expression: %s", x.Op)
		return true
	}
	c.emit(bytecode.OpConst, c.constIdx(v), 0, x.Pos())
	return true
}

// hasCharLit reports whether the expression contains a rune literal —
// constant folding uses it to keep the rune kind of the result.
func hasCharLit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.CHAR {
			found = true
		}
		return !found
	})
	return found
}

// constValue evaluates an expression made only of literal constants and
// returns its go/constant value. ok is false when any operand is not a
// constant (identifiers, calls, index expressions), letting the caller
// fall back to normal codegen.
func constValue(e ast.Expr) (cv constant.Value, ok bool) {
	defer func() {
		// go/constant panics on unsound inputs (shift by a negative
		// count, integer divide by zero); those keep runtime semantics.
		if recover() != nil {
			cv, ok = nil, false
		}
	}()
	switch x := e.(type) {
	case *ast.ParenExpr:
		return constValue(x.X)
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT, token.FLOAT, token.CHAR, token.STRING, token.IMAG:
			return constant.MakeFromLiteral(x.Value, x.Kind, 0), true
		}
	case *ast.UnaryExpr:
		xv, ok := constValue(x.X)
		if !ok {
			return nil, false
		}
		switch x.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return constant.UnaryOp(x.Op, xv, 0), true
		}
	case *ast.CallExpr:
		// complex(), real() and imag() on constant operands are constant
		// expressions — `imag(1i + complex(0,2)/3 - 5i/3)` must stay in
		// exact rational arithmetic (0), not round through a runtime
		// complex128 (-2.2e-16, $GOROOT/test/fixedbugs/issue43908.go).
		id, ok := x.Fun.(*ast.Ident)
		if !ok {
			return nil, false
		}
		switch id.Name {
		case "complex":
			if len(x.Args) != 2 {
				return nil, false
			}
			re, ok := constValue(x.Args[0])
			if !ok {
				return nil, false
			}
			im, ok := constValue(x.Args[1])
			if !ok {
				return nil, false
			}
			if (re.Kind() != constant.Float && re.Kind() != constant.Int) ||
				(im.Kind() != constant.Float && im.Kind() != constant.Int) {
				return nil, false
			}
			return constant.BinaryOp(re, token.ADD, constant.MakeImag(im)), true
		case "real", "imag":
			if len(x.Args) != 1 {
				return nil, false
			}
			cv, ok := constValue(x.Args[0])
			if !ok {
				return nil, false
			}
			switch cv.Kind() {
			case constant.Int, constant.Float, constant.Complex:
			default:
				return nil, false
			}
			// the parts of an untyped complex constant are untyped
			// FLOAT constants — go/constant normalizes a whole-number
			// part (and `complex(1, 0)` itself) to Int, which would
			// make `real(complex(1, 0))/2` an integer division.
			if id.Name == "real" {
				return constant.ToFloat(constant.Real(cv)), true
			}
			return constant.ToFloat(constant.Imag(cv)), true
		}
	case *ast.BinaryExpr:
		lv, ok := constValue(x.X)
		if !ok {
			return nil, false
		}
		rv, ok := constValue(x.Y)
		if !ok {
			return nil, false
		}
		switch x.Op {
		case token.SHL, token.SHR:
			// integer-valued float constants shift in the exact
			// integer domain (`1e100 >> 1000`, `x << 1.`).
			if lv.Kind() == constant.Float {
				lv = constant.ToInt(lv)
			}
			if rv.Kind() == constant.Float {
				rv = constant.ToInt(rv)
			}
			s, ok := constant.Uint64Val(rv)
			if !ok {
				return nil, false
			}
			return constant.Shift(lv, x.Op, uint(s)), true
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return constant.MakeBool(constant.Compare(lv, x.Op, rv)), true
		case token.LAND, token.LOR:
			// keep short-circuit semantics at runtime; && and || on
			// constants are rare enough not to fold here.
			return nil, false
		case token.QUO:
			// a quotient of two integer constants is an integer constant
			// (7/2 is 3): QUO computes the exact rational, QUO_ASSIGN is
			// go/constant's spelling for truncating integer division.
			if lv.Kind() == constant.Int && rv.Kind() == constant.Int {
				return constant.BinaryOp(lv, token.QUO_ASSIGN, rv), true
			}
			return constant.BinaryOp(lv, x.Op, rv), true
		default:
			return constant.BinaryOp(lv, x.Op, rv), true
		}
	}
	return nil, false
}

// ConstExprValue evaluates a package-scope constant expression without
// running any package initializer. It exists for type-level constants
// such as array lengths, whose values must be available while constants
// themselves are still being bound.
func ConstExprValue(pkg *runtime.Package, file *syntax.File, e ast.Expr) (constant.Value, bool) {
	c := &compiler{pkg: pkg, file: file, fs: newFScope(nil), iotaVal: -1}
	c.fs.pushBlock()
	return c.scopeConstValue(e, 0)
}

// scopeConstValue evaluates a constant expression the way constValue
// does, but also through names that denote constants at this point:
// `iota`, a local const binding's initializer, and package-level const
// decls — so a `[K]T` or keyed `[...]T` literal's compile-time length
// resolves without evaluating the literal.
func (c *compiler) scopeConstValue(e ast.Expr, depth int) (cv constant.Value, ok bool) {
	cv, _, ok = c.scopeConstVal(e, depth, -1)
	return cv, ok
}

// scopeConstVal is scopeConstValue with an explicit scope mode:
// pkgIota >= 0 walks a package const's spec — the use site's local
// bindings and iota are out of scope there; `iota` reads the spec's
// own index and names resolve only through the package's consts.
// pkgIota == -2 is package scope outside a const spec (a package var's
// initializer re-derived at a use site): the same package-only name
// resolution, but `iota` is undefined.
//
// typed reports that the folded value came from a declared type's
// const. A bare name's value is exact either way, but operators on a
// typed constant wrap at the declared width, which the bare
// constant.Value cannot express — those refuse to fold so the key
// emits and evaluates with the type's semantics.
func (c *compiler) scopeConstVal(e ast.Expr, depth, pkgIota int) (cv constant.Value, typed bool, ok bool) {
	if depth > 8 {
		return nil, false, false
	}
	if cv, ok := constValue(e); ok {
		return cv, false, true
	}
	defer func() {
		// like constValue: go/constant panics on unsound inputs; those
		// keep runtime semantics.
		if recover() != nil {
			cv, typed, ok = nil, false, false
		}
	}()
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.scopeConstVal(x.X, depth+1, pkgIota)
	case *ast.Ident:
		if pkgIota == -1 {
			if b := c.fs.lookupBinding(x.Name); b != nil && b.constVal != nil {
				return b.constVal, !b.untypedConst, true
			}
		}
		if c.pkg != nil && c.pkg.Index != nil {
			if cd := c.pkg.Index.Consts[x.Name]; cd != nil {
				if vs, ok := cd.Spec.(*ast.ValueSpec); ok {
					var v ast.Expr
					switch {
					case len(vs.Values) == len(vs.Names) && cd.NameIdx < len(vs.Values):
						v = vs.Values[cd.NameIdx]
					case cd.NameIdx < len(cd.Inherited):
						v = cd.Inherited[cd.NameIdx]
					}
					if v != nil {
						cv, vtyped, ok := c.scopeConstVal(v, depth+1, cd.Idx)
						if !ok {
							return nil, false, false
						}
						te := vs.Type
						if te == nil {
							te = cd.InheritedType
						}
						cv = c.roundConstForType(cv, te)
						return cv, vtyped || te != nil, true
					}
				}
			}
		}
		if x.Name == "iota" {
			// iota is a predeclared identifier, not a keyword — a
			// declared iota (`const iota = 10`) shadows it, so the
			// spec index reads only when nothing else resolved the
			// name.
			if pkgIota >= 0 {
				return constant.MakeInt64(int64(pkgIota)), false, true
			}
			if pkgIota == -1 && c.iotaVal >= 0 {
				return constant.MakeInt64(int64(c.iotaVal)), false, true
			}
		}
		return nil, false, false
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, false, false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return nil, false, false
		}
		res := c.askIndexed(ref)
		if res.Kind != semantics.Found && res.Kind != semantics.Deferred {
			return nil, false, false
		}
		p := res.Val
		if p == nil || p.Index == nil {
			return nil, false, false
		}
		cd := p.Index.Consts[x.Sel.Name]
		if cd == nil {
			return nil, false, false
		}
		vs, ok := cd.Spec.(*ast.ValueSpec)
		if !ok {
			return nil, false, false
		}
		var ve ast.Expr
		switch {
		case len(vs.Values) == len(vs.Names) && cd.NameIdx < len(vs.Values):
			ve = vs.Values[cd.NameIdx]
		case cd.NameIdx < len(cd.Inherited):
			ve = cd.Inherited[cd.NameIdx]
		}
		if ve == nil {
			return nil, false, false
		}
		nc := &compiler{pkg: p, file: cd.File, fs: newFScope(nil), iotaVal: -1, strictLazy: c.strictLazy}
		nc.fs.pushBlock()
		cv, vtyped, ok := nc.scopeConstVal(ve, depth+1, cd.Idx)
		if !ok {
			return nil, false, false
		}
		te := vs.Type
		if te == nil {
			te = cd.InheritedType
		}
		cv = nc.roundConstForType(cv, te)
		return cv, vtyped || te != nil, true
	case *ast.UnaryExpr:
		xv, xt, ok := c.scopeConstVal(x.X, depth+1, pkgIota)
		if !ok {
			return nil, false, false
		}
		switch x.Op {
		case token.ADD, token.SUB:
			// Typed constant arithmetic is exact; an overflow is a
			// compile-time error rather than a wrap, so valid programs
			// keep the untyped operation's mathematical result.
			return constant.UnaryOp(x.Op, xv, 0), xt, true
		case token.XOR:
			if xt {
				// `^K` on an unsigned typed constant is width-dependent
				// (`^uint8(0)` is 255), which the bare value cannot
				// express without knowing K's type.
				return nil, false, false
			}
			return constant.UnaryOp(x.Op, xv, 0), false, true
		case token.NOT:
			return constant.UnaryOp(x.Op, xv, 0), xt, true
		}
		return nil, false, false
	case *ast.BinaryExpr:
		lv, lt, ok := c.scopeConstVal(x.X, depth+1, pkgIota)
		if !ok {
			return nil, false, false
		}
		rv, rt, ok := c.scopeConstVal(x.Y, depth+1, pkgIota)
		if !ok {
			return nil, false, false
		}
		switch x.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			// comparisons never round — a typed operand's value
			// compares exactly and the result is an untyped
			// bool constant.
			return constant.MakeBool(constant.Compare(lv, x.Op, rv)), false, true
		case token.LAND, token.LOR:
			lb, rb := constant.BoolVal(lv), constant.BoolVal(rv)
			if x.Op == token.LAND {
				return constant.MakeBool(lb && rb), lt || rt, true
			}
			return constant.MakeBool(lb || rb), lt || rt, true
		}
		typed := lt || rt
		floatOp := lv.Kind() == constant.Float || rv.Kind() == constant.Float
		intOp := lv.Kind() == constant.Int && rv.Kind() == constant.Int
		if typed && !floatOp && !intOp {
			// Typed floating arithmetic is checked step-by-step below;
			// typed integer arithmetic is exact for valid programs
			// because overflow is a compile error, not a wrap. Other
			// typed domains need type details the bare value lacks.
			return nil, false, false
		}
		finish := func(cv constant.Value, typed bool) (constant.Value, bool, bool) {
			if typed && (lv.Kind() == constant.Float || rv.Kind() == constant.Float) {
				// Typed float operations round after every step. Accept
				// only a step exact even at float32 precision; anything
				// else needs the declared type and stays for the runtime
				// constant evaluator rather than risking a wrong fold.
				if _, exact := constant.Float32Val(cv); !exact {
					return nil, false, false
				}
			}
			return cv, typed, true
		}
		switch x.Op {
		case token.SHL, token.SHR:
			if lv.Kind() == constant.Float {
				lv = constant.ToInt(lv)
			}
			if rv.Kind() == constant.Float {
				rv = constant.ToInt(rv)
			}
			s, ok := constant.Uint64Val(rv)
			if !ok {
				return nil, false, false
			}
			return finish(constant.Shift(lv, x.Op, uint(s)), lt)
		case token.QUO:
			if lv.Kind() == constant.Int && rv.Kind() == constant.Int {
				return finish(constant.BinaryOp(lv, token.QUO_ASSIGN, rv), typed)
			}
			return finish(constant.BinaryOp(lv, x.Op, rv), typed)
		default:
			return finish(constant.BinaryOp(lv, x.Op, rv), typed)
		}
	case *ast.CallExpr:
		id, ok := ast.Unparen(x.Fun).(*ast.Ident)
		if !ok {
			return nil, false, false
		}
		switch id.Name {
		case "len", "cap":
			if len(x.Args) != 1 {
				return nil, false, false
			}
			sv, _, ok := c.scopeConstVal(x.Args[0], depth+1, pkgIota)
			if !ok || sv.Kind() != constant.String {
				return nil, false, false
			}
			return constant.MakeInt64(int64(len(constant.StringVal(sv)))), false, true
		case "min", "max":
			if len(x.Args) == 0 {
				return nil, false, false
			}
			op := token.LSS
			if id.Name == "max" {
				op = token.GTR
			}
			var best constant.Value
			var bTyped, floatResult bool
			for _, a := range x.Args {
				av, at, ok := c.scopeConstVal(a, depth+1, pkgIota)
				if !ok {
					return nil, false, false
				}
				bTyped = bTyped || at
				floatResult = floatResult || at && av.Kind() == constant.Float
				if best == nil || constant.Compare(av, op, best) {
					best = av
				}
			}
			// An untyped winner still takes the typed argument's type.
			// Keep it in the float domain so a later `/` does not become
			// integer division; a typed integer winner already has Kind Int.
			if floatResult && best.Kind() == constant.Int {
				f, _ := constant.Float64Val(best)
				best = constant.MakeFloat64(runtime.CanonConstZero(f))
			}
			return best, bTyped, true
		default:
			if len(x.Args) != 1 {
				return nil, false, false
			}
			av, _, ok := c.scopeConstVal(x.Args[0], depth+1, pkgIota)
			if !ok {
				return nil, false, false
			}
			te := c.unfoldTypExpr(x.Fun, 8)
			tid, _ := ast.Unparen(te).(*ast.Ident)
			if tid != nil {
				switch tid.Name {
				case "float32", "float64":
					return c.roundConstForType(av, te), true, true
				case "string":
					if av.Kind() == constant.String {
						return av, true, true
					}
				case "bool":
					if av.Kind() == constant.Bool {
						return av, true, true
					}
				}
			}
			if tid == nil || !constIntConvType(tid.Name) {
				return nil, false, false
			}
			iv := constant.ToInt(av)
			if iv.Kind() == constant.Unknown {
				return nil, false, false
			}
			// Constant conversions cannot wrap: the source value must
			// already be representable in the target integer type.
			return iv, true, true
		}
	}
	return nil, false, false
}

// roundConstForType applies the one narrowing a typed constant's
// declaration implies for array-length evaluation: constants stay exact
// in the source expression, but binding one to a float type rounds it to
// that type's precision. Integer types do not round — an out-of-range
// constant is invalid rather than modular — so they keep the exact
// payload here.
func (c *compiler) roundConstForType(cv constant.Value, te ast.Expr) constant.Value {
	if cv == nil || te == nil {
		return cv
	}
	id, ok := ast.Unparen(c.unfoldTypExpr(te, 8)).(*ast.Ident)
	if !ok {
		return cv
	}
	real := cv
	if cv.Kind() == constant.Complex {
		// A complex constant with a zero imaginary part converts to a
		// real type; a nonzero one is invalid rather than rounded away.
		if constant.Sign(constant.Imag(cv)) != 0 {
			return cv
		}
		real = constant.Real(cv)
	}
	// Float32Val/Float64Val's ok result reports exactness; constant
	// conversion to a float type rounds inexact values instead.
	switch id.Name {
	case "float32":
		f, _ := constant.Float32Val(real)
		return constant.MakeFloat64(runtime.CanonConstZero(float64(f)))
	case "float64":
		f, _ := constant.Float64Val(real)
		return constant.MakeFloat64(runtime.CanonConstZero(f))
	}
	return cv
}

func constIntConvType(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
		return true
	}
	return false
}

func binOpOf(tok token.Token) (bytecode.BinOp, bool) {
	switch tok {
	case token.ADD:
		return bytecode.BinAdd, true
	case token.SUB:
		return bytecode.BinSub, true
	case token.MUL:
		return bytecode.BinMul, true
	case token.QUO:
		return bytecode.BinQuo, true
	case token.REM:
		return bytecode.BinRem, true
	case token.AND:
		return bytecode.BinAnd, true
	case token.OR:
		return bytecode.BinOr, true
	case token.XOR:
		return bytecode.BinXor, true
	case token.AND_NOT:
		return bytecode.BinAndNot, true
	case token.SHL:
		return bytecode.BinShl, true
	case token.SHR:
		return bytecode.BinShr, true
	case token.LAND:
		return bytecode.BinLAnd, true
	case token.LOR:
		return bytecode.BinLOr, true
	case token.EQL:
		return bytecode.BinEql, true
	case token.NEQ:
		return bytecode.BinNeq, true
	case token.LSS:
		return bytecode.BinLss, true
	case token.LEQ:
		return bytecode.BinLeq, true
	case token.GTR:
		return bytecode.BinGtr, true
	case token.GEQ:
		return bytecode.BinGeq, true
	case token.ADD_ASSIGN:
		return bytecode.BinAdd, true
	case token.SUB_ASSIGN:
		return bytecode.BinSub, true
	case token.MUL_ASSIGN:
		return bytecode.BinMul, true
	case token.QUO_ASSIGN:
		return bytecode.BinQuo, true
	case token.REM_ASSIGN:
		return bytecode.BinRem, true
	case token.AND_ASSIGN:
		return bytecode.BinAnd, true
	case token.OR_ASSIGN:
		return bytecode.BinOr, true
	case token.XOR_ASSIGN:
		return bytecode.BinXor, true
	case token.SHL_ASSIGN:
		return bytecode.BinShl, true
	case token.SHR_ASSIGN:
		return bytecode.BinShr, true
	case token.AND_NOT_ASSIGN:
		return bytecode.BinAndNot, true
	}
	return 0, false
}
