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
	"reflect"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/internal/source"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// noteDeclTyp records the statically-known type expression a var/param
// name was declared with (or inferrably bound to by `:=`), so the
// operand-assignability gate can compare named types at compile time.
func (s *fscope) noteDeclTyp(name string, te ast.Expr, file *syntax.File) {
	if len(s.blocks) == 0 || te == nil {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.declTyp = te
		b.declTypFile = file
	}
}

// noteDeferTyp records that a name's declared type is a deferred fact —
// the value expression to ask about at run time.
func (s *fscope) noteDeferTyp(name string, x ast.Expr) {
	if len(s.blocks) == 0 || x == nil {
		return
	}
	if b := s.blocks[len(s.blocks)-1][name]; b != nil {
		b.declDefer = x
	}
}

// isIfaceTypeExpr reports whether e syntactically names an interface
// type: `interface{...}`, `any`, `error`, a local `type I interface{}`
// decl, or a package typedef whose spec is an interface. Used to pick
// pair-strict equality for interface-typed switch tags.
func (c *compiler) isIfaceTypeExpr(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.InterfaceType:
		return true
	case *ast.ParenExpr:
		return c.isIfaceTypeExpr(t.X)
	case *ast.Ident:
		info, found := c.resolveName(t.Name)
		return found && info.iface
	}
	return false
}

// isIfaceExpr reports whether e statically yields an interface value:
// a conversion to an interface type (`any(x)`, `interface{}(x)`,
// `error(v)`) or a type assert to one (`v.(I)`).
func (c *compiler) isIfaceExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.isIfaceExpr(x.X)
	case *ast.CallExpr:
		if len(x.Args) == 1 && c.isIfaceTypeExpr(x.Fun) {
			return true // a conversion to an interface type — any(x)
		}
		// a call whose declared result is interface-typed: `f() == x`
		// compares dynamically like `a == x` on `var a any`; reading it
		// as a plain binary op would reject differently-typed operands
		// where Go's interface equality answers.
		switch fn := x.Fun.(type) {
		case *ast.FuncLit:
			if rs := fn.Type.Results; rs != nil && len(rs.List) == 1 {
				return c.isIfaceTypeExpr(rs.List[0].Type)
			}
		case *ast.Ident:
			if c.fs.lookupBinding(fn.Name) == nil && c.pkg != nil && c.pkg.Index != nil {
				if fd := c.pkg.Index.Funcs[fn.Name]; fd != nil && fd.Func != nil {
					if rs := fd.Func.Type.Results; rs != nil && len(rs.List) == 1 {
						return c.isIfaceTypeExpr(rs.List[0].Type)
					}
				}
			}
		}
		return false
	case *ast.TypeAssertExpr:
		return x.Type != nil && c.isIfaceTypeExpr(x.Type)
	case *ast.Ident:
		if c.fs.isIfaceVar(x.Name) {
			return true
		}
		// a package-level `var x any [= iface-typed init]` marks it too —
		// global names resolve through the index, not the local scopes.
		// Only when no local binding declares the name: a shadowing
		// `var s []int` must not inherit a global `var s any`'s mark.
		if c.fs.lookupBinding(x.Name) != nil {
			return false
		}
		if c.pkg != nil && c.pkg.Index != nil {
			if vd := c.pkg.Index.Vars[x.Name]; vd != nil {
				if vs, ok := vd.Spec.(*ast.ValueSpec); ok {
					if vs.Type != nil && c.isIfaceTypeExpr(vs.Type) {
						return true
					}
					if vs.Type == nil && len(vs.Values) > 0 {
						vi := vd.NameIdx
						if len(vs.Values) == 1 {
							vi = 0
						}
						if vi < len(vs.Values) && c.isIfaceExpr(vs.Values[vi]) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// unaliasTypExpr follows alias declarations (`type A = uint8`) to the
// aliased type expression; any other type expression returns as is.
func (c *compiler) unaliasTypExpr(te ast.Expr, depth int) ast.Expr {
	for ; depth > 0; depth-- {
		id, ok := ast.Unparen(te).(*ast.Ident)
		if !ok {
			return te
		}
		info, found := c.resolveName(id.Name)
		if !found || info.tspec == nil || !info.tspec.Assign.IsValid() {
			return te
		}
		te = info.tspec.Type
	}
	return te
}

// opTyp is an operand's statically-known type picture for the
// assignability gate (opCmpGate): te is the declared type expression
// when one is statically known, untyped marks an untyped constant (it
// adopts the other operand's type), and iface marks an interface-typed
// operand (equality compares dynamic pairs — any two types may
// answer). A zero opTyp means "unknown": the gate leaves the op
// evaluating.
type opTyp struct {
	te         ast.Expr
	untyped    bool
	iface      bool
	nilOperand bool // the literal `nil` — untyped, but only nilable types adopt it
	// dte is an untyped operand's default type expression — the type it
	// binds at `x := e` — so a later comparison gates on the bound
	// type (`x := 1 + 2` is int, `x := 'a'` is rune) rather than
	// staying adoption-shaped forever.
	dte ast.Expr
	// deferred marks an operand whose static type is a deferred fact
	// (a package member the index couldn't answer): callers emit an
	// OpTypGate CallSite that resolves it at run time instead of
	// guessing unknown and dropping the fact. dexpr is the value
	// expression the deferred question asks about — the operand
	// itself, or the initializer a binding inherited the deferral
	// from (`var y = p.C` asks about p.C, not y).
	deferred bool
	dexpr    ast.Expr
	// elLit is a `[...]T` composite literal whose length the compile
	// time fold could not resolve — callers that must not evaluate the
	// operand (unsafe.Sizeof/Alignof) emit the key positions for a
	// runtime fold instead of evaluating the elements.
	elLit *ast.CompositeLit
	// elPos, parallel to elLit.Elts, bakes the key positions the
	// decl-scope fold could still see at capture time: a later emit
	// may run in a scope that shadows the literal's key names, so a
	// foldable key becomes an int const instead of an emitted expr.
	elPos []constant.Value
	// elFile is the file a package-var literal was declared in —
	// set only when the literal is re-derived at a use site. Keys
	// that did not fold emit an OpEvalAST fragment over it so their
	// names resolve in the decl's package scope rather than in the
	// use site's (a local shadow must not rebind them).
	elFile *syntax.File
	// teFile is the file a package decl spelled te in — set when the
	// operand's type re-derives from a package var spec, so a
	// use-site local type or const of the same name cannot shadow
	// the decl's spelling.
	teFile *syntax.File
	// maybeUntyped marks a deferred operand whose package is not yet
	// materialized (IndexedOnly): compile cannot tell whether the
	// member is an untyped const, so the run-time gate treats an
	// untyped-const resolution as adopting, a typed one as gated.
	maybeUntyped bool
}

// staticOpTyp resolves the statically-known type of an operand
// expression. Only shapes whose type is written or directly inferrable
// resolve — declared vars/params/consts, fields of known named structs,
// conversions, single-result calls of declared functions, builtins with
// fixed result types — everything else is "unknown" and the gate stays
// silent, matching the pre-gate evaluation.
func (c *compiler) staticOpTyp(e ast.Expr) opTyp {
	if e == nil || c.opTypDepth >= 8 {
		return opTyp{}
	}
	// package-level var/const initializers feed back through identOpTyp
	// (`const a = b`) — the depth cap bounds a named cycle.
	c.opTypDepth++
	defer func() { c.opTypDepth-- }()
	if c.isIfaceExpr(e) {
		return opTyp{iface: true}
	}
	switch x := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return opTyp{untyped: true, dte: defaultLitTyp(x)}
	case *ast.Ident:
		return c.identOpTyp(x)
	case *ast.SelectorExpr:
		return c.selOpTyp(x)
	case *ast.CallExpr:
		return c.callOpTyp(x)
	case *ast.IndexExpr:
		return opTyp{te: c.elemTypExpr(c.staticOpTyp(x.X).te, 4)}
	case *ast.SliceExpr:
		// a subslice keeps its type (so does a substring) — except an
		// array operand, which reads a slice of its elements, and so
		// does *A[:] on a pointer to array.
		ot := c.staticOpTyp(x.X)
		ue := ast.Unparen(c.unfoldTypExpr(ot.te, 4))
		if st, ok := ue.(*ast.StarExpr); ok {
			ue = ast.Unparen(c.unfoldTypExpr(st.X, 3))
		}
		if at, ok := ue.(*ast.ArrayType); ok && at.Len != nil {
			return opTyp{te: &ast.ArrayType{Lbrack: at.Lbrack, Elt: at.Elt}}
		}
		return ot
	case *ast.StarExpr:
		// *p dereferences a pointer-typed operand
		return opTyp{te: c.starBaseTypExpr(c.staticOpTyp(x.X).te, 4)}
	case *ast.TypeAssertExpr:
		if x.Type == nil {
			return opTyp{} // a type switch's guard, not an operand
		}
		return opTyp{te: x.Type}
	case *ast.UnaryExpr:
		switch x.Op {
		case token.NOT:
			// a negated constant stays an untyped bool — it adopts
			// into a named operand (`f == !false`).
			xt := c.staticOpTyp(x.X)
			if xt.untyped {
				return opTyp{te: &ast.Ident{Name: "bool"}, untyped: true, dte: &ast.Ident{Name: "bool"}}
			}
			if te := c.namedBoolTyp(xt); te != nil {
				return opTyp{te: te} // !v keeps a named bool's type
			}
			return opTyp{te: &ast.Ident{Name: "bool"}}
		case token.SUB, token.ADD, token.XOR:
			return c.staticOpTyp(x.X) // -x/+x/^x keeps the operand's type
		case token.ARROW:
			return opTyp{te: c.chanElemTypExpr(c.staticOpTyp(x.X).te, 4)}
		case token.AND:
			// &x answers a pointer to x's static type — `i == &d`
			// mismatches int and *D. An operand without a static
			// type keeps the address opaque.
			if t := c.staticOpTyp(x.X); t.te != nil {
				return opTyp{te: &ast.StarExpr{X: t.te}}
			}
			return opTyp{}
		}
		return opTyp{}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			// a comparison's result is always an untyped bool — it
			// adopts into a named operand (`f == (x < y)` with
			// `type B bool` is a B), constant operands or not.
			return opTyp{te: &ast.Ident{Name: "bool"}, untyped: true, dte: &ast.Ident{Name: "bool"}}
		case token.LAND, token.LOR:
			// a constant-only predicate is itself an untyped bool
			// constant — it adopts into a named operand (`f && (1 ==
			// 1)`), like any untyped literal.
			ux := c.staticOpTyp(x.X)
			uy := c.staticOpTyp(x.Y)
			if (ux.untyped || ux.nilOperand) && (uy.untyped || uy.nilOperand) {
				return opTyp{te: &ast.Ident{Name: "bool"}, untyped: true, dte: &ast.Ident{Name: "bool"}}
			}
			// `v && true` with `type B bool` is a B.
			if te := c.logicalNamedTyp(x); te != nil {
				return opTyp{te: te}
			}
			return opTyp{te: &ast.Ident{Name: "bool"}}
		}
		// a shift types its result by the left operand alone — the
		// count's type never participates (`1 << uint(max)` is not
		// uint). An untyped left operand stays untyped either way:
		// with a constant count the whole shift is an untyped constant
		// (`1 << _W` in math/big), and with a non-constant count the
		// spec converts it to "the type it would assume if the shift
		// expression were replaced by its left operand alone" — the
		// other operand's type in `m & (1 << n)` (io/fs's
		// FileMode.String), int only when nothing else types it. Both
		// adopt into the other operand, so the gate stays silent, and
		// a binding takes the default type.
		if x.Op == token.SHL || x.Op == token.SHR {
			lt := c.staticOpTyp(x.X)
			if lt.te != nil {
				return lt
			}
			if lt.untyped {
				return opTyp{untyped: true, dte: lt.dte}
			}
			return opTyp{} // an unknown left operand types the shift unknown
		}
		// arithmetic adopts the operand type: a named side wins; an
		// untyped side gives way (`d + 1` is Duration, `1 + i` is int).
		ux := c.staticOpTyp(x.X)
		if ux.te != nil {
			return ux
		}
		uy := c.staticOpTyp(x.Y)
		if uy.te != nil {
			return uy
		}
		if ux.untyped && uy.untyped {
			// both sides constant: the result stays untyped with the
			// higher-rank kind (`1 + 'a'` is a rune constant, `1 +
			// 1.5` a float) — the bind-time default follows it.
			return opTyp{untyped: true, dte: mergeUntypedDte(ux.dte, uy.dte)}
		}
		// an unknown side keeps the whole expression unknown — `f()
		// + 1` types by f's result, which the static walk can't see.
		return opTyp{}
	case *ast.CompositeLit:
		if at, ok := x.Type.(*ast.ArrayType); ok {
			if el, isEl := at.Len.(*ast.Ellipsis); isEl {
				// [...]T{...}: the declared length is the highest
				// position + 1 (or the element count) — the ellipsis
				// itself carries none. Keys are constant expressions,
				// so positions fold at compile time through
				// scopeConstValue (named consts, hex, arithmetic) and
				// the literal's elements never evaluate.
				// -2 folds in the literal's package scope (a package
				// var's initializer re-derived at a use site whose
				// locals must not shadow the decl's names); -1 folds
				// in the literal's own scope.
				mode := -1
				if c.pkgScopeFold {
					mode = -2
				}
				keyPos := func(e ast.Expr) (int64, bool) {
					cv, _, ok := c.scopeConstVal(e, 0, mode)
					if !ok || cv.Kind() != constant.Int {
						return 0, false
					}
					k, ok := constant.Int64Val(cv)
					if !ok || k < 0 {
						return 0, false
					}
					return k, true
				}
				n, next := int64(0), int64(0)
				known := true
				for _, e := range x.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						k, ok := keyPos(kv.Key)
						if !ok {
							known = false
							break
						}
						next = k + 1
					} else {
						next++
					}
					if next > n {
						n = next
					}
				}
				if !known {
					// a key needing scope the compile fold cannot see
					// (an imported const, a typed conversion) still
					// must not evaluate the elements — the caller
					// emits the key positions for a runtime fold.
					// Keys that DO fold here bake their positions:
					// the emit site may sit in a scope that shadows
					// the names the literal's keys resolve through.
					elPos := make([]constant.Value, len(x.Elts))
					for i, e := range x.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							if k, ok := keyPos(kv.Key); ok {
								elPos[i] = constant.MakeInt64(k)
							}
						}
					}
					return opTyp{te: x.Type, elLit: x, elPos: elPos}
				}
				return opTyp{te: &ast.ArrayType{
					Lbrack: at.Lbrack,
					Len:    &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10), ValuePos: el.Ellipsis},
					Elt:    at.Elt,
				}}
			}
		}
		return opTyp{te: x.Type}
	case *ast.FuncLit:
		return opTyp{te: x.Type}
	}
	return opTyp{}
}

// identOpTyp resolves an identifier operand's static type: the declared
// or inferrably-recorded type of a local binding, then package-level
// var/const specs. Untyped consts and untyped predeclareds report
// untyped; a type parameter or an inferred shape reports unknown.
func (c *compiler) identOpTyp(id *ast.Ident) opTyp {
	if b := c.fs.lookupBinding(id.Name); b != nil {
		if b.untypedConst {
			return opTyp{untyped: true, dte: b.dte}
		}
		if b.declDefer != nil {
			return opTyp{deferred: true, dexpr: b.declDefer}
		}
		if dc, ok := c.deferCallTemps[id.Name]; ok {
			// an undecidable member call's scratch: its type is the
			// call's own deferred question (`p.T(v)` -> T, or the
			// function's result type — the runtime gate resolves it).
			return opTyp{deferred: true, dexpr: dc.call}
		}
		return opTyp{te: b.declTyp, teFile: b.declTypFile}
	}
	if _, bound := c.binds[id.Name]; bound {
		return opTyp{} // a bound type parameter — its value is opaque here
	}
	// A package-level decl shadows the universe block — `const true =
	// 'a'` binds `true` as a rune here, so index before the literals.
	if c.pkg != nil && c.pkg.Index != nil {
		if vd := c.pkg.Index.Vars[id.Name]; vd != nil {
			if vs, ok := vd.Spec.(*ast.ValueSpec); ok {
				if vs.Type != nil {
					return opTyp{te: vs.Type, teFile: vd.File}
				}
				// `var x = e` — the initializer at the name's own slot
				// types it. An untyped initializer binds its literal
				// default, like noteDeclTyp's in-function path
				// (`var x = 5` is int).
				if len(vs.Values) == len(vs.Names) && vd.NameIdx < len(vs.Values) {
					rhs := vs.Values[vd.NameIdx]
					// the initializer's names live in the package's
					// scope — a local binding at the use site must
					// not shadow what the decl's keys resolve to.
					c.pkgScopeFold = true
					t := c.staticOpTyp(rhs)
					c.pkgScopeFold = false
					if !t.untyped {
						// the literal's own decl file wins over
						// a copying var's — `var b = a` in
						// another file still evaluates a's keys
						// under a's imports.
						if t.elLit != nil && t.elFile == nil {
							t.elFile = vd.File
						}
						return t
					}
					// an untyped initializer binds its constant's
					// default type (`var x = 1 + 2` is int like gc).
					return opTyp{te: t.dte}
				}
				// `var a, b = f()` / `var v, ok = m[k]` — name i types
				// by the call's i-th result; a comma-ok second name is
				// bool, the first carries the operand's static type.
				if len(vs.Values) == 1 && len(vs.Names) > 1 {
					if t := c.nthResultOpTyp(vs.Values[0], vd.NameIdx); t.te != nil {
						return t
					}
					if vd.NameIdx == 1 && commaOkStatic(vs.Values[0]) {
						return opTyp{te: &ast.Ident{Name: "bool"}}
					}
					if vd.NameIdx == 0 {
						if t := c.staticOpTyp(vs.Values[0]); t.te != nil {
							return t
						}
					}
				}
			}
			return opTyp{}
		}
		if cd := c.pkg.Index.Consts[id.Name]; cd != nil {
			if vs, ok := cd.Spec.(*ast.ValueSpec); ok {
				t := vs.Type
				if t == nil {
					t = cd.InheritedType
				}
				if t != nil {
					return opTyp{te: t}
				}
				// the const's own value — a carried spec (`const (a =
				// 1; b)`) reads the inherited list at the name's slot.
				var v ast.Expr
				switch {
				case len(vs.Values) == len(vs.Names) && cd.NameIdx < len(vs.Values):
					v = vs.Values[cd.NameIdx]
				case cd.NameIdx < len(cd.Inherited):
					v = cd.Inherited[cd.NameIdx]
				}
				if v != nil {
					// `const c = Duration(0)` — a typed initializer
					// still types the const; a genuinely untyped one
					// adopts but keeps its kind's default type.
					t := c.staticOpTyp(v)
					if t.te != nil {
						return t
					}
					if t.untyped {
						return opTyp{untyped: true, dte: t.dte}
					}
				}
			}
			return opTyp{untyped: true}
		}
	}
	switch id.Name {
	case "true", "false":
		return opTyp{untyped: true, dte: &ast.Ident{Name: "bool"}}
	case "iota":
		return opTyp{untyped: true, dte: &ast.Ident{Name: "int"}}
	case "nil":
		return opTyp{nilOperand: true}
	}
	return opTyp{}
}

// selOpTyp resolves a selector operand's static type: a field of a
// known named struct type. A package-qualified member (`time.Sunday`),
// a method value, or an unknown base reports unknown.
func (c *compiler) selOpTyp(x *ast.SelectorExpr) opTyp {
	if id, ok := x.X.(*ast.Ident); ok {
		// a closer declaration shadows the import name — `v.F` then
		// reads the local's field, never a package member.
		if c.fs.lookupBinding(id.Name) == nil {
			if ref, isPkg := c.importRef(id.Name); isPkg {
				return c.selPkgMemberOpTyp(x, ref)
			}
		}
	}
	base := c.staticOpTyp(x.X)
	if base.te == nil || base.untyped || base.iface {
		return opTyp{}
	}
	te := c.fieldTypExpr(base.te, x.Sel.Name, 4)
	if te == nil {
		// a selector through a pointer reads the pointee's fields
		// the same way — `p.A` on a *Pair is Pair's A.
		if sb := c.starBaseTypExpr(base.te, 4); sb != nil {
			te = c.fieldTypExpr(sb, x.Sel.Name, 4)
		}
	}
	if te == nil {
		return opTyp{}
	}
	if c.isIfaceTypeExpr(te) {
		return opTyp{iface: true}
	}
	return opTyp{te: te}
}

// valueSpecMemberOpTyp types one name of a foreign package's var/const
// spec: the written type when present (carried specs reuse the
// inherited type like valueSpec), else the value expression at the
// name's slot. Both read through scopeOperandExpr/scopeSpecType so the
// names inside resolve with the declaring file's scope.
func (c *compiler) valueSpecMemberOpTyp(p *runtime.Package, d *index.Decl, isConst bool) opTyp {
	vs, ok := d.Spec.(*ast.ValueSpec)
	if !ok {
		return opTyp{}
	}
	effType := vs.Type
	var val ast.Expr
	if isConst && len(vs.Values) == 0 {
		effType = d.InheritedType
		if d.NameIdx < len(d.Inherited) {
			val = d.Inherited[d.NameIdx]
		}
	} else if d.NameIdx < len(vs.Values) {
		val = vs.Values[d.NameIdx]
	}
	if effType != nil {
		te := c.scopeSpecType(p, d.File, effType)
		if te == nil {
			return opTyp{}
		}
		if c.isIfaceTypeExpr(te) {
			return opTyp{iface: true}
		}
		return opTyp{te: te}
	}
	if val == nil {
		return opTyp{}
	}
	scoped := c.scopeOperandExpr(p, d.File, val)
	if scoped == nil {
		return opTyp{}
	}
	t := c.staticOpTyp(scoped)
	if !isConst && t.untyped {
		// a var binds an untyped initializer's default type like gc —
		// `var V = 'q'` declares a rune, so `V == i` mismatches.
		return opTyp{te: t.dte}
	}
	return t
}

// scopeOperandExpr is scopeSpecType for a package-level VALUE
// expression (a var/const spec's initializer): an ident naming the
// declaring package's own top-level decl qualifies through the caller's
// import name for that package, and a selector's qualifier binds the
// declaring file's own import ref via synthImportName — the same
// qualifier in the caller may name a different package, or none.
// nil means a name cannot be
// resolved without the declaring scope (a dot import, a foreign
// constant chain the depth cap cut off): the operand stays opaque
// rather than risk reading a caller-side name. A FuncLit scopes its
// signature only — its body's names belong to the function's own
// scope, which static typing never inspects.
func (c *compiler) scopeOperandExpr(p *runtime.Package, sf *syntax.File, e ast.Expr) ast.Expr {
	if e == nil {
		return nil
	}
	switch t := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return t
	case *ast.Ident:
		if p == c.pkg {
			return t
		}
		// a declaring-package decl shadows the universe block — `const
		// true = 'a'` binds `true` there, so the index wins.
		if p.Index.Consts[t.Name] != nil || p.Index.Vars[t.Name] != nil ||
			p.Index.Funcs[t.Name] != nil || p.Index.Types[t.Name] != nil {
			return &ast.SelectorExpr{X: &ast.Ident{Name: c.callerImportName(p.Path)}, Sel: ast.NewIdent(t.Name)}
		}
		if universeIdent(t.Name) {
			// the bare ident reads the CALLER's scope back — keep it
			// only when the caller cannot rebind it to a closer or
			// package-level decl that differs from the universe one.
			if !c.callerShadows(t.Name) {
				return t
			}
		}
		return nil
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if ref, ok := c.pkgImportRef(p, sf, id.Name); ok {
				return &ast.SelectorExpr{X: &ast.Ident{Name: c.synthImportName(ref)}, Sel: t.Sel}
			}
		}
		// `v.F` on the package's own value: scope the base, keep Sel.
		if x := c.scopeOperandExpr(p, sf, t.X); x != nil {
			return &ast.SelectorExpr{X: x, Sel: t.Sel}
		}
		return nil
	case *ast.UnaryExpr:
		if x := c.scopeOperandExpr(p, sf, t.X); x != nil {
			return &ast.UnaryExpr{OpPos: t.OpPos, Op: t.Op, X: x}
		}
		return nil
	case *ast.BinaryExpr:
		x := c.scopeOperandExpr(p, sf, t.X)
		y := c.scopeOperandExpr(p, sf, t.Y)
		if x != nil && y != nil {
			return &ast.BinaryExpr{X: x, OpPos: t.OpPos, Op: t.Op, Y: y}
		}
		return nil
	case *ast.CallExpr:
		fun := c.scopeOperandExpr(p, sf, t.Fun)
		if fun == nil {
			return nil
		}
		args := make([]ast.Expr, len(t.Args))
		for i, a := range t.Args {
			args[i] = c.scopeOperandExpr(p, sf, a)
			if args[i] == nil {
				return nil
			}
		}
		return &ast.CallExpr{Fun: fun, Lparen: t.Lparen, Args: args, Ellipsis: t.Ellipsis, Rparen: t.Rparen}
	case *ast.IndexExpr:
		x := c.scopeOperandExpr(p, sf, t.X)
		i := c.scopeOperandExpr(p, sf, t.Index)
		if x != nil && i != nil {
			return &ast.IndexExpr{X: x, Lbrack: t.Lbrack, Index: i, Rbrack: t.Rbrack}
		}
		return nil
	case *ast.IndexListExpr:
		x := c.scopeOperandExpr(p, sf, t.X)
		if x == nil {
			return nil
		}
		indices := make([]ast.Expr, len(t.Indices))
		for i, ix := range t.Indices {
			indices[i] = c.scopeOperandExpr(p, sf, ix)
			if indices[i] == nil {
				return nil
			}
		}
		return &ast.IndexListExpr{X: x, Lbrack: t.Lbrack, Indices: indices, Rbrack: t.Rbrack}
	case *ast.SliceExpr:
		x := c.scopeOperandExpr(p, sf, t.X)
		l := c.scopeOperandExpr(p, sf, t.Low)
		h := c.scopeOperandExpr(p, sf, t.High)
		m := c.scopeOperandExpr(p, sf, t.Max)
		if x != nil {
			return &ast.SliceExpr{X: x, Lbrack: t.Lbrack, Low: l, High: h, Max: m, Slice3: t.Slice3, Rbrack: t.Rbrack}
		}
		return nil
	case *ast.StarExpr:
		if x := c.scopeOperandExpr(p, sf, t.X); x != nil {
			return &ast.StarExpr{Star: t.Star, X: x}
		}
		return nil
	case *ast.KeyValueExpr:
		k := c.scopeOperandExpr(p, sf, t.Key)
		v := c.scopeOperandExpr(p, sf, t.Value)
		if k != nil && v != nil {
			return &ast.KeyValueExpr{Key: k, Colon: t.Colon, Value: v}
		}
		return nil
	case *ast.CompositeLit:
		te := c.scopeSpecType(p, sf, t.Type)
		elts := make([]ast.Expr, len(t.Elts))
		for i, el := range t.Elts {
			elts[i] = c.scopeOperandExpr(p, sf, el)
			if elts[i] == nil {
				return nil
			}
		}
		return &ast.CompositeLit{Type: te, Lbrace: t.Lbrace, Elts: elts, Rbrace: t.Rbrace, Incomplete: t.Incomplete}
	case *ast.TypeAssertExpr:
		x := c.scopeOperandExpr(p, sf, t.X)
		if x == nil {
			return nil
		}
		return &ast.TypeAssertExpr{X: x, Lparen: t.Lparen, Type: c.scopeSpecType(p, sf, t.Type), Rparen: t.Rparen}
	case *ast.FuncLit:
		te, _ := c.scopeSpecType(p, sf, t.Type).(*ast.FuncType)
		if te == nil {
			return nil
		}
		return &ast.FuncLit{Type: te, Body: t.Body}
	case *ast.Ellipsis:
		if x := c.scopeOperandExpr(p, sf, t.Elt); x != nil {
			return &ast.Ellipsis{Ellipsis: t.Ellipsis, Elt: x}
		}
		return nil
	}
	return nil
}

// callerShadows reports whether a name read bare in a foreign
// package's value expression would pick up a NON-universe meaning in
// the caller's scope — a local binding, a bound type parameter, or a
// package-level decl. Such names cannot be carried through as idents.
func (c *compiler) callerShadows(name string) bool {
	if c.fs.lookupBinding(name) != nil {
		return true
	}
	if _, bound := c.binds[name]; bound {
		return true
	}
	if c.pkg != nil && c.pkg.Index != nil {
		return c.pkg.Index.Consts[name] != nil || c.pkg.Index.Vars[name] != nil ||
			c.pkg.Index.Funcs[name] != nil || c.pkg.Index.Types[name] != nil
	}
	return false
}

// qualTypSel reports the member name a tagged bound value's typedef
// spells inside its own package — "time.Month" yields "Month" when the
// tag belongs to the package at hand. A foreign or unqualified tag
// keeps the operand opaque rather than risk a wrong spelling.
func qualTypSel(p *runtime.Package, td *runtime.TypeDef) string {
	if td == nil || p == nil {
		return ""
	}
	if sel, ok := strings.CutPrefix(td.Name, p.Name+"."); ok {
		return sel
	}
	return ""
}

// hostScalarTypeDefOf finds the package typedef whose HostScalar Go
// type a raw bound value carries — `time.Second` binds a bare
// time.Duration, not a Named, so its declared-type tag comes from
// matching the host type back to the package's typedef table.
func hostScalarTypeDefOf(p *runtime.Package, v runtime.Value) *runtime.TypeDef {
	if gv, ok := v.(*runtime.GoValue); ok {
		v = gv.V
	}
	rt := reflect.TypeOf(v)
	if rt == nil {
		return nil
	}
	for _, name := range p.Globals.Names() {
		m, ok := p.Globals.Get(name)
		if !ok {
			continue
		}
		td, ok := m.(*runtime.TypeDef)
		if !ok || td.HostScalar == nil {
			continue
		}
		if reflect.TypeOf(td.HostScalar) == rt {
			return td
		}
	}
	return nil
}

// callOpTyp resolves a call operand's static type: conversions spell
// their target type, selected builtins have fixed result types, and a
// declared function or method contributes its single result type.
// Generic instantiations, multi-result calls, and host functions report
// unknown.
func (c *compiler) callOpTyp(x *ast.CallExpr) opTyp {
	if c.conversionCall(x) {
		return opTyp{te: x.Fun}
	}
	switch f := x.Fun.(type) {
	case *ast.Ident:
		// a local binding shadows the package function — `g := func()
		// T() {...}` answers its own signature, not g's.
		if b := c.fs.lookupBinding(f.Name); b != nil {
			if ft, ok := ast.Unparen(b.declTyp).(*ast.FuncType); ok {
				return c.funcTypeResultOpTyp(ft)
			}
			return opTyp{}
		}
		// a declared function resolves its result type regardless of
		// arity — the args gate below exists for builtins like len()
		// whose zero-arg form has no meaningful static result.
		if ft := c.calleeFuncTyp(x); ft != nil {
			return c.funcTypeResultOpTyp(ft)
		}
		if len(x.Args) == 0 {
			return opTyp{}
		}
		// builtin result types apply only when no nearer decl shadows
		// the name (a local `len` variable, a package func `len`).
		if _, found := c.resolveName(f.Name); !found {
			switch f.Name {
			case "len", "cap", "copy":
				return opTyp{te: &ast.Ident{Name: "int"}}
			case "new":
				return opTyp{te: &ast.StarExpr{Star: f.Pos(), X: x.Args[0]}}
			case "make":
				return opTyp{te: x.Args[0]} // make's first arg is a type expr
			case "min", "max":
				// gc: the result takes the first typed argument's
				// type — untyped constants convert to it — or
				// stays untyped when every argument is one.
				var t opTyp
				for _, a := range x.Args {
					at := c.staticOpTyp(a)
					if !at.untyped {
						return at
					}
					if t.dte == nil {
						t = at
					}
				}
				return t
			case "append":
				return c.staticOpTyp(x.Args[0])
			case "recover":
				return opTyp{iface: true}
			case "real", "imag":
				// constant args keep the result an untyped
				// constant; a typed complex arg's component type
				// is left undecided here.
				if !c.argsAllUntyped(x.Args) {
					return opTyp{}
				}
				return opTyp{untyped: true, dte: &ast.Ident{Name: "float64"}}
			case "complex":
				if !c.argsAllUntyped(x.Args) {
					return opTyp{}
				}
				return opTyp{untyped: true, dte: &ast.Ident{Name: "complex128"}}
			}
		}
		return opTyp{}
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if ref, isPkg := c.importRef(id.Name); isPkg {
				// an unindexed package leaves the member question
				// open: `pkg.T(v)` is a conversion whose result
				// carries T's type, `pkg.fn(v)` a call whose result
				// stays opaque — the run-time gate resolves which
				// once the member materializes, and a non-type
				// answer there declines like today.
				if c.askIndexed(ref).Kind != semantics.Found {
					return opTyp{deferred: true, dexpr: x}
				}
				return opTyp{} // a host-package function's result is opaque
			}
		}
		// a method call on a known named type resolves to its decl.
		if ft := c.calleeFuncTyp(x); ft != nil {
			return c.funcTypeResultOpTyp(ft)
		}
		return opTyp{}
	case *ast.IndexExpr, *ast.IndexListExpr:
		// generic instantiation: the result type mentions type params.
		return opTyp{}
	}
	return opTyp{}
}

// argsAllUntyped reports whether every argument reads as an untyped
// constant — the case where real/imag/complex keep their result
// untyped.
func (c *compiler) argsAllUntyped(args []ast.Expr) bool {
	if len(args) == 0 {
		return false
	}
	for _, a := range args {
		if ot := c.staticOpTyp(a); ot.untyped {
			continue
		}
		return false
	}
	return true
}

// funcTypeResultOpTyp resolves a signature to its single result type —
// only a single-result signature can type an operand; a result spelling
// a declared type parameter is opaque (each instantiation binds it, the
// static name doesn't).
func (c *compiler) funcTypeResultOpTyp(ft *ast.FuncType) opTyp {
	if ft == nil || ft.Results == nil || len(ft.Results.List) != 1 {
		return opTyp{}
	}
	res := ft.Results.List[0]
	if len(res.Names) > 1 {
		return opTyp{} // `(a, b T)` is a multi-result signature
	}
	te := res.Type
	if id, ok := ast.Unparen(te).(*ast.Ident); ok && funcTypeHasParam(ft, id.Name) {
		return opTyp{}
	}
	if c.isIfaceTypeExpr(te) {
		return opTyp{iface: true}
	}
	return opTyp{te: te}
}

// calleeFuncTyp resolves a call's callee to its signature: a declared
// function or method, or a local binding whose recorded type is a
// FuncType. Builtins, host-package functions, and opaque callees report
// nil. Shared by callOpTyp (result typing) and nthResultOpTyp
// (multi-result binding) so the callee walk lives in one place.
func (c *compiler) calleeFuncTyp(x *ast.CallExpr) *ast.FuncType {
	switch f := x.Fun.(type) {
	case *ast.Ident:
		if b := c.fs.lookupBinding(f.Name); b != nil {
			if ft, ok := ast.Unparen(b.declTyp).(*ast.FuncType); ok {
				return ft
			}
			return nil
		}
		if c.pkg != nil && c.pkg.Index != nil {
			if fd := c.pkg.Index.Funcs[f.Name]; fd != nil {
				return fd.Func.Type
			}
		}
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if _, isPkg := c.importRef(id.Name); isPkg {
				return nil
			}
		}
		base := c.staticOpTyp(f.X)
		if base.te == nil {
			return nil
		}
		if tdi := c.typeDeclOf(base.te); tdi != nil {
			if md := tdi.Methods[f.Sel.Name]; md != nil {
				return md.Func.Type
			}
		}
	}
	return nil
}

// nthResultOpTyp resolves the static type a multi-result call binds at
// position i — `a, b := f()` types a by result 0, b by result 1, like
// gc's signature-based typing. A non-call RHS, an opaque callee, or a
// result that spells a declared type parameter reports unknown.
func (c *compiler) nthResultOpTyp(e ast.Expr, i int) opTyp {
	x, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || c.conversionCall(x) {
		return opTyp{}
	}
	ft := c.calleeFuncTyp(x)
	if ft == nil || ft.Results == nil {
		return opTyp{}
	}
	if i == 0 && len(ft.Results.List) == 1 {
		// the single-result walk is the i=0 case.
		return c.funcTypeResultOpTyp(ft)
	}
	pos := 0
	for _, res := range ft.Results.List {
		arity := len(res.Names)
		if arity == 0 {
			arity = 1
		}
		if i < pos+arity {
			te := res.Type
			if id, ok := ast.Unparen(te).(*ast.Ident); ok && funcTypeHasParam(ft, id.Name) {
				return opTyp{}
			}
			if c.isIfaceTypeExpr(te) {
				return opTyp{iface: true}
			}
			return opTyp{te: te}
		}
		pos += arity
	}
	return opTyp{}
}

// funcTypeHasParam reports whether name is one of ft's declared type
// parameters — a result spelling it is opaque to the gate (each
// instantiation binds it differently).
func funcTypeHasParam(ft *ast.FuncType, name string) bool {
	if ft.TypeParams == nil {
		return false
	}
	for _, tp := range ft.TypeParams.List {
		for _, n := range tp.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// mergeUntypedDte resolves the default type of an untyped constant
// binary expression: gc keeps the kind with the highest rank — int <
// rune < float64 < complex128 — so `1 + 'a'` binds rune and `1 + 1.5`
// float64. Same-kind operands merge to that kind (string + string);
// anything else can't form a valid untyped constant anyway, so the
// expression reports no default.
func mergeUntypedDte(a, b ast.Expr) ast.Expr {
	an, _ := a.(*ast.Ident)
	bn, _ := b.(*ast.Ident)
	if an == nil || bn == nil {
		return nil
	}
	if an.Name == bn.Name {
		return a
	}
	rank := func(name string) int {
		switch name {
		case "int":
			return 1
		case "rune":
			return 2
		case "float64":
			return 3
		case "complex128":
			return 4
		}
		return 0
	}
	ra, rb := rank(an.Name), rank(bn.Name)
	if ra == 0 || rb == 0 {
		return nil
	}
	if ra > rb {
		return a
	}
	return b
}

// defaultLitTyp names an untyped literal's bind-time default type, like
// gc: 'a' is rune, 1.5 is float64, 1i is complex128.
func defaultLitTyp(lit *ast.BasicLit) *ast.Ident {
	switch lit.Kind {
	case token.INT:
		return &ast.Ident{Name: "int"}
	case token.FLOAT:
		return &ast.Ident{Name: "float64"}
	case token.IMAG:
		return &ast.Ident{Name: "complex128"}
	case token.CHAR:
		return &ast.Ident{Name: "rune"}
	case token.STRING:
		return &ast.Ident{Name: "string"}
	}
	return nil
}

// lenOperandArrayTyp resolves the static type of a len/cap operand to
// its underlying form, looking through one pointer: `len(arr)`, `len(p)`
// with p a *[N]T, and a field selector through a struct or a pointer to
// one (`len(w.a)` with w a *W, `len(s.b)` with s an anonymous struct).
// Nil when unknown.
func (c *compiler) lenOperandArrayTyp(arg ast.Expr) ast.Expr {
	te := c.staticOpTyp(arg).te
	if te == nil {
		if sel, ok := ast.Unparen(arg).(*ast.SelectorExpr); ok {
			te = c.structFieldTypExpr(c.staticOpTyp(sel.X).te, sel.Sel.Name)
		}
	}
	te = c.unfoldTypExpr(te, 8)
	if st, isPtr := te.(*ast.StarExpr); isPtr {
		te = c.unfoldTypExpr(st.X, 8)
	}
	return te
}

// structFieldTypExpr is fieldTypExpr that also sees through a pointer
// to the struct and accepts an anonymous struct type (direct fields
// only for the latter).
func (c *compiler) structFieldTypExpr(base ast.Expr, name string) ast.Expr {
	if base == nil {
		return nil
	}
	if st, isPtr := ast.Unparen(base).(*ast.StarExpr); isPtr {
		base = st.X
	}
	if te := c.fieldTypExpr(base, name, 4); te != nil {
		return te
	}
	st, ok := c.unfoldTypExpr(base, 8).(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return f.Type
			}
		}
	}
	return nil
}

// mayInvolveTypeParam reports whether an operand's static type may
// mention one of the bound type parameters — unknown counts as yes.
func (c *compiler) mayInvolveTypeParam(e ast.Expr) bool {
	te := c.staticOpTyp(e).te
	if te == nil {
		return true
	}
	found := false
	ast.Inspect(te, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if _, bound := c.binds[id.Name]; bound {
				found = true
			}
		}
		return !found
	})
	return found
}

// typeSpecOf resolves a type expression to its declaring TypeSpec — a
// local or package-level named type. Predeclared types, qualified host
// types, type parameters, and unnamed forms have no spec.
func (c *compiler) typeSpecOf(te ast.Expr) *ast.TypeSpec {
	id, ok := ast.Unparen(te).(*ast.Ident)
	if !ok {
		return nil
	}
	info, found := c.resolveName(id.Name)
	if !found || !info.isType {
		return nil
	}
	return info.tspec
}

// typeDeclOf resolves a type expression to its index TypeDeclInfo — the
// named type plus the methods declared on it (leading * stripped, since
// method sets include pointer receivers). Only package-level named
// types index methods; a shadowing local `type` has none.
func (c *compiler) typeDeclOf(te ast.Expr) *index.TypeDeclInfo {
	if c.pkg == nil || c.pkg.Index == nil {
		return nil
	}
	t := ast.Unparen(te)
	if s, ok := t.(*ast.StarExpr); ok {
		t = ast.Unparen(s.X)
	}
	id, ok := t.(*ast.Ident)
	if !ok {
		return nil
	}
	if b := c.fs.lookupBinding(id.Name); b != nil && b.typeDecl {
		return nil
	}
	return c.pkg.Index.Types[id.Name]
}

// unfoldTypExpr reduces a type expression to its underlying form: a
// named type unfolds through its spec (aliases pass straight through),
// parens unwrap; unnamed and unresolvable forms return as-is. The depth
// cap bounds named cycles (`type A []A`).
func (c *compiler) unfoldTypExpr(te ast.Expr, depth int) ast.Expr {
	if depth <= 0 || te == nil {
		return nil
	}
	t := ast.Unparen(te)
	switch tt := t.(type) {
	case *ast.Ident:
		if b := c.fs.lookupBinding(tt.Name); b != nil {
			if b.typeDecl && b.tspec != nil {
				// a function-local `type` decl — its spec names
				// things in this file's scope like the caller's.
				return c.unfoldTypExpr(b.tspec.Type, depth-1)
			}
			break // a var/const binding shadows the name — opaque
		}
		if _, isUp := c.fs.upmap[tt.Name]; isUp {
			break // a captured var — opaque
		}
		if _, bound := c.binds[tt.Name]; bound {
			break // a bound type parameter — opaque (resolveLitType peels it)
		}
		// A package-level spec's names resolve in its own package's
		// file scope, not the caller's.
		if ts := pkgTypeSpec(c.pkg, tt.Name); ts != nil {
			return c.unfoldSpecType(c.pkg, ts, depth-1)
		}
	case *ast.SelectorExpr:
		// pkg.T resolves through the imported package's source index —
		// `type FuncMap map[string]any` must expose its MapType shape so
		// a composite literal's keys evaluate as expressions.
		if ref, name, ok := c.selTypRef(tt); ok {
			if p, ts := c.selTypeSpec(ref, name); ts != nil {
				return c.unfoldSpecType(p, ts, depth-1)
			}
		}
	}
	return t
}

// unfoldSpecType continues unfoldTypExpr inside the package a type spec
// was declared in: an ident resolves through that package's index, a
// selector's qualifier through the imports of the spec's own file —
// never the caller's file, where the same qualifier may point at a
// different package. The leaf type keeps names of its declaring package,
// which the caller's scope could mis-resolve — scopeSpecType re-scopes
// them before the expr escapes.
func (c *compiler) unfoldSpecType(p *runtime.Package, ts *ast.TypeSpec, depth int) ast.Expr {
	if depth <= 0 || ts == nil {
		return nil
	}
	t := ast.Unparen(ts.Type)
	switch tt := t.(type) {
	case *ast.Ident:
		if ts2 := pkgTypeSpec(p, tt.Name); ts2 != nil {
			return c.unfoldSpecType(p, ts2, depth-1)
		}
	case *ast.SelectorExpr:
		if p2, ts2 := c.selTypeSpecIn(p, ts, tt); ts2 != nil {
			return c.unfoldSpecType(p2, ts2, depth-1)
		}
	}
	return c.scopeSpecType(p, specFileOf(p, ts.Pos()), t)
}

// scopeSpecType rewrites a spec's leaf type so its names resolve with
// the scope they were declared in, not the caller's. A bare ident
// naming a package-level type of p becomes pkg.T through the caller's
// import name for p — inside the caller's own package the ident
// already resolves there, so it stays bare. A selector's qualifier
// binds the spec file's own import ref via synthImportName — the same
// qualifier in the caller may name a different package, or none.
// Composite forms recurse; anything else returns as-is.
func (c *compiler) scopeSpecType(p *runtime.Package, sf *syntax.File, e ast.Expr) ast.Expr {
	switch t := ast.Unparen(e).(type) {
	case *ast.Ident:
		if p != c.pkg && pkgTypeSpec(p, t.Name) != nil {
			return &ast.SelectorExpr{X: &ast.Ident{Name: c.callerImportName(p.Path)}, Sel: &ast.Ident{Name: t.Name}}
		}
		return t
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if ref, ok := c.pkgImportRef(p, sf, id.Name); ok {
				return &ast.SelectorExpr{X: &ast.Ident{Name: c.synthImportName(ref)}, Sel: t.Sel}
			}
		}
		return t
	case *ast.ArrayType:
		if e := c.scopeSpecType(p, sf, t.Elt); e != t.Elt {
			return &ast.ArrayType{Lbrack: t.Lbrack, Len: t.Len, Elt: e}
		}
		return t
	case *ast.MapType:
		k, v := c.scopeSpecType(p, sf, t.Key), c.scopeSpecType(p, sf, t.Value)
		if k != t.Key || v != t.Value {
			return &ast.MapType{Map: t.Map, Key: k, Value: v}
		}
		return t
	case *ast.StarExpr:
		if x := c.scopeSpecType(p, sf, t.X); x != t.X {
			return &ast.StarExpr{Star: t.Star, X: x}
		}
		return t
	case *ast.ChanType:
		if v := c.scopeSpecType(p, sf, t.Value); v != t.Value {
			return &ast.ChanType{Begin: t.Begin, Arrow: t.Arrow, Dir: t.Dir, Value: v}
		}
		return t
	case *ast.Ellipsis:
		if e := c.scopeSpecType(p, sf, t.Elt); e != t.Elt {
			return &ast.Ellipsis{Ellipsis: t.Ellipsis, Elt: e}
		}
		return t
	case *ast.ParenExpr:
		if x := c.scopeSpecType(p, sf, t.X); x != t.X {
			return &ast.ParenExpr{Lparen: t.Lparen, X: x, Rparen: t.Rparen}
		}
		return t
	case *ast.StructType:
		if fs := c.scopeFieldList(p, sf, t.Fields); fs != t.Fields {
			return &ast.StructType{Struct: t.Struct, Fields: fs, Incomplete: t.Incomplete}
		}
		return t
	case *ast.InterfaceType:
		if ms := c.scopeFieldList(p, sf, t.Methods); ms != t.Methods {
			return &ast.InterfaceType{Interface: t.Interface, Methods: ms, Incomplete: t.Incomplete}
		}
		return t
	case *ast.FuncType:
		var dirty bool
		params, results := t.Params, t.Results
		if fs := c.scopeFieldList(p, sf, t.Params); fs != t.Params {
			params, dirty = fs, true
		}
		if fs := c.scopeFieldList(p, sf, t.Results); fs != t.Results {
			results, dirty = fs, true
		}
		if dirty {
			return &ast.FuncType{Func: t.Func, TypeParams: t.TypeParams, Params: params, Results: results}
		}
		return t
	case *ast.IndexExpr:
		if x := c.scopeSpecType(p, sf, t.X); x != t.X {
			return &ast.IndexExpr{X: x, Lbrack: t.Lbrack, Index: t.Index, Rbrack: t.Rbrack}
		}
		return t
	case *ast.IndexListExpr:
		if x := c.scopeSpecType(p, sf, t.X); x != t.X {
			return &ast.IndexListExpr{X: x, Lbrack: t.Lbrack, Indices: t.Indices, Rbrack: t.Rbrack}
		}
		return t
	}
	return e
}

// scopeFieldList re-scopes the types of one field list (struct fields,
// interface methods, func params/results), returning it unchanged when
// no field's type needed rewriting.
func (c *compiler) scopeFieldList(p *runtime.Package, sf *syntax.File, fl *ast.FieldList) *ast.FieldList {
	if fl == nil {
		return nil
	}
	var dirty bool
	list := make([]*ast.Field, len(fl.List))
	for i, f := range fl.List {
		nt := c.scopeSpecType(p, sf, f.Type)
		if nt != f.Type {
			dirty = true
			nf := *f
			nf.Type = nt
			list[i] = &nf
		} else {
			list[i] = f
		}
	}
	if !dirty {
		return fl
	}
	return &ast.FieldList{Opening: fl.Opening, List: list, Closing: fl.Closing}
}

// callerImportName returns the qualifier under which the caller's file
// reaches pkgPath — the import's local name. When the caller does not
// import the package the result is deliberately unresolvable: a bare
// path element like `time` could otherwise pick up a same-named caller
// decl, which belongs to a different scope entirely.
func (c *compiler) callerImportName(pkgPath string) string {
	if c.pkg != nil && c.file != nil {
		for name, ref := range c.pkg.Scopes[c.file] {
			if ref != nil && ref.Path == pkgPath && token.IsIdentifier(name) {
				return name
			}
		}
	}
	return "." + pkgPath
}

// synthImportName mints the qualifier a re-scoped expression uses for a
// foreign package's own import — the caller need not import that
// package at all, so the name binds the ref directly in synthImports
// (importRef resolves it) rather than re-spelling it through the
// caller's import list. The ".imp:" prefix can never collide with a
// declared identifier.
func (c *compiler) synthImportName(ref *runtime.ImportRef) string {
	name := ".imp:" + ref.Path
	if c.synthImports == nil {
		c.synthImports = map[string]*runtime.ImportRef{}
	}
	c.synthImports[name] = ref
	return name
}

// pkgDispName spells a package for gate diagnostics the way gc does —
// the package clause's name, not an import alias or a synthesized
// qualifier's minted text.
// semantics: fact (messages only) — semantics is unchanged, but the
// diagnostic wording must match gc's. When the lookup is Deferred the
// runtime trap prints the same resolved name.
func (c *compiler) pkgDispName(ref *runtime.ImportRef) string {
	if res := c.askIndexed(ref); res.Kind == semantics.Found && res.Val.Name != "" {
		return res.Val.Name
	}
	if i := strings.LastIndexByte(ref.Path, '/'); i >= 0 {
		return ref.Path[i+1:]
	}
	return ref.Path
}

// selTypeSpecIn resolves a pkg.T selector written inside a type spec of
// package p — the qualifier names an import of the spec's own file.
func (c *compiler) selTypeSpecIn(p *runtime.Package, ts *ast.TypeSpec, se *ast.SelectorExpr) (*runtime.Package, *ast.TypeSpec) {
	id, ok := se.X.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	sf := specFileOf(p, ts.Pos())
	if sf == nil {
		return nil, nil
	}
	ref, ok := c.pkgImportRef(p, sf, id.Name)
	if !ok {
		return nil, nil
	}
	return c.selTypeSpec(ref, se.Sel.Name)
}

// specFileOf returns the source file holding a declaration position,
// or nil when the package cannot map it back.
func specFileOf(p *runtime.Package, pos token.Pos) *syntax.File {
	if p == nil || p.Fset == nil || p.FileByName == nil || !pos.IsValid() {
		return nil
	}
	return p.FileByName[p.Fset.PositionFor(pos, false).Filename]
}

// pkgTypeSpec returns the spec a package-level type name declares —
// nil when the package is not source-indexed or the name declares no
// type.
func pkgTypeSpec(p *runtime.Package, name string) *ast.TypeSpec {
	if p == nil || p.Index == nil {
		return nil
	}
	tdi := p.Index.Types[name]
	if tdi == nil || tdi.Decl == nil {
		return nil
	}
	ts, _ := tdi.Decl.Spec.(*ast.TypeSpec)
	return ts
}

// selTypeSpec resolves an imported package's member name to the
// package and the type spec declaring it — nil when the name is not a
// source-indexed type there.
// semantics: fact — pkg.T's TypeSpec decides composite-literal key
// types and conversions.
func (c *compiler) selTypeSpec(ref *runtime.ImportRef, name string) (*runtime.Package, *ast.TypeSpec) {
	res := c.askIndexed(ref)
	if res.Kind != semantics.Found {
		return nil, nil
	}
	p := res.Val
	if ts := pkgTypeSpec(p, name); ts != nil {
		return p, ts
	}
	return nil, nil
}

// pkgImportRef resolves name to an import ref inside file sf of
// package p: first by the recorded local name, then by the package
// clause when it differs from the path's last element — the same
// real-name fallback importRef applies for the caller's file.
// semantics: fact — name resolution itself when a package clause
// differs from its path's last element.
func (c *compiler) pkgImportRef(p *runtime.Package, sf *syntax.File, name string) (*runtime.ImportRef, bool) {
	if ref, ok := p.Scopes[sf][name]; ok {
		return ref, true
	}
	for _, ref := range p.Imports[sf] {
		if ref.Alias != "" {
			continue
		}
		res := c.askIndexed(ref)
		if res.Kind != semantics.Found {
			continue
		}
		if res.Val.Name == name {
			return ref, true
		}
	}
	return nil, false
}

// elemTypExpr resolves the element type of an indexing operand — an
// array/slice's Elt or a map's Value, through a named type's spec.
func (c *compiler) elemTypExpr(te ast.Expr, depth int) ast.Expr {
	switch t := c.unfoldTypExpr(te, depth).(type) {
	case *ast.ArrayType:
		return t.Elt
	case *ast.MapType:
		return t.Value
	case *ast.StarExpr:
		// indexing a pointer to array dereferences it once — `pa[i]`
		// is `(*pa)[i]`; a second star stays unindexed.
		if a, ok := c.unfoldTypExpr(t.X, depth-1).(*ast.ArrayType); ok {
			return a.Elt
		}
	case *ast.Ident:
		// s[i] on a string operand yields a byte. A shadowed
		// `string` never reaches here — unfoldTypExpr already
		// reduced the name through its spec.
		if t.Name == "string" {
			return &ast.Ident{Name: "byte"}
		}
	}
	return nil
}

// chanElemTypExpr resolves the element type of a receive operand's
// channel type, through a named type's spec.
func (c *compiler) chanElemTypExpr(te ast.Expr, depth int) ast.Expr {
	if t, ok := c.unfoldTypExpr(te, depth).(*ast.ChanType); ok {
		return t.Value
	}
	return nil
}

// starBaseTypExpr resolves the base type of a deref operand's pointer
// type, through a named type's spec.
func (c *compiler) starBaseTypExpr(te ast.Expr, depth int) ast.Expr {
	if t, ok := c.unfoldTypExpr(te, depth).(*ast.StarExpr); ok {
		return t.X
	}
	return nil
}

// fieldTypExpr resolves a selector's field inside a known named struct
// type: te names the struct (or aliases to one), sel the field. Embedded
// fields are searched like selector promotion; the depth cap keeps a
// cyclic type graph finite. nil means the field isn't statically known.
func (c *compiler) fieldTypExpr(te ast.Expr, sel string, depth int) ast.Expr {
	if depth <= 0 {
		return nil
	}
	ts := c.typeSpecOf(te)
	if ts == nil {
		return nil
	}
	st, ok := ast.Unparen(ts.Type).(*ast.StructType)
	if !ok {
		return nil
	}
	// direct fields win over promoted ones regardless of declaration
	// order — Go's shallower-depth rule. A first pass over named
	// fields, then the embeds; the other order lets a promoted field
	// shadow a direct one, which gc rejects in the opposite direction.
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == sel {
				return f.Type
			}
		}
	}
	var promoted ast.Expr
	for _, f := range st.Fields.List {
		if len(f.Names) != 0 {
			continue
		}
		// an embedded field promotes its own fields; `*T` embeds
		// promote like `T` (a parens/star wrapper is not a selector).
		emb := ast.Unparen(f.Type)
		if s, ok := emb.(*ast.StarExpr); ok {
			emb = ast.Unparen(s.X)
		}
		t := c.fieldTypExpr(emb, sel, depth-1)
		if t == nil {
			continue
		}
		if promoted != nil {
			// two same-depth promotions — gc rejects the selector
			// itself as ambiguous, so the operand is opaque.
			return nil
		}
		promoted = t
	}
	return promoted
}

// namedTypID is a named type's compile-time identity: two operand types
// with different ids are different named types, which gc never assigns
// to each other (both named — the comparison is a compile error).
type namedTypID struct {
	id   any    // comparable identity: *ast.TypeSpec or a string
	name string // source spelling for the trap message
}

// namedIdentOf resolves a type expression to a named type's identity:
// a local or package-level declared type (the spec pointer — two
// distinct `type Tag string` decls never alias), an alias resolves to
// its target, a qualified `pkg.T` to "path.T", and a predeclared type to
// its canonical name (byte→uint8, rune→int32 — the same type in gc).
// Type parameters, interface types, and unnamed forms report none.
func (c *compiler) namedIdentOf(te ast.Expr) (namedTypID, bool) {
	switch t := ast.Unparen(te).(type) {
	case *ast.Ident:
		info, found := c.resolveName(t.Name)
		if !found || !info.isType || info.iface {
			return namedTypID{}, false
		}
		if info.td != nil {
			return namedTypID{}, false // a bound type parameter — opaque
		}
		if info.tspec != nil {
			if info.tspec.Assign.IsValid() {
				return c.namedIdentOf(info.tspec.Type) // alias — the target's identity
			}
			return namedTypID{id: info.tspec, name: t.Name}, true
		}
		// predeclared named types: byte and rune canonicalize to their
		// underlying types — they are the same type for assignability.
		return namedTypID{id: "predecl:" + runtime.CanonicalBasicName(t.Name), name: t.Name}, true
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return namedTypID{}, false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return namedTypID{}, false
		}
		if c.selIsIface(ref, t.Sel.Name) {
			// an interface operand compares dynamically — `rc ==
			// NoBody` is legal gc regardless of the other side.
			return namedTypID{}, false
		}
		name := id.Name
		if _, synth := c.synthImports[id.Name]; synth {
			// a minted qualifier has no source spelling — the
			// package clause's name is what gc prints.
			name = c.pkgDispName(ref)
		}
		return namedTypID{id: ref.Path + "." + t.Sel.Name, name: name + "." + t.Sel.Name}, true
	case *ast.StarExpr:
		// a pointer's identity wraps its base's — `i == &d` reads
		// as int vs *D, `&d == &e` as *D vs *E. Other composite
		// forms stay unnamed, keeping the gate conservative.
		inner, ok := c.namedIdentOf(t.X)
		if !ok {
			return namedTypID{}, false
		}
		return namedTypID{id: ptrTypID{elem: inner.id}, name: "*" + inner.name}, true
	}
	return namedTypID{}, false
}

// ptrTypID is the compile-time identity of a pointer to a named type:
// *D and *E are distinct types to gc even when D and E share an
// underlying, so the element's identity is part of the pointer's.
type ptrTypID struct {
	elem any
}

// selIsIface reports whether the name a qualified type expression picks
// out of an imported package names an interface: a bound typedef's
// KindInterface flag, or an InterfaceType spec in a source-indexed
// package. Unknown references and unresolved names report false.
func (c *compiler) selIsIface(ref *runtime.ImportRef, name string) bool {
	ts, td, ok := c.selMember(ref, name)
	if !ok {
		return false
	}
	if ts != nil {
		_, isIface := ast.Unparen(ts.Type).(*ast.InterfaceType)
		return isIface
	}
	return td != nil && td.Kind == runtime.KindInterface
}

// selMember resolves a package member name to what the compiler can
// know about it: the declaring TypeSpec for a source-indexed type decl,
// or the bound runtime typedef for a host-bound member. selIsType,
// selIsIface, and selNilable all read the same walk.
// semantics: fact — whether pkg.T names a type, and its spec/typedef;
// selIsType, selIsIface and selNilable all answer through this.
func (c *compiler) selMember(ref *runtime.ImportRef, name string) (*ast.TypeSpec, *runtime.TypeDef, bool) {
	res := c.askIndexed(ref)
	if res.Val == nil {
		// Missing, Failed — and Deferred without a materialized
		// package — all mean the oracle cannot answer here.
		return nil, nil, false
	}
	p := res.Val
	if p.Index != nil {
		if tdi := p.Index.Types[name]; tdi != nil {
			if tdi.Decl != nil {
				if ts, ok := tdi.Decl.Spec.(*ast.TypeSpec); ok {
					return ts, nil, true
				}
			}
			// a synthetic entry (a method receiver whose declaration
			// lives outside this index) still names a type — keep the
			// old ix.Types[name] != nil verdict even with no spec to
			// hand back; the globals lookup may still find its
			// materialized typedef.
			if p.Globals != nil {
				if v, ok := p.Globals.Get(name); ok {
					if td, ok := v.(*runtime.TypeDef); ok && td != nil {
						return nil, td, true
					}
				}
			}
			return nil, nil, true
		}
	}
	if p.Globals != nil {
		if v, ok := p.Globals.Get(name); ok {
			if td, ok := v.(*runtime.TypeDef); ok && td != nil {
				return nil, td, true
			}
		}
	}
	return nil, nil, false
}

// nilableTypExpr reports whether a resolved type expression denotes a
// type a `nil` comparison can lawfully answer — pointer, slice, map,
// channel, func, or interface — through named-type specs.
func (c *compiler) nilableTypExpr(te ast.Expr) (nilable, resolved bool) {
	switch t := c.unfoldTypExpr(te, 4).(type) {
	case *ast.StarExpr, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType:
		return true, true
	case *ast.Ellipsis:
		return true, true // a variadic parameter's type is a slice
	case *ast.ArrayType:
		return t.Len == nil, true // a slice
	case *ast.Ident:
		return t.Name == "error" || t.Name == "any", true
	case *ast.SelectorExpr:
		// a qualified name resolves when the member is knowable —
		// `time.Duration` proves non-nilable so `d == nil` traps like
		// gc's reject, while an interface (`io.Reader`) keeps the
		// lawful dynamic compare. An unresolved name reports
		// unresolved: the caller defers the nil check to run time
		// instead of guessing nilable.
		if ref, name, ok := c.selTypRef(t); ok {
			if n, ok := c.selNilable(ref, name); ok {
				return n, true
			}
		}
		return true, false
	}
	return false, true
}

// semantics: fact — decides whether `x == nil` on pkg.T traps the way
// gc rejects it (interfaces/pointers/slices/maps/chans/funcs are
// nilable; structs and basic types are not).
//
// selNilable reports whether the type a qualified name picks out of an
// imported package can be nil: interfaces, pointers, slices, maps,
// chans and funcs can; structs and basic types cannot. ok=false when
// the member can't be resolved at all.
func (c *compiler) selNilable(ref *runtime.ImportRef, name string) (bool, bool) {
	res := c.askIndexed(ref)
	if res.Kind != semantics.Found {
		return false, false
	}
	p := res.Val
	ts, td, ok := c.selMember(ref, name)
	if !ok {
		return false, false
	}
	if ts != nil {
		// the spec's inner names resolve in ITS package's file scope —
		// `type T = otherpkg.Nilable` must read otherpkg through the
		// spec's own imports, never the caller's, or a same-named
		// import there would flip the verdict.
		n, _ := c.nilableTypExpr(c.unfoldSpecType(p, ts, 4))
		return n, true
	}
	if td == nil {
		// a type name with no spec or typedef in view (synthetic
		// index entry): unknown shape, so conservatively non-nilable.
		return false, true
	}
	switch td.Kind {
	case runtime.KindInterface, runtime.KindPointer, runtime.KindSlice,
		runtime.KindMap, runtime.KindChan, runtime.KindFunc:
		return true, true
	}
	if td.HostNew != nil {
		// a bound host type (hostType) is classed a struct whatever
		// its shape; its zero's own kind tells a named slice or map
		// (net.IP, net.HardwareAddr) that answers `== nil`. A struct
		// binds a pointer zero, which is no evidence of nilability.
		switch td.HostType().Kind() {
		case reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
			return true, true
		}
	}
	return false, true
}

// noteDeclTyp records a value spec's type on the name's binding for the
// operand-assignability gate: the written type wins (`var x T`), an
// inferred decl (`var x = e`, `x := e`) takes the initializer's static
// type when the expression spells one (`x := 5` is int like gc), and an
// untyped const keeps no type — it adopts like a literal.
// noteDeclTyp records a declared name's type. preCV is the const
// initializer folded BEFORE the binding installed — `const K = K + 1`
// resolves the initializer's names in the enclosing scope, so callers
// fold the rhs ahead of bindLocal and pass the result here.
func (c *compiler) noteDeclTyp(name string, effType ast.Expr, isConst bool, rhs ast.Expr, preCV constant.Value) {
	if isConst {
		c.fs.noteConstDecl(name)
		if preCV != nil {
			c.fs.noteConstVal(name, preCV)
		}
	}
	te := effType
	var teFile *syntax.File
	if te == nil && rhs != nil {
		t := c.staticOpTyp(rhs)
		te = t.te
		teFile = t.teFile
		if te == nil && !isConst {
			if t.untyped {
				// an untyped constant binds its default type at
				// the decl, like gc — `x := 1 + 2` is int, `y :=
				// c` follows c's kind (opTyp.dte).
				te = t.dte
			}
			if te == nil && t.deferred {
				// `var y = p.C` inherits the deferral: reads of y
				// report the same question, asked about p.C.
				dx := t.dexpr
				if dx == nil {
					dx = rhs
				}
				c.fs.noteDeferTyp(name, dx)
				return
			}
			if te == nil {
				// an interface-typed initializer spells no operand
				// type for the gate but still declares one — `var
				// i = I(v)` binds i as I, so `var j = i` inherits
				// I instead of interface{}.
				te = c.inferredBindTyp(1, []ast.Expr{rhs}, 0)
			}
		}
		if te == nil && isConst && t.untyped {
			c.fs.noteUntypedConst(name, t.dte)
			return
		}
	}
	if te != nil {
		c.fs.noteDeclTyp(name, te, teFile)
		return
	}
	if isConst {
		c.fs.noteUntypedConst(name, nil)
	}
}

// staticTyp pushes the declared type the operand of an assertion is
// bound under — Go's "main.I" in "interface conversion: main.I is main.T,
// not io.Writer". A bare identifier carries its cell's declared typedef;
// conversions T(v) spell T; a single-result call on a func literal
// (`func() S {…}()`) spells its declared result. Anything else — and
// untyped cells — yields NIL and the panic falls back to "interface {}".
func (c *compiler) staticTyp(e ast.Expr) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		c.staticTyp(x.X)
		return
	case *ast.CallExpr:
		switch {
		case c.conversionCall(x):
			// T(v).(U) — the operand's static type is the conversion's
			// target type.
			c.typeExpr(x.Fun)
			return
		default:
			if lit, ok := x.Fun.(*ast.FuncLit); ok {
				rs := lit.Type.Results
				if rs != nil && len(rs.List) == 1 && len(rs.List[0].Names) <= 1 {
					// (func() S {…})().(T) — the call's static type is
					// the literal's declared result.
					c.typeExpr(rs.List[0].Type)
					return
				}
			}
		}
		c.emit(bytecode.OpNil, 0, 0, e.Pos())
		return
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		c.emit(bytecode.OpNil, 0, 0, e.Pos())
		return
	}
	isUp, idx, found := c.fs.find(id.Name)
	switch {
	case !found:
		if _, bound := c.binds[id.Name]; bound {
			c.emit(bytecode.OpNil, 0, 0, id.Pos())
			return
		}
		c.emit(bytecode.OpGlobalTyp, c.nameIdx(id.Name), 0, id.Pos())
	case !isUp:
		c.emit(bytecode.OpLocalTyp, idx, 0, id.Pos())
	default:
		c.emit(bytecode.OpUpvalTyp, idx, 0, id.Pos())
	}
}

// selTypRef resolves a `pkg.Name` expression to its import reference
// and member name — ok=false when e isn't a package-qualified
// selector (a nested selector or a non-package qualifier can't name a
// package member).
func (c *compiler) selTypRef(e ast.Expr) (*runtime.ImportRef, string, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return nil, "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, "", false
	}
	ref, isPkg := c.importRef(id.Name)
	if !isPkg {
		return nil, "", false
	}
	return ref, sel.Sel.Name, true
}

// selIsType reports whether the name a qualified expression picks out
// of an imported package names a type: a bound typedef in the
// package's globals, or a type declaration in its source index.
// Functions, variables, constants and unresolved references report
// false — `pkg.fn(x)` then keeps its meaning as a real call.
func (c *compiler) selIsType(ref *runtime.ImportRef, name string) bool {
	_, _, ok := c.selMember(ref, name)
	return ok
}

// isTypeName reports whether name resolves to a type: a type parameter, a
// local `type` decl, a package-level type decl, or a predeclared type —
// unless a nearer variable shadows it.
func (c *compiler) isTypeName(name string) bool {
	info, found := c.resolveName(name)
	return found && info.isType
}

// DeferFacts, when set, makes every indexed-fact query answer Deferred:
// questions the compiler would answer under AllowIndex fall through to
// the CallSite paths that resolve at run time — the M4-7b test knob
// previewing IndexedOnly emission.
var DeferFacts bool

// askIndexed is the one indexed-fact query in this package. It asks the
// import question under AllowIndex and — under DeferFacts — downgrades
// Found to Deferred so callers take the same fallbacks IndexedOnly
// will give them.
// askIndexed is the one indexed-fact query in this package. It asks the
// index under AllowIndex — under strict lazy compilation ([FuncStrict]
// and friends) it asks under IndexedOnly instead, so a package not yet
// indexed comes back Deferred with no Val and compilation never
// triggers an indexing build.
func (c *compiler) askIndexed(ref *runtime.ImportRef) semantics.Result[*runtime.Package] {
	if c.strictLazy {
		return source.PackageAtIndexed(ref, semantics.IndexedOnly)
	}
	res := source.PackageAtIndexed(ref, semantics.AllowIndex)
	if DeferFacts && res.Kind == semantics.Found {
		// downgrade but keep Val: callers answering an out-of-band
		// question (selMember) still read the materialized package,
		// while emission paths treating !Found as "not decidable"
		// take their CallSite fallbacks.
		res.Kind = semantics.Deferred
	}
	return res
}
