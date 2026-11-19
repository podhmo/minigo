package compile

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// opCmpGate emits an OpTrap in place of a binary op or switch-case
// compare whose operands statically resolve to distinct named types:
// gc rejects `d == i` / `f.Tag == s` / `d < len(x)` at compile time,
// and a run-time check cannot recover it — an any-carried operand
// reaches the same instruction and interface equality is lawful, which
// is why '=='/'!=' stayed dynamic under the runtime operand gate.
// The gate only speaks when statically certain — an untyped constant
// (it adopts), an interface operand (dynamic pairs compare lawfully),
// or any unknown side keeps the op evaluating, so a partial static
// picture never invents an error gc wouldn't issue. Returns true when
// it trapped. Shifts are exempt: their operands are only required to be
// integer-typed, not mutually assignable (`i << d` is legal Go).
func (c *compiler) opCmpGate(x, y ast.Expr, pos token.Pos) bool {
	return c.opGate(x, y, pos, false)
}

// opCaseGate is opCmpGate for a switch case clause — the same operand
// check with gc's case-clause wording: `invalid case s in switch on d
// (mismatched types string and D)`.
func (c *compiler) opCaseGate(e, tag ast.Expr, pos token.Pos) bool {
	return c.opGate(e, tag, pos, true)
}

func (c *compiler) opGate(x, y ast.Expr, pos token.Pos, inSwitch bool) bool {
	xt, yt := c.staticOpTyp(x), c.staticOpTyp(y)
	// `nil` compares only against nilable types: `d == nil` is a gc
	// compile error (mismatched types Duration and untyped nil), while
	// a pointer/slice/map/chan/func/interface operand legitimately
	// answers it. A side that did not resolve stays silent.
	// nilGate defers the nil check to run time when the non-nil
	// operand's type is itself a deferred fact (or a type expression
	// compile cannot name-resolve) — OpTypGate answers nilability once
	// the package member or qualified type materializes.
	nilGate := false
	if xt.nilOperand || yt.nilOperand {
		other := yt
		if yt.nilOperand {
			other = xt
		}
		if other.nilOperand || other.untyped || other.iface || (other.te == nil && !other.deferred) {
			return false
		}
		if other.deferred {
			nilGate = true
		} else {
			if nilable, resolved := c.nilableTypExpr(other.te); !resolved {
				// a deferred member lookup defers the nil check too —
				// but a genuinely absent member (`io.NotAReader`)
				// stays maybe-nilable and silent like before.
				if !c.typExprDeferred(other.te) {
					return false
				}
				nilGate = true
			} else if nilable {
				return false
			} else if oi, ok := c.namedIdentOf(other.te); !ok {
				// a spelled type compile cannot name — `Box[int]` —
				// defers too: run time resolves it and traps a named
				// non-nilable compare, while an anonymous shape
				// (struct{...}) still resolves unnamed and silent.
				nilGate = true
			} else {
				// gc orders the operands as written: `nil == i` reports
				// "untyped nil and int", `i == nil` the reverse.
				xName, yName := oi.name, "untyped nil"
				if xt.nilOperand {
					xName, yName = "untyped nil", oi.name
				}
				c.trapOpGate(x, y, pos, inSwitch, xName, yName)
				return true
			}
		}
	}

	// an untyped constant shifted by a non-constant count takes the
	// other operand's type, which must then be an integer type: `f +
	// 1<<n` with a float64 f is gc's "shifted operand 1 (type float64)
	// must be integer".
	for _, side := range [2]struct {
		e     ast.Expr
		t     opTyp
		other opTyp
	}{{x, xt, yt}, {y, yt, xt}} {
		if !side.t.untyped || side.other.te == nil || side.other.untyped || side.other.iface {
			continue
		}
		if !c.nonIntBasicTyp(side.other.te) {
			continue
		}
		if lhs := c.nonConstShiftOperand(side.e, 8); lhs != nil {
			name := c.exprSpell(side.other.te)
			c.trap(pos, "invalid operation: shifted operand %s (type %s) must be integer", c.exprSpell(lhs), name)
			return true
		}
	}
	// a switch tag's untyped value pins to its default type before the
	// case side compares: `switch x < y { case B(true): }` mismatches B
	// against bool even though `x < y` is untyped — an untyped case expr
	// (xt) still adopts into the tag's type.
	pinnedDte := ""
	if inSwitch && yt.untyped && yt.dte != nil {
		if id, ok := ast.Unparen(yt.dte).(*ast.Ident); ok {
			pinnedDte = id.Name
		}
		yt = opTyp{te: yt.dte, dte: yt.dte}
	}
	// a spelled type name whose package question is still open —
	// `time.Duration(5)` under IndexedOnly — is also a deferred fact:
	// the run-time gate resolves the qualified name once its package
	// materializes.
	teDeferred := (xt.te != nil && c.typExprDeferred(xt.te)) ||
		(yt.te != nil && c.typExprDeferred(yt.te))
	if xt.deferred || yt.deferred || nilGate || teDeferred {
		// the same operands the static gate declines stay undecided:
		// an untyped constant adopts the other operand's type, and an
		// interface-typed operand's legality is assignability, not
		// identity — `time.January == 1` and `rc == i` are both legal.
		if xt.untyped || yt.untyped || xt.iface || yt.iface {
			return false
		}
		// an operand's static type is a deferred fact — emit the
		// run-time gate instead of guessing: OpTypGate resolves both
		// operand typedefs and traps gc's mismatched-types wording
		// only when they settle as distinct named types. A deferred
		// operand goes in as a value site (mask bit set); an operand
		// whose type the compile already knows goes in as the type
		// expression itself.
		var sites [2]int
		mask := 0
		for i, o := range [2]struct {
			ot opTyp
			x  ast.Expr
		}{{xt, x}, {yt, y}} {
			switch {
			case o.ot.deferred || o.ot.nilOperand:
				dx := o.ot.dexpr
				if dx == nil {
					dx = o.x
				}
				sites[i] = c.exprSite(dx, o.x.Pos())
				mask |= 1 << i
				if o.ot.maybeUntyped {
					mask |= 1 << (i + 2)
				}
			default:
				te := o.ot.te
				if te == nil {
					te = o.ot.dte
				}
				if te == nil {
					return false
				}
				sites[i] = c.exprSiteAt(te, o.ot.teFile, o.x.Pos())
			}
		}
		c.emit3(bytecode.OpTypGate, sites[0], sites[1], mask, pos)
		return false
	}
	if xt.te == nil || yt.te == nil || xt.untyped || yt.untyped || xt.iface || yt.iface {
		return false
	}
	xi, ok := c.namedIdentOf(xt.te)
	if !ok {
		return false
	}
	var yi namedTypID
	if pinnedDte != "" && predeclaredTypeNames[pinnedDte] {
		// the tag's default type is always a predeclared basic type —
		// a local `type rune int` shadows the ident, so spell it
		// canonically instead of resolving the name in scope.
		yi = namedTypID{id: "predecl:" + runtime.CanonicalBasicName(pinnedDte), name: pinnedDte}
	} else if yi, ok = c.namedIdentOf(yt.te); !ok {
		return false
	}
	if yi.id == xi.id {
		return false
	}
	c.trapOpGate(x, y, pos, inSwitch, xi.name, yi.name)
	return true
}

// emitShift emits a shift: the count's constness decides whether an
// untyped-constant left operand keeps the shift a constant expression
// — a fact the VM cannot recover from the count's value alone.
//
// When compile cannot answer the count (`p.K` under DeferFacts), the
// question defers to the run time: x.X emits once, OpConstExpr jumps
// to the non-constant variant when the count resolves non-constant,
// and each variant evaluates the count and applies its own flag —
// ShiftConstCount keeps `1 << p.K` constant, ShiftNonConstCount keeps
// it dynamic.
func (c *compiler) emitShift(op bytecode.BinOp, x *ast.BinaryExpr) {
	c.sawDeferred = false
	isConst := c.isConstExpr(x.Y)
	nonConst := !isConst && c.certainlyNonConst(x.Y)
	flag := 0
	if lt := c.staticOpTyp(x.X); lt.te != nil && !lt.untyped {
		flag |= bytecode.ShiftTypedLeft
	}
	if !isConst && !nonConst && c.sawDeferred {
		xs, ys := x.X, x.Y
		hoisted := false
		if subs, ok := c.hoistEagerOps([]ast.Expr{x.X, x.Y}, "$bin"); ok {
			xs, ys, hoisted = subs[0], subs[1], true
		}
		c.expr(xs)
		if !hoisted {
			// no hoist: evaluate the count once up front so the
			// variants re-read it instead of evaluating it twice.
			c.expr(x.Y)
			name := c.fresh("$shift")
			slot := c.fs.declare(name, x.Y.Pos())
			if c.isIfaceExpr(x.Y) {
				c.fs.markIface(name)
			}
			c.emit(bytecode.OpNewLocal, slot, 0, x.Y.Pos())
			ys = ast.NewIdent(name)
		}
		gate := c.emit3(bytecode.OpConstExpr, 0, c.exprSite(x.Y, x.Y.Pos()), 0, x.Pos())
		c.expr(ys)
		c.emit(bytecode.OpBinary, int(op), flag|bytecode.ShiftConstCount, x.Pos())
		jEnd := c.emit(bytecode.OpJump, 0, 0, x.Pos())
		c.patchA(gate, len(c.ch.Code))
		c.expr(ys)
		c.emit(bytecode.OpBinary, int(op), flag|bytecode.ShiftNonConstCount, x.Pos())
		c.patchA(jEnd, len(c.ch.Code))
		return
	}
	c.binaryOperands(x.X, x.Y)
	switch {
	case isConst:
		// a constant count keeps an untyped-constant left
		// operand's shift a constant expression — the VM
		// cannot tell a typed constant count (`const T uint
		// = 3`, `len("abc")`, `bits.UintSize`) from a
		// variable by its value.
		flag |= bytecode.ShiftConstCount
	case nonConst:
		flag |= bytecode.ShiftNonConstCount
	}
	c.emit(bytecode.OpBinary, int(op), flag, x.Pos())
}

// exprSite records a deferred fact about a value expression — the
// constness question OpConstExpr asks and the operand-typedef question
// OpTypGate asks — pinning the same compile scope callSite pins.
func (c *compiler) exprSite(x ast.Expr, pos token.Pos) int {
	return c.exprSiteAt(x, nil, pos)
}

// exprSiteAt is exprSite for a type expression spelled in another file
// — a package decl's `otherpkg.Box[int]` keeps its own file's imports,
// never the file the compare happens to be compiled in.
func (c *compiler) exprSiteAt(x ast.Expr, teFile *syntax.File, pos token.Pos) int {
	file := c.file
	var localTypes map[string]*runtime.TypeDef
	switch {
	case teFile != nil:
		file = teFile
	case c.pkgScopeTyp != nil:
		file = c.pkgScopeTyp
	default:
		localTypes = c.fs.localTypeDefs()
	}
	var localConsts map[string]bool
	if c.pkgScopeTyp == nil {
		localConsts = c.fs.localConsts()
	}
	c.ch.CallSites = append(c.ch.CallSites, &bytecode.CallSite{
		Expr: x, File: file, Pos: pos,
		Ctx:    &runtime.TypeDef{Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes},
		Consts: localConsts,
		Sym:    -1,
		Quote:  -1,
	})
	return len(c.ch.CallSites) - 1
}

// typExprDeferred reports whether te is a qualified type name whose
// package question is still open — the index deferred it, or the
// package never loaded at all (Missing/Failed): an answer the
// run-time gate can still give, or surface the load error, once the
// package materializes. A FOUND package whose member is absent
// (io.NotAReader) is not deferred.
func (c *compiler) typExprDeferred(te ast.Expr) bool {
	if sel, ok := c.unfoldTypExpr(te, 4).(*ast.SelectorExpr); ok {
		if ref, _, ok := c.selTypRef(sel); ok {
			return c.askIndexed(ref).Kind != semantics.Found
		}
	}
	return false
}

// selPkgMemberOpTyp resolves a package-qualified operand `pkg.Name`:
// a bound const/var tagged with a typedef contributes that type —
// `time.January` is time.Month, so `d == time.January` mismatches like
// gc. An untyped bound const stays untyped with its kind's default.
// A source-indexed package's Globals stay empty at compile time, so the
// member's static type comes from its declaration spec instead — read
// in the declaring package's own scope, never the caller's. Types used
// as values and unknown members report unknown.
// semantics: fact — decides the static operand type of pkg.Name, which
// drives `==` lowering and gc-parity rejections. The typed-const
// defaulting path alone would be a hint; the rest is not.
func (c *compiler) selPkgMemberOpTyp(x *ast.SelectorExpr, ref *runtime.ImportRef) opTyp {
	res := c.askIndexed(ref)
	if res.Kind == semantics.Deferred {
		ot := opTyp{deferred: true, dexpr: x}
		// an untyped const stays untyped through the deferral — `p.C`
		// compared to a float64 adopts float64 like any untyped
		// operand, so it must never feed the run-time gate a default
		// type. The materialized package (res.Val survives the
		// downgrade) answers which members are untyped consts; dte is
		// kept so a switch tag still pins to the default type. Under
		// IndexedOnly no package materializes — maybeUntyped hands the
		// untyped-const question to the run-time gate.
		if p := res.Val; p != nil {
			if v, ok := p.Globals.Get(x.Sel.Name); ok {
				if uc, isU := v.(*runtime.UConst); isU {
					ot.untyped = true
					ot.dte = constKindDte(uc)
				}
			} else if p.Index != nil {
				if d := p.Index.Consts[x.Sel.Name]; d != nil {
					// valueSpecMemberOpTyp checks the initializer too —
					// `const C = T(1)` is typed T, not untyped.
					if mt := c.valueSpecMemberOpTyp(p, d, true); mt.untyped {
						ot.untyped = true
						ot.dte = mt.dte
					}
				}
			}
		} else {
			ot.maybeUntyped = true
		}
		return ot
	}
	if res.Kind != semantics.Found {
		return opTyp{}
	}
	p := res.Val
	if p.Globals != nil {
		if v, ok := p.Globals.Get(x.Sel.Name); ok {
			// an initialized source package binds members into
			// Cells; bound packages keep the value directly.
			if cell, isCell := v.(*runtime.Cell); isCell && cell != nil {
				v = cell.Elem
			}
			if n, ok := v.(*runtime.Named); ok && n.Typ != nil {
				if sel := qualTypSel(p, n.Typ); sel != "" {
					return opTyp{te: &ast.SelectorExpr{X: x.X, Sel: ast.NewIdent(sel)}}
				}
			}
			// a bound typed const may hold the raw host scalar rather
			// than a Named — `time.Second` binds a bare time.Duration.
			// Its tag comes from matching the host type back to the
			// package's own typedefs.
			if td := hostScalarTypeDefOf(p, v); td != nil {
				if sel := qualTypSel(p, td); sel != "" {
					return opTyp{te: &ast.SelectorExpr{X: x.X, Sel: ast.NewIdent(sel)}}
				}
			}
			if uc, ok := v.(*runtime.UConst); ok && uc.V != nil {
				return opTyp{untyped: true, dte: constKindDte(uc)}
			}
		}
	}
	if p.Index != nil {
		return c.srcMemberOpTyp(p, x.Sel.Name)
	}
	return opTyp{}
}

// srcMemberOpTyp resolves a source-indexed package member's static type
// from its declaration — the package never ran, so Globals has nothing.
// A declared type (`const C T`, `var V T`) scopes into the caller's
// view like scopeSpecType does for type specs; an inferred one takes
// the value expression's static shape once its names are re-scoped.
// Functions answer their signature, types used as values stay opaque.
func (c *compiler) srcMemberOpTyp(p *runtime.Package, name string) opTyp {
	if d := p.Index.Consts[name]; d != nil {
		return c.valueSpecMemberOpTyp(p, d, true)
	}
	if d := p.Index.Vars[name]; d != nil {
		return c.valueSpecMemberOpTyp(p, d, false)
	}
	if d := p.Index.Funcs[name]; d != nil && d.Func != nil && d.Func.Type != nil {
		if te := c.scopeSpecType(p, d.File, d.Func.Type); te != nil {
			return opTyp{te: te}
		}
	}
	return opTyp{}
}
