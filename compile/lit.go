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
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"strconv"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/spike"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// emitEllipsisLenFold emits the typedef of a `[...]T` literal whose
// length the compile-time fold could not resolve, for a call that must
// not evaluate the operand: the typedef's length folds at run time
// from the literal's index positions — the keys evaluate in scope the
// way Go evaluates constant keys — while the elements never run.
func (c *compiler) emitEllipsisLenFold(cl *ast.CompositeLit, te ast.Expr, elPos []constant.Value, elFile *syntax.File) {
	at := te.(*ast.ArrayType)
	file := c.file
	var localTypes map[string]*runtime.TypeDef
	if elFile != nil {
		// the literal belongs to the decl's file — its typedef
		// resolves there, with no use-site local types.
		file = elFile
	} else {
		localTypes = c.fs.localTypeDefs()
	}
	c.emit(bytecode.OpConst, c.constIdx(&runtime.TypeDef{Kind: runtime.KindSlice, Anon: at, Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes}), 0, cl.Pos())
	c.emit(bytecode.OpConst, c.constIdx(runtime.EllipsisLen), 0, cl.Pos())
	for i, e := range cl.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if i < len(elPos) && elPos[i] != nil {
				// baked at capture: a use-site binding must not
				// shadow what the key resolved to at the decl.
				k, _ := constant.Int64Val(constant.ToInt(elPos[i]))
				c.emit(bytecode.OpConst, c.constIdx(k), 0, kv.Key.Pos())
			} else if elFile != nil {
				// a key the fold could not see still resolves in
				// the decl's package scope — the use site may
				// shadow its names — so the fragment compiles and
				// evaluates there at run time.
				c.emit(bytecode.OpEvalAST, c.constIdx(&bytecode.ASTFragment{Expr: kv.Key, File: elFile}), 0, kv.Key.Pos())
			} else {
				// the key's position — constant or not, evaluated
				// in scope; a typed value (uint8(255)) keeps its
				// own width but the counter counts in int64.
				c.expr(kv.Key)
			}
		} else {
			// an unkeyed element resumes after the last position —
			// emitting a sentinel keeps the count out of the key's
			// own (possibly narrow) type.
			c.emit(bytecode.OpConst, c.constIdx(runtime.EllipsisNext), 0, e.Pos())
		}
	}
	c.emit(bytecode.OpPack, len(cl.Elts), 0, cl.Pos())
	c.emit(bytecode.OpNil, 0, 0, cl.Pos())
	c.emit(bytecode.OpCall, 1, 0, cl.Pos())
	c.emit(bytecode.OpFoldArrayLen, c.constIdx(at), 0, cl.Pos())
	c.emit(bytecode.OpLocalType, 0, 0, cl.Pos())
}

// resolveLitType resolves a composite literal's declared type to its
// underlying syntactic form — peeling parens and following named types
// through local `type` decls and the package index (`type M map[K]V`
// resolves M to the MapType). fuel bounds the named-type hops. It is
// unfoldTypExpr under the callers' own bound: the only semantic
// difference is exhaustion returning nil instead of the partial expr,
// and every caller applies a shape predicate that a partial expr fails
// anyway. mapLitType and litTypeAt differ in their predicates.
//
// A generic instantiation (`Pair[string, int]`, `pkg.G[T]`) resolves
// to its generic decl's type with the type arguments substituted for
// the parameters, so `type Pair[K comparable, V any] map[K]V` makes
// `Pair[string, int]` a MapType whose element literals peel to V's
// argument.
func (c *compiler) resolveLitType(t ast.Expr, fuel int) ast.Expr {
	for ; fuel > 0; fuel-- {
		u := c.unfoldTypExpr(t, fuel)
		inst := c.instantiateLitType(u)
		if inst == nil {
			// a bound type parameter stays opaque to unfoldTypExpr —
			// but a literal spelled against the parameter's own name
			// takes the argument's container shape (`M{k: v}` for
			// `M ~map[string]int` bound to map[string]int), with the
			// declared form's leaf names kept in their declaring
			// package's scope.
			if id, ok := ast.Unparen(u).(*ast.Ident); ok {
				if e := c.litBindShape(id.Name); e != nil {
					return e
				}
			}
			return u
		}
		t = inst
	}
	return nil
}

// litBindShape returns the container shape a bound type parameter's
// argument typedef declares — its Anon spelling, or its spec's type
// resolved and re-scoped through the declaring package the way
// unfoldSpecType does (a leaf `N` of `lib.Items` stays lib's `N`, never
// the caller's). Nil when the argument exposes no syntactic form.
func (c *compiler) litBindShape(name string) ast.Expr {
	td, ok := c.binds[name].(*runtime.TypeDef)
	if !ok || td == nil {
		return nil
	}
	if td.Anon != nil {
		if td.Pkg != nil && td.File != nil {
			return c.scopeSpecType(td.Pkg, td.File, td.Anon)
		}
		return td.Anon
	}
	if td.Spec != nil && td.Pkg != nil {
		return c.unfoldSpecType(td.Pkg, td.Spec, 8)
	}
	return nil
}

// instantiateLitType returns the type a generic instantiation `G[A...]`
// spells — G's declared type scoped to the caller with A substituted
// for G's type parameters — or nil when e is no instantiation of a
// source-indexed generic type.
func (c *compiler) instantiateLitType(e ast.Expr) ast.Expr {
	var base ast.Expr
	var args []ast.Expr
	switch x := e.(type) {
	case *ast.IndexExpr:
		base, args = x.X, []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		base, args = x.X, x.Indices
	default:
		return nil
	}
	var p *runtime.Package
	var ts *ast.TypeSpec
	switch b := ast.Unparen(base).(type) {
	case *ast.Ident:
		// function-local types cannot be generic: a local binding of
		// the name means the index is no instantiation.
		if c.fs.lookupBinding(b.Name) != nil {
			return nil
		}
		if _, isUp := c.fs.upmap[b.Name]; isUp {
			return nil
		}
		p, ts = c.pkg, pkgTypeSpec(c.pkg, b.Name)
	case *ast.SelectorExpr:
		if ref, name, ok := c.selTypRef(b); ok {
			p, ts = c.selTypeSpec(ref, name)
		}
	}
	if ts == nil {
		return nil
	}
	params := syntax.TypeParamNames(ts.TypeParams)
	if len(params) == 0 || len(params) != len(args) {
		return nil
	}
	subs := make(map[string]ast.Expr, len(params))
	for i, name := range params {
		subs[name] = args[i]
	}
	return substTypeParams(c.scopeSpecType(p, specFileOf(p, ts.Pos()), ts.Type), subs)
}

// substTypeParams replaces type parameter names with their arguments in
// the type positions a composite literal's elements peel through
// (element, key, value, pointee, instantiation args). Struct, interface
// and func types return as-is: their literal keys are field names and
// their members never take an elided literal.
func substTypeParams(e ast.Expr, subs map[string]ast.Expr) ast.Expr {
	switch t := e.(type) {
	case *ast.Ident:
		if a, ok := subs[t.Name]; ok {
			return a
		}
		return t
	case *ast.ParenExpr:
		return &ast.ParenExpr{Lparen: t.Lparen, X: substTypeParams(t.X, subs), Rparen: t.Rparen}
	case *ast.ArrayType:
		return &ast.ArrayType{Lbrack: t.Lbrack, Len: t.Len, Elt: substTypeParams(t.Elt, subs)}
	case *ast.MapType:
		return &ast.MapType{Map: t.Map, Key: substTypeParams(t.Key, subs), Value: substTypeParams(t.Value, subs)}
	case *ast.StarExpr:
		return &ast.StarExpr{Star: t.Star, X: substTypeParams(t.X, subs)}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Ellipsis: t.Ellipsis, Elt: substTypeParams(t.Elt, subs)}
	case *ast.ChanType:
		return &ast.ChanType{Begin: t.Begin, Arrow: t.Arrow, Dir: t.Dir, Value: substTypeParams(t.Value, subs)}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: t.X, Lbrack: t.Lbrack, Index: substTypeParams(t.Index, subs), Rbrack: t.Rbrack}
	case *ast.IndexListExpr:
		idx := make([]ast.Expr, len(t.Indices))
		for i, x := range t.Indices {
			idx[i] = substTypeParams(x, subs)
		}
		return &ast.IndexListExpr{X: t.X, Lbrack: t.Lbrack, Indices: idx, Rbrack: t.Rbrack}
	}
	return e
}

// mapLitType reports whether a composite literal's declared type is a
// map — syntactically (`map[K]V{}`) or through a named type
// (`type M map[K]V; M{}`), like litTypeAt resolves.
func (c *compiler) mapLitType(t ast.Expr, fuel int) bool {
	_, ok := c.resolveLitType(t, fuel).(*ast.MapType)
	return ok
}

func (c *compiler) compositeLit(x *ast.CompositeLit) {
	c.compileLit(x, litTyp{expr: x.Type})
}

// litTyp names a composite literal's declared type. expr is the type
// AST and depth the element peels to apply; when the literal's type
// only resolves through a deferred fact, key marks the map-key typedef
// of the peeled type and after peels past it — the site fields then
// spell the same question to the VM.
type litTyp struct {
	expr  ast.Expr
	depth int
	key   bool
	after int
}

// callSite records a deferred literal-type fact on the chunk and
// returns its CallSites index. The pinned context typedef carries the
// compile scope (package, file, binds, function-local types) the
// question was asked in.
func (c *compiler) callSite(lt litTyp, pos token.Pos) int {
	file := c.file
	var localTypes map[string]*runtime.TypeDef
	if c.pkgScopeTyp != nil {
		file = c.pkgScopeTyp
	} else {
		localTypes = c.fs.localTypeDefs()
	}
	c.ch.CallSites = append(c.ch.CallSites, &bytecode.CallSite{
		Expr: lt.expr, File: file, Pos: pos,
		Depth: lt.depth, Key: lt.key, After: lt.after,
		Ctx: &runtime.TypeDef{Pkg: c.pkg, File: file, Binds: c.binds, LocalTypes: localTypes},
	})
	return len(c.ch.CallSites) - 1
}

// compileLit emits a composite literal. lt is the declared type: an AST
// the typedef is derived from, or a deferred-fact spelling when the
// type only resolves at run time — element literals may omit their type
// (`{{1,2}}` inside `[][]int`), inheriting the enclosing literal's
// element type via run-time resolution either way.
func (c *compiler) compileLit(x *ast.CompositeLit, lt litTyp) {
	if lt.key || lt.after > 0 {
		// the typedef is itself a deferred fact: an elided literal
		// under a not-yet-resolved container.
		c.emit(bytecode.OpDeferredTyp, c.callSite(lt, x.Pos()), 0, x.Pos())
	} else {
		c.typeExpr(lt.expr)
		for i := 0; i < lt.depth; i++ {
			c.emit(bytecode.OpElemType, 0, 0, x.Pos())
		}
	}
	kv := false
	for _, el := range x.Elts {
		if _, ok := el.(*ast.KeyValueExpr); ok {
			kv = true
			break
		}
	}
	// own is the literal's resolved type with an elided `&` stripped
	// (`[]*T{{...}}` builds a T); it is resolved lazily — only keyed
	// elements and elided nested literals need it, and resolving an
	// imported name materializes its package.
	var own ast.Expr
	var ptr, ownDone bool
	ownType := func() ast.Expr {
		if !ownDone {
			ownDone = true
			if lt.key || lt.after > 0 {
				// the literal's type is a deferred fact — nothing the
				// AST can peel.
				own, ptr = nil, false
			} else {
				own, ptr = c.litTypeAt(lt.expr, lt.depth)
			}
		}
		return own
	}
	emitVal := func(val ast.Expr) {
		if lit, ok := val.(*ast.CompositeLit); ok && lit.Type == nil {
			child := lt
			if lt.key || lt.after > 0 {
				// one more element peel past the map key.
				child.after++
			} else {
				// an elided `&T` literal is typed *T at run time, so its
				// own elements sit one OpElemType below the pointee.
				child.depth = lt.depth + 1
				if ownType(); ptr {
					child.depth++
				}
			}
			c.compileLit(lit, child)
			return
		}
		c.expr(val)
	}
	// Whether an identifier key is a field name or a real expression is
	// decidable when the literal's type resolves to a map or an array —
	// `map[int]int{K: 1}` and `[]int{K: 1}` evaluate K, `S{K: 1}` names
	// a field.
	keysAreExprs := false
	for _, el := range x.Elts {
		if kvel, ok := el.(*ast.KeyValueExpr); ok {
			if _, ok := kvel.Key.(*ast.Ident); ok {
				switch ownType().(type) {
				case *ast.MapType, *ast.ArrayType:
					keysAreExprs = true
				}
				break
			}
		}
	}
	for _, el := range x.Elts {
		if kvel, isKV := el.(*ast.KeyValueExpr); kv && !isKV {
			// mixed keyed/positional elements — arrays allow it: the
			// positional element takes the running index, resolved at
			// run time since keys may be named constants.
			c.emit(bytecode.OpConst, c.constIdx(&runtime.ImplicitIndex{}), 0, el.Pos())
			emitVal(el)
		} else if kv {
			if lit, ok := kvel.Key.(*ast.CompositeLit); ok && lit.Type == nil {
				// an elided key literal (map[K]V{{...}: v}) inherits
				// the map's declared key type when it resolves; when the
				// literal's own type is undecided, the question rides a
				// CallSite to run time.
				if mt, ok := ownType().(*ast.MapType); ok {
					c.compileLit(lit, litTyp{expr: mt.Key})
				} else if undecidedLitType(ownType()) {
					c.compileLit(lit, litTyp{expr: lt.expr, depth: lt.depth, key: true, after: lt.after})
				} else {
					c.expr(kvel.Key)
				}
			} else if id, ok := kvel.Key.(*ast.Ident); ok && !keysAreExprs && !undecidedLitType(ownType()) {
				// In struct literals the key is a field name, not an
				// expression; the typedef confirms the map/index case.
				c.emit(bytecode.OpConst, c.constIdx(id.Name), 0, id.Pos())
			} else if id != nil && !keysAreExprs {
				// ownType()==nil: the literal's type is a deferred fact.
				// OpLitKey resolves it at run time — a map or array
				// evaluates the emitted key expression; anything else
				// pushes the field-name const and jumps over it.
				jm := c.emit3(bytecode.OpLitKey, 0, c.callSite(lt, id.Pos()), c.nameIdx(id.Name), id.Pos())
				c.expr(kvel.Key)
				je := c.emit(bytecode.OpJump, 0, 0, id.Pos())
				c.patchA(jm, len(c.ch.Code))
				c.patchA(je, len(c.ch.Code))
			} else {
				c.expr(kvel.Key)
			}
			emitVal(kvel.Value)
		} else {
			emitVal(el)
		}
	}
	b := 0
	if kv {
		b = 1
	}
	c.emit(bytecode.OpMakeComposite, len(x.Elts), b, x.Pos())
}

// undecidedLitType reports whether a litTypeAt result decides nothing
// about key reading: nil, or a leftover name or selector that
// resolveLitType could not unfold — a deferred fact (an unindexed or
// type-parameter-bound name). Only decided composite forms answer.
func undecidedLitType(t ast.Expr) bool {
	switch ast.Unparen(t).(type) {
	case *ast.MapType, *ast.ArrayType, *ast.StructType:
		return false
	}
	return true
}

// litTypeAt resolves the type a composite literal `depth` element peels
// below baseType takes — the type an elided nested literal (`{...}`
// inside `[]map[K]V{...}`) is built as. Each level resolves to its
// underlying form before it is peeled (array elt / map value / pointee
// / ellipsis), so a named, aliased, local, imported or instantiated
// generic container exposes its element: `type Ms []map[string]int;
// Ms{{k: 1}}` reaches the map. The result is resolved too; a pointer
// result is the elided `&T` form — t is then T resolved and ptr is
// set. A name that cannot be pinned down at compile time (a type
// parameter, an unindexed package) comes back unresolved, and callers
// keep the struct-style field-name reading of its keys.
func (c *compiler) litTypeAt(baseType ast.Expr, depth int) (t ast.Expr, ptr bool) {
	t = baseType
	for i := 0; t != nil && i < depth; i++ {
		t = syntax.PeelLitType(c.resolveLitType(t, 8), 1)
	}
	if t == nil {
		return nil, false
	}
	t = c.resolveLitType(t, 8)
	if st, ok := t.(*ast.StarExpr); ok {
		return c.resolveLitType(st.X, 8), true
	}
	return t, false
}

// emitLenFolds emits the in-scope evaluation of every non-literal array
// length inside a type AST. Each OpFoldArrayLen publishes the value as
// the length of its node (A: const index of the *ast.ArrayType) so
// `[n]int`/`[len(a)]*T` spell the concrete `[3]*T` at type-identity
// compares, like Go's constant folding. The AST itself is never
// rewritten — it is shared by every goroutine running the code.
// `[]T`, `[3]T` and `[...]T` emit nothing (runtime.ArrayLenNodes).
func (c *compiler) emitLenFolds(e ast.Expr) {
	for _, at := range runtime.ArrayLenNodes(e) {
		if c.pkgScopeTyp != nil {
			// the length belongs to a package decl — its names
			// resolve in that file's scope, not the use site's.
			c.emit(bytecode.OpEvalAST, c.constIdx(&bytecode.ASTFragment{Expr: at.Len, File: c.pkgScopeTyp}), 0, at.Len.Pos())
		} else {
			c.expr(at.Len)
		}
		c.emit(bytecode.OpFoldArrayLen, c.constIdx(at), 0, e.Pos())
	}
}

// funcLit compiles a function literal into a separate chunk and emits a
// closure creation.
func (c *compiler) funcLit(x *ast.FuncLit) {
	// a synthetic Decl carries the signature so inference can unify a
	// funclit argument against `func(E) R`-shaped parameters.
	c.litCount++
	litName := c.symName + ".func" + strconv.Itoa(c.litCount)
	if c.symName == "" {
		litName = "<funclit>"
	}
	inner := &runtime.Function{Pkg: c.pkg, File: c.file, Name: litName, Decl: &ast.FuncDecl{Type: x.Type, Body: x.Body}}
	ic := &compiler{pkg: c.pkg, file: c.file, fs: newFScope(c.fs), ch: &bytecode.Chunk{Name: litName}, labels: map[string]*labelInfo{}, binds: c.binds, symName: litName, iotaVal: -1}
	ic.fs.pushBlock()
	nparams := 0
	var coerces []paramCoerce
	if x.Type.Params != nil {
		for _, field := range x.Type.Params.List {
			names := field.Names
			if len(names) == 0 {
				// minted on the funclit's own compiler so it stays
				// unique against $arg hoists inside the body.
				names = []*ast.Ident{{Name: ic.fresh("$arg")}}
			}
			for _, n := range names {
				slot := ic.fs.declare(n.Name, n.Pos())
				coerces = append(coerces, paramCoerce{slot: slot, typ: field.Type})
				ic.fs.noteDeclTyp(n.Name, ic.declTypCanon(field.Type), nil)
				nparams++
			}
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				ic.ch.IsVararg = true
			}
		}
	}
	ic.ch.NParams = nparams
	ic.emitParamCoerces(coerces)
	if x.Type.Results != nil {
		for _, field := range x.Type.Results.List {
			for _, n := range field.Names {
				slot := ic.fs.declare(n.Name, n.Pos())
				ic.fs.noteDeclTyp(n.Name, ic.declTypCanon(field.Type), nil)
				ic.ch.NamedSlots = append(ic.ch.NamedSlots, slot)
				ic.emit(bytecode.OpNil, 0, 0, n.Pos())
				ic.emit(bytecode.OpNewLocal, slot, 0, n.Pos())
				ic.emitSigTypeCoerce(slot, field.Type, n.Pos())
			}
		}
		ic.ch.NResults = countResults(x.Type.Results)
	}
	ic.results = syntax.FieldTypes(x.Type.Results)
	if x.Body != nil {
		// same as Func: the body's outer scope is the signature's block.
		for _, s := range x.Body.List {
			ic.stmt(s)
		}
	}
	ic.resolveGotos()
	ic.emit(bytecode.OpReturn, ic.ch.NResults, 0, x.End())
	ic.ch.NLocals = ic.fs.nlocals
	ic.ch.Upvals = ic.fs.upvals
	ic.ch.Boxed = ic.fs.boxedSlice()
	spike.NoteChunkClassified(ic.ch.Boxed)
	inner.Chunk = ic.ch

	c.emit(bytecode.OpMakeClosure, c.constIdx(inner), 0, x.Pos())
}

// literalValue converts a BasicLit to a runtime value.
func literalValue(l *ast.BasicLit) (any, error) {
	switch l.Kind {
	case token.INT:
		v := constant.MakeFromLiteral(l.Value, token.INT, 0)
		// int literals stay untyped constants even when they fit an
		// int64, like floats/runes already do: materializing eagerly
		// loses the constness later operations need — `int8(300)` and
		// `var x int8 = 300` must trap (constant overflows), which a
		// bare int64 arriving at the conversion cannot distinguish from
		// `int8(x)` on a variable, a legal truncation.
		return &runtime.UConst{V: v}, nil
	case token.FLOAT:
		// float literals stay untyped constants like ints (and like
		// constOperand already emits them): exact-domain folds such as
		// `1.0 / (iota + 4)` keep exact-rational precision, and a later
		// float32 conversion sees the unrounded constant.
		return &runtime.UConst{V: constant.MakeFromLiteral(l.Value, token.FLOAT, 0)}, nil
	case token.IMAG:
		// imaginary literals exist only in the constant domain until
		// materialized into a complex64/128 value.
		return &runtime.UConst{V: constant.MakeFromLiteral(l.Value, token.IMAG, 0)}, nil
	case token.STRING:
		// string literals stay untyped constants like the numeric
		// kinds: a bare string is indistinguishable from a `var s
		// string`, which would let `tag == "json"` (const, lawful
		// adoption) and `tag == s` (typed var, gc's reject) reach
		// the runtime as the same value.
		v, err := strconv.Unquote(l.Value)
		if err != nil {
			return nil, err
		}
		return &runtime.UConst{V: constant.MakeString(v)}, nil
	case token.CHAR:
		// rune literals stay untyped so a bare 'a' defaults to rune
		// (int32) while still converting into any numeric target.
		return &runtime.UConst{V: constant.MakeFromLiteral(l.Value, token.CHAR, 0), Rune: true}, nil
	}
	return nil, fmt.Errorf("unsupported literal kind %s", l.Kind)
}

// orderSpecs sorts var/const specs in dependency order (Go spec: package-level
// initialization proceeds in dependency order, with source order as the
// tie-breaker). A spec depends on every package-level name free in its value
// expressions — including names reached transitively through function bodies
// (`var x = f()` depends on every package-level var f reads, and on what
// functions f calls read, recursively). Cyclic leftovers keep source order.
func orderSpecs(ix *index.Index, reps []*index.Decl) []*index.Decl {
	declared := map[string]bool{}
	for n := range ix.Vars {
		declared[n] = true
	}
	for n := range ix.Consts {
		declared[n] = true
	}

	// a method name stands for one declaring type's method (the first
	// the type map yields, as the per-name scan did)
	methods := map[string]*index.Decl{}
	for _, td := range ix.Types {
		for name, m := range td.Methods {
			if m != nil && methods[name] == nil {
				methods[name] = m
			}
		}
	}

	// package-level names referenced by an AST (also covers nested func
	// literals). Locals, fields and builtins never feed a dependency, so
	// they stay out of the sets funcRefs merges transitively.
	refs := func(n ast.Node, out map[string]bool) {
		ast.Inspect(n, func(x ast.Node) bool {
			if id, ok := x.(*ast.Ident); ok {
				if declared[id.Name] || ix.Funcs[id.Name] != nil || methods[id.Name] != nil {
					out[id.Name] = true
				}
			}
			return true
		})
	}

	// funcRefs(name) = package-level names reachable from the function's
	// body, transitively through other functions/methods it references.
	funcRefsCache := map[string]map[string]bool{}
	var funcRefs func(name string, depth int) map[string]bool
	funcRefs = func(name string, depth int) map[string]bool {
		if depth > 16 {
			return nil // recursion budget: cycles/deep chains keep source order
		}
		if r, ok := funcRefsCache[name]; ok {
			return r
		}
		d := ix.Funcs[name]
		if d == nil {
			d = methods[name]
		}
		if d == nil {
			funcRefsCache[name] = nil
			return nil
		}
		r := map[string]bool{}
		refs(d.Func, r)
		// follow function-valued references one level further
		for n := range r {
			if declared[n] {
				continue
			}
			for m := range funcRefs(n, depth+1) {
				r[m] = true
			}
		}
		funcRefsCache[name] = r
		return r
	}

	// spec -> specs it depends on (a spec provides its names)
	providedBy := map[string]*ast.ValueSpec{}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		for _, n := range vs.Names {
			providedBy[n.Name] = vs
		}
	}

	deps := map[*ast.ValueSpec]map[*ast.ValueSpec]bool{}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		vals := vs.Values
		if len(vals) == 0 {
			vals = d.Inherited
		}
		ds := map[*ast.ValueSpec]bool{}
		names := map[string]bool{}
		for _, e := range vals {
			refs(e, names)
		}
		// widen direct references through function bodies
		for n := range names {
			if declared[n] {
				continue
			}
			for m := range funcRefs(n, 0) {
				names[m] = true
			}
		}
		for n := range names {
			if !declared[n] {
				continue
			}
			if dep := providedBy[n]; dep != nil && dep != vs {
				ds[dep] = true
			}
		}
		deps[vs] = ds
	}

	// dependency-gated priority order, matching go's types2 initOrder:
	// repeatedly emit the unemitted spec with the fewest outstanding
	// dependencies, breaking ties by declaration order. Dependencies
	// only hold their dependants back — they are not pulled early — so
	// a no-dep var declared between a dependant and its dep runs first
	// ($GOROOT/test/fixedbugs/issue22326.go prints "abc_d": a, b, c in
	// declaration order, then the dep-blocked `_ = f("_", c, b)`, then d).
	bySpec := map[*ast.ValueSpec]*index.Decl{}
	order := map[*ast.ValueSpec]int{}
	for i, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		bySpec[vs] = d
		order[vs] = i
	}
	ndeps := map[*ast.ValueSpec]int{}
	consumers := map[*ast.ValueSpec][]*ast.ValueSpec{}
	for _, d := range reps {
		vs := d.Spec.(*ast.ValueSpec)
		ndeps[vs] = len(deps[vs])
		for dep := range deps[vs] {
			if _, ok := bySpec[dep]; ok {
				consumers[dep] = append(consumers[dep], vs)
			}
		}
	}
	var out []*index.Decl
	emitted := map[*ast.ValueSpec]bool{}
	// Const specs pop before every var spec — go/types2's priority puts
	// constants first so a var's const dependency never holds it back
	// (issue66575). The tier only applies among ready nodes: a minigo
	// const still evaluates its expression at init, so a const blocked
	// on unemitted var deps (len(b.a)) can't pop until they exist.
	isConstSpec := func(vs *ast.ValueSpec) bool {
		for _, n := range vs.Names {
			if ix.Consts[n.Name] != nil {
				return true
			}
		}
		return false
	}
	for len(emitted) < len(reps) {
		var best *index.Decl
		bestDeps, bestOrder := -1, -1
		bestRank := 1
		for _, d := range reps {
			vs := d.Spec.(*ast.ValueSpec)
			if emitted[vs] {
				continue
			}
			rank := 1
			if isConstSpec(vs) && ndeps[vs] == 0 {
				rank = 0
			}
			if best == nil || rank < bestRank ||
				(rank == bestRank && (ndeps[vs] < bestDeps || (ndeps[vs] == bestDeps && order[vs] < bestOrder))) {
				best, bestDeps, bestOrder, bestRank = d, ndeps[vs], order[vs], rank
			}
		}
		// a dependency cycle leaves every node dep-blocked; the minimum
		// still pops, keeping the order total like go's cycle fallback.
		vs := best.Spec.(*ast.ValueSpec)
		emitted[vs] = true
		out = append(out, best)
		for _, consumer := range consumers[vs] {
			ndeps[consumer]--
		}
	}
	return out
}
