// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// hostArgAsChecked is hostArgAs reporting a mismatch as gc's template
// error, `wrong type for value; expected T; got U`, rather than a trap.
func (v *VM) hostArgAsChecked(x runtime.Value, td *runtime.TypeDef, spell string) (r runtime.Value, err error) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(*runtime.Trap); !ok {
				panic(p)
			}
			r, err = nil, fmt.Errorf("wrong type for value; expected %s; got %s", spell, typeNameOf(x))
		}
	}()
	arg := x
	if u := v.peelNamed(td); u != nil && u.Kind != runtime.KindPointer && u.Kind != runtime.KindInterface {
		// like the template engine's own indirection: a pointer argument
		// fits a parameter of its pointee type.
		switch x.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if dv, ok := runtime.Deref(x); ok {
				arg = dv
			}
		}
	}
	return v.hostArgAs(&frame{fn: &runtime.Function{Name: "<template func>"}}, arg, td, 0), nil
}

// hostArgAs lifts x, an argument that crossed the host as `any`, to the
// value of the script type td a parameter declares. A script container
// reached the host as the generic []any / map[any]any projection (see
// deepHost), which a `[]Pt` or `map[string]Pt` parameter rejects; here
// its elements convert to the declared element type and the container
// takes the declared type, recursively. Whatever does not fit traps, as
// coerce does for a callee's own parameter binding.
func (v *VM) hostArgAs(fr *frame, x runtime.Value, td *runtime.TypeDef, depth int) runtime.Value {
	utd := v.peelNamed(td)
	if utd == nil || depth > 8 {
		return x
	}
	switch utd.Kind {
	case runtime.KindSlice:
		switch s := x.(type) {
		case *runtime.TypedNil:
			if hostGenericTyp(s.Typ) {
				x = &runtime.TypedNil{Typ: td}
			}
		case *runtime.Slice:
			if et := v.elemTypedef(nil, utd); et != nil && !s.Virtual() && hostGenericTyp(s.Typ) {
				// an array parameter takes only a value of its length
				if n, isArr := v.arrayLen(fr, utd); isArr && n != int64(len(s.Elems)) {
					fr.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
				}
				elems := make([]runtime.Value, len(s.Elems))
				for i, e := range s.Elems {
					elems[i] = v.hostArgAs(fr, e, et, depth+1)
				}
				x = &runtime.Slice{Elems: elems}
			}
		}
	case runtime.KindMap:
		switch m := x.(type) {
		case *runtime.TypedNil:
			if hostGenericTyp(m.Typ) {
				x = &runtime.TypedNil{Typ: td}
			}
		case *runtime.Map:
			kt, et := v.mapKeyTypedef(nil, utd), v.elemTypedef(nil, utd)
			if kt != nil && et != nil && (m.Typ == nil || hostGenericTyp(m.Typ)) {
				ku := v.peelNamed(kt)
				out := runtime.NewMap(nil, ku != nil && ku.Kind == runtime.KindInterface)
				for i := 0; i < m.Len(); i++ {
					k, e := m.At(i)
					out.Insert(v.hostArgAs(fr, k, kt, depth+1), v.hostArgAs(fr, e, et, depth+1))
				}
				x = out
			}
		}
	}
	r := v.coerce(fr, x, td)
	if _, ok := r.(*runtime.Struct); ok {
		// coerce copied the struct; an element of a slice the callee
		// writes through must stay the caller's own, as in Go (the
		// callee's parameter binding copies it again where Go would).
		if _, ok := x.(*runtime.Struct); ok {
			return x
		}
	}
	return r
}

// hostGenericTyp reports whether td is the type a generic host
// projection carries — []any or map[any]any, or none at all — the shapes
// hostArgAs lifts (a script value of a declared type is left to coerce).
func hostGenericTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return true
	}
	if td.Name != "" {
		return false
	}
	isAny := func(e ast.Expr) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return t.Name == "any"
		case *ast.InterfaceType:
			return t.Methods == nil || len(t.Methods.List) == 0
		}
		return false
	}
	switch t := td.Anon.(type) {
	case *ast.ArrayType:
		return t.Len == nil && isAny(t.Elt)
	case *ast.MapType:
		return isAny(t.Key) && isAny(t.Value)
	}
	return false
}

// anonSliceTyp builds the anonymous []name typedef used to tag slices
// unboxed from host values (no package context — the name is a builtin).
func anonSliceTyp(name string) *runtime.TypeDef {
	return anonSliceTypX(ast.NewIdent(name))
}

// anonSliceTypX builds the anonymous []T typedef from a real element
// expression — composite element types (maps, nested slices) need the
// structure a bare identifier cannot carry, or the typedef's spelling
// stops matching the script-side equivalent.
func anonSliceTypX(elt ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: elt}}
}

// anonMapTypX builds the anonymous map[K]V typedef from real key and
// element expressions — see anonSliceTypX for why a bare identifier is
// not enough.
func anonMapTypX(key, elem ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindMap, Anon: &ast.MapType{
		Key:   key,
		Value: elem,
	}}
}

// anonArrayTypX builds the anonymous [n]T typedef from a real element
// expression — see anonSliceTypX.
func anonArrayTypX(n int, elt ast.Expr) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{
		Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(n)},
		Elt: elt,
	}}
}

// anonExprOf renders a reflect type as the anonymous type expression a
// typedef carries: named types answer their bare identifier (the same
// spelling elemTypeName produced), anonymous composites answer their
// structure — an empty interface is `interface{}` like typeExprName
// spells it, so `map[string]any`, `[]any`, `chan int` and friends mint
// typedefs whose spelling matches the script-side equivalent instead
// of one unparseable identifier ("map[string]interface {}").
// reflectTypeExpr walks the same reflect.Type→ast.Expr shape with the
// deliberately different contract host-func signatures need — nil for
// named types rather than a fabricated identifier.
func anonExprOf(t reflect.Type) ast.Expr {
	if t == hostRValueType {
		return ast.NewIdent("reflect.Value")
	}
	if n := t.Name(); n != "" {
		return ast.NewIdent(n)
	}
	switch t.Kind() {
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return &ast.InterfaceType{Methods: &ast.FieldList{}}
		}
	case reflect.Map:
		return &ast.MapType{Key: anonExprOf(t.Key()), Value: anonExprOf(t.Elem())}
	case reflect.Slice:
		return &ast.ArrayType{Elt: anonExprOf(t.Elem())}
	case reflect.Array:
		return &ast.ArrayType{
			Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(t.Len())},
			Elt: anonExprOf(t.Elem()),
		}
	case reflect.Pointer:
		return &ast.StarExpr{X: anonExprOf(t.Elem())}
	case reflect.Chan:
		var dir ast.ChanDir
		switch t.ChanDir() {
		case reflect.RecvDir:
			dir = ast.RECV
		case reflect.SendDir:
			dir = ast.SEND
		default:
			dir = ast.RECV | ast.SEND
		}
		return &ast.ChanType{Dir: dir, Value: anonExprOf(t.Elem())}
	case reflect.Struct:
		fl := &ast.FieldList{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fd := &ast.Field{Type: anonExprOf(f.Type)}
			if !f.Anonymous {
				fd.Names = []*ast.Ident{ast.NewIdent(f.Name)}
			}
			if f.Tag != "" {
				fd.Tag = &ast.BasicLit{Kind: token.STRING, Value: "`" + string(f.Tag) + "`"}
			}
			fl.List = append(fl.List, fd)
		}
		return &ast.StructType{Fields: fl}
	case reflect.Func:
		fields := func(n int, at func(i int) reflect.Type) *ast.FieldList {
			fl := &ast.FieldList{}
			for i := 0; i < n; i++ {
				fl.List = append(fl.List, &ast.Field{Type: anonExprOf(at(i))})
			}
			return fl
		}
		ft := &ast.FuncType{Params: fields(t.NumIn(), t.In)}
		if t.IsVariadic() {
			last := ft.Params.List[len(ft.Params.List)-1]
			last.Type = &ast.Ellipsis{Elt: last.Type.(*ast.ArrayType).Elt}
		}
		if t.NumOut() > 0 {
			ft.Results = fields(t.NumOut(), t.Out)
		}
		return ft
	}
	return ast.NewIdent(t.String())
}

// typedefOf unwraps references down to a *TypeDef, or nil.
func typedefOf(v runtime.Value) *runtime.TypeDef {
	for {
		if td, ok := v.(*runtime.TypeDef); ok {
			return td
		}
		dv, ok := runtime.Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}

// inferForFuncTarget instantiates an unbound generic function value when
// the target typedef is a func type whose signature teaches the missing
// binds. Anything unresolvable passes the value through unchanged — an
// unbound generic traps on use, matching the compile-total contract.
func (v *VM) inferForFuncTarget(x runtime.Value, td *runtime.TypeDef) (runtime.Value, error) {
	u := v.peelNamed(td)
	if u == nil || u.Kind != runtime.KindFunc {
		return x, nil
	}
	sig := funcTypeExpr(u)
	if sig == nil || v.H.ResolveType == nil {
		return x, nil
	}
	fn, wrap := funcTarget(x)
	if fn == nil || len(fn.TParams) == 0 || !hasUnbound(fn.TParams, fn.Binds) {
		return x, nil
	}
	if fn.Decl == nil || fn.Decl.Type == nil {
		return x, nil
	}
	tset := map[string]bool{}
	for _, tp := range fn.TParams {
		if _, ok := fn.Binds[tp]; !ok {
			tset[tp] = true
		}
	}
	binds := map[string]runtime.Value{}
	for k, bv := range fn.Binds {
		binds[k] = bv
	}
	// the pattern is the callee's declared signature; the concrete side
	// is the target func type resolved in ITS own context.
	ctx := &runtime.TypeDef{Pkg: fn.Pkg, File: fn.File, Binds: binds}
	v.unifyFieldTypes(ctx, tset, binds, fn.Decl.Type.Params, sig.Params, u)
	v.unifyFieldTypes(ctx, tset, binds, fn.Decl.Type.Results, sig.Results, u)
	if len(binds) == len(fn.Binds) {
		return x, nil // the target signature taught nothing
	}
	if err := v.checkTArgs(ctx, fn.TParams, fn.TConstraints, binds); err != nil {
		return nil, err
	}
	inst := &runtime.Function{
		Pkg: fn.Pkg, File: fn.File, Decl: fn.Decl, Name: fn.Name,
		Recv: fn.Recv, PtrRecv: fn.PtrRecv,
		TParams: fn.TParams, TConstraints: fn.TConstraints,
		Binds: binds, Compile: fn.Compile,
	}
	switch w := wrap.(type) {
	case nil:
		return inst, nil
	case *runtime.Closure:
		return &runtime.Closure{Fn: inst, Upvals: w.Upvals}, nil
	case *runtime.BoundMethod:
		return &runtime.BoundMethod{Recv: w.Recv, Fn: inst}, nil
	}
	return inst, nil
}

// funcTarget unwraps a function-ish value to its *runtime.Function,
// reporting the wrapper to rebuild after instantiation.
func funcTarget(x runtime.Value) (*runtime.Function, runtime.Value) {
	switch t := x.(type) {
	case *runtime.Function:
		return t, nil
	case *runtime.Closure:
		return t.Fn, t
	case *runtime.BoundMethod:
		return t.Fn, t
	case *runtime.Named:
		if fn, w := funcTarget(t.V); fn != nil {
			if w == nil {
				return fn, t
			}
			// rebuild the inner wrapper under the Named tag lazily — a
			// named func value's generic params live on the function.
			return fn, w
		}
	}
	return nil, nil
}

// builtinTypeName reports whether name is a predeclared basic type —
// the names a conversion or var decl may tag a Named value with.
func builtinTypeName(name string) bool {
	switch name {
	case "int", "int64", "bool", "string", "error",
		"float32", "float64", "complex64", "complex128",
		"int8", "int16", "int32", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

// instantiate implements F[T, U] / T[Args] on generic functions and types;
// a non-generic base falls back to index lookup so `a[i]` and `F[T]` share
// one encoding.
func (v *VM) instantiate(f *frame, base runtime.Value, targs []runtime.Value, pos token.Pos) runtime.Value {
	if dv, ok := runtime.Deref(base); ok {
		base = dv
	}
	for i, a := range targs {
		// `type A = B` is transparent everywhere: binds record the
		// canonical target so `T[Alias]` and `T[B]` spell and compare
		// the same instantiation (main.T[main.Int], never the alias).
		if atd, ok := a.(*runtime.TypeDef); ok {
			if u := v.peelAlias(atd); u != atd {
				targs[i] = u
			}
		}
	}
	switch g := base.(type) {
	case *runtime.Function:
		if len(g.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		// a partial list (slices.Grow[S]) leaves the trailing params to
		// constraint/argument inference.
		if len(targs) > len(g.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Name, len(g.TParams), len(targs))
		}
		return v.instantiateFunc(f, g, targs)
	case *runtime.BoundMethod:
		// a generic method explicit instantiation: recv.M[T](...) — the
		// receiver's own type binds are kept, T binds the method's params.
		if len(g.Fn.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		if len(targs) != len(g.Fn.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Fn.Name, len(g.Fn.TParams), len(targs))
		}
		return &runtime.BoundMethod{Recv: g.Recv, Fn: v.instantiateFunc(f, g.Fn, targs)}
	case *runtime.BuiltinFunc:
		if g.GenFn == nil {
			return v.indexFallback(f, base, targs)
		}
		gen := g.GenFn
		inst := *g
		inst.GenFn = nil
		inst.Fn = func(vm runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return gen(vm, targs, args)
		}
		return &inst
	case *runtime.TypeDef:
		if len(g.TParams) == 0 {
			return v.indexFallback(f, base, targs)
		}
		if len(targs) != len(g.TParams) {
			f.trap("cannot instantiate %s: needs %d type arguments, got %d", g.Name, len(g.TParams), len(targs))
		}
		binds := map[string]runtime.Value{}
		bindArgs(binds, g.TParams, targs)
		ctx := &runtime.TypeDef{Pkg: g.Pkg, File: g.File, Binds: binds}
		if err := v.checkTArgs(ctx, g.TParams, g.TConstraints, binds); err != nil {
			f.trap("%s", err)
		}
		return v.specializeType(f, g, targs)
	default:
		return v.indexFallback(f, base, targs)
	}
}

// instantiateFunc binds a generic function's own type parameters to the
// explicit type arguments — merging over binds already carried by the
// value (a generic method keeps its receiver's type binds). The result
// is cached per package like inferCached's: the instance depends only
// on g's declaration and binds and on the type arguments, so `Id[int]`
// evaluated in a loop reuses one Function (and so its compiled chunk)
// instead of re-binding and re-checking constraints on every call.
func (v *VM) instantiateFunc(f *frame, g *runtime.Function, targs []runtime.Value) *runtime.Function {
	if g.Pkg == nil || g.Decl == nil {
		return v.instantiateFuncUncached(f, g, targs)
	}
	key, keep, ok := instFuncKey(g, targs)
	if !ok {
		return v.instantiateFuncUncached(f, g, targs)
	}
	if r, hit := g.Pkg.Inferred(key); hit {
		return r
	}
	// a failed constraint check traps out of here, so only checked
	// instances are recorded
	r := v.instantiateFuncUncached(f, g, targs)
	g.Pkg.SetInferred(key, g.Decl, keep, r)
	return r
}

// instFuncKey spells what instantiateFuncUncached reads: g's identity
// and binds (InstKey — a generic method's receiver binds, a partial
// instantiation's earlier args) and each explicit type argument by type
// identity (AppendTypeKey: distinct named types differ by Spec, local
// types by scope and generation). It shares the inference cache's map,
// so the "inst" prefix keeps the two key spaces apart. keep holds what
// the key's addresses point into.
func instFuncKey(g *runtime.Function, targs []runtime.Value) (string, []any, bool) {
	fk, ok := runtime.InstKey(g.Decl, g.File, g.Name, g.Binds)
	if !ok {
		return "", nil, false
	}
	var b strings.Builder
	b.WriteString("inst|")
	b.WriteString(fk)
	keep := make([]any, 0, len(targs)+1)
	keep = append(keep, g)
	for _, a := range targs {
		td, ok := a.(*runtime.TypeDef)
		if !ok || td == nil {
			return "", nil, false
		}
		b.WriteString("|t")
		runtime.AppendTypeKey(&b, td)
		keep = append(keep, td)
	}
	for _, n := range g.OuterTParams {
		b.WriteString("|o")
		b.WriteString(n)
	}
	return b.String(), keep, true
}

func (v *VM) instantiateFuncUncached(f *frame, g *runtime.Function, targs []runtime.Value) *runtime.Function {
	binds := map[string]runtime.Value{}
	for k, bv := range g.Binds {
		binds[k] = bv
	}
	bindArgs(binds, g.TParams, targs)
	ctx := &runtime.TypeDef{Pkg: g.Pkg, File: g.File, Binds: binds}
	if len(targs) < len(g.TParams) {
		tset := map[string]bool{}
		for _, t := range g.TParams[len(targs):] {
			if _, ok := binds[t]; !ok {
				tset[t] = true
			}
		}
		v.inferCoreTypes(ctx, tset, binds, g.TParams, g.TConstraints)
	}
	complete := true
	for _, t := range g.TParams {
		if _, ok := binds[t]; !ok {
			complete = false
		}
	}
	// still-unbound params are inferred (and checked) at the call
	if complete {
		if err := v.checkTArgs(ctx, g.TParams, g.TConstraints, binds); err != nil {
			f.trap("%s", err)
		}
	}
	return &runtime.Function{
		Pkg: g.Pkg, File: g.File, Decl: g.Decl, Name: g.Name,
		Recv: g.Recv, PtrRecv: g.PtrRecv,
		TParams: g.TParams, TConstraints: g.TConstraints,
		Binds: binds, Compile: g.Compile, OuterTParams: g.OuterTParams,
	}
}

// indexFallback applies plain index semantics when the [..] was not a
// generic instantiation after all (single index only).
func (v *VM) indexFallback(f *frame, base runtime.Value, targs []runtime.Value) runtime.Value {
	if len(targs) != 1 {
		f.trap("cannot index %T with %d indices", base, len(targs))
	}
	return v.index(f, base, targs[0])
}

// outerTypeArgs collects the type arguments of the generic instantiation
// enclosing f's frame, in declaration order — covering literals that
// close over a generic function via their OuterTParams. Local type decls
// and local-generic instantiations fold these into the type's identity.
func (v *VM) outerTypeArgs(f *frame) (names []string, args []runtime.Value) {
	if f == nil || f.fn == nil {
		return nil, nil
	}
	for _, tp := range f.fn.OuterParamNames() {
		bv, ok := f.fn.Binds[tp]
		if !ok {
			continue
		}
		names = append(names, tp)
		args = append(args, bv)
	}
	return names, args
}

// bindArgs folds a parallel name/argument list into binds in
// declaration order; names past len(args) stay unbound (a compile-time
// placeholder survives). Callers trap on arity before binding, so the
// guard only covers unchecked internal paths.
func bindArgs(binds map[string]runtime.Value, names []string, args []runtime.Value) {
	for i, n := range names {
		if i < len(args) {
			binds[n] = args[i]
		}
	}
}

// specializeType clones a generic typedef with its methods re-bound to the
// concrete type arguments. A function-local generic instantiating inside
// a generic function captures the enclosing type arguments too — they are
// part of the closure type's identity and render in reflect.Type.String
// as `pkg.T[outerArgs;ownArgs]`.
func (v *VM) specializeType(f *frame, g *runtime.TypeDef, targs []runtime.Value) *runtime.TypeDef {
	binds := map[string]runtime.Value{}
	var outer []runtime.Value
	if g.Local {
		names, args := v.outerTypeArgs(f)
		bindArgs(binds, names, args)
		outer = args
	}
	bindArgs(binds, g.TParams, targs)
	td := &runtime.TypeDef{
		Pkg: g.Pkg, Name: g.Name, File: g.File, Spec: g.Spec, Kind: g.Kind,
		Fields: g.Fields, FTags: g.FTags, Anon: g.Anon, TParams: g.TParams,
		TConstraints: g.TConstraints, Binds: binds,
		MReqs: g.MReqs, IEmbeds: g.IEmbeds,
		EmbedSpecs: g.EmbedSpecs, EmbedIdx: g.EmbedIdx, Embeds: g.Embeds,
		LocalTypes: g.LocalTypes, Elem: g.Elem, HostNew: g.HostNew,
		Local: g.Local, Gen: g.Gen, OuterArgs: outer,
	}
	if len(g.Methods) > 0 {
		td.Methods = make(map[string]*runtime.Function, len(g.Methods))
		for name, m := range g.Methods {
			binds := map[string]runtime.Value{}
			for k, bv := range m.Binds {
				binds[k] = bv
			}
			bindArgs(binds, g.TParams, targs)
			// the receiver may rename the type's parameters — `func (l
			// List[E])` on `type List[T]` scopes E in the method body, so
			// the receiver's own names bind to the same arguments.
			for i, rp := range recvTypeParamNames(m.Decl) {
				if rp == "_" {
					continue // `_` names no parameter to bind
				}
				if i < len(targs) {
					binds[rp] = targs[i]
				}
			}
			td.Methods[name] = &runtime.Function{
				Pkg: m.Pkg, File: m.File, Decl: m.Decl, Name: m.Name,
				Recv: m.Recv, PtrRecv: m.PtrRecv,
				TParams: m.TParams, TConstraints: m.TConstraints,
				Binds: binds, Compile: m.Compile,
			}
		}
	}
	// M4-3d: the instantiated descriptor is canonical — two mints of the
	// same instantiation are the same pointer.
	return v.internTypedef(td)
}

// recvTypeParamNames extracts the type parameter names a method receiver
// declares — `func (l List[E, F])` yields [E, F]. The ordered walk lives
// in syntax.RecvTypeParamNames, which also peels pointer/paren wrappers
// so `(p *Pair[A])` binds A like the inspect package already did.
// Non-generic receivers yield nil.
func recvTypeParamNames(d *ast.FuncDecl) []string {
	if d == nil || d.Recv == nil {
		return nil
	}
	return syntax.RecvTypeParamNames(d.Recv)
}

// instantiateType resolves the T of T[Args] and each argument typedef,
// then instantiates through the VM's generic binder.
func (s *specialCtx) instantiateType(e ast.Expr, x ast.Expr, argExprs []ast.Expr) (*runtime.TypeDef, error) {
	base, err := s.Resolve(x)
	if err != nil {
		return nil, err
	}
	targs := make([]runtime.Value, len(argExprs))
	for i, a := range argExprs {
		td, err := s.ResolveType(a)
		if err != nil {
			return nil, err
		}
		targs[i] = td
	}
	iv := s.v.instantiate(s.f, base, targs, e.Pos())
	td, ok := iv.(*runtime.TypeDef)
	if !ok {
		return nil, s.Errorf(e, "%s is not a type", s.Format(e))
	}
	return td, nil
}

// hasUnbound reports whether any of the given type params lacks a bind.
func hasUnbound(tparams []string, binds map[string]runtime.Value) bool {
	for _, tp := range tparams {
		if _, ok := binds[tp]; !ok {
			return true
		}
	}
	return false
}

// inferCached is inferBinds behind a per-package cache. Inference reads
// only the callee and, per argument, either the untyped constant it
// carries or the typedef it unifies against (the static type, else the
// value's), so a call with the same callee and argument types reuses
// the instance — every inferred call minted a fresh Function and binds
// map before.
func (v *VM) inferCached(fn *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (*runtime.Function, error) {
	if fn.Pkg == nil || fn.Decl == nil {
		return v.inferBinds(fn, args, statics, spreadTd)
	}
	key, keep, ok := v.inferKey(fn, args, statics, spreadTd)
	if !ok {
		return v.inferBinds(fn, args, statics, spreadTd)
	}
	if r, hit := fn.Pkg.Inferred(key); hit {
		return r, nil
	}
	r, err := v.inferBinds(fn, args, statics, spreadTd)
	if err == nil {
		fn.Pkg.SetInferred(key, fn.Decl, keep, r)
	}
	return r, err
}

// inferKey spells what inferBinds reads. keep lists the typedefs the
// key's addresses point into; the cache holds them so an address in a
// live key is never reused.
func (v *VM) inferKey(fn *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (string, []any, bool) {
	fk, ok := runtime.InstKey(fn.Decl, fn.File, fn.Name, fn.Binds)
	if !ok {
		return "", nil, false
	}
	var b strings.Builder
	b.WriteString(fk)
	keep := make([]any, 0, len(args)+2)
	keep = append(keep, fn)
	for i, a := range args {
		b.WriteString("|a")
		if v.untypedConstArg(a) {
			// a constant joins the bind by kind and representability,
			// so its exact value is part of the key
			u, _ := constPayload(a)
			fmt.Fprintf(&b, "c%d:%t:%s", u.V.Kind(), u.Rune, u.V.ExactString())
			if n, isN := a.(*runtime.Named); isN && n.Typ != nil {
				b.WriteByte(':')
				runtime.AppendTypeKey(&b, n.Typ)
				keep = append(keep, n.Typ)
			}
			continue
		}
		conc := staticAt(statics, i)
		if conc == nil {
			conc = v.argTypedef(a)
		}
		if conc == nil {
			b.WriteByte('_')
			continue
		}
		runtime.AppendTypeKey(&b, conc)
		keep = append(keep, conc)
	}
	if spreadTd != nil {
		b.WriteString("|spread:")
		runtime.AppendTypeKey(&b, spreadTd)
		keep = append(keep, spreadTd)
	}
	return b.String(), keep, true
}

// inferBinds binds a generic function's unbound type parameters from the
// runtime argument types — `Id(40)` infers T=int the way v1's heuristic
// did, and (Go 1.27) deeper param shapes infer structurally: `f func(E) R`
// against a func(int) string argument binds R=string. A method's receiver
// occupies args[0]; its own type binds (from the receiver type's
// instantiation) are kept, only the method's type params infer. Params
// that stay unbound resolve to a run-time trap on use, matching the
// compiler-is-total contract.
func (v *VM) inferBinds(fn *runtime.Function, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef) (*runtime.Function, error) {
	if fn.Decl == nil || fn.Decl.Type == nil || fn.Decl.Type.Params == nil {
		return fn, nil
	}
	tset := map[string]bool{}
	for _, t := range fn.TParams {
		if _, ok := fn.Binds[t]; !ok {
			tset[t] = true
		}
	}
	if len(tset) == 0 {
		return fn, nil
	}
	binds := map[string]runtime.Value{}
	for k, bv := range fn.Binds {
		binds[k] = bv
	}
	// ctx resolves named type expressions in the callee's own scope — its
	// package, file imports and binds so far.
	ctx := &runtime.TypeDef{Pkg: fn.Pkg, File: fn.File, Binds: binds}
	// Untyped-constant args never pin a type parameter: a constant
	// converts to whatever the bind ends up as, so it only joins the
	// bind through the common default afterward (`bar(0, f[T])` binds
	// T via f[T], and `g('a', 2.3)` joins at float64 rather than
	// letting 'a' pin P=rune).
	constArgs := map[string][]runtime.Value{}
	pos := 0
	if fn.Decl.Recv != nil {
		pos = 1 // args[0] is the receiver; declared params exclude it
	}
	for _, field := range fn.Decl.Type.Params.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		et := field.Type
		variadic := false
		if el, ok := et.(*ast.Ellipsis); ok {
			et = el.Elt
			variadic = true
		}
		for i := 0; i < n; i++ {
			if pos >= len(args) {
				break
			}
			v.unifyType(ctx, tset, binds, constArgs, et, args[pos], staticAt(statics, pos))
			pos++
		}
		// ...T consumes all remaining args; the first arg that yields a
		// typedef wins the binding.
		for variadic && pos < len(args) {
			v.unifyType(ctx, tset, binds, constArgs, et, args[pos], staticAt(statics, pos))
			pos++
		}
		if variadic && spreadTd != nil {
			// `Sum(n...)` spreads n's elements away, but the slice's
			// declared element type still teaches the bind — a nil or
			// empty []int expands to zero args yet infers T=int.
			v.unifyTypeDef(ctx, tset, binds, et, spreadTd)
		}
	}
	// Core-type inference runs before the constants' defaults, like
	// Go's: in `has([]uint8{0}, 0)` with `S ~[]E`, S teaches E=uint8,
	// and the constant 0 then converts to it instead of pinning E=int.
	v.inferCoreTypes(ctx, tset, binds, fn.TParams, fn.TConstraints)
	// Deferred untyped-constant contributors now bind their tparam to
	// the widest kind's default type ('a' + 2.3 → float64, 3 + 'a' →
	// rune). A tparam already bound by a real-typed argument keeps that
	// bind; its constants convert at the call.
	for name, cs := range constArgs {
		if _, ok := binds[name]; ok {
			continue
		}
		td, ok := v.commonConstTypedef(cs)
		if !ok {
			return nil, fmt.Errorf("cannot infer %s: untyped constant arguments do not join", name)
		}
		if td != nil {
			binds[name] = td
		}
	}
	v.inferCoreTypes(ctx, tset, binds, fn.TParams, fn.TConstraints)
	if len(binds) == len(fn.Binds) {
		return fn, nil // nothing inferred
	}
	if err := v.checkTArgs(ctx, fn.TParams, fn.TConstraints, binds); err != nil {
		return nil, err
	}
	return &runtime.Function{
		Pkg: fn.Pkg, File: fn.File, Decl: fn.Decl, Name: fn.Name,
		Recv: fn.Recv, PtrRecv: fn.PtrRecv,
		TParams: fn.TParams, TConstraints: fn.TConstraints,
		Binds: binds, Compile: fn.Compile,
	}, nil
}

// inferCoreTypes is Go's constraint type inference: a bound parameter
// whose constraint has a core type (`Map ~map[K]V`, `S ~[]E`) teaches the
// parameters that core type mentions — maps.Values(m) learns K and V from
// Map. It repeats while a round binds something new.
func (v *VM) inferCoreTypes(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, tparams []string, cons []ast.Expr) {
	for changed := true; changed; {
		changed = false
		for i, tp := range tparams {
			if i >= len(cons) {
				break
			}
			core := coreTypeExpr(cons[i])
			if core == nil {
				continue
			}
			td := typedefOf(binds[tp])
			if td == nil {
				continue
			}
			u := v.peelNamed(td)
			if u == nil {
				continue
			}
			if u.Anon == nil && u.Spec != nil {
				// a declared container (`type M map[string]int`): unify
				// against its underlying type expression.
				u = &runtime.TypeDef{Kind: u.Kind, Anon: u.Spec.Type, Pkg: u.Pkg, File: u.File, Binds: u.Binds}
			}
			before := len(binds)
			v.unifyTypeDef(ctx, tset, binds, core, u)
			changed = changed || len(binds) > before
		}
	}
}

// coreTypeExpr extracts the single composite type element of a
// constraint — `~[]E`, `[]E`, `interface{ ~map[K]V }` — or nil.
func coreTypeExpr(c ast.Expr) ast.Expr {
	switch t := c.(type) {
	case *ast.ParenExpr:
		return coreTypeExpr(t.X)
	case *ast.UnaryExpr:
		if t.Op == token.TILDE && isCompositeTypeExpr(t.X) {
			return t.X
		}
	case *ast.InterfaceType:
		if t.Methods != nil && len(t.Methods.List) == 1 && len(t.Methods.List[0].Names) == 0 {
			return coreTypeExpr(t.Methods.List[0].Type)
		}
	default:
		if isCompositeTypeExpr(c) {
			return c
		}
	}
	return nil
}

// unifyType learns type-argument binds by walking a parameter's declared
// type expression (the pattern, which may mention tparams) alongside the
// argument's runtime type. tset holds the tparam names still unbound;
// inferred binds land in binds. Everything is best-effort: mismatched or
// unsupported shapes simply teach nothing, and already-bound tparams are
// not re-bound (no consistency check — approximation).
func (v *VM) unifyType(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, constArgs map[string][]runtime.Value, pat ast.Expr, arg runtime.Value, static *runtime.TypeDef) {
	// An untyped-constant argument defers its bind: the constant
	// converts to whatever the type parameter becomes, so it must not
	// win the first-come binding over real-typed arguments.
	if id, ok := pat.(*ast.Ident); ok && tset[id.Name] && v.untypedConstArg(arg) {
		constArgs[id.Name] = append(constArgs[id.Name], arg)
		return
	}
	// Go infers T from the argument's static type, so a declared typedef
	// from the call site wins over the value's dynamic one — `id(e)`
	// with `var e error` binds T=error even when e holds *errorString.
	conc := static
	if conc == nil {
		conc = v.argTypedef(arg)
	}
	if conc == nil {
		return
	}
	v.unifyTypeDef(ctx, tset, binds, pat, conc)
}

func staticAt(statics []*runtime.TypeDef, i int) *runtime.TypeDef {
	if i >= 0 && i < len(statics) {
		return statics[i]
	}
	return nil
}

func (v *VM) unifyTypeDef(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat ast.Expr, conc *runtime.TypeDef) {
	switch p := pat.(type) {
	case *ast.Ident:
		if tset[p.Name] {
			if _, ok := binds[p.Name]; !ok {
				binds[p.Name] = conc
			}
		}
		// a bound or foreign ident teaches nothing
	case *ast.ParenExpr:
		v.unifyTypeDef(ctx, tset, binds, p.X, conc)
	case *ast.Ellipsis:
		v.unifyTypeDef(ctx, tset, binds, p.Elt, conc)
	case *ast.StarExpr:
		// *T unifies only against pointer-shaped args — a slice or
		// map's element type is not a pointee and must teach nothing.
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindPointer {
			et := v.elemTypedef(v.topFrame(), conc)
			if et == nil {
				et = conc.Elem
			}
			if et != nil {
				v.unifyTypeDef(ctx, tset, binds, p.X, et)
			}
		}
	case *ast.ArrayType:
		// []T unifies only against slice/array-shaped args — a pointer's
		// pointee is not an element and must teach nothing.
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindSlice {
			et := v.elemTypedef(v.topFrame(), conc)
			if et == nil {
				// element named by a type the typedef cannot resolve —
				// a function-local `type` decl, say — falls back to the
				// element evidence argTypedef lifted off the value.
				et = conc.Elem
			}
			if et != nil {
				et = v.sourceTwinOf(et)
				v.unifyTypeDef(ctx, tset, binds, p.Elt, et)
			}
		}
	case *ast.MapType:
		if mt, ok := conc.Anon.(*ast.MapType); ok && v.H.ResolveType != nil {
			if kt, err := v.resolveOperandType(conc, mt.Key); err == nil {
				v.unifyTypeDef(ctx, tset, binds, p.Key, kt)
			}
			if vt, err := v.resolveOperandType(conc, mt.Value); err == nil {
				v.unifyTypeDef(ctx, tset, binds, p.Value, vt)
			}
		}
	case *ast.ChanType:
		if u := v.peelNamed(conc); u != nil && u.Kind == runtime.KindChan {
			if et := v.elemTypedef(v.topFrame(), conc); et != nil {
				v.unifyTypeDef(ctx, tset, binds, p.Value, et)
			}
		}
	case *ast.FuncType:
		cs := funcTypeExpr(conc)
		if cs == nil || v.H.ResolveType == nil {
			break
		}
		v.unifyFieldTypes(ctx, tset, binds, p.Params, cs.Params, conc)
		v.unifyFieldTypes(ctx, tset, binds, p.Results, cs.Results, conc)
	case *ast.IndexExpr:
		v.unifyIndices(ctx, tset, binds, p.X, []ast.Expr{p.Index}, conc)
	case *ast.IndexListExpr:
		v.unifyIndices(ctx, tset, binds, p.X, p.Indices, conc)
	}
}

// resolveOperandType resolves a type expression inside conc's own type
// (a map key or value) to the typedef it denotes. ResolveType peels *T to
// T — right for method sets, wrong here: map[string]E over
// map[string]*Item must bind E=*Item — so a pointer expr becomes an
// anonymous pointer typedef in conc's context.
func (v *VM) resolveOperandType(conc *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if px, ok := x.(*ast.ParenExpr); ok {
		return v.resolveOperandType(conc, px.X)
	}
	if _, ok := x.(*ast.StarExpr); ok {
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: x, Pkg: conc.Pkg, File: conc.File, Binds: conc.Binds}, nil
	}
	return v.H.ResolveType(conc, x)
}

// unifyFieldTypes zips two flattened field lists (params or results):
// each declared type expr on the pattern side unifies against the
// resolved typedef on the concrete side. A pattern `...T` pairs with
// either `...U` or `[]U`.
func (v *VM) unifyFieldTypes(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, pat, conc *ast.FieldList, concCtx *runtime.TypeDef) {
	if pat == nil || conc == nil {
		return
	}
	pe := syntax.FieldTypes(pat)
	ce := syntax.FieldTypes(conc)
	for i, p := range pe {
		if i >= len(ce) {
			break
		}
		pv := p
		cvv := ce[i]
		peEl, _ := pv.(*ast.Ellipsis)
		ceEl, _ := cvv.(*ast.Ellipsis)
		if peEl != nil && ceEl == nil {
			// variadic pattern against a non-variadic concrete func:
			// approximate by unifying the element against the param as-is.
			pv = peEl.Elt
		}
		ct, err := v.resolveOperandType(concCtx, cvv)
		if err != nil || ct == nil {
			continue
		}
		if ceEl != nil {
			// the concrete side is `...U` — the pattern sees []U
			ct = &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: ceEl.Pos(), Elt: ceEl.Elt}, Pkg: concCtx.Pkg, File: concCtx.File, Binds: concCtx.Binds}
		}
		v.unifyTypeDef(ctx, tset, binds, pv, ct)
	}
}

// unifyIndices handles a `Name[E]`-shaped pattern against an instantiated
// concrete typedef: each tparam index binds to the concrete type's own
// binding for the base's parameter at that position.
func (v *VM) unifyIndices(ctx *runtime.TypeDef, tset map[string]bool, binds map[string]runtime.Value, base ast.Expr, idxs []ast.Expr, conc *runtime.TypeDef) {
	if v.H.ResolveType == nil {
		return
	}
	bt, err := v.H.ResolveType(ctx, base)
	if err != nil || bt == nil || len(bt.TParams) == 0 {
		return
	}
	for i, x := range idxs {
		if i >= len(bt.TParams) {
			break
		}
		id, ok := x.(*ast.Ident)
		if !ok || !tset[id.Name] {
			continue
		}
		if bv, ok := conc.Binds[bt.TParams[i]]; ok {
			if _, bound := binds[id.Name]; !bound {
				binds[id.Name] = bv
			}
		}
	}
}

// funcTypeExpr returns the *ast.FuncType carried by a func-typed typedef,
// from its anonymous or declared form.
func funcTypeExpr(td *runtime.TypeDef) *ast.FuncType {
	for _, x := range []ast.Expr{td.Anon, specTypeOf(td)} {
		if ft, ok := x.(*ast.FuncType); ok {
			return ft
		}
	}
	return nil
}

func specTypeOf(td *runtime.TypeDef) ast.Expr {
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// argTypedef is typeOfValue enriched for inference: stamped container
// typedefs pass through, pointers remember the pointee typedef, and
// function values carry their signature AST so `func(E) R`-shaped
// patterns can unify position-by-position.
func (v *VM) argTypedef(x runtime.Value) *runtime.TypeDef {
	switch xv := x.(type) {
	case *runtime.Slice:
		// the first element's own typedef is kept as Elem evidence —
		// elements of a function-local named type resolve nowhere else.
		var ev *runtime.TypeDef
		if len(xv.Elems) > 0 {
			ev = v.argTypedef(xv.Elems[0])
		}
		if xv.Typ != nil {
			// a resolvable static element type wins over the first
			// element's dynamic one — `[]Node{&A{}}` binds N=Node,
			// not *A (Go infers from the static type).
			if st := v.elemTypedef(nil, xv.Typ); st != nil {
				ev = st
			}
			return &runtime.TypeDef{
				Kind: xv.Typ.Kind, Name: xv.Typ.Name, Pkg: xv.Typ.Pkg,
				File: xv.Typ.File, Anon: xv.Typ.Anon, Spec: xv.Typ.Spec,
				Binds: xv.Typ.Binds, Elem: ev,
			}
		}
		return &runtime.TypeDef{Kind: runtime.KindSlice, Elem: ev}
	case *runtime.Map:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindMap}
	case *runtime.Chan:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindChan}
	case *runtime.Cell:
		// a pointer: Elem records the pointee typedef — Anon cannot name
		// it without the variable's declaration. Prefer the cell's
		// declared type over the stored value's: `var l0 L0` inside a
		// generic instantiation holds the outer-args clone, so `&l0`
		// spells `*main.L0[int]` like gc.
		pt := xv.Typ
		if pt == nil {
			pt = v.argTypedef(xv.Elem)
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: pt}
	case *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(x); ok {
			return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.argTypedef(dv)}
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer}
	case *runtime.GoValue:
		// a host error yields no typedef of its own, but every
		// statically-typed way it reaches a generic call declares the
		// param `error` — `errors.New`'s result type, `var e error`,
		// an `any` element's only usable bind. gc infers T=error at
		// those sites; binding error here keeps `kind(errors.New("e"))`
		// and range-var calls from leaving T unbound.
		if _, isErr := xv.V.(error); isErr {
			return v.builtinTypedef("error")
		}
		// a host named basic binds its source twin's typedef when the
		// script imports that package ([Hooks.HostTwin]): Go sees one
		// type, so eq(fi.Mode().Type(), fs.ModeDir) binds T=fs.FileMode
		// and the source constant still assigns to the second param.
		if _, rt, ok := runtime.HostNamedScalar(xv); ok && v.H.HostTwin != nil {
			if twin := v.H.HostTwin(rt); twin != nil {
				return twin
			}
		}
		if td := v.typeOfValue(x); td != nil {
			return td
		}
		// other host values — *strings.Reader, *csv.Reader — carry no
		// typedef either, so synthesize one from the reflect type: gc
		// binds T=*strings.Reader at this call site, and the typedef
		// only has to pin T (spell through DisplayName, satisfy the
		// constraint check), not type-check members.
		return v.hostTypedefOf(reflect.TypeOf(xv.V))
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return v.funcTypedefOf(x)
	}
	td := v.typeOfValue(x)
	if td == nil && v.H.HostTwin != nil {
		// a raw host scalar (a time.Duration a host call returned)
		// binds its bound typedef, like the boxed case above.
		if _, rt, ok := runtime.HostNamedPayload(x); ok {
			return v.H.HostTwin(rt)
		}
	}
	return td
}

// internTypedef routes a synthesized typedef through the session's
// intern table (M4-3c): every path minting the same type returns the
// same pointer, so pointer equality is a correct same-type check. A
// nil hook passes the descriptor through uninterned.
func (v *VM) internTypedef(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || v.H.InternTypedef == nil {
		return td
	}
	return v.H.InternTypedef(td)
}

// hostTypedefOf synthesizes a typedef from a host value's reflect type
// for generic inference. Named types spell `pkg.Name` (predeclared
// names resolve to the builtin typedef) and carry a HostNew like the
// bound host typedefs — the method-set walk reads the boxed type's
// reflect set, so `kind[T io.Reader]` sees *strings.Reader's methods
// and the constraint check passes. Composites wrap the element so a
// `*T`/`[]T` host value binds the same-shaped T. The result is the
// session's canonical descriptor (M4-3c).
func (v *VM) hostTypedefOf(rt reflect.Type) *runtime.TypeDef {
	return v.internTypedef(v.hostTypedefOfNew(rt))
}

func (v *VM) hostTypedefOfNew(rt reflect.Type) *runtime.TypeDef {
	if rt == nil {
		return nil
	}
	if rt.Name() != "" {
		// only a predeclared name (PkgPath=="") is the builtin typedef —
		// a host `type error struct{...}` in a real package is not `error`.
		if rt.PkgPath() == "" {
			if td := v.builtinTypedef(rt.Name()); td != nil {
				return td
			}
		}
		td := &runtime.TypeDef{Name: rt.String(), Kind: hostNamedKind(rt)}
		switch rt.Kind() {
		case reflect.Func:
			// carry the declared signature, like the unnamed walk —
			// generic inference reads the FuncType to bind tparams.
			if ft := v.reflectFuncTypedef(rt); ft != nil {
				td.Anon = ft.Anon
			}
		case reflect.Slice, reflect.Array, reflect.Chan, reflect.Map, reflect.Pointer:
			td.Elem = v.hostTypedefOf(rt.Elem())
		}
		// a HostNew box like the bound typedefs' — reflect.New hands
		// the pointer-shaped host value the method-set walk reads.
		td.HostNew = func() any { return reflect.New(rt).Interface() }
		return td
	}
	switch rt.Kind() {
	case reflect.Pointer:
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.hostTypedefOf(rt.Elem())}
	case reflect.Slice, reflect.Array:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Elem: v.hostTypedefOf(rt.Elem())}
	case reflect.Map:
		return &runtime.TypeDef{Kind: runtime.KindMap, Elem: v.hostTypedefOf(rt.Elem())}
	case reflect.Chan:
		return &runtime.TypeDef{Kind: runtime.KindChan, Elem: v.hostTypedefOf(rt.Elem())}
	case reflect.Func:
		// the same signature walk host builtins use — an unnamed
		// func argument still teaches inference its declared shape.
		if td := v.reflectFuncTypedef(rt); td != nil {
			return td
		}
	}
	return &runtime.TypeDef{Name: rt.String()}
}

// hostPkgElemTypedef is the element identity a host slice keeps when
// its element is a type declared in a package, or a pointer to one: the
// bare name anonExprOf spells (`[]Stmt`, `[]*Comment`) resolves nowhere,
// so generic inference reads this typedef instead (see
// [VM.sourceTwinOf]). Other elements return nil.
func hostPkgElemTypedef(rt reflect.Type) *runtime.TypeDef {
	if rt.Kind() == reflect.Pointer && rt.Name() == "" {
		if et := hostPkgElemTypedef(rt.Elem()); et != nil {
			return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
		}
		return nil
	}
	if rt.Name() == "" || rt.PkgPath() == "" {
		return nil
	}
	// identity only: a HostNew would make Zero build a host box where
	// an interface element's zero is nil.
	return &runtime.TypeDef{Name: rt.String(), Kind: hostNamedKind(rt), HostIdent: rt}
}

// hostNilPtrTypes memoizes [HostNilPtrTypedef] per reflect type: one
// typedef per host pointer type keeps a nil *T read cheap and lets the
// interface-satisfaction memo key on it.
var hostNilPtrTypes sync.Map

// HostNilPtrTypedef is the typedef a nil host *T carries as a
// [runtime.TypedNil] — [hostPkgElemTypedef]'s anonymous pointer to a
// named host type — or nil when rt is not a pointer to a named host
// type (such nils stay untyped). [HostNilPtrType] reads it back.
func HostNilPtrTypedef(rt reflect.Type) *runtime.TypeDef {
	if td, ok := hostNilPtrTypes.Load(rt); ok {
		return td.(*runtime.TypeDef)
	}
	if rt.Kind() != reflect.Pointer || rt.Name() != "" || rt.Elem().Name() == "" || rt.Elem().PkgPath() == "" {
		return nil
	}
	td := hostPkgElemTypedef(rt)
	if td == nil {
		return nil
	}
	got, _ := hostNilPtrTypes.LoadOrStore(rt, td)
	return got.(*runtime.TypeDef)
}

// hostNils memoizes [HostNil] per reflect type — including a nil
// *runtime.TypedNil for a type whose nils stay untyped.
var hostNils sync.Map

// HostNil is the typed nil a nil host value of type rt reads as: a nil
// *T of a named host type ([HostNilPtrTypedef]), a nil named host slice
// or map (net.IP, url.Values), or a nil chan, func or
// pointer of an unnamed type spelled from predeclared types only
// (`<-chan struct{}` from context.Background().Done(), exec.Cmd's
// `func() error` Cancel, `*int`). It compares equal to nil but keeps
// its dynamic type once stored in an interface. The value is shared per
// type — a nil read allocates nothing — so callers must not mutate it.
// Other types report nil: their nils read as untyped nil.
func HostNil(rt reflect.Type) *runtime.TypedNil {
	if tn, ok := hostNils.Load(rt); ok {
		return tn.(*runtime.TypedNil)
	}
	var tn *runtime.TypedNil
	if td := HostNilPtrTypedef(rt); td != nil {
		tn = &runtime.TypedNil{Typ: td}
	} else if (rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map) && rt.Name() != "" && rt.PkgPath() != "" {
		tn = &runtime.TypedNil{Typ: hostPkgElemTypedef(rt)}
	} else if rt.Name() == "" && (rt.Kind() == reflect.Chan || rt.Kind() == reflect.Func || rt.Kind() == reflect.Pointer) &&
		universeSpelled(rt) {
		tn = &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: hostNamedKind(rt), Anon: anonExprOf(rt)}}
	}
	got, _ := hostNils.LoadOrStore(rt, tn)
	return got.(*runtime.TypedNil)
}

// universeSpelled reports whether rt is spelled from predeclared types
// only, so [anonExprOf]'s spelling resolves in any file: no named type
// of a package, no unexported struct field, no non-empty unnamed
// interface.
func universeSpelled(rt reflect.Type) bool {
	if rt.Name() != "" {
		return rt.PkgPath() == "" && rt.Kind() != reflect.UnsafePointer
	}
	switch rt.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return universeSpelled(rt.Elem())
	case reflect.Map:
		return universeSpelled(rt.Key()) && universeSpelled(rt.Elem())
	case reflect.Func:
		for i := 0; i < rt.NumIn(); i++ {
			if !universeSpelled(rt.In(i)) {
				return false
			}
		}
		for i := 0; i < rt.NumOut(); i++ {
			if !universeSpelled(rt.Out(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < rt.NumField(); i++ {
			if f := rt.Field(i); f.PkgPath != "" || !universeSpelled(f.Type) {
				return false
			}
		}
		return true
	case reflect.Interface:
		return rt.NumMethod() == 0
	}
	return false
}

// HostNilType reports the named host slice or map type a nil of type td
// holds: the typedef [HostNil] mints for one (net.IP, url.Values), or a
// bound host type (hostType) whose zero is a slice or map. nil for any
// other typedef.
func HostNilType(td *runtime.TypeDef) reflect.Type {
	if td == nil || td.Spec != nil {
		return nil
	}
	if (td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) && td.HostIdent != nil {
		return td.HostIdent
	}
	if td.HostNew != nil {
		if rt := td.HostType(); rt != nil && (rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map) && rt.Name() != "" {
			return rt
		}
	}
	return nil
}

// HostNilPtrType reports the host pointer type behind a typedef minted by
// [HostNilPtrTypedef] — an anonymous pointer to a named host type — or
// nil for any other typedef.
func HostNilPtrType(td *runtime.TypeDef) reflect.Type {
	if td == nil || td.Kind != runtime.KindPointer || td.Name != "" || td.Spec != nil || td.Anon != nil ||
		td.Elem == nil || td.Elem.HostIdent == nil {
		return nil
	}
	return reflect.PointerTo(td.Elem.HostIdent)
}

// hostNamedKind classifies a named host type with the source
// classifier's kind table (arrays lump with slices, `type P *T` is
// KindPointer): named composites are not structs.
func hostNamedKind(rt reflect.Type) runtime.TypeKind {
	switch rt.Kind() {
	case reflect.Interface:
		return runtime.KindInterface
	case reflect.Slice, reflect.Array:
		return runtime.KindSlice
	case reflect.Map:
		return runtime.KindMap
	case reflect.Chan:
		return runtime.KindChan
	case reflect.Func:
		return runtime.KindFunc
	case reflect.Pointer:
		return runtime.KindPointer
	case reflect.Struct:
		return runtime.KindStruct
	}
	// basic kinds, unsafe.Pointer
	return runtime.KindNamedBasic
}

// funcTypedefOf is the func-value typedef shared by argTypedef and
// typeOfValue: a declared signature via FuncSigOf, a host builtin's
// adapted signature reflected into a synthesized FuncType (so generic
// inference binds tparams from `mapper(s, strconv.Itoa)`-style
// arguments and `any(f1) == any(f2)` pairs on identical signatures),
// or a bare KindFunc when neither is resolvable.
func (v *VM) funcTypedefOf(x runtime.Value) *runtime.TypeDef {
	switch xv := x.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
		if sig, pkg, file, binds := runtime.FuncSigOf(x); sig != nil {
			return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: sig, Pkg: pkg, File: file, Binds: binds}
		}
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	case *runtime.BuiltinFunc:
		if rt := reflect.TypeOf(xv.Target); rt != nil && rt.Kind() == reflect.Func {
			if td := v.reflectFuncTypedef(rt); td != nil {
				return td
			}
		}
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	}
	return nil
}

// reflectFuncTypedef synthesizes a FuncType typedef from a reflected Go
// signature so host functions without declared ASTs can still teach
// generic inference — `strconv.Itoa`'s `func(int) string` binds T in
// `mapper[F, T]([]F, func(F) T)`. Any component type the translator
// cannot render conservatively aborts the whole signature (unifying on a
// partial shape would bind tparams wrongly).
func (v *VM) reflectFuncTypedef(rt reflect.Type) *runtime.TypeDef {
	params := &ast.FieldList{}
	for i := 0; i < rt.NumIn(); i++ {
		e := reflectTypeExpr(rt.In(i))
		if e == nil {
			return nil
		}
		params.List = append(params.List, &ast.Field{Type: e})
	}
	results := &ast.FieldList{}
	for i := 0; i < rt.NumOut(); i++ {
		e := reflectTypeExpr(rt.Out(i))
		if e == nil {
			return nil
		}
		results.List = append(results.List, &ast.Field{Type: e})
	}
	return &runtime.TypeDef{
		Kind: runtime.KindFunc,
		Anon: &ast.FuncType{Params: params, Results: results},
	}
}

// reflectTypeExpr renders a reflected Go type as the AST a comparable
// source declaration would have had — basic names, pointer, slice,
// array, map and chan shapes, empty interface and error. Named or
// structural types return nil: spelling them from reflect would fabricate
// a name the caller's file never declared.
func reflectTypeExpr(rt reflect.Type) ast.Expr {
	switch rt.Kind() {
	case reflect.Bool:
		return &ast.Ident{Name: "bool"}
	case reflect.Int:
		return &ast.Ident{Name: "int"}
	case reflect.Int8:
		return &ast.Ident{Name: "int8"}
	case reflect.Int16:
		return &ast.Ident{Name: "int16"}
	case reflect.Int32:
		return &ast.Ident{Name: "int32"}
	case reflect.Int64:
		return &ast.Ident{Name: "int64"}
	case reflect.Uint:
		return &ast.Ident{Name: "uint"}
	case reflect.Uint8:
		return &ast.Ident{Name: "uint8"}
	case reflect.Uint16:
		return &ast.Ident{Name: "uint16"}
	case reflect.Uint32:
		return &ast.Ident{Name: "uint32"}
	case reflect.Uint64:
		return &ast.Ident{Name: "uint64"}
	case reflect.Uintptr:
		return &ast.Ident{Name: "uintptr"}
	case reflect.Float32:
		return &ast.Ident{Name: "float32"}
	case reflect.Float64:
		return &ast.Ident{Name: "float64"}
	case reflect.Complex64:
		return &ast.Ident{Name: "complex64"}
	case reflect.Complex128:
		return &ast.Ident{Name: "complex128"}
	case reflect.String:
		return &ast.Ident{Name: "string"}
	case reflect.Pointer:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.StarExpr{X: e}
		}
	case reflect.Slice:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.ArrayType{Elt: e}
		}
	case reflect.Array:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			return &ast.ArrayType{Len: &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(rt.Len())}, Elt: e}
		}
	case reflect.Map:
		k, e := reflectTypeExpr(rt.Key()), reflectTypeExpr(rt.Elem())
		if k != nil && e != nil {
			return &ast.MapType{Key: k, Value: e}
		}
	case reflect.Chan:
		if e := reflectTypeExpr(rt.Elem()); e != nil {
			var dir ast.ChanDir
			switch rt.ChanDir() {
			case reflect.RecvDir:
				dir = ast.RECV
			case reflect.SendDir:
				dir = ast.SEND
			}
			return &ast.ChanType{Dir: dir, Value: e}
		}
	case reflect.Interface:
		if rt.Name() == "error" {
			return &ast.Ident{Name: "error"}
		}
		if rt.NumMethod() == 0 {
			return &ast.Ident{Name: "any"}
		}
	}
	return nil
}

// typeOfValue returns a typedef describing a runtime value, for call-site
// inference. Composite builtin values get anonymous kinds (a []int arg
// binds T to "some slice" — enough for `var z T` and T(x) conversions).
func (v *VM) typeOfValue(x runtime.Value) *runtime.TypeDef {
	if dv, ok := runtime.Deref(x); ok {
		// a pointer argument: T binds to a pointer-ish typedef — the
		// element has no AST on this path, so Anon stays nil and
		// elem-typed operations on it degrade to traps.
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.typeOfValue(dv)}
	}
	switch xv := x.(type) {
	case int64:
		return v.builtinTypedef("int")
	case float64:
		return v.builtinTypedef("float64")
	case string:
		return v.builtinTypedef("string")
	case bool:
		return v.builtinTypedef("bool")
	case *runtime.Slice:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindSlice}
	case *runtime.Map:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindMap}
	case *runtime.Chan:
		if xv.Typ != nil {
			return xv.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindChan}
	case *runtime.Struct:
		return xv.Def
	case *runtime.Named:
		return xv.Typ // a named arg binds T to its declared type
	case *runtime.GoValue:
		// a boxed basic (complex128 is the common one — it has no
		// scalar runtime.Value) reports its builtin type so
		// TypeOf(ElemZero([]complex128)) still resolves the element.
		switch xv.V.(type) {
		case complex64:
			return v.builtinTypedef("complex64")
		case complex128:
			return v.builtinTypedef("complex128")
		}
		return nil
	case *runtime.TypedNil:
		return xv.Typ
	case *runtime.IfaceNil:
		return xv.Typ
	case *runtime.UConst:
		// an untyped-constant argument binds T to its default type —
		// `id(9)` infers T=int like a var bind would materialize it.
		switch xv.V.Kind() {
		case constant.Int:
			if xv.Rune {
				return v.builtinTypedef("rune")
			}
			return v.builtinTypedef("int")
		case constant.Float:
			return v.builtinTypedef("float64")
		case constant.String:
			return v.builtinTypedef("string")
		case constant.Bool:
			return v.builtinTypedef("bool")
		case constant.Complex:
			return v.builtinTypedef("complex128")
		}
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return v.funcTypedefOf(x)
	}
	// an engine-internal handle carries no case above — synthesize its
	// tag so call-site inference binds T instead of leaving it unbound.
	if td := v.internalTypedef(x); td != nil {
		return td
	}
	return nil
}

func (v *VM) builtinTypedef(name string) *runtime.TypeDef {
	if v.H.Builtin == nil {
		return nil
	}
	if bv, ok := v.H.Builtin(name); ok {
		if td, ok := bv.(*runtime.TypeDef); ok {
			return td
		}
	}
	return nil
}

// checkTArgs verifies an explicit F[...] / T[...] instantiation against
// the declared constraints. Failures trap with a Go-like message. ctx is
// the generic definition's own typedef context (its package, file imports
// and binds), used to resolve named constraints — including
// self-referential ones like `A Adder[A]` (Go 1.26), which check the
// constraint's required methods against the argument's method set.
func (v *VM) checkTArgs(ctx *runtime.TypeDef, tparams []string, cons []ast.Expr, binds map[string]runtime.Value) error {
	if ctx != nil && len(binds) > 0 {
		// constraint elements may mention the other type parameters
		// (`S ~[]E`): spell them with this instantiation's binds.
		c := *ctx
		c.TParams = tparams
		c.Binds = make(map[string]runtime.Value, len(ctx.Binds)+len(binds))
		for k, b := range ctx.Binds {
			c.Binds[k] = b
		}
		for k, b := range binds {
			c.Binds[k] = b
		}
		ctx = &c
	}
	for i, tp := range tparams {
		if i >= len(cons) {
			break
		}
		td := typedefOf(binds[tp])
		if td == nil || v.satisfiesConstraint(ctx, cons[i], td) {
			continue
		}
		return fmt.Errorf("type argument %s does not satisfy constraint %s", tdName(td), typeExprName(cons[i]))
	}
	return nil
}

// satisfiesConstraint approximates Go's constraint check. `any` and
// `comparable` pass; an interface literal checks its type elements
// (~T by underlying-type name, unions by any-match) plus its method
// requirements against the argument's method set; a named or
// instantiated-named constraint resolves through the type index and
// checks the same way — this covers self-referential constraints like
// `A Adder[A]` without ever instantiating the constraint type. Anything
// unresolvable is approximated satisfied — the compiler stays total and
// wrong instantiations may still fail later.
func (v *VM) satisfiesConstraint(ctx *runtime.TypeDef, cons ast.Expr, td *runtime.TypeDef) bool {
	switch t := cons.(type) {
	case nil:
		return true
	case *ast.ParenExpr:
		return v.satisfiesConstraint(ctx, t.X, td)
	case *ast.InterfaceType:
		var reqs []string
		for _, m := range t.Methods.List {
			if len(m.Names) > 0 {
				// method requirements — checked against the method set
				for _, n := range m.Names {
					reqs = append(reqs, n.Name)
				}
				continue
			}
			if !v.satisfiesTypeElem(ctx, m.Type, td) {
				return false
			}
		}
		return v.typeHasMethods(td, reqs)
	case *ast.Ident:
		switch t.Name {
		case "any", "comparable":
			return true
		}
		if isBuiltinTypeName(t.Name) {
			return tdNameOrAnon(td) == t.Name
		}
		// named constraint — resolve it and check its requirements
		if ok, done := v.satisfiesNamed(ctx, t, td); done {
			return ok
		}
		return true
	default:
		// top-level type elements — `T ~int`, `T ~A | ~B`, `T []int`
		// reach the element checker; named/selector/index constraint
		// exprs resolve against the type index first.
		switch cons.(type) {
		case *ast.BinaryExpr, *ast.UnaryExpr, *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.ChanType:
			return v.satisfiesTypeElem(ctx, cons, td)
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
			if ok, done := v.satisfiesNamed(ctx, cons, td); done {
				return ok
			}
		}
		return true
	}
}

// satisfiesTypeElem checks one type element inside a constraint interface.
func (v *VM) satisfiesTypeElem(ctx *runtime.TypeDef, e ast.Expr, td *runtime.TypeDef) bool {
	switch t := e.(type) {
	case *ast.BinaryExpr:
		if t.Op == token.OR {
			return v.satisfiesTypeElem(ctx, t.X, td) || v.satisfiesTypeElem(ctx, t.Y, td)
		}
		return true
	case *ast.UnaryExpr:
		if t.Op == token.TILDE {
			// `~int` matches any type whose UNDERLYING type is int —
			// a named `type MyInt int` satisfies it; peel the argument.
			u := v.peelNamed(td)
			if underlyingNameOf(u) == typeExprName(t.X) {
				return true
			}
			// a composite element usually mentions the other type
			// parameters (`S ~[]E` in slices.Sort): spell it with the
			// instantiation's binds (checkTArgs puts them on ctx).
			// A parameter not inferred yet (E, bound later from S's core
			// type) leaves only the composite's kind to check.
			if ctx != nil && isCompositeTypeExpr(t.X) {
				if mentionsUnbound(t.X, ctx) {
					return compositeKindMatches(t.X, u)
				}
				// only the argument's outer layer peels: E keeps its
				// declared identity inside `[]E` (TypSpelling, not the
				// underlying view, which would expand a named E).
				el := &runtime.TypeDef{Pkg: ctx.Pkg, File: ctx.File, Binds: ctx.Binds}
				return runtime.TypSpelling(t.X, el) == v.underlyingShape(td)
			}
			return false
		}
		return v.satisfiesTypeElem(ctx, t.X, td)
	case *ast.ParenExpr:
		return v.satisfiesTypeElem(ctx, t.X, td)
	case *ast.InterfaceType:
		// embedded interface literal — check its declared methods
		var reqs []string
		for _, m := range t.Methods.List {
			for _, n := range m.Names {
				reqs = append(reqs, n.Name)
			}
		}
		return v.typeHasMethods(td, reqs)
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		if ok, done := v.satisfiesNamed(ctx, e, td); done {
			return ok
		}
		if id, ok := e.(*ast.Ident); ok {
			switch id.Name {
			case "any":
				return true
			case "comparable":
				// approximated: slices, maps and funcs are never
				// comparable; everything else is let through.
				if u := v.peelNamed(td); u != nil {
					switch u.Kind {
					case runtime.KindSlice, runtime.KindMap, runtime.KindFunc:
						return false
					}
				}
				return true
			case "error":
				return v.typeHasMethods(td, []string{"Error"})
			}
		}
		// bare type element: exact type-name match
		return typeExprName(e) == tdNameOrAnon(td)
	default:
		return typeExprName(e) == tdNameOrAnon(td)
	}
}

// mentionsUnbound reports whether e names one of ctx's type parameters
// that has no bind yet.
func mentionsUnbound(e ast.Expr, ctx *runtime.TypeDef) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || found {
			return !found
		}
		for _, tp := range ctx.TParams {
			if tp == id.Name {
				if _, bound := ctx.Binds[tp]; !bound {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// compositeKindMatches reports whether td has the kind a composite type
// expression spells (`[]E`, `map[K]V`, `*T`, `chan T`, `func(...)`).
func compositeKindMatches(e ast.Expr, td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	switch e.(type) {
	case *ast.ArrayType:
		return td.Kind == runtime.KindSlice
	case *ast.MapType:
		return td.Kind == runtime.KindMap
	case *ast.StarExpr:
		return td.Kind == runtime.KindPointer
	case *ast.ChanType:
		return td.Kind == runtime.KindChan
	case *ast.FuncType:
		return td.Kind == runtime.KindFunc
	}
	return false
}

// isCompositeTypeExpr reports whether e spells a composite type literal.
func isCompositeTypeExpr(e ast.Expr) bool {
	switch e.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.ChanType, *ast.FuncType:
		return true
	}
	return false
}

// satisfiesNamed resolves a named or instantiated constraint element
// (`io.Reader`, `Adder[T]`) through the type index and checks the targ
// against it: an interface checks its required methods, a concrete type
// requires an exact match. done=false means unresolvable — callers fall
// back to their approximation.
func (v *VM) satisfiesNamed(ctx *runtime.TypeDef, e ast.Expr, td *runtime.TypeDef) (ok bool, done bool) {
	if v.H.ResolveType == nil {
		return false, false
	}
	var base ast.Expr
	switch x := e.(type) {
	case *ast.IndexExpr:
		base = x.X
	case *ast.IndexListExpr:
		base = x.X
	case *ast.Ident:
		if isBuiltinTypeName(x.Name) {
			return false, false
		}
		base = x
	case *ast.SelectorExpr:
		base = x
	default:
		return false, false
	}
	nt, err := v.H.ResolveType(ctx, base)
	if err != nil || nt == nil {
		return false, false
	}
	if nt.Kind == runtime.KindInterface {
		// the constraint's type elements and embedded interfaces
		// constrain the argument too — checking only the method set
		// would let `interface{ ~int }` accept every type.
		for _, e := range nt.IEmbeds {
			if !v.satisfiesTypeElem(nt, e, td) {
				return false, true
			}
		}
		return v.typeHasMethods(td, v.ifaceReqNames(nt)), true
	}
	return sameTypeDef(td, nt), true
}

// ifaceReqNames collects a resolved interface typedef's required method
// names through the engine's recursive interface requirement walk.
func (v *VM) ifaceReqNames(nt *runtime.TypeDef) []string {
	if v.H.IfaceReqs != nil {
		if reqs, err := v.H.IfaceReqs(nt); err == nil && reqs != nil {
			out := make([]string, 0, len(reqs))
			for name := range reqs {
				out = append(out, name)
			}
			return out
		}
	}
	return nt.MReqs
}

// typeHasMethods reports whether td's method set covers every required
// name — generic methods are already excluded from method sets (they
// cannot satisfy interfaces). nil td or no method hook passes: an
// unresolvable targ traps later on use anyway.
func (v *VM) typeHasMethods(td *runtime.TypeDef, reqs []string) bool {
	if len(reqs) == 0 || td == nil || v.H.TypeMethods == nil {
		return true
	}
	ms, err := v.H.TypeMethods(td)
	if err != nil || ms == nil {
		return len(reqs) == 0
	}
	for _, r := range reqs {
		if _, ok := ms[r]; !ok {
			return false
		}
	}
	return true
}

// tdNameOrAnon is the exact-type name for bare constraint elements:
// `interface{ int }` matches only `int`, not a defined `type MyInt int`.
func tdNameOrAnon(td *runtime.TypeDef) string {
	if td.Name != "" {
		return td.Name
	}
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	return ""
}

// underlyingNameOf follows Anon to a builtin-ish shape name for `~T`.
func underlyingNameOf(td *runtime.TypeDef) string {
	if td.Anon != nil {
		return typeExprName(td.Anon)
	}
	if td.Name != "" {
		return td.Name
	}
	return ""
}

var builtinTypeNames = map[string]bool{
	"bool": true, "string": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "byte": true, "rune": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

func isBuiltinTypeName(n string) bool { return builtinTypeNames[n] }
