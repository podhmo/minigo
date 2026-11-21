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
	"strconv"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

func (c *compiler) stmt(s ast.Stmt) {
	switch st := s.(type) {
	case *ast.BlockStmt:
		c.fs.pushBlock()
		for _, x := range st.List {
			c.stmt(x)
		}
		c.fs.popBlock()
	case *ast.ExprStmt:
		if _, isCall := ast.Unparen(st.X).(*ast.CallExpr); isCall {
			// an expression statement discards its value — `f()`
			// — so a result tuple is legal here.
			c.spreadTuple = true
		}
		c.expr(st.X)
		c.spreadTuple = false
		c.emit(bytecode.OpPop, 0, 0, st.Pos())
	case *ast.DeclStmt:
		gd := st.Decl.(*ast.GenDecl)
		switch gd.Tok {
		case token.VAR, token.CONST:
			for specIdx, effective := range index.ValueSpecs(gd) {
				vs := effective.Spec
				isConst := gd.Tok == token.CONST
				vals, effType := effective.Values, effective.Type
				if isConst {
					// iota is the spec's own index in the GenDecl;
					// a hidden local carries it so `iota` just reads a name.
					c.emit(bytecode.OpConst, c.constIdx(int64(specIdx)), 0, vs.Pos())
					c.emit(bytecode.OpSetLocal, c.fs.iotaSlot(), 0, vs.Pos())
					c.iotaVal = specIdx
				}
				// `var x T` binds a typed zero / typed nil via OpCoerce; typed
				// consts coerce the same way — locals are always cells.
				coerce := func(name *ast.Ident, slot int) {
					if effType == nil || slot < 0 {
						return
					}
					c.emitTypeCoerce(slot, effType, name.Pos())
				}
				// A declared type coerces the value on the stack before the
				// bind: an untyped constant converts straight to T (`var r
				// MyRune = 'a'`) instead of first materializing its default.
				coerceTop := func() {
					if effType == nil {
						return
					}
					c.typeExpr(effType)
					c.emit(bytecode.OpCoerceTop, 0, 0, vs.Pos())
				}
				// interface-typed decls mark their names so an
				// interface-tag switch compares strict pairs.
				ifaceType := effType != nil && c.isIfaceTypeExpr(effType)
				markIface := func(name *ast.Ident, rhs ast.Expr) {
					if name.Name == "_" {
						return
					}
					if ifaceType || (effType == nil && rhs != nil && c.isIfaceExpr(rhs)) {
						c.fs.markIface(name.Name)
					}
				}
				// An inferred `var x = e` carries its RHS's declared
				// type like `x := e` does (stampInferredTyps): `var i =
				// I(s)` binds an I-typed cell so `&i` is *I.
				stampVars := func() {
					if isConst || effType != nil {
						return
					}
					lhs := make([]ast.Expr, len(vs.Names))
					for i, name := range vs.Names {
						lhs[i] = name
					}
					c.stampInferredTyps(lhs, vals)
				}
				if len(vals) == 1 && len(vs.Names) > 1 {
					// commaOkRhs emits the ok-form operand, so it runs at
					// most once — its shape doubles as the typing decision.
					isCommaOk := !isConst && len(vs.Names) == 2 && c.commaOkRhs(vals[0])
					if !isCommaOk {
						if _, isCall := ast.Unparen(vals[0]).(*ast.CallExpr); isCall {
							// `var v, w = f()`: a legal result-tuple
							// spread — the call must not append
							// OpRejectTuple.
							c.spreadTuple = true
						}
						c.expr(vals[0])
						c.spreadTuple = false
					}
					c.emit3(bytecode.OpUnpack, len(vs.Names), 0, 0, vs.Pos())
					for i := len(vs.Names) - 1; i >= 0; i-- {
						coerceTop()
						coerce(vs.Names[i], c.bindLocal(vs.Names[i].Name, vs.Names[i].Pos(), isConst))
						markIface(vs.Names[i], vals[0])
						switch {
						case isCommaOk && i == 1:
							// `var v, ok = m[k]` — the second name is bool.
							c.fs.noteDeclTyp(vs.Names[i].Name, &ast.Ident{Name: "bool"}, nil)
						default:
							if t := c.nthResultOpTyp(vals[0], i); t.te != nil {
								c.fs.noteDeclTyp(vs.Names[i].Name, c.declTypCanon(t.te), t.teFile)
							} else if t.deferred {
								// `var a, b = p.F()` — the name defers
								// to the call's i-th declared result.
								if src := c.deferOperandOf(vals[0]); src != nil {
									c.fs.noteDeferTyp(vs.Names[i].Name, &ast.IndexExpr{X: src,
										Index: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(i)}})
								}
							} else {
								c.noteDeclTyp(vs.Names[i].Name, effType, isConst, nil, nil)
							}
						}
					}
					stampVars()
					c.iotaVal = -1
					continue
				}
				for i, name := range vs.Names {
					typedConst := false
					switch {
					case len(vals) == 0:
						c.emit(bytecode.OpNil, 0, 0, name.Pos())
					case isConst && effType != nil && c.typedConstExpr(effType, vals[i]):
						typedConst = true
					default:
						if isConst {
							c.constExpr(vals[i])
						} else {
							c.expr(vals[i])
						}
						coerceTop()
					}
					var rhs ast.Expr
					if i < len(vals) {
						rhs = vals[i]
					}
					// fold before the binding installs the name: a
					// `const K = K + 1` initializer's K resolves in
					// the enclosing scope, not against the new K.
					var cv constant.Value
					if isConst && rhs != nil {
						cv, _ = c.scopeConstValue(rhs, 0)
					}
					if slot := c.bindLocal(name.Name, name.Pos(), isConst); !typedConst {
						coerce(name, slot)
					}
					markIface(name, rhs)
					c.noteDeclTyp(name.Name, effType, isConst, rhs, cv)
				}
				stampVars()
				c.iotaVal = -1
			}
		case token.TYPE:
			for _, spec := range gd.Specs {
				// `type S struct{...}` inside a function binds the TypeDef as a
				// local value: S{...} literals, var x S, x.(S) all resolve it.
				c.localTypeDecl(spec.(*ast.TypeSpec))
			}
		case token.IMPORT:
			c.trap(st.Pos(), "import inside function is not valid Go")
		}
	case *ast.AssignStmt:
		c.assign(st)
	case *ast.IncDecStmt:
		op := bytecode.BinAdd
		if st.Tok == token.DEC {
			op = bytecode.BinSub
		}
		one := func() {
			// the untyped constant 1 — `mu++` on a named int type adds
			// in the declared type's domain like `mu + 1` would.
			u := &runtime.UConst{V: constant.MakeInt64(1)}
			c.emit(bytecode.OpConst, c.constIdx(u), 0, st.Pos())
		}
		xe := ast.Unparen(st.X)
		switch t := xe.(type) {
		case *ast.Ident:
			c.getRef(t.Name, t.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.setRef(t.Name, t.Pos())
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
			// Keep the storage ref through the update: `s[i].f++`
			// writes the live element — a plain index read yields
			// a copy (struct elements copy in Go), so the value
			// path silently dropped the store. Same ref/deref/add/
			// store shape as `x op= y`.
			c.refTarget(xe, false, false)
			c.emit(bytecode.OpDup, 0, 0, xe.Pos())
			c.emitDerefSite(xe.Pos())
			one()
			c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
			c.emit(bytecode.OpSetRefs, 1, 0, st.Pos())
		default:
			c.trap(st.Pos(), "++/-- on %T is not supported", st.X)
		}
	case *ast.IfStmt:
		c.ifStmt(st)
	case *ast.ForStmt:
		c.forStmt(st)
	case *ast.RangeStmt:
		c.rangeStmt(st)
	case *ast.SwitchStmt:
		c.switchStmt(st)
	case *ast.ReturnStmt:
		c.returnStmt(st)
	case *ast.BranchStmt:
		c.branchStmt(st)
	case *ast.EmptyStmt:
	case *ast.DeferStmt:
		c.callStmt(st.Call, bytecode.OpDefer, st.Pos())
	case *ast.GoStmt:
		c.callStmt(st.Call, bytecode.OpGo, st.Pos())
	case *ast.SendStmt:
		c.expr(st.Chan)
		c.expr(st.Value)
		c.emit(bytecode.OpSend, 0, 0, st.Pos())
	case *ast.SelectStmt:
		c.selectStmt(st)
	case *ast.LabeledStmt:
		c.labeledStmt(st)
	case *ast.TypeSwitchStmt:
		c.typeSwitchStmt(st)
	case *ast.CaseClause, *ast.CommClause:
		c.trap(st.Pos(), "case clause outside switch")
	default:
		c.trap(s.Pos(), "unsupported statement %T", s)
	}
}

// callStmt compiles `defer f(x)` / `go f(x)`: callee and args are
// evaluated immediately; op (OpDefer/OpGo) decides when the call runs.
func (c *compiler) callStmt(call *ast.CallExpr, op bytecode.Op, pos token.Pos) {
	c.calleeExpr(call.Fun)
	c.callArgs(call.Args)
	c.emit3(op, len(call.Args), callSpread(call), c.callSiteArgs(c.argHints(call.Args), pos), pos)
}

func (c *compiler) ifStmt(st *ast.IfStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	c.expr(st.Cond)
	jElse := c.emit(bytecode.OpJumpFalse, 0, 0, st.Cond.Pos())
	c.stmt(st.Body)
	jEnd := c.emit(bytecode.OpJump, 0, 0, st.Pos())
	c.patchA(jElse, len(c.ch.Code))
	if st.Else != nil {
		c.stmt(st.Else)
	}
	c.patchA(jEnd, len(c.ch.Code))
	c.fs.popBlock()
}

func (c *compiler) forStmt(st *ast.ForStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	lc := &ctrlCtx{isLoop: true, continueIP: -1, labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, lc)
	condIP := len(c.ch.Code)
	var jEnd int
	if st.Cond != nil {
		c.expr(st.Cond)
		jEnd = c.emit(bytecode.OpJumpFalse, 0, 0, st.Cond.Pos())
	}
	// Go 1.22+ iteration semantics: names the init statement declares get a
	// fresh binding per iteration — rebind them inside the body so closures
	// (and goroutines) capture distinct cells, then copy the body's
	// (possibly mutated) value back so post and the next cond see it.
	shadows := loopInitNames(st.Init)
	c.fs.pushBlock()
	var shadowPairs [][2]int
	for _, name := range shadows {
		outer, _ := c.fs.lookupLocal(name.Name)
		slot := c.fs.declare(name.Name, name.Pos())
		shadowPairs = append(shadowPairs, [2]int{outer, slot})
		c.emit(bytecode.OpLocal, outer, 0, name.Pos())
		c.emit(bytecode.OpNewLocal, slot, 0, name.Pos())
	}
	for _, bs := range st.Body.List {
		c.stmt(bs)
	}
	c.fs.popBlock()
	lc.continueIP = len(c.ch.Code)
	for _, pr := range shadowPairs {
		c.emit(bytecode.OpLocal, pr[1], 0, st.Init.Pos())
		c.emit(bytecode.OpSetLocal, pr[0], 0, st.Init.Pos())
	}
	if st.Post != nil {
		c.stmt(st.Post)
	}
	// continues inside the body were emitted before post's position existed
	for _, ci := range lc.continues {
		c.patchA(ci, lc.continueIP)
	}
	c.emit(bytecode.OpJump, condIP, 0, st.Pos())
	end := len(c.ch.Code)
	if st.Cond != nil {
		c.patchA(jEnd, end)
	}
	for _, b := range lc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// loopInitNames lists the names a 3-clause for's init statement declares
// (`i := 0`): only :=-style inits get per-iteration bindings.
func loopInitNames(s ast.Stmt) []*ast.Ident {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE {
		return nil
	}
	var out []*ast.Ident
	for _, lhs := range as.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
			out = append(out, id)
		}
	}
	return out
}

func (c *compiler) rangeStmt(st *ast.RangeStmt) {
	c.fs.pushBlock()
	// `range *p` must not evaluate the dereference eagerly: ranging a
	// nil *[N]T still yields its static indices 0..N-1 in Go — only an
	// element read dereferences the nil pointer. gc ranges through the
	// pointer only when the operand is lvalue-capable (a variable,
	// selector, index, or nested deref); a call result or other rvalue
	// materializes `*x` eagerly and panics on nil. Compiling the pointer
	// operand lets OpIter's nil-array path handle it; B=1 keeps the
	// deref panic for non-array pointers.
	rx := ast.Unparen(st.X)
	star := false
	if se, ok := rx.(*ast.StarExpr); ok && st.Value == nil && c.rangeStarLazy(se.X) {
		rx = se.X
		star = true
	}
	// A second LHS operand — even a blank `_` — makes gc evaluate the
	// range expression eagerly: `range *p` dereferences p (nil panics,
	// the array is copied) instead of iterating the pointer. With at
	// most one operand the pointer form stays lazy.
	c.expr(rx)
	iterB := 0
	if star {
		iterB = 1
	}
	c.emit(bytecode.OpIter, 0, iterB, st.X.Pos())
	itSlot := c.fs.declare(c.fresh("$it"), token.NoPos)
	c.emit(bytecode.OpNewLocal, itSlot, 0, st.X.Pos())

	nvars := 0
	if st.Key != nil {
		nvars++
	}
	// A non-blank element var reads the element every iteration — on a
	// nil *[N]T that read is a nil dereference (Go panics).
	// `for i, _ := range p` never reads it and iterates the static
	// indices like the one-var form. Bit 2 of C marks the read.
	nc := nvars
	if st.Value != nil {
		nvars++
		nc = nvars
		if !isBlankIdent(st.Value) {
			nc |= 4
		}
	}
	lc := &ctrlCtx{isLoop: true, labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, lc)
	// An `=` range target is a per-iteration multi-assign: OpRangeNext
	// pushes the (key, val) pair, then the LHS refs evaluate — still
	// seeing the targets' pre-iteration operands (`for i, x[i] =` binds
	// x[i] through the previous i, and `*getvar(&x)` runs once per live
	// iteration, not on the exit probe). `:=` only allows ident targets
	// and keeps the store-target path.
	useRefs := st.Tok != token.DEFINE
	topIP := len(c.ch.Code)
	nextI := c.emit3(bytecode.OpRangeNext, 0, itSlot, nc, st.Pos())
	lc.continueIP = topIP

	if useRefs {
		if st.Key != nil {
			c.refTarget(st.Key, true, false)
		}
		if st.Value != nil {
			c.refTarget(st.Value, true, false)
		}
		c.emit3(bytecode.OpSetRefs, nvars, 1, 0, st.Pos())
	} else {
		// OpRangeNext pushes nvars values (key, val); bind in reverse.
		keyTyp, valTyp, tfile, deferred := c.rangeVarTyps(st.X)
		if st.Value != nil {
			c.bindRangeVar(st.Value, st.Tok == token.DEFINE)
			c.noteBindVar(st.Value, valTyp, tfile, st.Tok == token.DEFINE)
		}
		if st.Key != nil {
			c.bindRangeVar(st.Key, st.Tok == token.DEFINE)
			c.noteBindVar(st.Key, keyTyp, tfile, st.Tok == token.DEFINE)
		}
		if deferred {
			// the bound names' types are facts of the deferred operand:
			// k/v of a map or slice are its key/index and element — the
			// synthetic index/indexlist sites answer them at run time.
			if src := c.deferOperandOf(st.X); src != nil {
				c.noteDeferBindVar(st.Key, st.Tok == token.DEFINE, &ast.IndexListExpr{X: src})
				c.noteDeferBindVar(st.Value, st.Tok == token.DEFINE, &ast.IndexExpr{X: src})
			}
		}
	}
	c.stmt(st.Body)
	c.emit(bytecode.OpJump, topIP, 0, st.Pos())
	end := len(c.ch.Code)
	c.patchA(nextI, end)
	for _, b := range lc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// rangeStarLazy reports whether e — the operand of a `range *e` — is a
// form gc iterates through the pointer: identifiers, selectors, indexes
// and nested dereferences are all lvalue-capable, so `*e` never
// materializes the array and a nil pointer still yields indices. An
// operand that forces evaluation — a non-conversion call or a channel
// receive anywhere inside (`*get().P`, `*(<-ch)`) — evaluates `*e`
// eagerly instead and panics on nil.
func (c *compiler) rangeStarLazy(e ast.Expr) bool {
	return storageShape(e, true) && !c.lenOperandCalls(e)
}

func (c *compiler) bindRangeVar(e ast.Expr, define bool) {
	if id, ok := e.(*ast.Ident); ok && id.Name == "_" {
		c.emit(bytecode.OpPop, 0, 0, e.Pos())
		return
	}
	c.storeTarget(e, define)
}

// noteBindVar records the static type a `:=` bind introduces — a range
// variable, a select case's receive — so the operand gate sees it:
// `for i := range arr` binds int and comparing it against a named type
// traps like gc. Only `:=` binds declare a new name.
//
// An interface-typed bind also stamps the fresh cell: `[]any`'s
// element is declared any, and an unstamped cell's OpLocalTyp would
// answer the concrete element's type instead.
func (c *compiler) noteBindVar(e ast.Expr, te ast.Expr, teFile *syntax.File, define bool) {
	if !define || te == nil {
		return
	}
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	c.fs.noteDeclTyp(id.Name, c.declTypCanon(te), nil)
	// teFile scopes the type name to the declaring file — an `I`
	// shadowed by a local type here is still the declared I, both for
	// the iface check and for the coerce the stamp emits.
	c.pkgScopeTyp = teFile
	iface := c.isIfaceTypeExpr(te)
	if iface {
		if b := c.fs.lookupBinding(id.Name); b != nil && b.declDefer == nil {
			c.typeExpr(te)
			c.emit(bytecode.OpCoerce, b.slot, 0, e.Pos())
		}
	}
	c.pkgScopeTyp = nil
}

// noteDeferBindVar is noteBindVar for a bind whose operand type is a
// deferred fact: `for i := range p.Arr` cannot spell i's declared type
// without indexing p, so the name defers to a synthetic expression of
// the range operand — `p.Arr[0]`'s element for a value var — and the
// run-time gate still traps "mismatched types int and D" like gc.
func (c *compiler) noteDeferBindVar(e ast.Expr, define bool, dx ast.Expr) {
	if !define || dx == nil {
		return
	}
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	c.fs.noteDeferTyp(id.Name, dx)
}

// deferOperandOf normalizes the operand a deferred bind-site marker
// records: `items := p.Ints; for _, v := range items` leaves p.Ints on
// items' own binding, and a marker holding the bare `items` name would
// at run time resolve it as a package member — answering a same-named
// global's type instead of the local's. Substitute the chain's
// original deferred expr. A name that is NOT function-local (a package
// member like `var Items = p.Ints`) keeps its spelling — the
// package-member lookup answers it correctly. A local bound without
// deferred info answers nil: nothing useful is recordable, and a
// package-member answer would be wrong.
func (c *compiler) deferOperandOf(e ast.Expr) ast.Expr {
	for {
		id, ok := e.(*ast.Ident)
		if !ok {
			return e
		}
		if _, _, found := c.fs.find(id.Name); !found {
			return e
		}
		b := c.fs.lookupBinding(id.Name)
		if b == nil || b.declDefer == nil || c.deferExprHasLocal(b.declDefer) {
			return nil
		}
		e = b.declDefer
	}
}

// deferExprHasLocal reports whether e names a function-local binding:
// a marker resolves in package scope at run time, where a local name
// resolves to a same-named package member — or, wrapped in a call or
// conversion, fails the walk outright. Either way the marker must not
// carry it.
//
// The walk is manual because marker exprs carry nil children
// (IndexExpr.Index is empty on element markers), which ast.Inspect
// panics on; a SelectorExpr's Sel is a member name, not a binding.
func (c *compiler) deferExprHasLocal(e ast.Expr) bool {
	local := false
	var visit func(ast.Expr)
	visit = func(x ast.Expr) {
		if local || x == nil {
			return
		}
		switch t := x.(type) {
		case *ast.Ident:
			_, _, local = c.fs.find(t.Name)
		case *ast.SelectorExpr:
			visit(t.X)
		case *ast.IndexExpr:
			visit(t.X)
			visit(t.Index)
		case *ast.IndexListExpr:
			visit(t.X)
			for _, ix := range t.Indices {
				visit(ix)
			}
		case *ast.CallExpr:
			visit(t.Fun)
			for _, a := range t.Args {
				visit(a)
			}
		case *ast.ParenExpr:
			visit(t.X)
		case *ast.StarExpr:
			visit(t.X)
		case *ast.UnaryExpr:
			visit(t.X)
		case *ast.BinaryExpr:
			visit(t.X)
			visit(t.Y)
		case *ast.BasicLit:
			// no names
		default:
			// an unhandled shape may hide a local — stay silent
			local = true
		}
	}
	visit(e)
	return local
}

// rangeVarTyps resolves the (key, value) types a `for k, v := range e`
// binds: int indices for arrays, slices, and strings; the operand's own
// type for an integer range; a map's key and value types; a channel's
// element on the single-variable form. An opaque range expression
// reports none.
func (c *compiler) rangeVarTyps(e ast.Expr) (key, val ast.Expr, teFile *syntax.File, deferred bool) {
	t := c.staticOpTyp(e)
	te := t.te
	if te == nil {
		te = t.dte // `for i := range 10` iterates ints
	}
	if te == nil {
		return nil, nil, t.teFile, t.deferred
	}
	u := c.unfoldTypExpr(te, 4)
	if s, ok := u.(*ast.StarExpr); ok {
		// ranging a pointer to array iterates the array
		u = c.unfoldTypExpr(s.X, 3)
	}
	intIdent := &ast.Ident{Name: "int"}
	switch tt := u.(type) {
	case *ast.ArrayType:
		return intIdent, tt.Elt, t.teFile, false
	case *ast.MapType:
		return tt.Key, tt.Value, t.teFile, false
	case *ast.ChanType:
		// `for v := range ch` binds the element to the single var;
		// a second variable is a compile error elsewhere anyway.
		return tt.Value, nil, t.teFile, false
	case *ast.Ident:
		if tt.Name == "string" {
			// `for i, r := range s` — int index, rune element
			return intIdent, &ast.Ident{Name: "rune"}, t.teFile, false
		}
		if intTypeNames[runtime.CanonicalBasicName(tt.Name)] {
			// `for i := range n` binds n's own type — a named
			// integer keeps its name (gc: i is N, not int).
			return te, nil, t.teFile, false
		}
	}
	return nil, nil, t.teFile, false
}

// isBlankIdent reports whether e is the blank identifier — possibly
// parenthesized — so a range element bound to it is never read.
func isBlankIdent(e ast.Expr) bool {
	id, ok := ast.Unparen(e).(*ast.Ident)
	return ok && id.Name == "_"
}

func (c *compiler) switchStmt(st *ast.SwitchStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	tagSlot := -1
	strict := false
	if st.Tag != nil {
		c.expr(st.Tag)
		tagSlot = c.fs.declare(c.fresh("$tag"), token.NoPos)
		c.emit(bytecode.OpNewLocal, tagSlot, 0, st.Tag.Pos())
		// An interface-typed tag compares dynamic (type, value) pairs:
		// `case 1:` (int) must not match an any(float64(1.0)) tag.
		strict = c.isIfaceExpr(st.Tag)
	}
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	var jumpOuts []int
	var pendingFalls []int // fallthrough sites in the previous clause body
	defaultBody := -1      // body start of the default clause, if any
	lastJNext := -1        // "all tests failed" continuation of the last clause
	for i, s := range st.Body.List {
		clause := s.(*ast.CaseClause)
		// tests: tag == e (or truthy e for tag-less switch); JumpTrue -> body.
		// `default:` matches only after every other clause fails — it
		// has no tests, but a non-final default still needs a jump
		// past its body so later clauses' tests run first.
		isDefault := clause.List == nil
		isLast := i == len(st.Body.List)-1
		bodyJumps := []int{}
		for _, e := range clause.List {
			if tagSlot >= 0 {
				// a case expr of a statically distinct named type fails
				// gc's operand check — `switch tag { case s: }`. The case
				// side is the left operand, matching gc's message order
				// "(mismatched types string and Tag)".
				if c.opCaseGate(e, st.Tag, e.Pos()) {
					continue
				}
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				// A constant case expr stays a constant: emitting the
				// materialized value (`float64(1e19)`) would lose the
				// constness the comparison needs to convert it to the
				// tag's type first — `case 1e19` against a uint64 tag
				// compares exactly only as an untyped constant
				// ($GOROOT/test/fixedbugs/issue43480.go).
				if cv, ok := constValue(e); ok {
					if v, ok2 := constOperand(cv, hasCharLit(e)); ok2 {
						c.emit(bytecode.OpConst, c.constIdx(v), 0, e.Pos())
					} else {
						c.expr(e)
					}
				} else {
					c.expr(e)
				}
				op := bytecode.BinEql
				if strict {
					op = bytecode.BinEqlIface
				}
				c.emit(bytecode.OpBinary, int(op), 0, e.Pos())
			} else {
				c.expr(e)
			}
			bodyJumps = append(bodyJumps, c.emit(bytecode.OpJumpTrue, 0, 0, e.Pos()))
		}
		// no test matched: continue to next clause's tests (emitted after
		// this body). A final default clause just drops into its body —
		// it is where "all failed" lands.
		jNext := -1
		if !isDefault || !isLast {
			jNext = c.emit(bytecode.OpJump, 0, 0, clause.Pos())
		}
		bodyStart := len(c.ch.Code)
		if isDefault {
			defaultBody = bodyStart
		}
		for _, bj := range bodyJumps {
			c.patchA(bj, bodyStart)
		}
		for _, fi := range pendingFalls {
			c.patchA(fi, bodyStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		var falls []int
		c.falls = &falls
		for _, bs := range clause.Body {
			c.stmt(bs)
		}
		c.falls = nil
		c.fs.popBlock()
		pendingFalls = falls
		jumpOuts = append(jumpOuts, c.emit(bytecode.OpJump, 0, 0, clause.Pos()))
		if jNext >= 0 {
			lastJNext = jNext
			c.patchA(jNext, len(c.ch.Code))
		}
	}
	// every clause's tests failed: run the default body if one exists.
	if defaultBody >= 0 && lastJNext >= 0 {
		c.patchA(lastJNext, defaultBody)
	}
	if len(pendingFalls) > 0 {
		ti := c.trap(st.Pos(), "fallthrough out of the final case clause")
		for _, fi := range pendingFalls {
			c.patchA(fi, ti)
		}
	}
	end := len(c.ch.Code)
	for _, j := range jumpOuts {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// selectStmt compiles a real blocking select. The Go spec evaluates every
// case's channel operand (and a send case's value) exactly once, in source
// order, on entry — so operands land in temp slots first, then OpSelArm
// builds a select descriptor per case and OpSelWait blocks on
// reflect.Select (random ready choice, blocking without a default). The
// wait dispatches through a jump table of OpJumps laid down immediately
// after it — case i's body is at table[i], the default at table[n].
func (c *compiler) selectStmt(st *ast.SelectStmt) {
	c.fs.pushBlock()
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	type selCase struct {
		body     []ast.Stmt
		pos      token.Pos
		chanSlot int
		valSlot  int // send cases only
		nrecv    int // receive arity: 0, 1, or 2
		lhs      []ast.Expr
		define   bool
		send     bool
		recv     ast.Expr // receive case's channel operand
	}
	var cases []selCase
	var defaultBody []ast.Stmt
	hasDefault := false
	for _, s := range st.Body.List {
		clause := s.(*ast.CommClause)
		if clause.Comm == nil {
			// a `default:` with an empty body still counts as a default —
			// without the arm the select blocks instead of falling through
			hasDefault = true
			defaultBody = clause.Body
			continue
		}
		sc := selCase{body: clause.Body, pos: clause.Pos(), chanSlot: -1, valSlot: -1}
		switch comm := clause.Comm.(type) {
		case *ast.SendStmt:
			sc.send = true
			sc.chanSlot = c.fs.declare(c.fresh("$sel"), token.NoPos)
			sc.valSlot = c.fs.declare(c.fresh("$sel"), token.NoPos)
			c.expr(comm.Chan)
			c.emit(bytecode.OpSetLocal, sc.chanSlot, 0, comm.Chan.Pos())
			c.expr(comm.Value)
			c.emit(bytecode.OpSetLocal, sc.valSlot, 0, comm.Value.Pos())
		default:
			recv, lhs, define := selectRecv(clause.Comm)
			if recv == nil {
				c.trap(clause.Comm.Pos(), "unsupported select case %T", clause.Comm)
				continue
			}
			sc.chanSlot = c.fs.declare(c.fresh("$sel"), token.NoPos)
			c.expr(recv.X)
			c.emit(bytecode.OpSetLocal, sc.chanSlot, 0, recv.X.Pos())
			sc.nrecv = len(lhs)
			sc.lhs = lhs
			sc.define = define
			sc.recv = recv.X
		}
		cases = append(cases, sc)
	}

	// arms: one OpSelArm per case, pushed in source order for OpSelWait
	for _, sc := range cases {
		if sc.send {
			c.emit(bytecode.OpLocal, sc.chanSlot, 0, sc.pos)
			c.emit(bytecode.OpLocal, sc.valSlot, 0, sc.pos)
			c.emit(bytecode.OpSelArm, 0, 1, sc.pos)
		} else {
			c.emit(bytecode.OpLocal, sc.chanSlot, 0, sc.pos)
			c.emit(bytecode.OpSelArm, sc.nrecv, 0, sc.pos)
		}
	}
	bDefault := 0
	if hasDefault {
		bDefault = 1
	}
	c.emit(bytecode.OpSelWait, len(cases), bDefault, st.Pos())
	// jump table OpSelWait dispatches into: one OpJump per case, then the
	// default's OpJump as the last slot
	jmps := make([]int, 0, len(cases)+1)
	for _, sc := range cases {
		jmps = append(jmps, c.emit(bytecode.OpJump, 0, 0, sc.pos))
	}
	if hasDefault {
		jmps = append(jmps, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
	}

	var exits []int
	for i, sc := range cases {
		c.patchA(jmps[i], len(c.ch.Code))
		c.fs.pushBlock()
		if !sc.send {
			c.bindRecv(sc.lhs, sc.define, sc.recv)
		}
		for _, bs := range sc.body {
			c.stmt(bs)
		}
		c.fs.popBlock()
		exits = append(exits, c.emit(bytecode.OpJump, 0, 0, sc.pos))
	}
	if hasDefault {
		c.patchA(jmps[len(cases)], len(c.ch.Code))
		for _, bs := range defaultBody {
			c.stmt(bs)
		}
	}
	end := len(c.ch.Code)
	for _, j := range exits {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}

// selectRecv extracts a receive case from a CommClause's comm statement:
// `case <-ch`, `case v := <-ch`, `case v, ok := <-ch`, or assignments.
func selectRecv(comm ast.Stmt) (recv *ast.UnaryExpr, lhs []ast.Expr, define bool) {
	arrow := func(e ast.Expr) *ast.UnaryExpr {
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.ARROW {
			return u
		}
		return nil
	}
	switch s := comm.(type) {
	case *ast.ExprStmt:
		if u := arrow(s.X); u != nil {
			return u, nil, false
		}
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			if u := arrow(s.Rhs[0]); u != nil {
				return u, s.Lhs, s.Tok == token.DEFINE
			}
		}
	}
	return nil, nil, false
}

// bindRecv binds a select receive payload (already on the stack) to the
// case's LHS: a Tuple for two binds, a plain value for one, discarded else.
// A `:=` name also learns its static type for the operand gate — the
// channel's element for v, bool for ok.
func (c *compiler) bindRecv(lhs []ast.Expr, define bool, chExpr ast.Expr) {
	if len(lhs) == 0 {
		c.emit(bytecode.OpPop, 0, 0, 0)
		return
	}
	var elem ast.Expr
	var elemFile *syntax.File
	var deferred ast.Expr
	if define {
		chExpr = ast.Unparen(chExpr)
		t := c.staticOpTyp(chExpr)
		if t.te != nil {
			elem = c.chanElemTypExpr(t.te, t.teFile, 4)
			elemFile = t.teFile
		} else if src := c.deferOperandOf(chExpr); src != nil {
			// An imported var may have an inferred type. Ask about
			// its element in the declaring package at run time.
			_, _, imported := c.selTypRef(src)
			if t.deferred || imported && !c.deferExprHasLocal(src) {
				deferred = &ast.IndexExpr{X: src}
			}
		}
	}
	if len(lhs) > 1 {
		c.emit3(bytecode.OpUnpack, len(lhs), 0, 0, 0)
	}
	for i := len(lhs) - 1; i >= 0; i-- {
		c.storeTarget(lhs[i], define)
	}
	c.noteBindVar(lhs[0], elem, elemFile, define)
	c.noteDeferBindVar(lhs[0], define, deferred)
	for _, l := range lhs[1:] {
		c.noteBindVar(l, &ast.Ident{Name: "bool"}, nil, define)
	}
}

func (c *compiler) returnStmt(st *ast.ReturnStmt) {
	if len(st.Results) == 0 {
		// bare return: coerce named slots to their declared types so
		// `return` under `func f() (r *T)` yields a typed nil.
		for i, slot := range c.ch.NamedSlots {
			if i < len(c.results) && c.results[i] != nil {
				c.emitSigTypeCoerce(slot, c.results[i], st.Pos())
			}
		}
		c.emit(bytecode.OpReturn, -1, 0, st.Pos()) // -1: use named result slots
		return
	}
	if len(st.Results) == 1 && len(c.results) > 1 {
		// `return pair()`: one expr produces N results — push every
		// declared result type and coerce the tuple element-wise, so an
		// `any` slot still boxes a typed nil into an IfaceNil.
		if _, isCall := ast.Unparen(st.Results[0]).(*ast.CallExpr); isCall {
			c.spreadTuple = true
		}
		c.expr(st.Results[0])
		c.spreadTuple = false
		for _, rt := range c.results {
			c.sigTypeExpr(rt)
		}
		c.emit(bytecode.OpCoerceN, len(c.results), 0, st.Results[0].Pos())
		// spread the tuple back to N values so OpReturn's named-slot
		// store (and a caller's unpack) sees each element, not one Tuple.
		c.emit3(bytecode.OpUnpack, len(c.results), 0, 0, st.Pos())
		c.emit(bytecode.OpReturn, len(c.results), 0, st.Pos())
		return
	} else {
		// a single `any` result accepts a result tuple — the repl's
		// `__stepN() any { return f() }` convention relies on it.
		anyResult := len(st.Results) == 1 && len(c.results) == 1 && func() bool {
			switch t := c.results[0].(type) {
			case *ast.Ident:
				return t.Name == "any"
			case *ast.InterfaceType:
				return t.Methods == nil || len(t.Methods.List) == 0
			}
			return false
		}()
		for i, r := range st.Results {
			if anyResult {
				if _, isCall := ast.Unparen(r).(*ast.CallExpr); isCall {
					c.spreadTuple = true
				}
			}
			c.expr(r)
			c.spreadTuple = false
			// `return e` coerces e to the declared result type — `return nil`
			// under a *T result yields a typed nil, under any an IfaceNil.
			if i < len(c.results) && c.results[i] != nil {
				c.sigTypeExpr(c.results[i])
				c.emit(bytecode.OpCoerceTop, 0, 0, r.Pos())
			}
		}
	}
	c.emit(bytecode.OpReturn, len(st.Results), 0, st.Pos())
}

func (c *compiler) branchStmt(st *ast.BranchStmt) {
	if st.Label != nil {
		switch st.Tok {
		case token.BREAK:
			for i := len(c.ctrl) - 1; i >= 0; i-- {
				cc := c.ctrl[i]
				for _, name := range cc.labels {
					if name == st.Label.Name {
						cc.breaks = append(cc.breaks, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
						return
					}
				}
			}
			c.trap(st.Pos(), "break label %s is not defined", st.Label.Name)
		case token.CONTINUE:
			for i := len(c.ctrl) - 1; i >= 0; i-- {
				cc := c.ctrl[i]
				if !cc.isLoop {
					continue
				}
				for _, name := range cc.labels {
					if name == st.Label.Name {
						if cc.continueIP >= 0 {
							c.emit(bytecode.OpJump, cc.continueIP, 0, st.Pos())
						} else {
							cc.continues = append(cc.continues, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
						}
						return
					}
				}
			}
			c.trap(st.Pos(), "continue label %s is not defined", st.Label.Name)
		case token.GOTO:
			gb, gv := c.fs.scopeSnapshot()
			if li, ok := c.labels[st.Label.Name]; ok {
				if why := gotoViolation(li, gb, gv); why != "" {
					c.trap(st.Pos(), "%s", why)
					return
				}
				c.emit(bytecode.OpJump, li.ip, 0, st.Pos())
				return
			}
			c.pendingGotos = append(c.pendingGotos, pendingGoto{
				ins:    c.emit(bytecode.OpJump, 0, 0, st.Pos()),
				name:   st.Label.Name,
				pos:    st.Pos(),
				blocks: gb,
				vars:   gv,
			})
		case token.FALLTHROUGH:
			c.trap(st.Pos(), "fallthrough cannot have a label")
		}
		return
	}
	switch st.Tok {
	case token.BREAK:
		if len(c.ctrl) > 0 {
			cc := c.ctrl[len(c.ctrl)-1]
			cc.breaks = append(cc.breaks, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			return
		}
		c.trap(st.Pos(), "break outside loop/switch")
	case token.CONTINUE:
		for i := len(c.ctrl) - 1; i >= 0; i-- {
			cc := c.ctrl[i]
			if !cc.isLoop {
				continue
			}
			if cc.continueIP >= 0 {
				c.emit(bytecode.OpJump, cc.continueIP, 0, st.Pos())
			} else {
				cc.continues = append(cc.continues, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			}
			return
		}
		c.trap(st.Pos(), "continue outside loop")
	case token.FALLTHROUGH:
		if c.falls != nil {
			*c.falls = append(*c.falls, c.emit(bytecode.OpJump, 0, 0, st.Pos()))
			return
		}
		c.trap(st.Pos(), "fallthrough outside switch case")
	}
}

// labeledStmt registers a label and compiles its statement. A label
// directly wrapping a control construct (for/range/switch/select/type
// switch) is claimed by that construct so `break L`/`continue L` work.
func (c *compiler) labeledStmt(st *ast.LabeledStmt) {
	if st.Label.Name == "_" {
		// A blank label is not a declaration: it never redeclares and
		// cannot be a goto/break/continue target — compile the
		// statement without registering the name.
		c.stmt(st.Stmt)
		return
	}
	lb, lv := c.fs.scopeSnapshot()
	li := &labelInfo{name: st.Label.Name, ip: len(c.ch.Code), blocks: lb, vars: lv}
	if _, dup := c.labels[st.Label.Name]; dup {
		c.trap(st.Pos(), "label %s redeclared", st.Label.Name)
	}
	c.labels[st.Label.Name] = li
	switch st.Stmt.(type) {
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.SelectStmt, *ast.TypeSwitchStmt:
		c.pendingLabels = append(c.pendingLabels, li)
		c.stmt(st.Stmt)
		c.pendingLabels = nil
	default:
		c.stmt(st.Stmt)
	}
}

// typeSwitchStmt compiles `switch v := x.(type) { case T: ... }`. The
// subject is evaluated once into a hidden slot; each case emits OpAssertOK
// keeping the asserted value on stack for the case body to bind (narrowed
// v) or discard.
func (c *compiler) typeSwitchStmt(st *ast.TypeSwitchStmt) {
	c.fs.pushBlock()
	if st.Init != nil {
		c.stmt(st.Init)
	}
	var subj ast.Expr
	varName := ""
	switch a := st.Assign.(type) {
	case *ast.ExprStmt:
		if ta, ok := a.X.(*ast.TypeAssertExpr); ok {
			subj = ta.X
		}
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			if ta, ok := a.Rhs[0].(*ast.TypeAssertExpr); ok {
				subj = ta.X
			}
		}
		if len(a.Lhs) > 0 {
			if id, ok := a.Lhs[0].(*ast.Ident); ok {
				varName = id.Name
			}
		}
	}
	if subj == nil {
		c.trap(st.Pos(), "type switch without a type assertion")
		c.fs.popBlock()
		return
	}
	c.expr(subj)
	tagSlot := c.fs.declare(c.fresh("$tsubj"), token.NoPos)
	c.emit(bytecode.OpNewLocal, tagSlot, 0, subj.Pos())
	cc := &ctrlCtx{labels: c.takeLabels()}
	c.ctrl = append(c.ctrl, cc)

	var defaultBody []ast.Stmt
	var jumpOuts []int
	var pendingFalls []int
	for _, s := range st.Body.List {
		clause := s.(*ast.CaseClause)
		if clause.List == nil {
			defaultBody = clause.Body
			continue
		}
		bodyJumps := []int{}
		// each test leaves one value on the stack for the body to bind
		// (the assert result, or the subject itself for `case nil:`);
		// JumpTrue pops the flag, the false path pops the leftover value.
		for _, e := range clause.List {
			if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.emit(bytecode.OpNil, 0, 0, e.Pos())
				c.emit(bytecode.OpBinary, int(bytecode.BinEql), 0, e.Pos())
			} else {
				c.emit(bytecode.OpLocal, tagSlot, 0, e.Pos())
				c.typeExpr(e)
				c.emit(bytecode.OpAssertOK, 0, 0, e.Pos())
				c.emit3(bytecode.OpUnpack, 2, 0, 0, e.Pos())
			}
			bodyJumps = append(bodyJumps, c.emit(bytecode.OpJumpTrue, 0, 0, e.Pos()))
			c.emit(bytecode.OpPop, 0, 0, e.Pos())
		}
		jNext := c.emit(bytecode.OpJump, 0, 0, clause.Pos())
		bodyStart := len(c.ch.Code)
		for _, bj := range bodyJumps {
			c.patchA(bj, bodyStart)
		}
		for _, fi := range pendingFalls {
			c.patchA(fi, bodyStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		if varName != "" && varName != "_" {
			slot := c.fs.declare(varName, clause.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, clause.Pos())
		} else {
			c.emit(bytecode.OpPop, 0, 0, clause.Pos())
		}
		var falls []int
		c.falls = &falls
		for _, bs := range clause.Body {
			c.stmt(bs)
		}
		c.falls = nil
		c.fs.popBlock()
		pendingFalls = falls
		jumpOuts = append(jumpOuts, c.emit(bytecode.OpJump, 0, 0, clause.Pos()))
		c.patchA(jNext, len(c.ch.Code))
	}
	if defaultBody != nil {
		defStart := len(c.ch.Code)
		for _, fi := range pendingFalls {
			c.patchA(fi, defStart)
		}
		pendingFalls = nil
		c.fs.pushBlock()
		if varName != "" && varName != "_" {
			// default binds the un-narrowed subject value
			slot := c.fs.declare(varName, st.Pos())
			c.emit(bytecode.OpLocal, tagSlot, 0, st.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, st.Pos())
		}
		for _, bs := range defaultBody {
			c.stmt(bs)
		}
		c.fs.popBlock()
	}
	if len(pendingFalls) > 0 {
		ti := c.trap(st.Pos(), "fallthrough out of the final case clause")
		for _, fi := range pendingFalls {
			c.patchA(fi, ti)
		}
	}
	end := len(c.ch.Code)
	for _, j := range jumpOuts {
		c.patchA(j, end)
	}
	for _, b := range cc.breaks {
		c.patchA(b, end)
	}
	c.ctrl = c.ctrl[:len(c.ctrl)-1]
	c.fs.popBlock()
}
