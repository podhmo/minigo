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
	"go/token"
	"sort"
	"strconv"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/syntax"
)

// paramCoerce is a slot + declared-type pair for the function prologue.
type paramCoerce struct {
	slot int
	typ  ast.Expr
}

// emitParamCoerces emits typeExpr+OpCoerce for each declared parameter.
// The coerce carries the parameter type's position so a failed coercion
// points at the signature, not nowhere.
func (c *compiler) emitParamCoerces(pcs []paramCoerce) {
	for _, pc := range pcs {
		c.emitSigTypeCoerce(pc.slot, pc.typ, pc.typ.Pos())
	}
}

// sigTypeExpr emits typeExpr for a type written in the function's
// signature. Go resolves those in the enclosing scope, so the function's
// own names are hidden meanwhile: a parameter named like an imported
// package (`func next(token token.Token)`) must not shadow the package
// when the parameter, a named result or a `return` coerces to it.
func (c *compiler) sigTypeExpr(t ast.Expr) {
	saved := c.fs.blocks
	hidden := make([]map[string]*binding, len(saved))
	for i := range hidden {
		hidden[i] = map[string]*binding{}
	}
	c.fs.blocks = hidden
	defer func() { c.fs.blocks = saved }()
	c.typeExpr(t)
}

// emitSigTypeCoerce is emitTypeCoerce for a signature type.
func (c *compiler) emitSigTypeCoerce(slot int, t ast.Expr, pos token.Pos) {
	if t == nil {
		return
	}
	c.sigTypeExpr(t)
	c.emit(bytecode.OpCoerce, slot, 0, pos)
}

// emitTypeCoerce emits typeExpr(t) + OpCoerce(slot). The type expr keeps
// the compiler total: an unresolvable type name traps only when reached.
func (c *compiler) emitTypeCoerce(slot int, t ast.Expr, pos token.Pos) {
	if t == nil {
		return
	}
	c.typeExpr(t)
	c.emit(bytecode.OpCoerce, slot, 0, pos)
}

func countResults(fl *ast.FieldList) int {
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

// conversionCall reports whether the call's callee names a type, making
// the CallExpr a conversion — a transparent non-call operation for
// argument evaluation order (T(x)'s own computation defers like an
// index or arithmetic op, unlike a real call).
func (c *compiler) conversionCall(x *ast.CallExpr) bool {
	if c.isTypeForm(x.Fun) {
		return true // []T(x), *T(x), struct{...}(x) — syntactic type forms
	}
	switch f := x.Fun.(type) {
	case *ast.Ident:
		return c.isTypeName(f.Name)
	case *ast.SelectorExpr:
		// pkg.T(x) is a conversion when pkg is an import and T names a
		// type in it; pkg.fn(x) stays a real call.
		if ref, name, ok := c.selTypRef(f); ok {
			return c.selIsType(ref, name)
		}
	case *ast.IndexExpr:
		// T[Args](x) is a conversion only when T names a generic type;
		// a generic function f[T](x) is a real call.
		if id, ok := f.X.(*ast.Ident); ok {
			return c.isTypeName(id.Name)
		}
		if ref, name, ok := c.selTypRef(f.X); ok {
			return c.selIsType(ref, name)
		}
	case *ast.IndexListExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return c.isTypeName(id.Name)
		}
		if ref, name, ok := c.selTypRef(f.X); ok {
			return c.selIsType(ref, name)
		}
	}
	return false
}

// hoistedArgCalls lists the operations inside an argument that Go
// materializes eagerly, in lexical order: non-conversion CallExpr nodes,
// channel receives, slice expressions, type assertions, and map literals
// (gc's order pass lowers these to temporaries during its left-to-right
// operand walk — an out-of-range s[i:j] panics before a later argument's
// call runs). Conversions and index/deref/arithmetic stay deferred (their
// eager sub-expressions still hoist — index can panic but gc schedules it
// at the call site, not at a temp). Only the outermost op of a nested
// chain is listed — its own operands already evaluate left-to-right, so
// descending would double-evaluate them. Returns ok=false when the
// argument contains conditional call sites (&&/||) or other shapes
// hoisting would reorder; the caller then falls back to plain sequential
// evaluation.
func (c *compiler) hoistedArgCalls(e ast.Expr) (calls []ast.Expr, ok bool) {
	ok = true
	ast.Inspect(e, func(n ast.Node) bool {
		if !ok || n == nil {
			return false
		}
		switch t := n.(type) {
		case *ast.FuncLit:
			return false // body calls run at invocation, not arg time
		case *ast.BinaryExpr:
			if t.Op == token.LAND || t.Op == token.LOR {
				ok = false
				return false
			}
		case *ast.CallExpr:
			if c.isSpecialCall(t) {
				// a registered special form evaluates at its
				// position like any call — hoist it eagerly, but
				// never descend: its args stay quoted for the
				// handler.
				calls = append(calls, t)
				return false
			}
			if c.isConstExpr(t) {
				// a constant call (`len("abc")`, `max(T, 3)`,
				// `unsafe.Sizeof(x)`) evaluates nothing; it stays
				// inline so a shift still sees its count as constant.
				// Checked first: under IndexedOnly `unsafe.Sizeof`
				// also reads as an undecidable member call, and its
				// operand must never evaluate.
				return false
			}
			if _, undecidable := c.undecidableMemberCall(t); undecidable {
				// conversion or call is a deferred question — the call
				// itself stays inline like a conversion so hoistEagerOps
				// can emit the member-shape branch, but its interior
				// eager ops still hoist (a conversion's interior calls
				// do, and the branch's arg slots read the scratches).
				return true
			}
			if c.conversionCall(t) {
				return true // transparent op — descend into its args
			}
			calls = append(calls, t)
			return false
		case *ast.UnaryExpr:
			if t.Op == token.ARROW {
				calls = append(calls, t) // receive: ordered like a call
				return false
			}
		case *ast.SliceExpr:
			calls = append(calls, t) // bounds materialize eagerly
			return false
		case *ast.TypeAssertExpr:
			calls = append(calls, t) // assertions materialize eagerly
			return false
		case *ast.CompositeLit:
			if c.mapLitType(t.Type, 8) {
				// a map literal builds eagerly entry-by-entry; struct,
				// array and slice literals stay inline like their
				// element expressions.
				calls = append(calls, t)
				return false
			}
		}
		return true
	})
	return calls, ok
}

// hoistEagerOps runs Go's two-phase evaluation over an expression
// list (call arguments, binary operands): the eager operations inside
// each expr (hoistedArgCalls) materialize into scratch locals first in
// lexical order across the whole list, and the returned exprs rewrite
// each hoisted node as its scratch-slot load via substCallArg. prefix
// names the scratch locals. ok is false when a conditional call site
// makes hoisting unsafe, when there is nothing to hoist, or when
// hoisting would not reorder anything — the caller then picks its own
// fallback order (callArgs runs plain sequential, binaryOperands tries
// the pure-operand defer).
func (c *compiler) hoistEagerOps(es []ast.Expr, prefix string) (subs []ast.Expr, ok bool) {
	// inOrder stays true while every expr up to the last one holding an
	// eager op is that op alone (`f(x) + g(y)`, `h(f(), g())`): the
	// sequential walk then runs the same order the two phases would, so
	// the scratch locals (a cell per slot per frame) are skipped.
	inOrder, sawDeferred := true, false
	var work []ast.Expr
	for _, e := range es {
		ec, eok := c.hoistedArgCalls(e)
		if !eok {
			return nil, false
		}
		ucs := c.undecidableCalls(e)
		var eff []ast.Expr
		eff = append(eff, ec...)
		for _, uc := range ucs {
			eff = append(eff, uc)
		}
		sort.SliceStable(eff, func(i, j int) bool { return eff[i].Pos() < eff[j].Pos() })
		switch {
		case len(eff) == 0:
			sawDeferred = true
		case sawDeferred || len(eff) > 1 || eff[0] != ast.Unparen(e):
			inOrder = false
		}
		if len(ucs) > 0 {
			// a deferred member call is half conversion — its operand
			// side defers — so a later arg's eager op must hoist:
			// `take(p.T(x), bump())` reads x after bump like normal.
			sawDeferred = true
		}
		work = append(work, eff...)
	}
	if len(work) == 0 || inOrder {
		return nil, false
	}
	// eager ops and undecidable member calls interleave in lexical
	// order — a member load belongs to phase 1 for the call outcome —
	// keyed so an undecidable call follows the interior ops hoisted out
	// of its own args (its emission references their scratch slots).
	wkey := func(x ast.Expr) token.Pos {
		if uc, ok := x.(*ast.CallExpr); ok {
			if _, isUC := c.undecidableMemberCall(uc); isUC {
				return uc.End()
			}
		}
		return x.Pos()
	}
	sort.SliceStable(work, func(i, j int) bool { return wkey(work[i]) < wkey(work[j]) })
	// Phase 1: evaluate each hoisted op into a scratch local, in order.
	// An interface-typed hoisted expr marks its scratch name like a :=
	// bind does, so a substituted == operand still picks pair equality.
	names := map[ast.Expr]string{}
	for _, call := range work {
		uc, _ := call.(*ast.CallExpr)
		if sel, isUC := c.undecidableMemberCall(uc); isUC {
			// `p.T(x)` with the member question open: load the
			// member eagerly (cheap — no user side effect), run the
			// call now only when it is not a *TypeDef, and let the
			// operand-position ident pick the conversion path when
			// it is — a conversion's args defer like always. Args
			// read their hoisted scratches on both paths.
			c.expr(sel)
			cname := c.fresh("$cm")
			c.emit(bytecode.OpNewLocal, c.fs.declare(cname, sel.Pos()), 0, sel.Pos())
			rname := c.fresh("$cv")
			rslot := c.fs.declare(rname, call.Pos())
			subArgs := make([]ast.Expr, len(uc.Args))
			for i, a := range uc.Args {
				subArgs[i] = substCallArg(a, names)
			}
			c.emit(bytecode.OpLocal, c.fs.lookupBinding(cname).slot, 0, call.Pos())
			jT := c.emit(bytecode.OpJumpIfTypedef, 0, 0, call.Pos())
			synth := &ast.CallExpr{Fun: &ast.Ident{Name: cname, NamePos: uc.Fun.Pos()}, Args: subArgs, Lparen: uc.Lparen, Ellipsis: uc.Ellipsis, Rparen: uc.Rparen}
			if callSpread(uc) == 2 && callSpread(synth) != 2 {
				// `p.F(pair())` — the sole call arg spreads its
				// result tuple into F's params; after substitution
				// the arg reads as an Ident, so emit the call with
				// the source call's flag.
				c.emit(bytecode.OpLocal, c.fs.lookupBinding(cname).slot, 0, call.Pos())
				for _, a := range subArgs {
					c.expr(a)
				}
				c.argStatics(subArgs)
				c.emit(bytecode.OpCall, len(uc.Args), 2, call.Pos())
			} else {
				c.expr(synth)
			}
			c.emit(bytecode.OpNewLocal, rslot, 0, call.Pos())
			c.patchA(jT, len(c.ch.Code))
			if c.deferCallTemps == nil {
				c.deferCallTemps = map[string]*deferredCallTemp{}
			}
			c.deferCallTemps[rname] = &deferredCallTemp{callee: cname, res: rname, call: uc, args: subArgs}
			names[call] = rname
			continue
		}
		c.expr(call)
		name := c.fresh(prefix)
		slot := c.fs.declare(name, call.Pos())
		if c.isIfaceExpr(call) {
			c.fs.markIface(name)
		}
		// the assignability gate keeps the hoisted call's static type:
		// `d < len(x)` compares Duration to int through the scratch name.
		// A deferred result type (`p.T(v)` under IndexedOnly, where the
		// conversion question is open) stays deferred through the temp.
		if t := c.staticOpTyp(call); t.deferred {
			c.fs.noteDeferTyp(name, t.dexpr)
		} else if t.te != nil {
			c.fs.noteDeclTyp(name, c.declTypCanon(t.te), t.teFile)
		}
		c.emit(bytecode.OpNewLocal, slot, 0, call.Pos())
		names[call] = name
	}
	// Phase 2: swap each hoisted node for its scratch-slot load.
	subs = make([]ast.Expr, len(es))
	for i, e := range es {
		subs[i] = substCallArg(e, names)
	}
	return subs, true
}

// undecidableCalls collects the package-qualified calls whose member
// question is still open — the conversion-vs-call branch hoistEagerOps
// emits for each. Nodes a phase-1 emission would evaluate wholesale
// (eager calls, receives, slices, asserts, map lits, function literals
// and conditional operators) keep their interiors: an undecidable call
// nested inside one already evaluates with it.
func (c *compiler) undecidableCalls(e ast.Expr) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(e, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.FuncLit:
			return false // body calls run at invocation, not arg time
		case *ast.BinaryExpr:
			if t.Op == token.LAND || t.Op == token.LOR {
				return false
			}
		case *ast.CallExpr:
			if c.isSpecialCall(t) {
				// a registered special form is never undecidable —
				// the registry answers it — and its quoted args run
				// inside the handler, so neither collect nor descend.
				return false
			}
			if c.isConstExpr(t) {
				// constant calls evaluate nothing (`unsafe.Sizeof`,
				// `len("abc")`) — under IndexedOnly `unsafe.Sizeof`
				// also reads as undecidable; its operand must never
				// evaluate, so neither collect nor descend.
				return false
			}
			if _, isUC := c.undecidableMemberCall(t); isUC {
				out = append(out, t)
				// its own args may hold a nested undecidable call —
				// `p.Min(float64(q.Bump(&x)), read())` — each gets
				// its own member-shape branch (keyed before the
				// parent's by End()). Eager-op interiors inside the
				// args still stay out, by the arms below.
				return true
			}
			if c.conversionCall(t) {
				return true // transparent op — descend into its args
			}
			return false // an eager op — its interior evaluates with it
		case *ast.UnaryExpr:
			if t.Op == token.ARROW {
				return false
			}
		case *ast.SliceExpr, *ast.TypeAssertExpr:
			return false
		case *ast.CompositeLit:
			if c.mapLitType(t.Type, 8) {
				return false
			}
		}
		return true
	})
	return out
}

// emitDeferredTypOperand pushes the resolved typedef of a deferred
// operand for unsafe.Sizeof/Alignof: nil for the value slot, then the
// typedef the OpDeferredValueTyp hook resolves at run time — a member
// conversion's target, a member function's declared result, an
// untyped const's default.
func (c *compiler) emitDeferredTypOperand(operand ast.Expr, pos token.Pos) {
	// a nil value + the operand's typedef, resolved at run time from
	// the site — the DeferredValueTyp hook reads a member's declared
	// signature for `p.F(v)`, so the call itself never evaluates
	// (unsafe.Sizeof folds statically under gc; running p.F's args
	// would invent side effects gc never has).
	c.emit(bytecode.OpNil, 0, 0, pos)
	c.emit(bytecode.OpDeferredValueTyp, c.exprSite(operand, pos), 0, pos)
}

// deferredCallTemp ties a hoist scratch name to an undecidable member
// call: the callee slot holds the loaded member, res holds the phase-1
// call result when the member was not a TypeDef.
type deferredCallTemp struct {
	callee string
	res    string
	call   *ast.CallExpr
	// args is call.Args with hoisted interior ops rewritten to their
	// scratch idents — the phase-2 conversion path evaluates exactly
	// the arguments phase 1 would have.
	args []ast.Expr
}

// undecidableMemberCall reports whether a CallExpr's callee is a
// package-qualified name whose member question is still open — under
// IndexedOnly `p.T(v)` could be a conversion (a transparent op whose
// args defer to the operand position) or a real call (eager).
func (c *compiler) undecidableMemberCall(x *ast.CallExpr) (*ast.SelectorExpr, bool) {
	if x == nil {
		return nil, false
	}
	// a registered special form's dispatch is decided by the
	// registry, not the package member — the question is answered.
	if c.isSpecialCall(x) {
		return nil, false
	}
	sel, ok := ast.Unparen(x.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	// a local binding shadows the import name — `inner := …;
	// inner.F()` selects the local's member, so there is no package
	// member question to defer.
	if id, ok := sel.X.(*ast.Ident); ok && c.fs.lookupBinding(id.Name) != nil {
		return nil, false
	}
	if ref, _, ok := c.selTypRef(sel); !ok {
		return nil, false
	} else if c.askIndexed(ref).Kind == semantics.Found {
		return nil, false
	}
	return sel, true
}

// deferredCallOperand emits the operand position of an undecidable
// member call: when the pre-loaded member is a TypeDef the original
// call runs here as a conversion (args evaluated now, like normal
// mode's deferred conversion); otherwise the phase-1 call already
// produced the result and this is a plain slot read.
func (c *compiler) deferredCallOperand(dc *deferredCallTemp, pos token.Pos) {
	call := dc.call
	c.emit(bytecode.OpLocal, c.fs.lookupBinding(dc.callee).slot, 0, pos)
	jConv := c.emit(bytecode.OpJumpIfTypedef, 0, 0, pos)
	// not a TypeDef — the phase-1 call ran; load its result.
	c.getRef(dc.res, pos)
	jEnd := c.emit(bytecode.OpJump, 0, 0, pos)
	c.patchA(jConv, len(c.ch.Code))
	c.expr(&ast.CallExpr{Fun: &ast.Ident{Name: dc.callee, NamePos: call.Fun.Pos()}, Args: dc.args, Lparen: call.Lparen, Ellipsis: call.Ellipsis, Rparen: call.Rparen})
	c.patchA(jEnd, len(c.ch.Code))
}

// substCallArg rewrites the nodes of e listed in subs (hoisted calls)
// as references to their scratch slots. Nodes without hoisted
// descendants are shared, not copied.
func substCallArg(e ast.Expr, subs map[ast.Expr]string) ast.Expr {
	if name, ok := subs[e]; ok {
		return &ast.Ident{Name: name, NamePos: e.Pos()}
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		nx := substCallArg(x.X, subs)
		if nx == x.X {
			return e
		}
		return &ast.ParenExpr{Lparen: x.Lparen, X: nx, Rparen: x.Rparen}
	case *ast.BinaryExpr:
		nx, ny := substCallArg(x.X, subs), substCallArg(x.Y, subs)
		if nx == x.X && ny == x.Y {
			return e
		}
		return &ast.BinaryExpr{X: nx, OpPos: x.OpPos, Op: x.Op, Y: ny}
	case *ast.UnaryExpr:
		nx := substCallArg(x.X, subs)
		if nx == x.X {
			return e
		}
		return &ast.UnaryExpr{OpPos: x.OpPos, Op: x.Op, X: nx}
	case *ast.StarExpr:
		nx := substCallArg(x.X, subs)
		if nx == x.X {
			return e
		}
		return &ast.StarExpr{Star: x.Star, X: nx}
	case *ast.SelectorExpr:
		nx := substCallArg(x.X, subs)
		if nx == x.X {
			return e
		}
		return &ast.SelectorExpr{X: nx, Sel: x.Sel}
	case *ast.IndexExpr:
		nx, ni := substCallArg(x.X, subs), substCallArg(x.Index, subs)
		if nx == x.X && ni == x.Index {
			return e
		}
		return &ast.IndexExpr{X: nx, Lbrack: x.Lbrack, Index: ni, Rbrack: x.Rbrack}
	case *ast.IndexListExpr:
		nx := substCallArg(x.X, subs)
		changed := nx != x.X
		var inds []ast.Expr
		for _, i := range x.Indices {
			ni := substCallArg(i, subs)
			changed = changed || ni != i
			inds = append(inds, ni)
		}
		if !changed {
			return e
		}
		return &ast.IndexListExpr{X: nx, Lbrack: x.Lbrack, Indices: inds, Rbrack: x.Rbrack}
	case *ast.SliceExpr:
		nx := substCallArg(x.X, subs)
		changed := nx != x.X
		var low, high, max ast.Expr
		if x.Low != nil {
			low = substCallArg(x.Low, subs)
			changed = changed || low != x.Low
		}
		if x.High != nil {
			high = substCallArg(x.High, subs)
			changed = changed || high != x.High
		}
		if x.Max != nil {
			max = substCallArg(x.Max, subs)
			changed = changed || max != x.Max
		}
		if !changed {
			return e
		}
		return &ast.SliceExpr{X: nx, Lbrack: x.Lbrack, Low: low, High: high, Max: max, Slice3: x.Slice3, Rbrack: x.Rbrack}
	case *ast.TypeAssertExpr:
		nx := substCallArg(x.X, subs)
		if nx == x.X {
			return e
		}
		return &ast.TypeAssertExpr{X: nx, Lparen: x.Lparen, Type: x.Type, Rparen: x.Rparen}
	case *ast.CallExpr:
		// only transparent conversion calls reach here — real calls were
		// hoisted and substituted at the top of this walk.
		changed := false
		var args []ast.Expr
		for _, a := range x.Args {
			na := substCallArg(a, subs)
			changed = changed || na != a
			args = append(args, na)
		}
		if !changed {
			return e
		}
		return &ast.CallExpr{Fun: x.Fun, Lparen: x.Lparen, Args: args, Ellipsis: x.Ellipsis, Rparen: x.Rparen}
	case *ast.CompositeLit:
		changed := false
		var elts []ast.Expr
		for _, el := range x.Elts {
			ne := substCallArg(el, subs)
			changed = changed || ne != el
			elts = append(elts, ne)
		}
		if !changed {
			return e
		}
		return &ast.CompositeLit{Type: x.Type, Lbrace: x.Lbrace, Elts: elts, Rbrace: x.Rbrace, Incomplete: x.Incomplete}
	case *ast.KeyValueExpr:
		nk, nv := substCallArg(x.Key, subs), substCallArg(x.Value, subs)
		if nk == x.Key && nv == x.Value {
			return e
		}
		return &ast.KeyValueExpr{Key: nk, Colon: x.Colon, Value: nv}
	}
	return e
}

// callArgs emits call arguments in Go's two-phase order: the eager
// operations inside the arguments (calls, channel receives, slices,
// assertions, map literals — hoistedArgCalls) evaluate first in lexical
// order across the whole argument list, and each argument's deferred
// computation — index/deref/arithmetic/conversion — materializes
// afterwards in argument order. Go schedules s[i]'s bounds check past a
// later argument's call — fmt.Sprintf("%d", s[10], f()) reports f's
// panic — but a slice's bounds check happens eagerly:
// fmt.Sprintf("%d", s[10:0], f()) reports the slice's panic.
func (c *compiler) callArgs(args []ast.Expr) {
	if subs, ok := c.hoistEagerOps(args, "$arg"); ok {
		for _, a := range subs {
			c.expr(a)
		}
		return
	}
	for _, a := range args {
		c.expr(a)
	}
}

func (c *compiler) call(x *ast.CallExpr) {
	// F[...] is explicit generic instantiation — unwrap to reach a
	// special form like define.Convert[Dst, Src](...); the type args
	// carry no meaning for the quoted handler.
	fun := x.Fun
	for {
		switch ix := fun.(type) {
		case *ast.IndexExpr:
			fun = ix.X
		case *ast.IndexListExpr:
			fun = ix.X
		case *ast.ParenExpr:
			fun = ix.X
		default:
			goto unwrapped
		}
	}
unwrapped:
	// unsafe.Offsetof(s.f): the selector operand only yields the
	// field's value — the field's identity, which is what the offset
	// needs, is lost. Emit the base and the field name instead so the
	// intrinsic can measure the offset against the base's declared
	// field order. Only a simple selector rewrites: nested
	// `unsafe.Offsetof(s.f.g)` keeps the one-argument form and the
	// intrinsic reports its unsupported shape.
	if sel, ok := fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Offsetof" && len(x.Args) == 1 {
		if id, ok2 := sel.X.(*ast.Ident); ok2 {
			if ref, ok3 := c.importRef(id.Name); ok3 && ref.Path == "unsafe" {
				if fs, ok4 := x.Args[0].(*ast.SelectorExpr); ok4 {
					name := &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(fs.Sel.Name), ValuePos: fs.Sel.Pos()}
					c.calleeExpr(x.Fun)
					// the base never evaluates — a nil *Pair still
					// offsets; its declared typedef describes the
					// struct layout. An opaque base falls back to the
					// evaluated value.
					var bte ast.Expr
					var bteFile *syntax.File
					if ot := c.staticOpTyp(fs.X); ot.te != nil {
						bte = ot.te
						bteFile = ot.teFile
					}
					if bte != nil {
						c.pkgScopeTyp = bteFile
						c.typeExpr(bte)
						c.pkgScopeTyp = nil
					} else {
						c.expr(fs.X)
					}
					c.expr(name)
					if bte != nil {
						c.pkgScopeTyp = bteFile
						c.typeExpr(bte)
						c.pkgScopeTyp = nil
					} else {
						c.staticTyp(fs.X)
					}
					c.emit(bytecode.OpNil, 0, 0, x.Pos())
					c.emit(bytecode.OpCall, 2, 0, x.Pos())
					return
				}
			}
		}
	}
	// unsafe.Sizeof(x)/unsafe.Alignof(x): the answer comes from the
	// operand's DECLARED type — `var x any = int64(1)` measures the
	// interface pair (16), not the stored int64; `p` measures the
	// pointer (8). The stored value cannot always recover it
	// (interface slots arrive unboxed), so the operand's declared
	// typedef rides along as a second argument — nil when the
	// operand has no static type. An indexed operand `c[i]` passes
	// its container's declared typedef as a third argument so the
	// intrinsic sizes the element type.
	if sel, ok := fun.(*ast.SelectorExpr); ok &&
		(sel.Sel.Name == "Sizeof" || sel.Sel.Name == "Alignof") &&
		len(x.Args) == 1 {
		if id, ok2 := sel.X.(*ast.Ident); ok2 {
			if ref, ok3 := c.importRef(id.Name); ok3 && ref.Path == "unsafe" {
				operand := x.Args[0]
				for {
					if p, ok4 := operand.(*ast.ParenExpr); ok4 {
						operand = p.X
						continue
					}
					break
				}
				c.calleeExpr(x.Fun)
				ot := c.staticOpTyp(operand)
				var te ast.Expr
				switch {
				case ot.te != nil:
					te = ot.te
				case ot.untyped:
					// a literal's size is its default type's
					// (`unsafe.Sizeof(1)` measures int).
					te = ot.dte
				case ot.iface:
					te = &ast.InterfaceType{Interface: x.Pos(), Methods: &ast.FieldList{}}
				case ot.logical:
					// `!x`, `x && y`, `x || y` with an opaque operand
					// is SOME bool-flavoured type — every bool type
					// has bool's size and alignment, so the intrinsic
					// answers from bool's typedef and the operand is
					// never evaluated (gc folds it statically).
					te = &ast.Ident{Name: "bool"}
				}
				if te != nil {
					// gc folds Sizeof/Alignof statically — the operand
					// never evaluates; a nil stands in for its value
					// and the resolved typedef answers the size.
					c.emit(bytecode.OpNil, 0, 0, x.Pos())
					if cl := ot.elLit; cl != nil {
						c.emitEllipsisLenFold(cl, te, ot.elPos, ot.elFile)
					} else {
						c.pkgScopeTyp = ot.teFile
						c.typeExpr(te)
						c.pkgScopeTyp = nil
					}
				} else if ot.deferred {
					// the operand's type is a deferred fact
					// (`p.T(v)` under IndexedOnly): Sizeof answers
					// from the resolved typedef at run time.
					c.emitDeferredTypOperand(operand, x.Pos())
				} else {
					// the operand's type is statically opaque (a host
					// func's result, a reflect value) — evaluate it
					// and let the intrinsic size the boxed shape.
					c.expr(x.Args[0])
					c.staticTyp(operand)
				}
				if ix, ok4 := operand.(*ast.IndexExpr); ok4 {
					c.staticTyp(ix.X)
				} else {
					c.emit(bytecode.OpNil, 0, 0, x.Pos())
				}
				if te != nil {
					c.pkgScopeTyp = ot.teFile
					c.typeExpr(te)
					c.pkgScopeTyp = nil
				} else if ot.deferred {
					c.emit(bytecode.OpDeferredValueTyp, c.exprSite(operand, x.Pos()), 0, x.Pos())
				} else {
					c.staticTyp(operand)
				}
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
				c.emit(bytecode.OpCall, 3, 0, x.Pos())
				return
			}
		}
	}
	if sel, ok := fun.(*ast.SelectorExpr); ok && c.trySpecial(x, sel) {
		return
	}
	// len(x[i]) / cap(x[i]): when x's element type is an array the call
	// folds to a constant — Go never evaluates the index. Emit the
	// base, then OpLenIdxFold skips the emitted index+OpIndex+OpCall
	// run when the runtime element typedef turns out to be an array.
	// Two guards keep the fold honest: a user declaration of len/cap
	// wins (Go calls it like any function), and any call or receive in
	// the operand — the index or the indexed base alike — still
	// evaluates (Go folds only when nothing in the operand calls out).
	if id, ok := fun.(*ast.Ident); ok && (id.Name == "len" || id.Name == "cap") &&
		len(x.Args) == 1 && !x.Ellipsis.IsValid() && !c.declared(id.Name) {
		if ix, ok := x.Args[0].(*ast.IndexExpr); ok {
			calls, linear := c.hoistedArgCalls(ix)
			if linear && len(calls) == 0 {
				c.calleeExpr(x.Fun)
				c.expr(ix.X)
				jm := c.emit(bytecode.OpLenIdxFold, 0, 0, x.Pos())
				c.expr(ix.Index)
				c.emit(bytecode.OpIndex, 0, 0, ix.Lbrack)
				c.emit(bytecode.OpNil, 0, 0, x.Pos())
				c.emit(bytecode.OpCall, 1, 0, x.Pos())
				c.patchA(jm, len(c.ch.Code))
				return
			}
		}
		// len(*p) / cap(*p): the call is a constant when p's type is
		// *[N]T — Go never evaluates the dereference (only operands with
		// calls or receives evaluate). Two OpLenDerefFold probes mirror
		// the index fold: the declared typedef tier never evaluates the
		// operand at all, the pointer-value tier still reads p but skips
		// the deref (covering nil pointers the static tier can't type).
		// An operand that calls out — `len(*f())` — evaluates and derefs
		// like any expression, so the nil panic stays faithful.
		if st, ok := x.Args[0].(*ast.StarExpr); ok && !c.lenOperandCalls(st) {
			c.calleeExpr(x.Fun)
			c.lenDerefStaticTyp(st.X)
			jm0 := c.emit(bytecode.OpLenDerefFold, 0, 0, x.Pos())
			c.expr(st.X)
			jm1 := c.emit(bytecode.OpLenDerefFold, 0, 1, x.Pos())
			c.emit(bytecode.OpDeref, 0, 0, st.Pos())
			c.emit(bytecode.OpNil, 0, 0, x.Pos())
			c.emit(bytecode.OpCall, 1, 0, x.Pos())
			c.patchA(jm0, len(c.ch.Code))
			c.patchA(jm1, len(c.ch.Code))
			return
		}
		// every other operand: emit the resolved callee and the operand,
		// then OpLenCallFold — a hit (the callee still is the predeclared
		// builtin) runs it without the OpCall machinery and skips the
		// emitted static+call run.
		c.calleeExpr(x.Fun)
		c.callArgs(x.Args)
		b := 0
		if id.Name == "cap" {
			b = 1
		}
		jm := c.emit(bytecode.OpLenCallFold, 0, b, x.Pos())
		c.staticTyp(x.Args[0])
		c.emit(bytecode.OpCall, 1, callSpread(x), x.Pos())
		c.patchA(jm, len(c.ch.Code))
		return
	}
	c.calleeExpr(x.Fun)
	newCall := isNewCall(x)
	args := x.Args
	// make(T, ...) takes a type as first argument; new(T) also
	// accepts an arbitrary expression (Go 1.26) — only a syntactic
	// type form compiles as a type, anything else evaluates to a
	// value and the builtin distinguishes a typedef argument from
	// a value at run time.
	if len(args) > 0 && (isTypePositionCall(x) || (newCall && c.isTypeForm(args[0]))) {
		c.typeExpr(args[0])
		args = args[1:]
	}
	c.callArgs(args)
	c.argStatics(x.Args)
	c.emit(bytecode.OpCall, len(x.Args), callSpread(x), x.Pos())
}

// lenOperandCalls reports whether a len/cap operand forces evaluation
// in Go — only a non-conversion function call or a channel receive
// does. Assertions, slices, indexing and literals stay constant, so
// `len(*x.(T))` never fires the assert and `len(*s[:][i])` never
// evaluates the slice.
func (c *compiler) lenOperandCalls(e ast.Expr) bool {
	calls := false
	ast.Inspect(e, func(n ast.Node) bool {
		if calls || n == nil {
			return false
		}
		switch t := n.(type) {
		case *ast.FuncLit:
			return false // body calls run at invocation, not operand time
		case *ast.CallExpr:
			if c.conversionCall(t) {
				return true // transparent — descend into its args
			}
			calls = true
			return false
		case *ast.UnaryExpr:
			if t.Op == token.ARROW {
				calls = true
				return false
			}
		}
		return true
	})
	return calls
}

// lenDerefStaticTyp pushes the declared typedef of a len/cap `*x`
// operand for OpLenDerefFold's static tier — an ident's declared type
// like staticTyp, plus the element typedef of an indexed base so
// `len(*ps[i])`/`len(*m[k])` fold without evaluating the index either.
// Anything else pushes NIL and the run-time tier decides.
func (c *compiler) lenDerefStaticTyp(e ast.Expr) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		c.lenDerefStaticTyp(x.X)
	case *ast.IndexExpr:
		c.lenDerefStaticTyp(x.X)
		c.emit(bytecode.OpElemTypeOrNil, 0, 0, e.Pos())
	case *ast.SliceExpr:
		c.lenDerefStaticTyp(x.X) // s[lo:hi] keeps s's element type
	case *ast.TypeAssertExpr:
		if x.Type != nil {
			c.typeExpr(x.Type) // x.(T) has static type T
		} else {
			c.emit(bytecode.OpNil, 0, 0, e.Pos())
		}
	default:
		c.staticTyp(e)
	}
}

// argStatics pushes each argument's declared typedef (or nil) so generic
// calls unify type arguments against the argument's static type — Go's
// inference binds T to the declared type, not the value's dynamic one:
// `Describe(v)` with `var v Shape` binds T=Shape even while v holds a
// *Tri, and `id(e)` with `var e error` binds T=error. One typedef is
// pushed per source argument, above the argument values, so OpCall's
// popArgs pops them alongside the args; a trailing `xs...` source's
// static is the slice type and is dropped in favour of the element
// typedef popArgs already carries as spreadTd.
func (c *compiler) argStatics(args []ast.Expr) {
	for _, a := range args {
		c.staticTyp(a)
	}
}

// callSpread reports the OpCall B flag for the argument list: 1
// spreads a trailing `x...`; 2 marks a lone call argument whose
// result tuple spreads into the callee's params (`swap(swap(a, b))`
// — the only multi-value spread Go allows).
func callSpread(x *ast.CallExpr) int {
	if x.Ellipsis.IsValid() {
		return 1
	}
	if len(x.Args) == 1 {
		if _, ok := ast.Unparen(x.Args[0]).(*ast.CallExpr); ok {
			return 2
		}
	}
	return 0
}

// selectorBase emits the operand of a `.Sel` select — shared by OpSelect
// and the callee-position OpSelectCall.
func (c *compiler) selectorBase(x ast.Expr) {
	// `s[i].M()` is `(&s[i]).M()` — Go's selector lowering hands the
	// element's storage to a pointer receiver. Emit the element's ref
	// (a tolerated map element resolves to its copy at select time) so
	// the write lands; a type-form operand (F[T].M) stays an
	// instantiation.
	if ix, ok := indexOperand(x); ok && !c.isTypeForm(ix.Index) && c.refableIndexBase(ix.X) {
		c.refTargetBase(ix.X, false)
		c.expr(ix.Index)
		c.emit(bytecode.OpIndexRef, 0, 1, ix.Lbrack)
	} else if c.selectorBaseIsVar(x) {
		// `x.M()` lowers to `(&x).M()` when M needs a pointer — a
		// scalar named value is a detached copy otherwise, so the
		// receiver binds the var's storage ref for the write to land.
		c.refTargetBase(x, false)
	} else {
		c.expr(x)
	}
}

// calleeExpr compiles the called expression; a syntactic type form means a
// conversion call T(x).
func (c *compiler) calleeExpr(fun ast.Expr) {
	if c.isTypeForm(fun) {
		c.typeExpr(fun)
		return
	}
	if t, ok := fun.(*ast.SelectorExpr); ok {
		// a callee-position select on a function-local name binds for
		// the direct call: a value method on a nil *T in an interface
		// panics as a nil dereference (gc devirtualizes the call it can
		// see — local vars, params, upvalues), while any other receiver
		// shape — a package var, container element, or a lazily bound
		// method value — keeps the "value method" wrapper text.
		if id, isId := ast.Unparen(t.X).(*ast.Ident); isId {
			if _, _, found := c.fs.find(id.Name); found {
				c.boxReceiver(t.X, t.Sel.Name)
				c.selectorBase(t.X)
				c.emit(bytecode.OpSelectCall, c.nameIdx(t.Sel.Name), 0, t.Sel.Pos())
				return
			}
		}
	}
	c.expr(fun)
}

// isTypePositionCall reports whether the call's first argument is a type
// (the make builtin).
func isTypePositionCall(x *ast.CallExpr) bool {
	id, ok := x.Fun.(*ast.Ident)
	return ok && id.Name == "make"
}

// isNewCall reports whether the call names the new builtin — its first
// argument is a type OR an expression since Go 1.26.
func isNewCall(x *ast.CallExpr) bool {
	id, ok := x.Fun.(*ast.Ident)
	return ok && id.Name == "new"
}

// isTypeForm reports whether e is syntactically a type expression (and thus
// a conversion when used as a call callee).
func (c *compiler) isTypeForm(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.ParenExpr:
		return c.isTypeForm(t.X) // (*T)(x) is a conversion, not a deref call
	case *ast.StarExpr:
		// `*T` is a pointer type but `*p` is a dereference — the forms
		// coincide only in syntax (`a[*i]` vs `F[*T]`), so the operand
		// name decides: a variable is a dereference, a type a pointer.
		return !c.starOperandIsValue(t.X)
	case *ast.ArrayType, *ast.MapType, *ast.StructType, *ast.FuncType,
		*ast.InterfaceType, *ast.ChanType:
		return true
	}
	return false
}

// starOperandIsValue reports whether X in `*X` is a value expression —
// `*p` dereferences a pointer where `*T` names a pointer type, and the
// two are syntactically identical. The operand resolves like any name:
// a local var or a package-level value is a dereference operand; a type
// decl or type parameter keeps `*X` a type form. Unresolvable operands
// (imported `pkg.T` members, forward refs) default to type form,
// matching the old blanket rule.
func (c *compiler) starOperandIsValue(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		info, found := c.resolveName(t.Name)
		return found && !info.isType
	case *ast.SelectorExpr:
		// `*x.f` dereferences x's field; `*pkg.T` is a pointer type.
		// A qualifier resolving to a value is a dereference base.
		if id, ok := t.X.(*ast.Ident); ok {
			info, found := c.resolveName(id.Name)
			return found && !info.isType
		}
		return false
	case *ast.IndexExpr:
		// `*s[i]` dereferences an element; `*List[T]` is a pointer to an
		// instantiation — the indexed operand decides like a selector.
		return c.starOperandIsValue(t.X)
	case *ast.IndexListExpr:
		// same, for multi-arg instantiations `*Pair[K,V]`.
		return c.starOperandIsValue(t.X)
	case *ast.UnaryExpr, *ast.CallExpr, *ast.CompositeLit,
		*ast.BasicLit, *ast.BinaryExpr, *ast.SliceExpr,
		*ast.TypeAssertExpr, *ast.FuncLit:
		// Value-producing operands — &x, f(), T{...}, literals, x.(T) —
		// none of them can name a pointer type, so `*` must dereference
		// (e.g. `*(&Point{})` is a deref, not a `*T` type).
		return true
	case *ast.ParenExpr:
		return c.starOperandIsValue(t.X)
	case *ast.StarExpr:
		return c.starOperandIsValue(t.X)
	}
	return false
}
