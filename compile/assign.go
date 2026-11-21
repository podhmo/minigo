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
	"strings"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

func (c *compiler) getRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		// compile-time bindings from generic instantiation (type param -> TypeDef)
		if bv, bound := c.binds[name]; bound {
			c.emit(bytecode.OpConst, c.constIdx(bv), 0, pos)
			return
		}
		c.emit(bytecode.OpGlobal, c.nameIdx(name), 0, pos)
	case !isUp:
		if c.iotaVal >= 0 && idx == c.fs.iota {
			c.emit(bytecode.OpConst, c.constIdx(&runtime.UConst{V: constant.MakeInt64(int64(c.iotaVal))}), 0, pos)
			return
		}
		c.emit(bytecode.OpLocal, idx, 0, pos)
	default:
		c.emit(bytecode.OpUpval, idx, 0, pos)
	}
}

// declared reports whether name resolves through a declaration rather
// than a builtin — a local/upval from fscope, a generic instantiation
// binding, a package-level decl in the index, or a predeclared type —
// so `const true = 31` shadows the predeclared literal.
func (c *compiler) declared(name string) bool {
	_, found := c.resolveName(name)
	return found
}

func (c *compiler) setRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		c.emit(bytecode.OpSetGlobal, c.nameIdx(name), 0, pos)
	case !isUp:
		c.emit(bytecode.OpSetLocal, idx, 0, pos)
	default:
		c.emit(bytecode.OpSetUpval, idx, 0, pos)
	}
}

func (c *compiler) refRef(name string, pos token.Pos) {
	isUp, idx, ok := c.fs.find(name)
	switch {
	case !ok:
		c.emit(bytecode.OpGlobalRef, c.nameIdx(name), 0, pos)
	case !isUp:
		c.emit(bytecode.OpLocalRef, idx, 0, pos)
	default:
		c.emit(bytecode.OpUpvalRef, idx, 0, pos)
	}
}

// valueSpec emits a whole var/const spec: all of its names are bound.
// Vars become package cells (OpNewGlobal); consts read-only cells
// (OpNewGlobal with B=1) so a later `k = v` store traps like Go.
func (c *compiler) valueSpec(vs *ast.ValueSpec, d *index.Decl) {
	isConst := d.Kind == index.ConstDecl
	bind := func(name *ast.Ident) {
		readonly := int32(0)
		if isConst {
			readonly = 1
		}
		c.emit(bytecode.OpNewGlobal, c.nameIdx(name.Name), int(readonly), name.Pos())
	}
	vals := vs.Values
	effType := vs.Type
	if isConst && len(vals) == 0 {
		// the empty spec repeats the previous values AND their type —
		// `B` under `A T = e` binds `B T = e`.
		vals = d.Inherited
		effType = d.InheritedType
	}
	// `var x T` at package level gets the same declared-type coerce as a
	// local: zero values materialize (var s Sq -> Struct), typed nils too.
	coerceVar := func(name *ast.Ident) {
		if isConst || vs.Type == nil {
			return
		}
		c.typeExpr(vs.Type)
		c.emit(bytecode.OpCoerceGlobal, c.nameIdx(name.Name), 0, name.Pos())
	}
	// A typed decl coerces its value on the stack before binding — consts
	// are stored as plain globals OpCoerceGlobal cannot reach, and a var
	// would otherwise materialize an untyped-constant value at bind time,
	// losing `var r MyRune = 'a'`-style direct conversion to the decl.
	coerceTop := func() {
		if effType == nil {
			return
		}
		c.typeExpr(effType)
		c.emit(bytecode.OpCoerceTop, 0, 0, vs.Pos())
	}
	switch {
	case !isConst && len(vals) == 0 && len(vs.Names) == 1 && vs.Type != nil && embedPatterns(d.Gen, vs) != nil:
		// `//go:embed pat...` on `var x T`: the hidden embed builtin
		// reads the matched files from the package directory and
		// builds the string, []byte or embed.FS value.
		pats := embedPatterns(d.Gen, vs)
		c.expr(&ast.Ident{NamePos: vs.Pos(), Name: EmbedBuiltin})
		c.typeExpr(vs.Type)
		for _, pat := range pats {
			c.emit(bytecode.OpConst, c.constIdx(pat), 0, vs.Pos())
		}
		for range len(pats) + 1 {
			c.emit(bytecode.OpNil, 0, 0, vs.Pos()) // no static arg types
		}
		c.emit(bytecode.OpCall, len(pats)+1, 0, vs.Pos())
		bind(vs.Names[0])
		coerceVar(vs.Names[0])
	case len(vals) == 0:
		for _, name := range vs.Names {
			c.emit(bytecode.OpNil, 0, 0, name.Pos())
			bind(name)
			coerceVar(name)
		}
	case len(vals) == 1 && len(vs.Names) > 1:
		// a two-name spec binds a comma-ok RHS — `var v, ok = m[k]`,
		// `= x.(T)`, `= <-ch` — like the := path; a plain call/unpack
		// RHS falls through to OpUnpack.
		if isConst || len(vs.Names) != 2 || !c.commaOkRhs(vals[0]) {
			c.expr(vals[0])
		}
		c.emit3(bytecode.OpUnpack, len(vs.Names), 0, 0, vs.Pos())
		for i := len(vs.Names) - 1; i >= 0; i-- {
			coerceTop()
			bind(vs.Names[i])
			coerceVar(vs.Names[i])
		}
	default:
		for i, name := range vs.Names {
			if hv, ok := c.pkg.HostVars[name.Name]; ok && !isConst {
				// a host-pinned var: the declared initializer never runs
				c.emit(bytecode.OpConst, c.constIdx(hv), 0, name.Pos())
			} else if isConst && effType != nil && c.typedConstExpr(effType, vals[i]) {
				bind(name)
				continue
			} else if isConst {
				c.constExpr(vals[i])
			} else {
				c.expr(vals[i])
			}
			coerceTop()
			bind(name)
			coerceVar(name)
		}
	}
	// An inferred `var x = e` carries its RHS's declared type like
	// `x := e` does: `var i = I(s)` binds an I-typed cell so `&i` is *I.
	if !isConst && vs.Type == nil {
		lhs := make([]ast.Expr, len(vs.Names))
		for i, name := range vs.Names {
			lhs[i] = name
		}
		c.stampVarTyps(lhs, vals)
	}
}

// EmbedBuiltin is the name of the builtin that compiled code calls to
// initialize a `//go:embed` var, as EmbedBuiltin(T, patterns...); the host
// must provide it. The trailing '#' makes the name unwritable in Go source,
// so a user declaration can never shadow it.
const EmbedBuiltin = "__minigo_embed__#"

// embedPatterns collects the `//go:embed` patterns on a var spec — from
// its own doc, or the GenDecl's for an unparenthesized `var x T`. Quoted
// patterns ("a b" or `a b`) keep their spaces. nil when there are none.
func embedPatterns(gen *ast.GenDecl, vs *ast.ValueSpec) []string {
	var groups []*ast.CommentGroup
	if vs.Doc != nil {
		groups = append(groups, vs.Doc)
	}
	if gen != nil && gen.Doc != nil && !gen.Lparen.IsValid() {
		groups = append(groups, gen.Doc)
	}
	var out []string
	for _, g := range groups {
		for _, cm := range g.List {
			rest, ok := strings.CutPrefix(cm.Text, "//go:embed")
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			out = append(out, splitEmbedPatterns(rest)...)
		}
	}
	return out
}

// splitEmbedPatterns splits a directive's argument list on spaces,
// honoring Go-quoted ("...") and raw (`...`) patterns.
func splitEmbedPatterns(s string) []string {
	var out []string
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out
		}
		if q := s[0]; q == '"' || q == '`' {
			if pre, err := strconv.QuotedPrefix(s); err == nil {
				if u, err := strconv.Unquote(pre); err == nil {
					out = append(out, u)
				}
				s = s[len(pre):]
				continue
			}
		}
		i := strings.IndexAny(s, " \t")
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s = s[i:]
	}
}

func (c *compiler) bindLocal(name string, pos token.Pos, isConst bool) int {
	if name == "_" {
		c.emit(bytecode.OpPop, 0, 0, pos)
		return -1
	}
	slot := c.fs.declare(name, pos)
	readonly := 0
	if isConst {
		readonly = 1
	}
	c.emit(bytecode.OpNewLocal, slot, readonly, pos)
	return slot
}

// localTypeDecl binds a `type` declaration inside a function body: the
// typedef is a compile-time const pushed as the local's value, so name
// resolution, composite literals, conversions and asserts all see it.
// Local types carry no methods (Go forbids methods on them anyway).
func (c *compiler) localTypeDecl(ts *ast.TypeSpec) {
	td := &runtime.TypeDef{
		Pkg: c.pkg, File: c.file, Name: ts.Name.Name,
		Spec: ts, Anon: ts.Type, Binds: c.binds, Local: true,
	}
	// the decl's `·gen` index was assigned at index time — functions
	// compile lazily in call order, which is not source order.
	if !ts.Assign.IsValid() && c.pkg != nil && c.pkg.Index != nil {
		td.Gen = c.pkg.Index.LocalGen(ts)
	}
	if ts.TypeParams != nil {
		for _, tp := range ts.TypeParams.List {
			for _, n := range tp.Names {
				td.TParams = append(td.TParams, n.Name)
				td.TConstraints = append(td.TConstraints, tp.Type)
			}
		}
	}
	isIface := false
	switch t := ts.Type.(type) {
	case *ast.StructType:
		td.SetStructShape(t)
	case *ast.InterfaceType:
		td.SetInterfaceShape(t)
		isIface = true
	case *ast.ArrayType:
		td.Kind = runtime.KindSlice
	case *ast.MapType:
		td.Kind = runtime.KindMap
	case *ast.FuncType:
		td.Kind = runtime.KindFunc
	case *ast.ChanType:
		td.Kind = runtime.KindChan
	case *ast.StarExpr:
		td.Kind = runtime.KindPointer
	default:
		td.Kind = runtime.KindNamedBasic
	}
	if ts.Assign.IsValid() {
		td.Kind = runtime.KindAlias
	}
	td.LocalTypes = c.fs.localTypeDefs()
	slot := c.fs.declare(ts.Name.Name, ts.Pos())
	c.fs.recordType(ts.Name.Name, ts, td, isIface)
	c.emit(bytecode.OpConst, c.constIdx(td), 0, ts.Pos())
	// array lengths inside the local decl may name a function-local
	// const — fold them in this scope, like a local var's `[n]T`;
	// runtime.arrayLen resolves only package-scoped const names.
	c.emitLenFolds(ts.Type)
	// gc treats every function-local type declared inside a generic
	// function as implicitly parameterized by the enclosing type
	// arguments: `type X int` in F[T] is a different X per F[T]'s
	// instantiation. OpLocalType clones the decl typedef with the
	// resolved outer args at run time.
	c.emit(bytecode.OpLocalType, 0, 0, ts.Pos())
	c.emit(bytecode.OpNewLocal, slot, 0, ts.Pos())
}

// assign handles =, :=, and compound ops.
func (c *compiler) assign(st *ast.AssignStmt) {
	isDefine := st.Tok == token.DEFINE
	simple := st.Tok == token.ASSIGN || isDefine

	if !simple {
		if len(st.Lhs) != 1 || len(st.Rhs) != 1 {
			c.trap(st.Pos(), "compound assignment requires single operands")
			return
		}
		op, ok := binOpOf(st.Tok)
		if !ok {
			c.trap(st.Pos(), "unsupported assign op %s", st.Tok)
			return
		}
		target := ast.Unparen(st.Lhs[0])
		if !storageShape(target, false) {
			c.trap(st.Pos(), "compound assignment on %T is not supported", target)
			return
		}
		// `x op= y` reads x's value when the RHS evaluates, not at
		// ref time: ref, rhs, then read+op+store. The ref resolves
		// to the variable's live storage, so a RHS that replaces
		// the variable lands the result there.
		c.refTarget(target, false)
		c.expr(st.Rhs[0])
		c.emit(bytecode.OpSwap, 0, 0, target.Pos())
		c.emit(bytecode.OpDup, 0, 0, target.Pos())
		c.emit(bytecode.OpDeref, 0, 0, target.Pos())
		c.emit(bytecode.OpRot3, 0, 0, target.Pos())
		c.emit(bytecode.OpBinary, int(op), 0, st.Pos())
		c.emit(bytecode.OpSetRefs, 1, 0, st.Pos())
		return
	}

	n := len(st.Lhs)
	// Go assigns in two phases: index/pointer/selector operands on the
	// left resolve BEFORE the right side evaluates (`x[i], i = 100, 1`
	// binds x[i] through the old i), then stores land left-to-right.
	// Push each target's reference first so `=` keeps that order; `:=`
	// only allows identifier targets, so it keeps the value-stack path.
	useRefs := !isDefine
	if useRefs {
		// multi-assign pins deref/index operands like gc's ascompatee
		// save pass — `p, *p = fp()` binds *p through the old p; a
		// single `*p = f()` keeps the live-deref store (f reseating p
		// redirects the write, matching OAS).
		pin := len(st.Lhs) > 1
		for _, l := range st.Lhs {
			c.refTarget(l, pin)
		}
	}
	if len(st.Rhs) == 1 && n > 1 {
		// a two-target assign binds a comma-ok RHS — `v, ok := m[k]`,
		// `:= x.(T)`, `:= <-ch`; a plain call/unpack RHS falls through.
		if n == 2 && c.commaOkRhs(st.Rhs[0]) {
			c.emit3(bytecode.OpUnpack, 2, 0, 0, st.Pos())
			c.storeAll(st.Lhs, isDefine, useRefs, st.Pos())
			c.noteIfaceBinds(st)
			return
		}
		c.expr(st.Rhs[0])
		c.emit3(bytecode.OpUnpack, n, 0, 0, st.Pos())
	} else {
		for _, r := range st.Rhs {
			c.expr(r)
		}
	}
	c.storeAll(st.Lhs, isDefine, useRefs, st.Pos())
	c.noteIfaceBinds(st)
}

// commaOkRhs emits the two-valued form of a comma-ok RHS — `m[k]`,
// `x.(T)`, or `<-ch` — for `v, ok = expr` / `var v, ok = expr`
// positions. Returns false for any other expression, which the caller
// then emits as a plain value (a nil .Type reports the usual
// ".(type) outside type switch" trap there).
func (c *compiler) commaOkRhs(e ast.Expr) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.UnaryExpr:
		if x.Op != token.ARROW {
			return false
		}
		c.expr(x.X)
		c.emit(bytecode.OpRecvOK, 0, 0, x.Pos())
	case *ast.IndexExpr:
		c.expr(x.X)
		c.expr(x.Index)
		c.emit(bytecode.OpIndexOK, 0, 0, x.Lbrack)
	case *ast.TypeAssertExpr:
		if x.Type == nil {
			return false
		}
		c.expr(x.X)
		c.typeExpr(x.Type)
		c.emit(bytecode.OpAssertOK, 0, 0, x.Pos())
	default:
		return false
	}
	return true
}

// storeAll emits the phase-2 stores for an assignment: OpSetRefs when the
// refs were pushed (plain `=`), otherwise the per-target stack stores in
// reverse order (stack top = last value).
func (c *compiler) storeAll(lhs []ast.Expr, isDefine, useRefs bool, pos token.Pos) {
	if useRefs {
		c.emit(bytecode.OpSetRefs, len(lhs), 0, pos)
		return
	}
	for i := len(lhs) - 1; i >= 0; i-- {
		c.storeTarget(lhs[i], isDefine)
	}
}

// noteIfaceBinds marks names a `:=` binds to an interface-typed RHS
// (`any(x)`, `v.(I)`) so an interface-tag switch later picks strict
// pair equality. A multi-value RHS marks every bound name — `v, ok :=`
// forms keep the value slot's type on name[0].
func (c *compiler) noteIfaceBinds(st *ast.AssignStmt) {
	if st.Tok != token.DEFINE {
		return
	}
	for i, l := range st.Lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		rhs := st.Rhs[0]
		if len(st.Rhs) == len(st.Lhs) {
			rhs = st.Rhs[i]
		}
		if c.isIfaceExpr(rhs) {
			c.fs.markIface(id.Name)
		}
		// the assignability gate learns a `:=` name's type from its RHS:
		// `x := Duration(0)` compares like a declared Duration. The
		// second name of a comma-ok bind is always bool; a multi-result
		// call types each name from the callee's signature.
		if len(st.Rhs) == len(st.Lhs) {
			c.noteDeclTyp(id.Name, nil, false, st.Rhs[i], nil)
		} else if len(st.Rhs) == 1 && commaOkStatic(st.Rhs[0]) {
			if i == 0 {
				c.noteDeclTyp(id.Name, nil, false, st.Rhs[0], nil)
			} else {
				c.fs.noteDeclTyp(id.Name, &ast.Ident{Name: "bool"}, nil)
			}
		} else if len(st.Rhs) == 1 {
			if t := c.nthResultOpTyp(st.Rhs[0], i); t.te != nil || t.iface {
				if t.iface {
					c.fs.markIface(id.Name)
				}
				c.fs.noteDeclTyp(id.Name, t.te, t.teFile)
			} else if i == 0 {
				c.noteDeclTyp(id.Name, nil, false, st.Rhs[0], nil)
			}
		}
	}
	c.stampInferredTyps(st.Lhs, st.Rhs)
}

// commaOkStatic reports whether e is a comma-ok-producing expression
// (map index, type assert, channel receive): `v, ok := e` binds the
// second name to bool.
func commaOkStatic(e ast.Expr) bool {
	switch ast.Unparen(e).(type) {
	case *ast.IndexExpr, *ast.TypeAssertExpr:
		return true
	case *ast.UnaryExpr:
		return ast.Unparen(e).(*ast.UnaryExpr).Op == token.ARROW
	}
	return false
}

// stampInferredTyps gives each `:=` cell its declared type when the RHS
// spells one statically — a conversion, type assert, or composite
// literal — so a later store coerces like `var x T` (x = 300 keeps the
// int8 tag, x = otherNamed traps) and an interface-typed RHS keeps the
// cell's type loose instead of adopting the stored dynamic tag.
func (c *compiler) stampInferredTyps(lhs, rhs []ast.Expr) {
	for i, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		t := c.inferredBindTyp(len(lhs), rhs, i)
		if t == nil {
			continue
		}
		b := c.fs.lookupBinding(id.Name)
		if b == nil || b.slot < 0 {
			continue
		}
		c.typeExpr(t)
		c.emit(bytecode.OpCoerce, b.slot, 0, id.Pos())
	}
}

// stampVarTyps is stampInferredTyps for package-level `var x = e`
// specs: an inferred var carries its RHS's declared type like `x := e`
// does, so `var i = I(s)` binds an I-typed cell — `&i` builds *I and a
// field of type *I accepts it, where an untagged cell would hand back
// the contained concrete type.
func (c *compiler) stampVarTyps(lhs, rhs []ast.Expr) {
	for i, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		t := c.inferredBindTyp(len(lhs), rhs, i)
		if t == nil {
			continue
		}
		c.typeExpr(t)
		c.emit(bytecode.OpCoerceGlobal, c.nameIdx(id.Name), 0, id.Pos())
	}
}

// inferredBindTyp returns the declared type expression the i-th LHS
// name's RHS spells: the conversion/assert/literal type, or an empty
// interface when the RHS is interface-typed (any(v), an iface var, a
// .(I) assert). nil means the bind carries no static type.
func (c *compiler) inferredBindTyp(nlhs int, rhs []ast.Expr, i int) ast.Expr {
	var r ast.Expr
	switch {
	case len(rhs) == nlhs:
		r = rhs[i]
	case len(rhs) == 1 && i == 0:
		// a multi-value RHS types only its first result — `x, ok :=
		// v.(T)` binds x to T.
		if _, isAssert := rhs[0].(*ast.TypeAssertExpr); !isAssert {
			return nil
		}
		r = rhs[0]
	default:
		return nil
	}
	t := c.inferredTypExpr(r)
	if t == nil && c.isIfaceExpr(r) {
		// an interface-typed RHS (any(v), an iface var, a .(I)
		// assert) declares an interface type with no literal. An ident
		// keeps its statically-known declared type — `var j = i` binds
		// i's I, not interface{} — so `&j` still builds *I.
		if id, ok := ast.Unparen(r).(*ast.Ident); ok {
			t = c.ifaceIdentTypExpr(id, 4)
		}
		if t == nil {
			t = &ast.InterfaceType{Interface: r.Pos(), Methods: &ast.FieldList{}}
		}
	}
	return t
}

// ifaceIdentTypExpr resolves an interface-typed identifier's declared
// type expression for inferred stamping: a local's recorded declTyp, a
// package var's written type, or an inferred global's own spelled type
// (`var j = i` follows i's `var i = I(s)` to I). nil when unknown.
// fuel bounds the global-initializer recursion.
func (c *compiler) ifaceIdentTypExpr(id *ast.Ident, fuel int) ast.Expr {
	if fuel <= 0 {
		return nil
	}
	if b := c.fs.lookupBinding(id.Name); b != nil {
		return b.declTyp
	}
	if c.pkg != nil && c.pkg.Index != nil {
		if vd := c.pkg.Index.Vars[id.Name]; vd != nil {
			if vs, ok := vd.Spec.(*ast.ValueSpec); ok {
				if vs.Type != nil {
					return vs.Type
				}
				if len(vs.Values) == len(vs.Names) && vd.NameIdx < len(vs.Values) {
					rhs := vs.Values[vd.NameIdx]
					if t := c.inferredTypExpr(rhs); t != nil {
						return t
					}
					if rid, ok := ast.Unparen(rhs).(*ast.Ident); ok {
						return c.ifaceIdentTypExpr(rid, fuel-1)
					}
				}
			}
		}
	}
	return nil
}

// inferredTypExpr returns the type expression an RHS spells when its
// static type is syntactically obvious — T(v) conversions, v.(T)
// asserts, T{...} composites and &T{...} — else nil.
func (c *compiler) inferredTypExpr(e ast.Expr) ast.Expr {
	switch x := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		if c.conversionCall(x) {
			return x.Fun
		}
	case *ast.TypeAssertExpr:
		return x.Type // nil for the type-switch form — the caller skips
	case *ast.CompositeLit:
		return x.Type
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			if lit, ok := ast.Unparen(x.X).(*ast.CompositeLit); ok && lit.Type != nil {
				return &ast.StarExpr{Star: x.OpPos, X: lit.Type}
			}
		}
	}
	return nil
}

// refTarget emits code pushing the assignment target's storage reference
// — the phase-1 operand evaluation Go runs before the right side: cells
// for names, FieldRef/IndexRef for `s.f` / `s[i]`, the pointer itself for
// `*p`. `_` pushes nil — OpSetRefs discards its value. pin is multi-assign
// mode: a deref/index operand evaluates to its value now (gc's ascompatee
// copies operands a previous target could overwrite — `p, *p = fp()` still
// writes the old pointee) instead of re-resolving the live storage at
// store time.
func (c *compiler) refTarget(lhs ast.Expr, pin bool) {
	target := ast.Unparen(lhs)
	switch t := target.(type) {
	case *ast.Ident:
		if t.Name == "_" {
			c.emit(bytecode.OpNil, 0, 0, t.Pos())
			return
		}
		isUp, idx, ok := c.fs.find(t.Name)
		switch {
		case !ok:
			c.emit(bytecode.OpGlobalRef, c.nameIdx(t.Name), 0, t.Pos())
		case !isUp:
			c.emit(bytecode.OpLocalRef, idx, 0, t.Pos())
		default:
			c.emit(bytecode.OpUpvalRef, idx, 0, t.Pos())
		}
	case *ast.SelectorExpr:
		// B=1: store target — the nil-base check defers to the store so
		// the RHS still evaluates first (p.f = before()). The base is a
		// storage ref, not a value snapshot: `s.f` is s's storage + f,
		// so `s.f = replace(&s)` still targets s's live fields after the
		// RHS rewrites the whole variable. gc drills a value-field path
		// to its named root the same way — only `(*p).f`'s pointer
		// operand pins (handled by refTargetBase).
		c.refTargetBase(t.X, pin)
		// B=1 store target; +2 pins pointer bases (multi-assign): p.f
		// is an implicit indirection whose operand gc saves before the
		// stores, so `p, p.f = new(T), v` writes the old pointee.
		b := 1
		if pin {
			b |= 2
		}
		c.emit(bytecode.OpFieldRef, c.nameIdx(t.Sel.Name), b, t.Pos())
	case *ast.IndexExpr:
		// B=1: the ref is a store target — a map element is legal here
		// (m[k] = v), unlike `&` which Go forbids on map values, and the
		// bounds check defers to the store so the RHS evaluates first.
		// Single-assign keeps the storage ref (s[i] resolves s at store
		// time — `s[i] = f()` writes the new slice when f replaces s);
		// multi-assign pins the container value like gc's save pass
		// (`s, s[i] = f()` writes the old s's element).
		if pin {
			c.aliasBase(t.X)
		} else {
			c.refTargetBase(t.X, pin)
		}
		c.expr(t.Index)
		c.emit(bytecode.OpIndexRef, 0, 1, t.Lbrack)
	case *ast.StarExpr:
		if pin {
			// `*p` pins the pointer operand — `p, *p = fp()` writes
			// through the old p like gc's operand save, not the nil p
			// lands after the earlier store.
			c.expr(t.X)
		} else if c.isStorageBase(t.X) {
			// `*p` is the location p points at — resolve p's storage at
			// store time so `*p = f()` still writes the live pointee
			// when f reseats p.
			c.refTargetBase(t.X, pin)
			c.emit(bytecode.OpDerefRef, 0, 0, t.Pos())
		} else {
			c.expr(t.X)
		}
	default:
		c.trap(lhs.Pos(), "unsupported assignment target %T", lhs)
	}
}

// storageShape reports whether e — parens transparent — has a
// storage-location shape: an identifier, field selection, element
// index, or nested dereference. indexList counts an F[T, ...]
// instantiation index in: `range *e` accepts the shape, store targets
// do not.
func storageShape(e ast.Expr, indexList bool) bool {
	switch ast.Unparen(e).(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
		return true
	case *ast.IndexListExpr:
		return indexList
	}
	return false
}

// isStorageBase reports whether an expression denotes a storage
// location (an ident, field, element, or deref of one) — as opposed to
// a value-producing expression like a call, whose pointer result is the
// target itself.
func (c *compiler) isStorageBase(e ast.Expr) bool {
	return storageShape(e, false)
}

// selectorBaseIsVar reports whether a selector's base denotes a mutable
// lvalue whose storage ref a pointer receiver can write through. Type
// names and other value-shaped bases stay values: method expressions
// (T.M, F[T].M, (*T).M) resolve on the type, a call result has no
// storage, and a package qualifier is guarded inside refTargetBase.
func (c *compiler) selectorBaseIsVar(e ast.Expr) bool {
	e = ast.Unparen(e)
	switch t := e.(type) {
	case *ast.Ident:
		// a var (local, upval, or package-level) has storage; a type
		// name selects a method expression on the type, and an
		// unresolved name is an import or an error that OpGlobal
		// reports better than OpGlobalRef ("undefined: fmt").
		info, found := c.resolveName(t.Name)
		return found && !info.isType
	case *ast.SelectorExpr:
		// x.f.M binds &x.f when the field path itself is an lvalue —
		// a map element or a call result field reads as a copy.
		return c.selectorBaseIsVar(t.X)
	case *ast.IndexExpr:
		// refable elements went through the index-ref branch already;
		// F[T] instantiations and non-refable bases stay values.
		return false
	case *ast.StarExpr:
		// (*p).M binds the pointee's ref; (*T).M, (*F[T]).M and
		// (*pkg.T).M are pointer method expressions on the type.
		return c.starOperandIsValue(t.X)
	}
	return false
}

// indexOperand unwraps parens and reports e as an index expression.
func indexOperand(e ast.Expr) (*ast.IndexExpr, bool) {
	ix, ok := ast.Unparen(e).(*ast.IndexExpr)
	return ix, ok
}

// refableIndexBase reports whether an index operand's base denotes
// storage a selector's element ref can resolve — a var, a nested
// lvalue (x.f[i], (*p)[i], a[i][j]) — so a pointer-receiver method
// writes through like (&s[i]).M(). A type name means F[T]
// instantiation, and a package qualifier or a call result has no
// writable element storage; both stay on the value path.
func (c *compiler) refableIndexBase(e ast.Expr) bool {
	e = ast.Unparen(e)
	switch t := e.(type) {
	case *ast.Ident:
		info, found := c.resolveName(t.Name)
		return found && !info.isType
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && !c.isImportName(id.Name)
	case *ast.IndexExpr, *ast.StarExpr:
		return true
	}
	return false
}

// aliasBase emits the container operand of an index store or
// address-of by value — `h[k]` in `h[k][i] = v` / `&h[k][i]`, pinned by a
// multi-assign. Each nested element read it emits (OpIndex B=1) keeps a
// boxed host map or slice element over its host storage, where the plain
// read (OpInstantiate) hands a write a script copy; every other base
// reads as c.expr does.
func (c *compiler) aliasBase(e ast.Expr) {
	if ix, ok := indexOperand(e); ok && !c.isTypeForm(ix.Index) {
		c.aliasBase(ix.X)
		c.expr(ix.Index)
		c.emit(bytecode.OpIndex, 0, 1, ix.Lbrack)
		return
	}
	c.expr(e)
}

// refTargetBase emits the operand of a field/index ref — a storage ref
// for addressable bases (idents, fields, elements, derefs), the
// evaluated value for anything else (e.g. a call returning a pointer).
// pin propagates multi-assign pinning into deref bases (`(*p).f`).
func (c *compiler) refTargetBase(e ast.Expr, pin bool) {
	// A package name evaluates to the package object — it has no
	// storage ref, and OpFieldRef/OpSelect must see the value.
	if id, isIdent := e.(*ast.Ident); isIdent {
		if c.isImportName(id.Name) {
			c.expr(e)
			return
		}
		if info, found := c.resolveName(id.Name); !found || info.isType {
			// Unresolved at compile: under deferred facts the name may
			// be an import the index never consulted — emit the value
			// read so a package object still reaches OpFieldRef (a
			// real global resolves as a name, and a missing one traps
			// on the read the same way GlobalRef would).
			c.expr(e)
			return
		}
	}
	if c.isStorageBase(e) {
		c.refTarget(e, pin)
		return
	}
	c.expr(e)
}

// isImportName reports whether an identifier names an import in this
// file's scope (unless a closer declaration shadows it) — `pkg.X = v`
// assigns through the package value, not a storage ref.
func (c *compiler) isImportName(name string) bool {
	if _, _, found := c.fs.find(name); found {
		return false
	}
	_, ok := c.importRef(name)
	return ok
}

// importRef resolves an identifier to this file's import ref: first by
// the recorded local name, then by the package clause when it differs
// from the path's last element — Scopes keys on the basename, so
// `foo.V` misses when the clause says `package realname`; learn the
// real name by materializing each unnamed import, like the VM's global
// resolution does (vm.go's step 2.5).
func (c *compiler) importRef(name string) (*runtime.ImportRef, bool) {
	if ref, ok := c.synthImports[name]; ok {
		return ref, true
	}
	if c.pkg == nil || c.file == nil {
		return nil, false
	}
	return pkgImportRef(c.pkg, c.file, name)
}

// storeTarget emits the store for one LHS expression; the value is on stack.
func (c *compiler) storeTarget(lhs ast.Expr, isDefine bool) {
	switch t := lhs.(type) {
	case *ast.Ident:
		if t.Name == "_" {
			c.emit(bytecode.OpPop, 0, 0, t.Pos())
			return
		}
		if isDefine && !c.fs.inCurrentBlock(t.Name) {
			slot := c.fs.declare(t.Name, t.Pos())
			c.emit(bytecode.OpNewLocal, slot, 0, t.Pos())
		} else {
			c.setRef(t.Name, t.Pos())
		}
	case *ast.SelectorExpr:
		// stack: [.. v]; eval base -> [v base]; swap -> [base v];
		// OpSetField pops v then base.
		c.expr(t.X)
		c.emit(bytecode.OpSwap, 0, 0, t.Pos())
		c.emit(bytecode.OpSetField, c.nameIdx(t.Sel.Name), 0, t.Pos())
	case *ast.IndexExpr:
		// stack: [v]; eval base,idx -> [v base idx]; rot3 -> [base idx v]
		c.aliasBase(t.X)
		c.expr(t.Index)
		c.emit(bytecode.OpRot3, 0, 0, t.Pos())
		c.emit(bytecode.OpSetIndex, 0, 0, t.Lbrack)
	case *ast.StarExpr:
		c.expr(t.X)
		c.emit(bytecode.OpSwap, 0, 0, t.Pos())
		c.emit(bytecode.OpSetInd, 0, 0, t.Pos())
	default:
		c.trap(lhs.Pos(), "unsupported assignment target %T", lhs)
	}
}
