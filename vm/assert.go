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
	"go/token"
	"sort"
	"strings"
	"weak"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// refBase unwraps a ref's base to the identity object pointer
// equality means: the container behind any Cell/Named wrappers.
func refBase(v runtime.Value) runtime.Value {
	for {
		if _, isGV := v.(*runtime.GoValue); isGV {
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			return v
		}
		if dv, ok := runtime.Deref(v); ok {
			v = dv
			continue
		}
		return v
	}
}

// zeroSizeValue reports whether a value's type occupies no bytes — an
// empty struct, a [0]T array, or a composite of only zero-size fields.
// Go stores all such values at the same address (runtime.zerobase).
func zeroSizeValue(v runtime.Value) bool {
	return runtime.IsZeroSizeValue(v)
}

// sliceTypOf: slicing an array-typed value yields the slice type []Elem —
// `a[1:]` on [4]int is []int, not [4]int. Carrying the array typedef
// would make later binds deep-copy it like an array (losing the shared
// backing) and would key it differently as a map key.
func sliceTypOf(td *runtime.TypeDef) *runtime.TypeDef {
	at := runtime.ArrayASTOf(td)
	if at == nil || at.Len == nil {
		return td
	}
	return &runtime.TypeDef{
		Kind:       runtime.KindSlice,
		Elem:       td.Elem,
		Anon:       &ast.ArrayType{Elt: at.Elt},
		Pkg:        td.Pkg,
		File:       td.File,
		Binds:      td.Binds,
		LocalTypes: td.LocalTypes,
	}
}

// uncomparableTyp reports whether a typedef's kind is uncomparable in
// Go — slices, maps and funcs panic on == even when nil.
func uncomparableTyp(td *runtime.TypeDef) bool {
	return td != nil && (td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap || td.Kind == runtime.KindFunc)
}

// uncomparableTypDeep reports whether the typedef's shape contains an
// uncomparable kind anywhere — an equality on the enclosing type
// panics naming itself (Go names the outermost uncomparable type).
// Interface-typed members stay comparable: the dynamic type decides at
// compare time.
func (v *VM) uncomparableTypDeep(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if uncomparableTyp(td) && !runtime.ArrayTypedef(td) {
		return true // slice / map / func leaf
	}
	switch {
	case runtime.ArrayTypedef(td):
		return v.uncomparableTypDeep(v.elemTypedef(nil, td))
	case td.Kind == runtime.KindStruct:
		for _, ft := range v.fieldTypedefs(td) {
			if v.uncomparableTypDeep(ft) {
				return true
			}
		}
	}
	return false
}

// uncomparableDynamicTyp returns the value's dynamic typedef when its
// type is uncomparable — a slice, map or nil-of-either — so an ==
// between two interface values carrying the same uncomparable dynamic
// type panics like Go, including nil-of-slice against a live slice.
func uncomparableDynamicTyp(v runtime.Value) *runtime.TypeDef {
	switch x := v.(type) {
	case *runtime.Slice:
		if x.Typ != nil && !runtime.ArrayTypedef(x.Typ) {
			return x.Typ
		}
	case *runtime.Map:
		return x.Typ
	case *runtime.TypedNil:
		if uncomparableTyp(x.Typ) {
			return x.Typ
		}
	case *runtime.IfaceNil:
		if uncomparableTyp(x.Typ) {
			return x.Typ
		}
	}
	return nil
}

// uncomparableValue reports whether an == over v must panic: slices,
// maps and funcs (live or typed-nil) are uncomparable types.
func uncomparableValue(v runtime.Value) bool {
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Slice:
		return !runtime.ArrayTypedef(x.Typ)
	case *runtime.Map, *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return true
	case *runtime.TypedNil:
		return uncomparableTyp(x.Typ)
	}
	return false
}

// typeExprFor renders a typedef back to a type expression for synthetic
// wrappers like the `(*T)` of a pointer method expression. Named types
// render as the name so a later ResolveType finds the declaration (and
// its methods) through the package index.
func typeExprFor(td *runtime.TypeDef) ast.Expr {
	if td.Name != "" {
		return ast.NewIdent(td.Name)
	}
	if td.Anon != nil {
		return td.Anon
	}
	return ast.NewIdent("interface{}")
}

// instExprFor spells the type expression a typedef denotes, restoring
// the instantiation arguments an instantiated typedef carries only in
// Binds — `G[string]`, not the bare `G` typeExprFor gives. Anonymous or
// partially bound typedefs fall back to the unindexed spelling.
func instExprFor(td *runtime.TypeDef) ast.Expr {
	if td.Name == "" || len(td.TParams) == 0 || len(td.Binds) == 0 {
		return typeExprFor(td)
	}
	args := make([]ast.Expr, 0, len(td.TParams))
	for _, tp := range td.TParams {
		btd, ok := td.Binds[tp].(*runtime.TypeDef)
		if !ok {
			return typeExprFor(td)
		}
		args = append(args, instExprFor(btd))
	}
	base := ast.NewIdent(td.Name)
	if len(args) == 1 {
		return &ast.IndexExpr{X: base, Index: args[0]}
	}
	return &ast.IndexListExpr{X: base, Indices: args}
}

// sameTypeDef reports whether two typedefs name the same type — the
// shared runtime.TypIdentical judgment (decl object, or name+package+
// binds for named types, or equal shape spellings for anonymous ones).
func sameTypeDef(a, b *runtime.TypeDef) bool {
	if a == b {
		return true
	}
	return runtime.TypIdentical(a, b)
}

// typeExprName renders a type AST to a comparable shape string for
// anonymous-type identity (approximation: structural equality by shape,
// not by the go/types identity rules).
func typeExprName(p *runtime.Package, e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeExprName(p, t.X)
	case *ast.ArrayType:
		return "[" + runtime.TypLenName(runtime.ArrayLenExpr(p, t)) + "]" + typeExprName(p, t.Elt)
	case *ast.MapType:
		return "map[" + typeExprName(p, t.Key) + "]" + typeExprName(p, t.Value)
	case *ast.ChanType:
		// direction is part of the type identity: <-chan T, chan<- T
		// and chan T are three different types.
		if t.Dir == ast.SEND {
			return "chan<- " + typeExprName(p, t.Value)
		}
		if t.Dir == ast.RECV {
			return "<-chan " + typeExprName(p, t.Value)
		}
		return "chan " + typeExprName(p, t.Value)
	case *ast.SelectorExpr:
		return typeExprName(p, t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return typeExprName(p, t.X) + "[" + typeExprName(p, t.Index) + "]"
	case *ast.IndexListExpr:
		s := typeExprName(p, t.X) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += typeExprName(p, x)
		}
		return s + "]"
	case *ast.ParenExpr:
		return typeExprName(p, t.X)
	case *ast.UnaryExpr:
		if t.Op == token.TILDE {
			return "~" + typeExprName(p, t.X)
		}
	case *ast.Ellipsis:
		return "..." + typeExprName(p, t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}

// tdName is a readable name for a typedef in diagnostics.
func tdName(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil type>"
	}
	if td.Name != "" {
		return td.Name
	}
	if td.Anon != nil {
		return typeExprName(td.Pkg, td.Anon)
	}
	switch td.Kind {
	case runtime.KindInterface:
		return "interface{}"
	case runtime.KindSlice:
		return "slice"
	case runtime.KindMap:
		return "map"
	case runtime.KindFunc:
		return "func"
	case runtime.KindChan:
		return "chan"
	}
	return "type"
}

// tdNameIn spells td for a diagnostic issued from cur's compilation
// unit: a named type declared in another package qualifies by package
// name like gc (`origin.Count`), while names in the same package and
// names already carrying a qualifier print bare.
func tdNameIn(cur *runtime.Package, td *runtime.TypeDef) string {
	if td != nil && td.Name != "" && td.Pkg != nil && td.Pkg != cur && td.Pkg.Name != "" && !strings.ContainsRune(td.Name, '.') {
		return td.Pkg.Name + "." + td.Name
	}
	return tdName(td)
}

// typeAssert implements x.(T): returns x on match, panics with a script
// Panic (recoverable) on mismatch — Go semantics for a failed assertion.
// static is the operand's declared type (a bare identifier's cell type,
// NIL when the expression has none) — it names the interface in the
// message the way Go does: "main.I is main.T, not io.Writer".
func (v *VM) typeAssert(f *frame, x, tdv, static runtime.Value, pos token.Pos) runtime.Value {
	td := typedefOf(tdv)
	if td == nil {
		f.trap("type assertion target %T is not a type", tdv)
	}
	if v.typeMatches(f, td, x) {
		return unboxAsserted(x, td)
	}
	// asserting to a non-empty interface names the missing method:
	// "main.T is not io.Writer: missing method Write".
	if td.Kind == runtime.KindInterface {
		if miss := v.ifaceFailReason(td, x); miss != "" {
			panic(&runtime.Panic{Value: &runtime.GoValue{V: &runtime.TypeAssertionError{Msg: fmt.Sprintf("interface conversion: %s is not %s: missing method %s", typeNameOf(x), spelledTyp(td), miss)}}})
		}
	}
	staticName := "interface {}"
	if st, ok := static.(*runtime.TypeDef); ok {
		staticName = spelledTyp(st)
		if staticName == "interface{}" {
			staticName = "interface {}"
		}
	}
	got, want := typeNameOf(x), spelledTyp(td)
	// Two distinct types can spell identically — func-local `type T`s in
	// the same package or like-named types across packages. Go appends a
	// disambiguator then: "is main.T, not main.T (types from different
	// scopes)" (issue26094). Package identity compares the Pkg objects —
	// a bound typedef's synthesized Pkg shares the script package's Path.
	suffix := ""
	if got == want {
		var dynPkg *runtime.Package
		if dyn := v.typeOfValue(x); dyn != nil {
			dynPkg = dyn.Pkg
		}
		if dynPkg == td.Pkg || (dynPkg != nil && td.Pkg != nil && dynPkg.Path == td.Pkg.Path) {
			suffix = " (types from different scopes)"
		} else {
			suffix = " (types from different packages)"
		}
	}
	panic(&runtime.Panic{Value: &runtime.GoValue{V: &runtime.TypeAssertionError{Msg: fmt.Sprintf("interface conversion: %s is %s, not %s%s", staticName, got, want, suffix)}}})
}

// ifaceFailReason names the required method x fails on — Go's
// "missing method Write" detail when an interface assertion fails on
// the method set. The runtime itab comparison reports both an absent
// method and a same-named method with a different signature as
// "missing method M" (verified against gc: `func (T) M() int` does
// not satisfy `I{ M() string }` and panics "missing method M"), so
// this returns the first failing requirement in either category,
// sorted for determinism. Empty when the check can't name one (hooks
// missing, or the failure came from something else).
func (v *VM) ifaceFailReason(td *runtime.TypeDef, x runtime.Value) string {
	if x == nil || x == runtime.NIL || runtime.IfaceTaggedNil(x) != nil {
		// no dynamic type — the generic "interface {} is nil, not T"
		// panic stands; there is no missing method to name.
		return ""
	}
	if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 {
		return ""
	}
	if v.H.IfaceReqs == nil {
		return ""
	}
	reqs, err := v.H.IfaceReqs(td)
	if err != nil || len(reqs) == 0 {
		return ""
	}
	var have map[string]bool
	var unsure bool
	var dynFns map[string]*runtime.Function
	if tn, ok := asTypedNil(x); ok {
		// a typed nil asserts on its declared typedef's method set —
		// the same typedef-level check typeMatchesTD runs.
		if v.H.TypeMethods == nil {
			return ""
		}
		if have, err = v.H.TypeMethods(tn.Typ); err != nil {
			return ""
		}
		if v.H.TypeMethodFuncs != nil {
			if dynFns, err = v.H.TypeMethodFuncs(tn.Typ); err != nil {
				dynFns = nil
			}
		}
	} else if v.H.MethodInfoOf != nil {
		// one walk: names, unsure and signature-bearing funcs come from
		// the same method set. A nil set is a legitimately empty one —
		// a basic type's dynamic value has no declared methods — so
		// every requirement is missing and names a method.
		ms, err := v.H.MethodInfoOf(x)
		if err != nil {
			return ""
		}
		if ms != nil {
			have, unsure, dynFns = ms.Names, ms.Unsure, ms.Funcs
		}
	} else {
		if v.H.MethodSetOf != nil {
			have, unsure, err = v.H.MethodSetOf(x)
		} else if v.H.MethodsOf != nil {
			have, err = v.H.MethodsOf(x)
		} else {
			return "" // no method-set hook answered — leave the generic panic
		}
		if err != nil {
			return ""
		}
		if v.H.MethodFuncsOf != nil {
			dynFns, _ = v.H.MethodFuncsOf(x)
		}
	}
	missing := make([]string, 0, len(reqs))
	for m := range reqs {
		if !have[m] && !unsure {
			missing = append(missing, m)
		}
	}
	if v.H.IfaceSigs != nil {
		if reqFns, err := v.H.IfaceSigs(td); err == nil {
			for m, reqFn := range reqFns {
				if !have[m] && !unsure {
					continue // already counted above
				}
				if dynFn, ok := dynFns[m]; ok {
					sig, _, _, _ := runtime.FuncSigOf(dynFn)
					if sig != nil {
						if !v.memberSigEq(reqFn, dynFn) {
							missing = append(missing, m)
						}
						continue
					}
				}
				if !v.hostMethodSigMatch(reqFn, m, x) {
					missing = append(missing, m)
				}
			}
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return ""
	}
	return missing[0]
}

// spelledTyp spells a typedef the way Go's runtime does in conversion
// panics — package-qualified ("main.I", "io.Writer"), anonymous forms
// rendered from their AST.
func spelledTyp(td *runtime.TypeDef) string {
	if td == nil {
		return "interface {}"
	}
	return runtime.DisplayName(td)
}

// typeAssertOK implements the comma-ok form: pushes Tuple{val, ok}.
func (v *VM) typeAssertOK(f *frame, x, tdv runtime.Value) runtime.Value {
	td := typedefOf(tdv)
	if td == nil {
		f.trap("type assertion target %T is not a type", tdv)
	}
	if v.typeMatches(f, td, x) {
		return &runtime.Tuple{Elems: []runtime.Value{unboxAsserted(x, td), true}}
	}
	// Go binds the asserted type's zero value on failure.
	return &runtime.Tuple{Elems: []runtime.Value{v.zeroValue(f, td), false}}
}

// unboxAsserted: a successful assert to a concrete type T pulls the bare
// dynamic value out of an interface box — x.(*int) yields *int, not any.
func unboxAsserted(x runtime.Value, td *runtime.TypeDef) runtime.Value {
	if td.Kind != runtime.KindInterface {
		if in, ok := x.(*runtime.IfaceNil); ok {
			return &runtime.TypedNil{Typ: in.Typ}
		}
	}
	// a declared-over-interface tag rides no concrete value: the
	// asserted value is the payload itself, not the A wrapper.
	return unwrapIfaceTag(x)
}

// typeMatches implements duck-typing: interfaces check the method set via
// engine hooks; concrete typedefs match by descriptor identity (or name
// for builtins / primitives). Typed nils match by declared type identity.
func (v *VM) typeMatches(f *frame, td *runtime.TypeDef, x runtime.Value) bool {
	if x == nil || x == runtime.NIL {
		// nil has no dynamic type: every assert fails, including .(any).
		// (`case nil:` in a type switch is matched by BinEql, not here.)
		return false
	}
	if td.Kind == runtime.KindAlias {
		// aliases are transparent: `x.(A)` on `type A = T` asserts to T
		if u := v.peelAlias(td); u != td {
			td = u
		}
	}
	if n, ok := x.(*runtime.Named); ok {
		if n.Typ != nil && (n.Typ.IfaceBase || n.Typ.Kind == runtime.KindInterface) {
			// a declared-over-interface tag is never the dynamic
			// type — the payload's pair is what an assertion sees:
			// `var a A = 42` (`type A any`) passes `a.(int)`.
			return v.typeMatches(f, td, n.V)
		}
		// a Named value's dynamic type is its declared typedef —
		// `x.(MyInt)` on MyInt matches, `x.(int)` does not.
		return v.typeMatchesTD(f, td, n.Typ, n.V)
	}
	if tn, ok := asTypedNil(x); ok {
		return v.typeMatchesTD(f, td, tn.Typ, nil)
	}
	if runtime.IfaceTaggedNil(x) != nil {
		// a nil interface value has no dynamic type — every assert
		// fails, including .(any).
		return false
	}
	if td.Kind == runtime.KindInterface || td.IfaceBase {
		// a declared-over-interface target asserts like the interface:
		// `var e E = v` (`type E error`) passes `e.(E)` when v's
		// dynamic type satisfies it.
		return v.satisfiesIface(f, td, x)
	}
	if td.Kind == runtime.KindPointer {
		if td.Spec != nil {
			// a declared pointer type asserts on its tag alone — the
			// Named branch above already handled it; a bare cell's
			// dynamic type is the anonymous *Elem, never P.
			return false
		}
		// asserting *T on a non-nil value: dereference one level and match
		// the element type (a Cell IS the pointer in this model).
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			et = v.localElemTypedef(f, td)
			if et == nil {
				if err != nil {
					f.trap("%s", err)
				}
				f.trap("cannot resolve element type of %s", tdName(td))
			}
		}
		if gv, ok := x.(*runtime.GoValue); ok {
			// a host box never dereferences through Deref — compare the
			// boxed native type instead. Host typedefs box a pointer
			// zero (sync.Mutex -> *sync.Mutex), so *T matches when the
			// native type equals the typedef's own box; a *T whose T is
			// declared in source (go/ast interpreted from source) matches
			// the host pointer to the same package path and name; other
			// *T fall back to comparing the printed type spelling.
			if et.HostNew != nil {
				// a host facade boxes a wrapper type but asserts as the
				// API type it spells — the scriptFile/EngineStdout
				// wrappers are *os.File as far as the script sees.
				if at, ok := gv.V.(interface{ APIType() hostbridge.Type }); ok {
					return at.APIType() == et.HostType()
				}
				return hostbridge.TypeOf(gv.V) == et.HostType()
			}
			if rt := hostbridge.TypeOf(gv.V); rt.Kind() == hostbridge.KindPointer && v.hostTypedefIs(rt.Elem(), et) {
				return true
			}
			return hostbridge.TypeOf(gv.V).String() == tdName(td)
		}
		if pt := v.pointeeTag(x); pt != nil && sameTypeDef(pt, et) {
			// any ref's stamped pointee type settles the assert as a
			// tag compare — the same "stamped type first" contract as
			// coerce/convert. Engine-internal handles (*runtime.Package)
			// reach this arm too: they ride no GoValue/Cell tag, so
			// pointeeTag synthesizes the pointee's typedef for them.
			return true
		}
		if et.HostNew != nil && hostbridge.TypeOf(x) == et.HostType() {
			// an unboxed host native still matches its typedef's box —
			// the backstop for handles pointeeTag does not tag.
			return true
		}
		dv, ok := runtime.Deref(x)
		if !ok {
			return false
		}
		return v.typeMatches(f, et, dv)
	}
	// a Cell/other ref inside an interface box is a *T value, not a T
	// lvalue — `var b any = &v` asserts to *rd, not rd. The pointer arm
	// above already handled that spelling; a bare Struct here is the T
	// value itself.
	switch xv := x.(type) {
	case *runtime.Struct:
		if xv.Def == td {
			return true
		}
		// anonymous struct spellings are the same type when their
		// field shapes match: `interface{}(struct{}{}).(struct{})`.
		if td.Name == "" && xv.Def != nil && xv.Def.Name == "" {
			return v.convShapeEq(xv.Def, td)
		}
		return xv.Def != nil && td.Name != "" && td.Pkg != nil && sameTypeDef(xv.Def, td)
	case int64:
		// a bare int64's dynamic type is int — int64(x) conversions and
		// the sized ints carry Named tags, so x.(int64), x.(int8) or
		// x.(MyInt) must not match here.
		return td.Name == "int"
	case float64:
		return td.Name == "float64"
	case string:
		return td.Name == "string"
	case bool:
		return td.Name == "bool"
	case *runtime.Slice:
		return v.containerAssert(f, td, xv.Typ, runtime.KindSlice)
	case *runtime.Map:
		return v.containerAssert(f, td, xv.Typ, runtime.KindMap)
	case *runtime.Chan:
		return v.containerAssert(f, td, xv.Typ, runtime.KindChan)
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return v.funcAssert(td, xv)
	case *runtime.GoValue:
		// a host box's dynamic type is its native Go type — complex64
		// boxes (there is no script complex type) assert back to
		// complex64, and interface{}(bytes.Buffer{}) asserts to
		// bytes.Buffer. Compare type spellings like the KindPointer
		// GoValue branch above.
		rt := hostbridge.TypeOf(xv.V)
		if rt == nil {
			return false
		}
		// hostbridge.Value is the minireflect facade, boxed as its pointer
		// (the typedef's HostNew) though it spells reflect.Value.
		if td.Name == "reflect.Value" && td.HostNew != nil {
			return rt == td.HostType()
		}
		// a boxed host func compares signatures like a script func
		// value — func(int) int never asserts to func(string).
		if rt.Kind() == hostbridge.KindFunc && td.Name == "" {
			if ft, ok := td.Anon.(*ast.FuncType); ok {
				return rt.String() == runtime.TypGoSpelling(ft, td)
			}
		}
		if v.hostTypedefIs(rt, td) {
			return true
		}
		return rt.String() == tdName(td)
	default:
		// a raw host scalar — a time.Duration a host call returned, or
		// `any(2*time.Second)` whose constant folded untagged — is a
		// value of its bound typedef: x.(time.Duration) matches.
		return td.HostScalar != nil && hostbridge.TypeOf(x) == hostbridge.TypeOf(td.HostScalar)
	}
}

// funcAssert implements x.(T) for a function value: an anonymous func
// typedef asserts by signature identity — func(int) int matches only a
// value declared with that signature — and a defined func type matches
// only its own tag (the *runtime.Named branch above handles declared
// func types, so a named target is false for every bare func value).
func (v *VM) funcAssert(td *runtime.TypeDef, x runtime.Value) bool {
	if td.Kind != runtime.KindFunc {
		return false
	}
	if td.Name != "" {
		// a host-bound func still names its real Go type through Target,
		// so a bound context.CancelFunc asserts back to context.CancelFunc.
		if bf, ok := x.(*runtime.BuiltinFunc); ok && bf.Target != nil {
			return hostbridge.TypeOf(bf.Target).String() == runtime.DisplayName(td)
		}
		return false
	}
	ft, ok := td.Anon.(*ast.FuncType)
	if !ok {
		// the asserted func typedef carries no signature AST to compare
		// against — fall back to the kind match.
		return td.Spec == nil
	}
	if sig, pkg, file, binds := runtime.FuncSigOf(x); sig != nil {
		dyn := &runtime.TypeDef{Kind: runtime.KindFunc, Anon: sig, Pkg: pkg, File: file, Binds: binds}
		return v.sigTypEq(td, dyn)
	}
	if bf, ok := x.(*runtime.BuiltinFunc); ok && bf.Target != nil {
		// a bound host func compares by its real Go signature.
		return hostbridge.TypeOf(bf.Target).String() == runtime.TypGoSpelling(ft, td)
	}
	// intrinsics and synthesized adapters declare no signature to check —
	// keep the historical kind match.
	return td.Spec == nil
}

// containerAssert runs x.(T) on a stamped slice/map/chan: a declared tag
// asserts by typedef identity (`type A []int` is not `[]int`), anonymous
// tags compare by underlying shape ([]byte IS []uint8), and untagged
// (host-produced) containers fall back to the kind check.
func (v *VM) containerAssert(f *frame, td, tag *runtime.TypeDef, kind runtime.TypeKind) bool {
	if td.Kind != kind {
		return false
	}
	if tag == nil {
		return true
	}
	if td.Name != "" || tag.Name != "" {
		return v.typeMatchesTD(f, td, tag, nil)
	}
	if kind == runtime.KindSlice && tag.Elem != nil && v.hostElemTwin(f, tag, td) {
		// a host-unboxed slice spells its element bare (`[]*Field`);
		// the element identity it kept decides x.([]*goast.Field).
		return true
	}
	return v.sigTypEq(td, tag)
}

// satisfiesIface checks a value against an interface typedef's method set.
func (v *VM) satisfiesIface(f *frame, td *runtime.TypeDef, x runtime.Value) bool {
	ok, err := v.ifaceSatisfied(td, x)
	if err != nil {
		f.trap("%s", err)
	}
	return ok
}

// ifaceKey identifies one interface-satisfaction question: the method
// set a value offers depends only on its typedef and whether it was
// reached through a pointer (methodInfoOfValue), so the answer can be
// memoized per (interface, dynamic typedef, pointer) like Go's itabs.
// A host box's set is its reflect type's (host), and a nil interface
// value's is its tag's (nilIface keeps it apart from a value of the
// same typedef, which methodInfoOfValue resolves differently).
type ifaceKey struct {
	iface, dyn *runtime.TypeDef
	host       hostbridge.Type
	ptr        bool
	nilIface   bool
}

// maxIfaceCache bounds the per-VM memo: scripts that mint typedefs per
// call (anonymous interfaces, fresh instantiations) would otherwise grow
// it without limit. Reaching it starts over.
const maxIfaceCache = 4096

// ifaceDynKey reports x's memo key (iface left unset) — struct and named
// values, directly or through one pointer cell, host boxes and tagged nil
// interface values; anything else is checked uncached.
func ifaceDynKey(x runtime.Value) (key ifaceKey, ok bool) {
	switch t := x.(type) {
	case *runtime.Struct:
		return ifaceKey{dyn: t.Def}, t.Def != nil
	case *runtime.Named:
		return ifaceKey{dyn: t.Typ}, namedKeyable(t)
	case *runtime.GoValue:
		// hostMethodSet reads only the reflect type, and host methods
		// carry no signatures for the second pass.
		rt := hostbridge.TypeOf(t.V)
		return ifaceKey{host: rt}, rt != nil
	case *runtime.IfaceNil:
		return ifaceKey{dyn: t.Typ, nilIface: true}, t.Typ != nil
	case *runtime.Cell:
		// `&x` keys on the pointee's declared type, not the stored
		// value's: `(&w).(io.Writer)` on an interface var is *W (empty
		// set) while `(&s).(io.Writer)` on a struct is *T — sharing the
		// payload's key would leak one result into the other.
		if t.Typ != nil {
			return ifaceKey{dyn: t.Typ, ptr: true}, true
		}
		switch e := t.Elem.(type) {
		case *runtime.Struct:
			return ifaceKey{dyn: e.Def, ptr: true}, e.Def != nil
		case *runtime.Named:
			return ifaceKey{dyn: e.Typ, ptr: true}, namedKeyable(e)
		}
	}
	return ifaceKey{}, false
}

// namedKeyable reports whether a Named value's method set follows from
// its typedef alone: a tag declaring no methods over a host box exposes
// the boxed value's reflect methods, which vary per value.
func namedKeyable(n *runtime.Named) bool {
	if n.Typ == nil {
		return false
	}
	if _, host := runtime.Unwrap(n.V).(*runtime.GoValue); host && len(n.Typ.Methods) == 0 {
		return false
	}
	return true
}

// ifaceSatisfied is satisfiesIface's error-returning core, usable from
// contexts without a running frame (conversions). Answers are memoized
// per VM (see ifaceKey) until a method set is edited in place
// (runtime.MethodSetsChanged).
func (v *VM) ifaceSatisfied(td *runtime.TypeDef, x runtime.Value) (bool, error) {
	key, keyed := ifaceDynKey(x)
	if !keyed {
		return v.ifaceSatisfiedUncached(td, x)
	}
	if epoch := runtime.MethodSetEpoch(); v.ifaceMemo == nil || v.ifaceEpoch != epoch || len(v.ifaceMemo) >= maxIfaceCache {
		v.ifaceMemo = map[ifaceKey]bool{}
		v.ifaceEpoch = epoch
	}
	key.iface = td
	if ok, hit := v.ifaceMemo[key]; hit {
		return ok, nil
	}
	ok, err := v.ifaceSatisfiedUncached(td, x)
	if err == nil {
		v.ifaceMemo[key] = ok
	}
	return ok, err
}

func (v *VM) ifaceSatisfiedUncached(td *runtime.TypeDef, x runtime.Value) (bool, error) {
	// an iface-tagged Named satisfies through the payload it carries:
	// re-coercing `var e E = myErr{}` reads myErr's methods, not E's.
	x = unwrapIfaceTag(x)
	if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 && !td.IfaceBase {
		// a declared-over-interface type carries its requirements on
		// the underlying spec, resolved through IfaceReqs below.
		return true, nil // empty interface
	}
	if v.H.IfaceReqs == nil || v.H.MethodsOf == nil {
		return false, fmt.Errorf("interface checks require engine hooks")
	}
	reqs, err := v.H.IfaceReqs(td)
	if err != nil {
		return false, err
	}
	if len(reqs) == 0 {
		return true, nil
	}
	var have map[string]bool
	var unsure bool
	var ms *runtime.MethodSet
	if v.H.MethodInfoOf != nil {
		var err error
		ms, err = v.H.MethodInfoOf(x)
		if err != nil {
			return false, err
		}
		if ms != nil {
			have, unsure = ms.Names, ms.Unsure
		}
	} else {
		if v.H.MethodSetOf != nil {
			have, unsure, err = v.H.MethodSetOf(x)
		} else {
			have, err = v.H.MethodsOf(x)
		}
		if err != nil {
			return false, err
		}
	}
	for m := range reqs {
		if !have[m] && !unsure {
			// an unresolvable embedded type may promote any missing
			// method, so only a complete method set can fail the check
			return false, nil
		}
	}
	return v.ifaceSigsMatch(td, x, ms)
}

// ifaceSigsMatch runs the signature pass of interface satisfaction: a
// same-named method whose declared signature differs does not satisfy
// the requirement — `foo() float64` never fulfills `foo() int`.
// Members without a comparable signature (facade requirements, host
// reflect methods) satisfy by name alone, as before. ms, when non-nil,
// carries the method set computed for the name pass, so the signature
// pass reads the same walk's funcs instead of walking again.
func (v *VM) ifaceSigsMatch(td *runtime.TypeDef, x runtime.Value, ms *runtime.MethodSet) (bool, error) {
	if v.H.IfaceSigs == nil {
		return true, nil
	}
	if ms == nil && v.H.MethodFuncsOf == nil {
		return true, nil
	}
	reqFns, err := v.H.IfaceSigs(td)
	if err != nil {
		return false, err
	}
	if len(reqFns) == 0 {
		return true, nil
	}
	var dynFns map[string]*runtime.Function
	if ms != nil {
		dynFns = ms.Funcs
	} else {
		dynFns, err = v.H.MethodFuncsOf(x)
		if err != nil {
			return false, err
		}
	}
	return v.memberSigsMatch(reqFns, dynFns, x), nil
}

// memberSigsMatch reports whether every signature-bearing requirement
// is met by a same-named member with an identical signature. A member
// absent from dynFns kept its name-only pass — except on a host box,
// whose reflect method signature is compared by spelling instead
// (`*fmt.wrapError`'s `Unwrap() error` never fulfills `Unwrap() []error`).
func (v *VM) memberSigsMatch(reqFns, dynFns map[string]*runtime.Function, x runtime.Value) bool {
	// a pkg-qualified requirement (a.m) also lists its plain alias (m)
	// for member selection — but satisfaction resolves the qualified
	// key, so comparing the alias too would match a different package's
	// same-named member. Skip a plain key when a qualified sibling
	// carries the requirement.
	aliased := map[string]bool{}
	for m := range reqFns {
		if i := strings.LastIndexByte(m, '.'); i >= 0 {
			aliased[m[i+1:]] = true
		}
	}
	for m, reqFn := range reqFns {
		if aliased[m] {
			continue
		}
		if dynFn, ok := dynFns[m]; ok {
			sig, _, _, _ := runtime.FuncSigOf(dynFn)
			if sig != nil {
				if !v.memberSigEq(reqFn, dynFn) {
					return false
				}
				continue
			}
		}
		if !v.hostMethodSigMatch(reqFn, m, x) {
			return false
		}
	}
	return true
}

// memberSigEq compares two members' declared signatures — an interface
// requirement and the concrete method offered against it — through the
// shared semantic comparator.
func (v *VM) memberSigEq(req, dyn *runtime.Function) bool {
	return v.H.SigMemo.Identical(req, dyn, sigResolver{v})
}

// sigTypEq compares two typedefs by signature identity through the
// shared semantic comparator, resolving against this VM's hooks.
func (v *VM) sigTypEq(a, b *runtime.TypeDef) bool {
	return runtime.SigTypEq(a, b, sigResolver{v})
}

// sigResolver adapts the VM's engine hooks to runtime.TypeResolver for
// the shared signature comparator — a missing hook reports as
// unavailable, letting each normalization fall back to its spelling.
type sigResolver struct{ v *VM }

func (s sigResolver) PeelAlias(td *runtime.TypeDef) *runtime.TypeDef { return s.v.peelAlias(td) }

func (s sigResolver) IfaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	if s.v.H.IfaceReqs == nil {
		return nil, errSigNoHook
	}
	return s.v.H.IfaceReqs(td)
}

func (s sigResolver) IfaceSigs(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	if s.v.H.IfaceSigs == nil {
		return nil, errSigNoHook
	}
	return s.v.H.IfaceSigs(td)
}

func (s sigResolver) ResolveType(td *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if s.v.H.ResolveType == nil {
		return nil, nil
	}
	return s.v.H.ResolveType(td, x)
}

func (s sigResolver) ElemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if s.v.H.ElemOf == nil {
		return nil, nil
	}
	return s.v.H.ElemOf(td)
}

func (s sigResolver) Underlying(td *runtime.TypeDef) *runtime.TypeDef { return s.v.peelNamed(td) }

var errSigNoHook = fmt.Errorf("type resolution hook unavailable")

// asTypedNil unwraps an interface-boxed nil down to its TypedNil.
func asTypedNil(x runtime.Value) (*runtime.TypedNil, bool) {
	if tn, ok := x.(*runtime.TypedNil); ok {
		return tn, true
	}
	if td := runtime.BoxedNilTyp(x); td != nil {
		return &runtime.TypedNil{Typ: td}, true
	}
	return nil, false
}

// typeMatchesTD runs x.(T) when x is a typed nil: the "dynamic type" is
// the typedef the nil was declared with. Interfaces check the typedef's
// method set; concrete types compare by type identity.
func (v *VM) typeMatchesTD(f *frame, td, dyn *runtime.TypeDef, x runtime.Value) bool {
	if td.Kind == runtime.KindInterface || td.IfaceBase {
		if len(td.MReqs) == 0 && len(td.IEmbeds) == 0 && !td.IfaceBase {
			return true
		}
		if v.H.IfaceReqs == nil || v.H.TypeMethods == nil {
			f.trap("interface checks require engine hooks")
		}
		reqs, err := v.H.IfaceReqs(td)
		if err != nil {
			f.trap("%s", err)
		}
		have, err := v.H.TypeMethods(dyn)
		if err != nil {
			f.trap("%s", err)
		}
		for m := range reqs {
			if !have[m] {
				return false
			}
		}
		if v.H.IfaceSigs != nil && v.H.TypeMethodFuncs != nil {
			reqFns, err := v.H.IfaceSigs(td)
			if err != nil {
				f.trap("%s", err)
			}
			if len(reqFns) > 0 {
				dynFns, err := v.H.TypeMethodFuncs(dyn)
				if err != nil {
					f.trap("%s", err)
				}
				return v.memberSigsMatch(reqFns, dynFns, x)
			}
		}
		return true
	}
	if td.Kind == runtime.KindSlice && td.Name == "" && dyn.Name == "" && v.hostElemTwin(f, dyn, td) {
		// a nil host slice's tag spells its element bare — see
		// containerAssert.
		return true
	}
	if hostbridge.HostNilPtrType(dyn) != nil || ((dyn.Kind == runtime.KindSlice || dyn.Kind == runtime.KindMap) && dyn.HostIdent != nil) {
		// a nil host *T (or named host slice or map, net.IP) asserts to its
		// source twin's type (or to the bound one) by identity, like
		// a non-nil host box.
		return v.sameTypedef(dyn, td)
	}
	return v.sigTypEq(td, dyn)
}

// typedMember resolves base.name on a container value that carries its
// declared typedef in Typ — values of `type L []E`, `type M map[K]V` or
// `type C chan T` keep the declared type's methods, including the generic
// methods added in Go 1.27. `what` names the container for the trap.
func (v *VM) typedMember(f *frame, td *runtime.TypeDef, name string, recv runtime.Value, what string) runtime.Value {
	if td != nil {
		if m, ok := td.Methods[name]; ok && selVisible(selPkg(f), td.Pkg, name) {
			if err := m.EnsureCompiled(); err != nil {
				f.trap("%s", err)
			}
			r := recv
			if m.PtrRecv {
				r = v.ptrReceiver(r)
			} else {
				if dv, ok := runtime.Deref(r); ok {
					r = dv
				}
				r = valueCopy(r)
			}
			return &runtime.BoundMethod{Recv: r, Fn: m}
		}
	}
	f.trap("select %s on %s", name, what)
	return nil
}

// memberOfType resolves base.name when base is a nil value carrying a
// type — a method on *T still binds (the body panics on field access);
// a field select on a nil pointer panics like Go.
func (v *VM) memberOfType(f *frame, td *runtime.TypeDef, name string, recv runtime.Value, isIface bool) runtime.Value {
	// methods resolve on the nil's own typedef first, then through an
	// anonymous pointer chain to the pointee (a *T nil keeps *T's method
	// set). A declared pointer typedef (`type P *Sq`) does not promote
	// pointee methods — P's method set is only what is declared on P.
	// recv may arrive as the operand's storage ref — `var l List; l.Add`
	// selects through l's cell so a pointer receiver binds &l — in which
	// case the nil checks below read the stored value (rv) while the
	// bound receiver keeps the ref.
	rv := recv
	if dv, ok := runtime.Deref(recv); ok {
		rv = dv
	}
	peeled := false
	for td != nil {
		// a nil interface value has no method at all — any call on it
		// panics like a nil-pointer dereference in Go. The nil arrives
		// as a typed nil on the value path or as an interface-kind
		// IfaceNil through a storage ref — both are the nil interface.
		if td.Kind == runtime.KindInterface {
			if runtime.IsNilIface(rv) {
				panic(runtime.NilDerefPanic())
			}
			if _, isNil := asTypedNil(rv); isNil {
				panic(runtime.NilDerefPanic())
			}
		}
		if m, ok := td.Methods[name]; ok && selVisible(selPkg(f), td.Pkg, name) {
			// A value receiver dereferences a peeled pointer chain at
			// dispatch — panic on nil like Go. A nil carrying a nilable
			// typedef (declared pointer/slice/map/chan/func values can
			// be nil) is a valid receiver: the call binds it and the
			// body decides. Through an interface the box itself is the
			// receiver — the method value binds lazily and the *T→T
			// wrapper panics when the call runs (defer i.M() panics at
			// invocation, not registration).
			if !m.PtrRecv && !isIface {
				if _, isNil := asTypedNil(rv); isNil && (peeled || !v.nilableTypedef(td)) {
					if _, isIfaceNil := rv.(*runtime.IfaceNil); isIfaceNil {
						// a value method dispatched through a nil
						// interface box reports Go's wrapper text, not
						// a bare nil dereference.
						panic(runtime.PlainPanic(fmt.Sprintf("value method %s.%s called using nil *%s pointer", spelledTyp(td), name, td.Name)))
					}
					panic(runtime.NilDerefPanic())
				}
			}
			r := recv
			if in, isNil := rv.(*runtime.IfaceNil); isNil {
				// an interface holding a nil binds the concrete typed
				// nil as the receiver — `var i I = (*P)(nil); i.M()`
				// calls M on (*P)(nil), not on a nil interface.
				r = &runtime.TypedNil{Typ: in.Typ}
			}
			if m.PtrRecv {
				// a nil *T operand binds the nil itself (ptrReceiver
				// passes a pointer-kind TypedNil through); any other
				// nil under a storage ref binds the ref as &x, like
				// the named/struct member paths do.
				r = v.ptrReceiver(r)
			} else if dv, ok := runtime.Deref(r); ok {
				r = valueCopy(dv)
			}
			return &runtime.BoundMethod{Recv: r, Fn: m}
		}
		if td.Kind != runtime.KindPointer || td.Spec != nil {
			break
		}
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil {
			et = v.localElemTypedef(f, td)
			if et == nil {
				break
			}
		}
		td = et
		peeled = true
	}
	// field access on a nil pointer panics in Go; on a nil slice/map/chan
	// it is a plain invalid select. A nil *T may still carry methods —
	// Go dispatches pointer methods on nil receivers and the method
	// body decides, so a host-backed typedef tries a bound zero-receiver
	// method before giving up to the deref panic.
	if tn, ok := rv.(*runtime.TypedNil); ok && tn.Typ.Kind == runtime.KindPointer {
		if bf, ok := v.hostNilMethod(tn.Typ, name); ok {
			return bf
		}
		panic(runtime.NilDerefPanic())
	}
	if in, ok := rv.(*runtime.IfaceNil); ok && in.Typ.Kind == runtime.KindPointer {
		if bf, ok := v.hostNilMethod(in.Typ, name); ok {
			return bf
		}
		panic(runtime.NilDerefPanic())
	}
	if nt, ok := asTypedNil(rv); ok && hostbridge.HostNilType(nt.Typ) != nil {
		// a nil named host slice (net.IP) dispatches its value
		// methods on the nil receiver, as in Go
		if bf, ok := v.hostNilMethod(nt.Typ, name); ok {
			return bf
		}
	}
	if isIface {
		f.trap("interface value has no field %s", name)
	}
	f.trap("select %s on nil %s", name, tdName(td))
	return nil
}

// declaredType reports whether td is a defined (declared) type rather than
// a builtin or anonymous shape: builtins carry only a Name, declared types
// carry their TypeSpec (or at least a package).
func declaredType(td *runtime.TypeDef) bool {
	return td.Spec != nil || td.Pkg != nil
}

// tagIsNamed reports whether td names a defined type — a `type` spec
// (Spec) or a predeclared name like int (Name). Anonymous structural
// typedefs carry a Pkg for name resolution but name nothing.
func tagIsNamed(td *runtime.TypeDef) bool {
	return td != nil && (td.Spec != nil || td.Name != "")
}

// declaredTag returns the declared type a value carries: a Named wrap's
// Typ, a named struct's Def, or a stamped container Typ. Bare values and
// values of unnamed (anonymous) types report nil.
func declaredTag(x runtime.Value) *runtime.TypeDef {
	var td *runtime.TypeDef
	switch t := x.(type) {
	case *runtime.Named:
		td = t.Typ
	case *runtime.Struct:
		td = t.Def
	case *runtime.Map:
		td = t.Typ
	case *runtime.Slice:
		td = t.Typ
	case *runtime.Chan:
		td = t.Typ
	}
	if tagIsNamed(td) {
		return td
	}
	return nil
}

// pointeeTag returns the declared type of a pointer value's pointee: the
// cell's stamped type first, else the stored value's own tag. The cell
// type may be anonymous (`var pw *uval` stamps a `*uval` typedef) — a
// pointer conversion still needs it to compare pointee identities. Nil
// only when the pointee carries no declared type.
func (v *VM) pointeeTag(x runtime.Value) *runtime.TypeDef {
	if c, ok := x.(*runtime.Cell); ok && c.Typ != nil {
		return c.Typ
	}
	dv, ok := runtime.Deref(x)
	if !ok {
		// an engine-internal handle is the pointer itself — its pointee
		// type synthesizes from the native element.
		if td := v.internalTypedef(x); td != nil && td.Kind == runtime.KindPointer {
			return td.Elem
		}
		return nil
	}
	return declaredTag(dv)
}

// internalTypedef synthesizes the typedef an engine-internal handle
// carries: *runtime.Package, *runtime.TypeDef, *runtime.Iterator and
// *runtime.ImportRef are opaque host objects the intrinsics hand
// scripts directly — they ride no Cell, Named or GoValue tag, so the
// tag funnels used to answer nil for them and asserts/coerces fell
// back to a raw reflect-type compare. Naming the class once here lets
// every funnel give the same answer the hostTypedefOf path gives
// unregistered host values: the synthesized named typedef is identical
// to the registered one under TypIdentical (same name, same kind).
func (v *VM) internalTypedef(x runtime.Value) *runtime.TypeDef {
	switch x.(type) {
	case *runtime.Package, *runtime.TypeDef, *runtime.Iterator, *runtime.ImportRef:
		return v.hostTypedefOf(hostbridge.TypeOf(x))
	}
	return nil
}

// containerTyp reads the declared-type tag stamped on a container value.
func containerTyp(x runtime.Value) *runtime.TypeDef {
	switch t := x.(type) {
	case *runtime.Map:
		return t.Typ
	case *runtime.Slice:
		return t.Typ
	case *runtime.Chan:
		return t.Typ
	}
	return nil
}

// stampContainerTyp returns x with its declared-type tag set. The tag
// lives on the container header, which is a value in Go's terms: passing
// a slice/map/chan copies the header and only the backing aliases, so
// stamping must not mutate the shared header in place — a `[]Word`
// parameter receiving a `nat` argument would otherwise strip the
// caller's method set (math/big divW hands z to divWVW(z []Word,...)).
// A map's new header shares its contents ([runtime.Map.Header]), so an
// insert, delete or clear through either header is seen by both.
func stampContainerTyp(x runtime.Value, td *runtime.TypeDef) runtime.Value {
	switch t := x.(type) {
	case *runtime.Map:
		c := t.Header()
		c.Typ = td
		return c
	case *runtime.Slice:
		c := *t
		c.Typ = td
		return &c
	case *runtime.Chan:
		c := *t
		c.Typ = td
		return &c
	}
	return x
}

// sameTypedef reports whether two typedefs denote the same type: the
// interned canonical descriptors equal (M4-4 — a stamped fs.DirEntry
// converges on the source declaration), or one side is a spelled twin
// interning cannot resolve — a host-minted tag carrying only a name
// (Spec == nil), or two source-anonymous descriptors whose spellings
// agree in different resolution contexts.
// hostTypedefIs reports whether td denotes the host type rt: a bare
// HostIdent stamp canonicalizes to rt's bound or source twin, so
// interned descriptors agree exactly when td is that twin —
// [hostbridge.HostTwinOf] remains the fallback for typedefs the intern
// table cannot key.
func (v *VM) hostTypedefIs(rt hostbridge.Type, td *runtime.TypeDef) bool {
	if rt == nil || td == nil {
		return false
	}
	// resolve rt's twin through the hook, never a minted stamp: a bare
	// HostIdent stamp interned here would land a hollow descriptor in
	// the table before the real typedef resolves — the table answers
	// whichever descriptor registered first. An unnamed pointer rt is
	// the exception: canonicalization resolves its named element's real
	// typedef and mints an anon *T, so no hollow key is stored — this
	// covers `x.(**T)` where rt.Elem() has no name for the hook.
	if v.H.HostTwin != nil {
		if twin := v.H.HostTwin(rt); twin != nil && v.sameTypedef(twin, td) {
			return true
		}
	}
	if rt.Kind() == hostbridge.KindPointer && rt.Name() == "" {
		// `x.(**T)` hands an unnamed *T — or a chain of them: peel the
		// unnamed pointer layers of rt, resolve the named element's
		// twin, and mint the *...*T typedef directly — interning a
		// HostIdent stamp could park a hollow descriptor in the table,
		// and a NAMED pointer type (`type P *T`) is not *T.
		ert := rt
		depth := 0
		for ert.Kind() == hostbridge.KindPointer && ert.Name() == "" {
			ert = ert.Elem()
			depth++
		}
		if depth > 0 && ert.Name() != "" && ert.PkgPath() != "" {
			var want *runtime.TypeDef
			if v.H.HostTwin != nil {
				if et := v.H.HostTwin(ert); et != nil {
					// a bound twin's name need not spell the declared
					// element: `lib.T` resolves through td's own
					// package scope to the bound td even when the bound
					// name is not the host type's Go name (`T` over
					// starPtrT), where a spelling compare would mint
					// `*example.com/lib.starPtrT`.
					e2 := td
					d2 := 0
					for e2 != nil && e2.Kind == runtime.KindPointer && e2.Spec == nil {
						el := e2.Elem
						if el == nil && v.H.ElemOf != nil {
							el, _ = v.H.ElemOf(v.peelNamed(e2))
						}
						e2 = el
						d2++
					}
					if d2 == depth && e2 != nil && v.sameTypedef(et, e2) {
						return true
					}
					want = et
					for i := 0; i < depth; i++ {
						want = runtime.AnonPtrTypedefOf(want)
					}
				}
			}
			if want == nil {
				// the element's package is not loaded — no twin, no
				// descriptor to stamp. Compare by import-path spelling
				// only: sameTypedef would intern the minted td and park
				// it under the spelled key the real *T resolves to,
				// leaving a hollow descriptor behind.
				expr := ast.Expr(&ast.SelectorExpr{
					X:   ast.NewIdent(ert.PkgPath()),
					Sel: ast.NewIdent(ert.Name()),
				})
				for i := 0; i < depth; i++ {
					expr = &ast.StarExpr{X: expr}
				}
				want := &runtime.TypeDef{
					Kind: runtime.KindPointer,
					Anon: expr,
				}
				if td.Anon != nil {
					if gs := runtime.TypGoSpelling(td.Anon, td); gs != "" && gs == runtime.TypSpelling(want.Anon, want) {
						return true
					}
				}
			} else if v.sameTypedef(want, td) {
				return true
			}
		}
	}
	return hostbridge.HostTwinOf(rt, td)
}

// HostNamedTwin is [VM.hostTypedefIs] for intrinsics holding only a
// [runtime.VMCaller] (see [runtime.HostNamedTwinner]).
func (v *VM) HostNamedTwin(rt hostbridge.Type, td *runtime.TypeDef) bool {
	return v.hostTypedefIs(rt, td)
}

func (v *VM) sameTypedef(a, b *runtime.TypeDef) bool {
	if a == nil || b == nil {
		return a == b
	}
	if v.internTypedef(a) == v.internTypedef(b) {
		return true
	}
	// the spelled acceptance needs actual content on both sides — a
	// fabricated bare {Kind: Slice} descriptor would equal every other
	// unspellable slice via tdName's kind fallback. Func is excluded:
	// the fabricated signature-free FuncType spells "func()" for any
	// signature and must not equate a real `func(int)`.
	na, nb := tdName(a), tdName(b)
	return a.Spec == nil &&
		a.Kind != runtime.KindFunc &&
		b.Kind != runtime.KindFunc &&
		(a.Name != "" || a.Anon != nil) &&
		(b.Name != "" || b.Anon != nil) &&
		na == nb &&
		na != "struct{}" && na != "func()" && na != "interface{}"
}

// hostElemTwin reports whether tag is an unnamed host slice whose
// element ([hostbridge.HostPkgElemTypedef]) is the very type td's element declares
// in source: host []ast.Stmt against go/ast's []Stmt spelled through a
// type parameter, which the spelling compare cannot see.
func (v *VM) hostElemTwin(f *frame, tag, td *runtime.TypeDef) bool {
	if tag == nil || tag.Kind != runtime.KindSlice || tag.Name != "" || tag.Elem == nil {
		return false
	}
	return v.sameTypedef(tag.Elem, v.elemTypedef(f, td))
}

// HostNilTwin is [VM.sameTypedef] for intrinsics (reflect.DeepEqual):
// nil types denote the same type when their canonical descriptors do —
// a nil host *T interns to the *T of its source or bound twin (M4-4).
func (v *VM) HostNilTwin(a, b *runtime.TypeDef) bool { return v.sameTypedef(a, b) }

// peelAlias follows only Alias links, one hop at a time: `type A = B`
// is the same type as B, while `type A B` defines a distinct named type
// — peeling past it would let a B-tagged value bind an A slot and keep
// the wrong tag. Underlying is transitive (it crosses `type A B` too),
// so this needs the dedicated hook; without it only sameTypeDef runs.
func (v *VM) peelAlias(td *runtime.TypeDef) *runtime.TypeDef {
	if v.H.AliasOf == nil {
		return td
	}
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		u, err := v.H.AliasOf(td)
		if err != nil || u == nil || u == td {
			break
		}
		td = u
	}
	return td
}

// peelNamed follows a typedef through Alias and `type A B` (NamedBasic)
// chains to the typedef that gives its storage shape — builtin or
// composite — stopping on unresolvable references.
func (v *VM) peelNamed(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && (td.Kind == runtime.KindAlias || td.Kind == runtime.KindNamedBasic) && v.H.Underlying != nil && i < 32; i++ {
		u, err := v.H.Underlying(td)
		if err != nil || u == nil || u == td {
			break
		}
		td = u
	}
	return td
}

// nilableTypedef reports whether a typedef's values can be nil at all —
// declared pointers, slices, maps, chans, funcs and interfaces have a
// nil zero, so a nil receiver of one of these types still binds as a
// method receiver (the body decides). Structs and basics cannot be nil.
func (v *VM) nilableTypedef(td *runtime.TypeDef) bool {
	u := v.peelNamed(td)
	if u == nil {
		return false
	}
	switch u.Kind {
	case runtime.KindPointer, runtime.KindSlice, runtime.KindMap,
		runtime.KindChan, runtime.KindFunc, runtime.KindInterface:
		return true
	}
	if u.HostNew != nil {
		// a bound host type is classed a struct whatever its shape;
		// its zero's own kind tells a named slice or map (net.IP,
		// net.HardwareAddr) that answers `== nil`.
		switch u.HostType().Kind() {
		case hostbridge.KindSlice, hostbridge.KindMap, hostbridge.KindChan, hostbridge.KindFunc:
			return true
		}
	}
	return false
}

// tdShapeEq reports whether two typedefs have the same underlying shape —
// for typed-nil retagging (`var s S = ([]int)(nil)` needs S ~ []int).
// Shape spelling normalizes byte/rune and resolves bound type params
// (convShapeEq), so []byte and []uint8 count as one shape.
func (v *VM) tdShapeEq(a, b *runtime.TypeDef) bool {
	return runtime.NewShapeEq(sigResolver{v}).Same(a, b)
}

// samePointeeAlias reports whether two anonymous pointer typedefs point
// at the same type once aliases peel — a nil *inner.Tree binds *tree for
// `type tree = inner.Tree` (kin-openapi's originTree).
func (v *VM) samePointeeAlias(a, b *runtime.TypeDef) bool {
	return runtime.NewShapeEq(sigResolver{v}).PointeeAlias(a, b)
}

// tdShapeEval is tdShapeEq with evaluated array lengths: `[len(x)]*T`
// and `[3]*T` spell differently but name the same type once len(x)
// folds. Only reached when the plain spelling compare failed.
func (v *VM) tdShapeEval(f *frame, a, b *runtime.TypeDef) bool {
	if v.tdShapeEq(a, b) {
		return true
	}
	na, aok := v.arrayLen(f, a)
	nb, bok := v.arrayLen(f, b)
	if !aok || !bok || na != nb {
		return false
	}
	ea := v.elemTypedef(f, a)
	eb := v.elemTypedef(f, b)
	if ea == nil || eb == nil {
		return ea == nil && eb == nil
	}
	return v.tdShapeEval(f, ea, eb)
}

// shapeOK checks a bare value against a fully-peeled (non-named) typedef:
// basic families match by kind, structs by typedef identity, containers by
// kind. Unverifiable cases pass — the check is assignability, not typing.
func (v *VM) shapeOK(f *frame, x runtime.Value, td *runtime.TypeDef) bool {
	switch td.Kind {
	case runtime.KindNamedBasic, runtime.KindAlias:
		name := td.Name
		if name == "" {
			if id, ok := td.Anon.(*ast.Ident); ok {
				name = id.Name
			}
		}
		switch name {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
			_, ok := x.(int64)
			return ok
		case "float32", "float64":
			// int64 is accepted too: the VM cannot distinguish an untyped
			// constant (`var f float64 = 1`) from an int-typed variable.
			switch x.(type) {
			case int64, float64:
				return true
			}
			return false
		case "complex64", "complex128":
			// same cannot-distinguish rule: numerics promote to complex.
			switch x.(type) {
			case int64, float64:
				return true
			}
			if gv, ok := x.(*runtime.GoValue); ok {
				switch gv.V.(type) {
				case complex64, complex128:
					return true
				}
			}
			return false
		case "string":
			_, ok := x.(string)
			return ok
		case "bool":
			_, ok := x.(bool)
			return ok
		}
		return true // unknown underlying name — pass through
	case runtime.KindStruct:
		s, ok := x.(*runtime.Struct)
		if !ok {
			return false
		}
		if s.Def == nil || td.Name == "" {
			return true // anonymous shape — approximated ok
		}
		if sameTypeDef(s.Def, td) {
			return true
		}
		// an unnamed struct value binds a named struct type when the
		// field sets match (Go: identical underlying, V unnamed ->
		// assignable); a NAMED Def never reaches here — the declared-tag
		// check in coerceConcrete decides those binds. Field tags are
		// not tracked, so the comparison is names only.
		if !tagIsNamed(s.Def) {
			return runtime.StructFieldsEq(s.Def, td)
		}
		return false
	case runtime.KindSlice:
		_, ok := x.(*runtime.Slice)
		return ok
	case runtime.KindMap:
		_, ok := x.(*runtime.Map)
		return ok
	case runtime.KindChan:
		_, ok := x.(*runtime.Chan)
		return ok
	case runtime.KindFunc:
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			return true
		}
		return false
	case runtime.KindPointer:
		if _, ok := runtime.Deref(x); ok {
			return true
		}
		// an engine-internal handle IS the pointer — not a Deref-able
		// ref — so pointer-shaped membership in the handle class is
		// enough; identity was checked through the tag funnels.
		return v.internalTypedef(x) != nil
	}
	return true
}

// unsafePtrTd reports whether td resolves to unsafe.Pointer — the one
// type Go converts to and from every pointer type. A declared type
// spelled over it (`type P unsafe.Pointer`) resolves through the
// named-underlying chain to the same bound typedef.
func (v *VM) unsafePtrTd(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Name == "unsafe.Pointer" && td.Kind == runtime.KindPointer {
		return true
	}
	if u := v.peelNamed(td); u != nil && u != td {
		return v.unsafePtrTd(u)
	}
	return false
}

// unsafePtrLike reports whether x can serve as an unsafe.Pointer
// payload — anything a pointer can point at or be: a script ref, a
// boxed host pointer, or an engine-internal handle (which IS a
// pointer). Everything else (ints, strings, funcs, slices) cannot
// convert to unsafe.Pointer, like gc's conversion rule.
func (v *VM) unsafePtrLike(x runtime.Value) bool {
	if _, ok := runtime.Deref(x); ok {
		return true
	}
	return hostPtrBox(x) || v.internalTypedef(x) != nil
}

// hostPtrBox reports whether x is a GoValue wrapping a host pointer —
// bound functions hand host *T values to scripts boxed like this.
func hostPtrBox(x runtime.Value) bool {
	gv, ok := x.(*runtime.GoValue)
	if !ok {
		return false
	}
	rv := hostbridge.RVOf(gv.V)
	return rv.IsValid() && (rv.Kind() == hostbridge.KindPtr || rv.Kind() == hostbridge.KindUnsafePointer)
}

// unsafePtrAddr mints a uintptr for an unsafe.Pointer payload — the
// same address %p would print. A ref mints its storage slot's address
// (the cell, the backing-array element, the struct field) so the same
// location mints the same value across evaluations, and a realloc moves
// it like gc's; a host box mints the host pointer itself.
func (v *VM) unsafePtrAddr(x runtime.Value, box *runtime.Named) (uint64, bool) {
	x = anchorMintRef(runtime.Unwrap(x))
	if addr, ok := v.unsafePtrAddrOf(x); ok {
		v.mintUnsafePtr(addr, x, box)
		return addr, true
	}
	return 0, false
}

// anchorMintRef resolves a ref to the storage its address names, so a
// revived pointer keeps addressing the same slot after the path it was
// minted through is rebound — an embedded pointer swapped, a slice or
// pointer variable reassigned. Values that already name stable storage
// (cells, host boxes, address payloads) pass through.
func anchorMintRef(x runtime.Value) runtime.Value {
	switch r := x.(type) {
	case *runtime.FieldRef:
		// A field whose storage the base variable owns — `&s.N`, or
		// `&s.E.N` through an inline embed — is overwritten in place on
		// a whole-struct rebind, so the mint keeps the dynamic ref:
		// anchoring it would freeze the slot the variable no longer
		// names. Only an indirection — a pointer-typed base (`&p.N` on
		// a *S), a slice/map element base (`&sl[i].N`), or a pointer
		// hop on the promotion path (`&s.EP.N` through *E) — names heap
		// storage the variable does not own; there a store like
		// `s.EP = &E{}` must not retarget, so anchor on the resolved
		// owning struct.
		if !fieldRefIndirect(r) {
			return x
		}
		if st, idx, ok := r.Find(); ok {
			name := r.Name
			if st.Def != nil && idx < len(st.Def.Fields) {
				name = st.Def.Fields[idx]
			}
			return &runtime.FieldRef{Base: st, Name: name, Def: r.Def, Path: []int{idx}}
		}
	case *runtime.IndexRef:
		// the element's slot lives in the backing array — anchor on the
		// resolved slice, not the variable that held it.
		if sl := r.Slice(); sl != nil {
			return &runtime.IndexRef{Base: sl, Key: r.Key}
		}
		if m := r.Map(); m != nil {
			return &runtime.IndexRef{Base: m, Key: r.Key}
		}
	case *runtime.DerefRef:
		// `&*p` is the pointer value p held at mint — pin that value,
		// not the cell re-dereferenced on every access. It may itself
		// be a ref, which anchors the same way.
		if pv, ok := runtime.Deref(r.Ptr); ok {
			return anchorMintRef(pv)
		}
	}
	return x
}

// fieldRefIndirect reports whether resolving r's field reads through
// storage its base variable does not own — a pointer-typed base, a
// slice/map element base, or a pointer step on the promotion path.
// Peeling a Named or reading a Cell is still the variable's own
// storage; any other ref read crosses an indirection.
func fieldRefIndirect(r *runtime.FieldRef) bool {
	v := r.Base
	var s *runtime.Struct
	for i := 0; i < 8; i++ {
		switch x := v.(type) {
		case *runtime.Named:
			v = x.V
		case *runtime.Cell:
			v = x.Elem
		case *runtime.Struct:
			s = x
		default:
			// DerefRef/IndexRef/FieldRef — the base already reads
			// through another ref's storage.
			return true
		}
		if s != nil {
			break
		}
	}
	if s == nil {
		return true
	}
	if len(r.Path) > 1 {
		// an explicit index path crosses only where an intermediate
		// field holds a pointer; inline embeds keep s's own storage.
		for _, ix := range r.Path[:len(r.Path)-1] {
			if ix < 0 || ix >= len(s.Fields) {
				break
			}
			if inlineStruct(s.Fields[ix]) == nil {
				return true
			}
			s = runtime.StructOf(s.Fields[ix])
			if s == nil {
				break
			}
		}
		return false
	}
	// name-resolved: a direct hit on s is inline; a promoted hit is
	// inline only when the embedded chain stays in s's storage.
	st, _, ok := r.Find()
	if !ok {
		return false
	}
	return !inlineReaches(s, st, 0)
}

// inlineStruct peels Named wrappers only — a Struct reached through a
// ref is a separate heap object, not the owner's inline storage.
func inlineStruct(v runtime.Value) *runtime.Struct {
	for {
		switch x := v.(type) {
		case *runtime.Struct:
			return x
		case *runtime.Named:
			v = x.V
		default:
			return nil
		}
	}
}

// inlineReaches reports whether target is reachable from s through
// embedded fields that live inside s's own storage (no pointer hop).
func inlineReaches(s, target *runtime.Struct, depth int) bool {
	if s == target {
		return true
	}
	if s == nil || s.Def == nil || depth >= 8 {
		return false
	}
	for _, i := range s.Def.EmbedIdx {
		if i < 0 || i >= len(s.Fields) {
			continue
		}
		if emb := inlineStruct(s.Fields[i]); emb != nil && inlineReaches(emb, target, depth+1) {
			return true
		}
	}
	return false
}

// mintEnt is one recorded uintptr referent. The referent resolves
// through a weak handle on the storage it reads — a collected referent
// can never legally be revived, so the entry dies with it. While a
// minted unsafe.Pointer box lives, the matching pin roots that storage
// for it — in gc the unsafe.Pointer value itself roots the object — so
// `u := unsafe.Pointer(x)` keeps the referent revivable even when x
// went out of reach. A box that collects drops its pin: a mint through
// a temporary box then survives only while the referent itself stays
// reachable. An entry whose pin list overflowed keeps a permanent
// root (pinned) and rides the table's FIFO bound, as does a referent
// that offers no weak storage (an integer address).
type mintEnt struct {
	refFn  func() runtime.Value
	liveFn func() bool
	// pin is the storage the referent reads through (a variable's
	// cell, a field's owning struct, an index's container); every
	// attached box roots it while it lives. A box's pin can never be
	// resolved from the box — the uintptr payload is plain bits — so
	// the entry keeps it from mint time rather than resolving the
	// referent again, which would read script storage an unsafe.Pointer
	// conversion has no business touching.
	pin    runtime.Value
	boxes  []weak.Pointer[runtime.Named]
	pins   []runtime.Value
	pinned runtime.Value
}

// mintBoxCap bounds the box list per address; past it the entry is
// pinned for the FIFO to evict rather than lose a live root.
const mintBoxCap = 16

// Dead reports whether the referent is unreachable: no permanent pin,
// no live box, and the weak probe finds the storage gone. The probe is
// separate from refFn so the FIFO sweep pays no allocation per entry.
func (e *mintEnt) Dead() bool {
	if e.pinned != nil || e.liveBoxes() > 0 {
		return false
	}
	return e.liveFn == nil || !e.liveFn()
}

// cellBaseOf unwraps the Named layers of a ref base to the cell it
// names — the variable's own storage — or reports none.
func cellBaseOf(v runtime.Value) *runtime.Cell {
	for {
		switch x := v.(type) {
		case *runtime.Named:
			v = x.V
		case *runtime.Cell:
			return x
		default:
			return nil
		}
	}
}

// mintWeakRefOf builds the entry's referent resolver over a weak
// handle on the storage the referent reads through — a variable's
// cell, an anchored field's owning struct, an index's backing
// container, the host box itself. The second return probes that
// handle without building the ref (the FIFO sweep's liveness check);
// the third is the storage to pin while a minted box lives — nil when
// the referent is a plain address with no storage (the resolver stays
// constant and the probe reports live forever).
func mintWeakRefOf(x runtime.Value) (func() runtime.Value, func() bool, runtime.Value) {
	switch r := x.(type) {
	case *runtime.Cell:
		// the closures must not capture r — holding the referent
		// strongly would make the weak handle meaningless.
		w := weak.Make(r)
		return func() runtime.Value {
			if c := w.Value(); c != nil {
				return c
			}
			return nil
		}, func() bool { return w.Value() != nil }, r
	case *runtime.GoValue:
		w := weak.Make(r)
		return func() runtime.Value {
			if g := w.Value(); g != nil {
				return g
			}
			return nil
		}, func() bool { return w.Value() != nil }, r
	case *runtime.FieldRef:
		// a variable-based ref keeps its dynamic base — the rebuilt ref
		// follows a whole-struct rebind the way the original does. Only
		// the name/def/path may be captured — r and c pin the storage.
		if c := cellBaseOf(r.Base); c != nil {
			w := weak.Make(c)
			name, def, path := r.Name, r.Def, r.Path
			return func() runtime.Value {
				cw := w.Value()
				if cw == nil {
					return nil
				}
				return &runtime.FieldRef{Base: cw, Name: name, Def: def, Path: path}
			}, func() bool { return w.Value() != nil }, c
		}
		if st, idx, ok := r.Find(); ok {
			name := r.Name
			if st.Def != nil && idx < len(st.Def.Fields) {
				name = st.Def.Fields[idx]
			}
			w := weak.Make(st)
			def := r.Def
			return func() runtime.Value {
				s := w.Value()
				if s == nil {
					return nil
				}
				return &runtime.FieldRef{Base: s, Name: name, Def: def, Path: []int{idx}}
			}, func() bool { return w.Value() != nil }, st
		}
	case *runtime.IndexRef:
		if sl := r.Slice(); sl != nil {
			w := weak.Make(sl)
			key := r.Key
			return func() runtime.Value {
				s := w.Value()
				if s == nil {
					return nil
				}
				return &runtime.IndexRef{Base: s, Key: key}
			}, func() bool { return w.Value() != nil }, sl
		}
		if m := r.Map(); m != nil {
			w := weak.Make(m)
			key := r.Key
			return func() runtime.Value {
				s := w.Value()
				if s == nil {
					return nil
				}
				return &runtime.IndexRef{Base: s, Key: key}
			}, func() bool { return w.Value() != nil }, m
		}
	case *runtime.DerefRef:
		if pv, ok := runtime.Deref(r.Ptr); ok {
			return mintWeakRefOf(pv)
		}
	}
	return func() runtime.Value { return x }, func() bool { return true }, nil
}

func (e *mintEnt) liveBoxes() int {
	n := 0
	for _, b := range e.boxes {
		if b.Value() != nil {
			n++
		}
	}
	return n
}

// attach records another minted box for the address — and the
// referent storage it pins while it lives — reaping collected boxes
// as it goes.
func (e *mintEnt) attach(box *runtime.Named, pin runtime.Value) {
	bs := e.boxes[:0]
	ps := e.pins[:0]
	for i, b := range e.boxes {
		if b.Value() != nil {
			bs = append(bs, b)
			ps = append(ps, e.pins[i])
		}
	}
	e.boxes, e.pins = bs, ps
	if len(e.boxes) < mintBoxCap {
		e.boxes = append(e.boxes, weak.Make(box))
		e.pins = append(e.pins, pin)
		return
	}
	// over the box cap: keep a permanent root so the entry pins the
	// referent for the FIFO bound rather than losing a live root.
	e.boxes = e.boxes[:0]
	e.pins = e.pins[:0]
	e.pinned = pin
}

// mintUnsafePtr records the referent behind a minted address; box is
// the unsafe.Pointer value being converted to uintptr, when there is
// one. The table lives on the process so a pointer minted on one
// goroutine revives on a sibling (goroutine args); a VM outside any
// process — setup work before the first Call — falls back to its own
// table.
func (v *VM) mintUnsafePtr(addr uint64, ref runtime.Value, box *runtime.Named) {
	make_ := func() any {
		refFn, liveFn, pin := mintWeakRefOf(ref)
		ent := &mintEnt{refFn: refFn, liveFn: liveFn, pin: pin}
		if box != nil {
			ent.boxes = append(ent.boxes, weak.Make(box))
			ent.pins = append(ent.pins, pin)
		}
		return ent
	}
	// a second mint of a live address merges: overwriting the entry
	// would drop the earlier boxes' liveness records, so a still-held
	// pointer could lose its referent when a temporary box collects.
	merge := func(e any) {
		if ent, ok := e.(*mintEnt); ok {
			ent.refFn, ent.liveFn, ent.pin = mintWeakRefOf(ref)
			if box != nil {
				ent.attach(box, ent.pin)
			}
		}
	}
	if p := v.proc.Load(); p != nil {
		p.UpsertUintptr(addr, make_, merge)
		return
	}
	if v.unsafePtrMint == nil {
		v.unsafePtrMint = map[uint64]*mintEnt{}
	}
	if old, ok := v.unsafePtrMint[addr]; ok {
		if old.Dead() {
			delete(v.unsafePtrMint, addr)
		} else {
			merge(old)
			return
		}
	}
	v.unsafePtrMint[addr] = make_().(*mintEnt)
}

// mintEntry returns the live entry for addr, or nil.
func (v *VM) mintEntry(addr uint64) *mintEnt {
	var ref any
	var ok bool
	if p := v.proc.Load(); p != nil {
		ref, ok = p.ReviveUintptr(addr, func(e any) (any, bool) { return e, true })
	} else {
		ref, ok = v.unsafePtrMint[addr]
		if ok {
			if ent, isEnt := ref.(*mintEnt); isEnt && ent.Dead() {
				delete(v.unsafePtrMint, addr)
				return nil
			}
		}
	}
	if !ok {
		return nil
	}
	ent, _ := ref.(*mintEnt)
	return ent
}

// reviveUnsafePtr reports the referent a mint recorded for addr.
func (v *VM) reviveUnsafePtr(addr uint64) (runtime.Value, bool) {
	if p := v.proc.Load(); p != nil {
		// the referent read runs under the table lock — merge may be
		// rewriting ent.refFn on another goroutine.
		return p.ReviveUintptr(addr, func(e any) (any, bool) {
			if ent, ok := e.(*mintEnt); ok && ent.refFn != nil {
				if ref := ent.refFn(); ref != nil {
					return ref, true
				}
			}
			return nil, false
		})
	}
	if ent := v.mintEntry(addr); ent != nil && ent.refFn != nil {
		if ref := ent.refFn(); ref != nil {
			return ref, true
		}
	}
	return nil, false
}

// unsafePtrRoot links a minted unsafe.Pointer box to its entry, so the
// referent stays pinned for as long as the script can still hold the
// pointer — like gc rooting the object through the unsafe.Pointer
// value itself.
func (v *VM) unsafePtrRoot(box *runtime.Named) {
	addr, ok := v.unsafePtrAddrOf(anchorMintRef(runtime.Unwrap(box)))
	if !ok {
		return
	}
	// the new box roots the referent's storage, not its own payload —
	// the entry kept the pin it minted with, so no referent read is
	// needed (or wanted: it would race a concurrent whole-struct
	// write) to land it.
	if p := v.proc.Load(); p != nil {
		p.AttachUintptr(addr, func(e any) {
			if ent, isEnt := e.(*mintEnt); isEnt {
				ent.attach(box, ent.pin)
			}
		})
		return
	}
	if ent := v.unsafePtrMint[addr]; ent != nil {
		if ent.Dead() {
			delete(v.unsafePtrMint, addr)
		} else {
			ent.attach(box, ent.pin)
		}
	}
}

// unsafePtrAddrOf computes the minted address itself — see unsafePtrAddr
// for what each payload kind mints.
func (v *VM) unsafePtrAddrOf(x runtime.Value) (uint64, bool) {
	// an integer payload is a uintptr-minted pointer: the int IS the
	// address, so converting back hands the same bits.
	switch n := x.(type) {
	case int64:
		return uint64(n), true
	case uint64:
		return n, true
	}
	if gv, ok := x.(*runtime.GoValue); ok {
		if rv := hostbridge.RVOf(gv.V); rv.IsValid() && (rv.Kind() == hostbridge.KindPtr || rv.Kind() == hostbridge.KindUnsafePointer) {
			return uint64(rv.Pointer()), true
		}
		return 0, false
	}
	// a nil unsafe.Pointer mints 0, like %p on a nil pointer.
	if tn, ok := asTypedNil(x); ok && v.unsafePtrTd(tn.Typ) {
		return 0, true
	}
	switch r := x.(type) {
	case *runtime.IndexRef:
		// &s[i]'s address is the backing-array slot's, not the ref
		// wrapper's — each `&s[i]` builds a fresh wrapper, so its own
		// Go address would mint a different value every evaluation.
		if sl := r.Slice(); sl != nil {
			if i, ok := runtime.Unwrap(r.Key).(int64); ok && i >= 0 && i < int64(len(sl.Elems)) {
				return uint64(hostbridge.RVOf(sl.Elems).Index(int(i)).Addr().Pointer()), true
			}
		}
		return 0, false
	case *runtime.FieldRef:
		// &s.f's address is the field's slot in the struct — stable for
		// the storage no matter how the ref was spelled or promoted.
		if st, idx, ok := r.Find(); ok {
			return uint64(hostbridge.RVOf(st.Fields).Index(idx).Addr().Pointer()), true
		}
		return 0, false
	case *runtime.DerefRef:
		// &*p is p — mint the pointer value stored at its location.
		if pv, ok := runtime.Deref(r.Ptr); ok {
			return v.unsafePtrAddrOf(pv)
		}
		return 0, false
	}
	if _, ok := runtime.Deref(x); ok {
		return uint64(hostbridge.RVOf(x).Pointer()), true
	}
	return 0, false
}

// typeNameOf spells x's dynamic type the way Go names it in conversion
// panics: pointer boxes count one leading star per hop (*runtime.Cell →
// *main.Point) and container/func/typed-nil leaves spell their declared
// typedef ([]int, map[string]int, *main.Point) instead of the runtime
// Go struct name.
func typeNameOf(x runtime.Value) string {
	depth := 0
	for {
		switch x.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			depth++
		case *runtime.DerefRef:
			// *p is a view of base's pointee — the pointer layer
			// belongs to the base's type, not the ref.
		default:
			goto base
		}
		dv, ok := runtime.Deref(x)
		if !ok {
			goto base
		}
		x = dv
	}
base:
	name := typeBaseName(x)
	for ; depth > 0; depth-- {
		name = "*" + name
	}
	return name
}

// typeBaseName spells a non-reference value's dynamic type.
func typeBaseName(x runtime.Value) string {
	switch xv := x.(type) {
	case *runtime.UConst:
		return xv.DefaultName()
	case *runtime.Named:
		if runtime.IfaceTaggedNil(xv.V) != nil {
			// a nil interface value wrapped in its declared tag still
			// has no dynamic type — Go spells it "nil" in the
			// "is %s, not %s" panic slot, not <nil>.
			return "nil"
		}
		return spelledTyp(xv.Typ)
	case *runtime.Struct:
		if xv.Def != nil {
			return spelledTyp(xv.Def)
		}
		return "struct"
	case *runtime.TypedNil:
		return spelledTyp(xv.Typ)
	case *runtime.IfaceNil:
		if runtime.IfaceTaggedNil(xv) != nil {
			// a nil interface value has no dynamic type — "nil",
			// not <nil>, in Go's panic text.
			return "nil"
		}
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "nil"
	case *runtime.Slice:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "slice"
	case *runtime.Map:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "map"
	case *runtime.Chan:
		if xv.Typ != nil {
			return spelledTyp(xv.Typ)
		}
		return "chan"
	case *runtime.Function:
		return funcTypeName(xv)
	case *runtime.Closure:
		return funcTypeName(xv.Fn)
	case *runtime.BoundMethod:
		return funcTypeName(xv.Fn)
	case *runtime.BuiltinFunc:
		if m := xv.Method; m != nil {
			return m.Type.String()
		}
		if xv.Target != nil {
			return fmt.Sprintf("%T", xv.Target)
		}
		return "func"
	case *runtime.GoValue:
		// a host box names its Go type — "*errors.errorString" in a
		// conversion panic, like the real runtime prints it.
		return fmt.Sprintf("%T", xv.V)
	case int64:
		// the bare int64 is Go's int; sized ints arrive as Named{int64}
		// and spell themselves through spelledTyp above.
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case runtime.Nil:
		return "nil"
	default:
		return fmt.Sprintf("%T", x)
	}
}

// funcTypeName renders a script function's declared signature for the
// panic text — Go spells `func(int)`, `func() error` and friends.
func funcTypeName(xv *runtime.Function) string {
	if s, ok := runtime.FuncGoSpelling(xv); ok {
		return s
	}
	return "func"
}
