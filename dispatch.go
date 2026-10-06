package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// ---- interface satisfaction, method sets, embedded dispatch ----
//
// Duck-typing: an interface typedef carries its required method names
// (declared + transitively embedded); a value satisfies it when its
// callable method names cover the requirement set. Method names on
// structs include promoted methods via embedded fields, computed lazily
// here because promotion needs type resolution the index doesn't do.

// methodsOfValue returns the method names callable on a dynamic value.
// Structs offer declared + promoted methods; host GoValues expose their
// reflect method set. Other values have no methods.
func (e *Engine) methodsOfValue(v runtime.Value) (map[string]bool, error) {
	set, _, err := e.methodSetOfValue(v)
	return set, err
}

// methodSetOfValue implements the Hooks.MethodSetOf hook: methodsOfValue
// plus an "unsure" flag — true when an embedded type failed to resolve
// (e.g. a vendored stdlib package), so the returned set may be missing
// promoted methods that the embed would have contributed.
func (e *Engine) methodSetOfValue(v runtime.Value) (map[string]bool, bool, error) {
	set, _, unsure, err := e.methodInfoOfValue(v)
	return set, unsure, err
}

// methodInfoOfValue is the single value walk behind the method-set
// hooks: it resolves a dynamic value to the typedef its methods come
// from (dereferencing pointers, honoring Named tags and host boxes) and
// reports the callable names, the signature-bearing member functions,
// and the unsure report in one pass — MethodSetOf and MethodFuncsOf
// each pick their view.
func (e *Engine) methodInfoOfValue(v runtime.Value) (map[string]bool, map[string]*runtime.Function, bool, error) {
	ptr := false
	for {
		// a Named value exposes its own declared method set — `type A B`
		// does not inherit B's methods (Go). Checked inside the deref
		// loop: a pointer like &c lands on a *Cell{Named} and must stop
		// on the tag rather than deref past it.
		if n, ok := v.(*runtime.Named); ok {
			if gv, ok := runtime.Unwrap(n.V).(*runtime.GoValue); ok {
				// a named type boxing a host value (type C128 complex128)
				// still exposes its declared methods — the reflect set
				// only fills in when the tag declares none (host box).
				// Host reflect methods carry no decl signature, so the
				// func view reports nil for the pure host box.
				if n.Typ != nil && len(n.Typ.Methods) > 0 {
					return e.typeMethodInfoU(n.Typ, ptr)
				}
				return hostMethodSet(gv.V), nil, false, nil
			}
			return e.typeMethodInfoU(n.Typ, ptr)
		}
		dv, ok := runtime.Deref(v)
		if !ok {
			break
		}
		// reached through a pointer: the pointee's method set includes
		// pointer receivers — `var _ Stringer = &c` sees c's (*C).String.
		v = dv
		ptr = true
	}
	switch x := v.(type) {
	case *runtime.Struct:
		names, funcs, unsure := e.methodWalkU(x.Def, ptr, map[*runtime.TypeDef]bool{})
		return names, funcs, unsure, nil
	case *runtime.TypedNil:
		return e.typeMethodInfoU(x.Typ, ptr)
	case *runtime.IfaceNil:
		return e.typeMethodInfoU(x.Typ, ptr)
	case *runtime.Slice:
		// a slice carrying a declared typedef (`type htmlSig []byte`)
		// exposes that type's methods — same for maps and channels.
		return e.typeMethodInfoU(x.Typ, ptr)
	case *runtime.Map:
		return e.typeMethodInfoU(x.Typ, ptr)
	case *runtime.Chan:
		return e.typeMethodInfoU(x.Typ, ptr)
	case *runtime.GoValue:
		// host values satisfy requirements by name alone — reflect
		// methods carry no declared signature for the func view.
		return hostMethodSet(x.V), nil, false, nil
	default:
		return nil, nil, false, nil
	}
}

// hostMethodSet reports a host value's reflect method names — the empty
// set when the box holds nil (a nil interface boxes no methods, and
// TypeOf would nil-deref).
func hostMethodSet(x any) map[string]bool {
	set := map[string]bool{}
	t := reflect.TypeOf(x)
	if t == nil {
		return set
	}
	for i := 0; i < t.NumMethod(); i++ {
		set[t.Method(i).Name] = true
	}
	return set
}

// typeMethods implements the Hooks.TypeMethods hook: the method set of a
// typedef (a typed nil still dispatches its declared methods, like Go).
func (e *Engine) typeMethods(td *runtime.TypeDef) (map[string]bool, error) {
	// a typedef queried on its own reports the VALUE method set — the
	// pointer method set asks through the *T typedef, which peels to ptr.
	set, _, err := e.typeMethodsU(td, false)
	return set, err
}

// typeMethodsU is typeMethods plus an "unsure" report: true when a
// pointer's pointee or an embedded type failed to resolve, so the set
// may be missing methods the unresolved type would have contributed.
// ptr reports whether the set is computed through a pointer — Go's
// method set for *T includes pointer receivers while T's does not.
func (e *Engine) typeMethodsU(td *runtime.TypeDef, ptr bool) (map[string]bool, bool, error) {
	set, _, unsure, err := e.typeMethodInfoU(td, ptr)
	return set, unsure, err
}

// typeMethodInfoU is the typedef-level dispatch behind typeMethodsU:
// one resolution (alias peel → anonymous-*T peel → interface and host
// checks → embedded walk) producing both views — the callable name set
// and the signature-bearing member functions.
func (e *Engine) typeMethodInfoU(td *runtime.TypeDef, ptr bool) (map[string]bool, map[string]*runtime.Function, bool, error) {
	// resolve aliases first — `type A = sync.Mutex` carries A's typedef
	// but its method set is the host type's reflect set; checking
	// HostNew on the unresolved alias would drop it.
	td = e.peelAliasTd(td)
	if td == nil {
		return nil, nil, false, nil
	}
	var unsure bool
	// an anonymous *T typedef sees T's method set including pointer
	// receivers; a declared pointer typedef (`type P *Sq`) keeps only
	// methods declared on P itself — Go forbids those outright, so in
	// valid programs the set is empty.
	if td.Kind == runtime.KindPointer && td.Spec == nil {
		if et, err := e.elemOf(td); err == nil && et != nil {
			td, ptr = et, true
		} else {
			unsure = true
		}
	}
	// the pointee may itself be an alias (`type A = sync.Mutex` in `*A`)
	// — peel again so the interface and host checks see the real type.
	td = e.peelAliasTd(td)
	if td == nil {
		return nil, nil, unsure, nil
	}
	names, funcs, subUnsure := e.methodWalkU(td, ptr, map[*runtime.TypeDef]bool{})
	unsure = unsure || subUnsure
	if td.HostNew != nil {
		// a host-backed typedef's name set is the boxed host type's
		// reflect set — td.Methods is empty by construction, so
		// `var l sync.Locker = &sync.Mutex{}` and Type.Method both see
		// the real methods. The func view keeps the walk's result:
		// host reflect methods carry no declared signature.
		t := reflect.TypeOf(td.HostNew())
		set := map[string]bool{}
		for i := 0; i < t.NumMethod(); i++ {
			set[t.Method(i).Name] = true
		}
		return set, funcs, unsure, nil
	}
	return names, funcs, unsure, nil
}

// aliasOf implements the Hooks.AliasOf hook: a KindAlias typedef resolves
// its target expression — one hop only, so `type A = B` gives B's own
// typedef even when B is itself a declared type (unlike underlying).
func (e *Engine) aliasOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if td == nil || td.Kind != runtime.KindAlias || td.Anon == nil {
		return td, nil
	}
	return e.resolveTypeRef(td, td.Anon)
}

// underlying implements the Hooks.Underlying hook: a KindAlias typedef
// resolves through its aliased expression to the real typedef.
func (e *Engine) underlying(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	// aliases and named basics both peel to their underlying typedef —
	// `type S string` bottoms out at the builtin "string" typedef so a
	// zero value picks the right literal kind.
	for td != nil && (td.Kind == runtime.KindAlias || td.Kind == runtime.KindNamedBasic) && td.Anon != nil {
		next, err := e.resolveTypeRef(td, td.Anon)
		if err != nil || next == nil || next == td {
			return nil, err
		}
		td = next
	}
	return td, nil
}

// peelAliasTd follows a typedef's alias links — `type A = B` IS B, so
// method sets, interface requirements and signatures all read through
// the alias to the aliased declaration. Named basics stay put: `type A B`
// declares a distinct method set, not B's.
func (e *Engine) peelAliasTd(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		next, err := e.aliasOf(td)
		if err != nil || next == nil || next == td {
			break
		}
		td = next
	}
	return td
}

// methodWalkU is the single traversal behind the method-set views (the
// callable-name set) and methodFuncs (the member-function map): one
// walk applies alias peeling, the anonymous-*T elem peel, Go's receiver
// rule, the per-path cycle guard and unsure tracking so the two views
// cannot drift apart (they once disagreed on embedded interface facades
// that carry MReqs without an AST). Generic methods (Go 1.27) are
// excluded — they never satisfy interfaces. Interface satisfaction
// treats an unsure set as optimistic: a missing requirement may live on
// an unresolved embed. An interface typedef contributes its requirement
// names to the name set — including AST-less facades — while the func
// map only gains members backed by a declared signature.
func (e *Engine) methodWalkU(td *runtime.TypeDef, ptr bool, seen map[*runtime.TypeDef]bool) (names map[string]bool, funcs map[string]*runtime.Function, unsure bool) {
	td = e.peelAliasTd(td)
	if td == nil || seen[td] {
		return nil, nil, false
	}
	// seen guards cycles along a path only — a sibling embed path that
	// reaches the same type under a different ptr condition still has
	// methods to contribute (`struct{ A; *T }` where A embeds T:
	// visiting A.T as a value must not hide the *T embed's pointer
	// receivers), so the mark lifts when the path unwinds like
	// methodInner's.
	seen[td] = true
	defer delete(seen, td)
	// an anonymous *T typedef sees T's method set including pointer
	// receivers; a declared pointer typedef keeps only its own decls.
	if td.Kind == runtime.KindPointer && td.Spec == nil {
		if et, err := e.elemOf(td); err == nil && et != nil {
			td, ptr = et, true
		}
	}
	if td.Kind == runtime.KindInterface {
		names = e.ifaceReqsRec(td, map[*runtime.TypeDef]bool{})
		funcs = e.ifaceSigFuncs(td)
		return names, funcs, false
	}
	names = map[string]bool{}
	funcs = map[string]*runtime.Function{}
	for name, m := range td.Methods {
		if m == nil || len(m.TParams) > 0 {
			continue // a generic method contributes no interface method
		}
		if m.PtrRecv && !ptr {
			continue // pointer receivers live only on *T's method set
		}
		names[name] = true
		funcs[name] = m
	}
	for _, spec := range td.EmbedSpecs {
		emb, err := e.resolveTypeRef(td, spec)
		if err != nil || emb == nil {
			unsure = true
			continue
		}
		if emb.Kind == runtime.KindInterface {
			// an embedded interface field satisfies its own requirements:
			// the name set covers every required name (facade typedefs
			// without AST still count) while the func map keeps only the
			// members a signature can be synthesized for.
			for m := range e.ifaceReqsRec(emb, map[*runtime.TypeDef]bool{}) {
				names[m] = true
			}
			for name, fn := range e.ifaceSigFuncs(emb) {
				if _, dup := funcs[name]; !dup {
					funcs[name] = fn
				}
			}
			continue
		}
		// methods of an embedded pointer field promote with their
		// receiver kind intact; an embedded value field promotes only
		// its value receivers (plus all receivers under a *S parent).
		embPtr := ptr
		if _, isStar := spec.(*ast.StarExpr); isStar || emb.Kind == runtime.KindPointer {
			embPtr = true
		}
		subNames, subFuncs, subUnsure := e.methodWalkU(emb, embPtr, seen)
		unsure = unsure || subUnsure
		for m := range subNames {
			names[m] = true
		}
		for name, m := range subFuncs {
			if _, dup := funcs[name]; !dup {
				funcs[name] = m
			}
		}
	}
	return names, funcs, unsure
}

// ifaceSigFunc synthesizes the member Function an interface typedef's
// required method resolves to — the declared signature plus the typedef
// context (package, file imports, binds) that spells it.
func ifaceSigFunc(sig ifaceSig) *runtime.Function {
	return &runtime.Function{
		Decl: &ast.FuncDecl{Type: sig.decl},
		Pkg:  sig.ctx.Pkg, File: sig.ctx.File, Binds: sig.ctx.Binds,
	}
}

// ifaceSigFuncs maps an interface typedef's signature-bearing required
// methods to their synthesized member Functions — nil when it declares
// no signature (name-only requirements). The single conversion behind
// methodWalkU's interface arms and the IfaceSigs hook. The result is
// cached on td — every interface assertion and conversion consults it —
// and its stable shells let the VM memoize signature comparisons per
// (requirement, method) pair. Callers must not modify the map.
func (e *Engine) ifaceSigFuncs(td *runtime.TypeDef) map[string]*runtime.Function {
	if td == nil {
		return nil
	}
	if out, ok := td.CachedIfaceSigs(); ok {
		return out
	}
	sigs := e.ifaceSigsOf(td, map[*runtime.TypeDef]bool{})
	var out map[string]*runtime.Function
	if len(sigs) > 0 {
		out = make(map[string]*runtime.Function, len(sigs))
		for name, sig := range sigs {
			out[name] = ifaceSigFunc(sig)
		}
	}
	td.SetIfaceSigs(out)
	return out
}

// methodSet implements the minireflect MethodSet hook and the
// Hooks.TypeMethodFuncs hook: the signature-bearing method set of a
// typedef.
func (e *Engine) methodSet(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	return e.methodFuncs(td, false, map[*runtime.TypeDef]bool{}), nil
}

// methodFuncs collects the method FUNCTIONS of a typedef — declared plus
// promoted — honoring Go's receiver rule: pointer-receiver methods join
// the set only when the type is reached through a pointer (an anonymous
// *T typedef or an embedded pointer field). Unexported members stay in
// the set; callers apply their own visibility rules (reflect exposes
// exported methods only). An interface typedef yields synthesized
// members carrying each required method's declared signature.
func (e *Engine) methodFuncs(td *runtime.TypeDef, ptr bool, seen map[*runtime.TypeDef]bool) map[string]*runtime.Function {
	_, funcs, _ := e.methodWalkU(td, ptr, seen)
	return funcs
}

// ifaceSigReqs implements the Hooks.IfaceSigs hook: the interface's
// required methods that carry a declared signature, as Function shells
// spelling that signature in their declaring typedef's context.
func (e *Engine) ifaceSigReqs(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	return e.ifaceSigFuncs(td), nil
}

// methodFuncsOfValue implements the Hooks.MethodFuncsOf hook: the
// signature-bearing twin of methodSetOfValue — declared and promoted
// script methods plus synthesized interface members. Host reflect
// methods carry no decl signature, so host boxes report nil here and
// satisfy requirements by name alone.
func (e *Engine) methodFuncsOfValue(v runtime.Value) (map[string]*runtime.Function, error) {
	_, funcs, _, err := e.methodInfoOfValue(v)
	return funcs, err
}

// ifaceSig is an interface's required method together with the typedef
// context that declared it — its package, file imports and binds spell
// the signature.
type ifaceSig struct {
	decl *ast.FuncType
	ctx  *runtime.TypeDef
}

// ifaceSigsOf maps an interface's required methods to their declared
// signatures — a name-only set can't distinguish F(int) from F(string),
// which interface satisfaction via reflect needs.
func (e *Engine) ifaceSigsOf(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool) map[string]ifaceSig {
	td = e.peelAliasTd(td)
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	out := map[string]ifaceSig{}
	var it *ast.InterfaceType
	for _, x := range []ast.Expr{td.Anon, specType(td)} {
		if s, ok := x.(*ast.InterfaceType); ok {
			it = s
			break
		}
	}
	if it != nil {
		for _, m := range it.Methods.List {
			if len(m.Names) == 0 {
				continue
			}
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			for _, n := range m.Names {
				out[n.Name] = ifaceSig{decl: ft, ctx: td}
			}
		}
	}
	for _, spec := range td.IEmbeds {
		emb, err := e.resolveTypeRef(td, spec)
		if err != nil || emb == nil {
			continue
		}
		for name, sig := range e.ifaceSigsOf(emb, seen) {
			if _, dup := out[name]; !dup {
				out[name] = sig
			}
		}
	}
	return out
}

// ifaceReqs returns the required method set of an interface typedef:
// declared methods union the requirements of embedded interface elements.
// Constraint elements (~T, unions) are approximated away — satisfaction
// checks treat them as fulfilled.
func (e *Engine) ifaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	return e.ifaceReqsRec(td, map[*runtime.TypeDef]bool{}), nil
}

func (e *Engine) ifaceReqsRec(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool) map[string]bool {
	td = e.peelAliasTd(td)
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	set := map[string]bool{}
	for _, m := range td.MReqs {
		set[m] = true
	}
	for _, spec := range td.IEmbeds {
		sub, err := e.resolveTypeRef(td, spec)
		if err != nil || sub == nil {
			continue // constraint exprs (~T, |) don't resolve to typedefs
		}
		for m := range e.ifaceReqsRec(sub, seen) {
			set[m] = true
		}
	}
	return set
}

// fieldTypes implements the Hooks.FieldTypes hook: the declared type of
// each field, parallel to td.Fields, resolved from the struct's field
// ASTs (embedded fields count once, like td.Fields itself). Unresolvable
// types yield nil entries; generic binds resolve `T`-style names first.
// The result is cached on td: every field store consults it, and
// resolving each field's type from AST again dominated allocation in
// field-heavy scripts (go/parser over a large package).
func (e *Engine) fieldTypes(td *runtime.TypeDef) ([]*runtime.TypeDef, error) {
	if fts, ok := td.CachedFieldTypes(); ok {
		return fts, nil
	}
	fts := e.resolveFieldTypes(td)
	td.SetFieldTypes(fts)
	return fts, nil
}

func (e *Engine) resolveFieldTypes(td *runtime.TypeDef) []*runtime.TypeDef {
	var st *ast.StructType
	for _, x := range []ast.Expr{td.Anon, specType(td)} {
		if s, ok := x.(*ast.StructType); ok {
			st = s
			break
		}
	}
	if st == nil {
		return nil
	}
	out := make([]*runtime.TypeDef, len(td.Fields))
	i := 0
	for _, fld := range st.Fields.List {
		n := len(fld.Names)
		if n == 0 {
			n = 1 // embedded field occupies one slot
		}
		for k := 0; k < n && i < len(out); k++ {
			if id, ok := fld.Type.(*ast.Ident); ok && td.Binds != nil {
				if bv, ok := td.Binds[id.Name]; ok {
					if btd, ok := bv.(*runtime.TypeDef); ok {
						out[i] = btd
						i++
						continue
					}
				}
			}
			ft, err := e.elemTypeRef(td, fld.Type)
			if err == nil {
				out[i] = ft
			} else {
				// the declared type did not resolve (missing import, unbound
				// name): keep a hole typedef instead of nil so the field zero
				// still produces a typed nil, not a bare NIL.
				out[i] = &runtime.TypeDef{
					Kind: runtime.KindNamedBasic,
					Anon: fld.Type,
					Pkg:  td.Pkg,
					File: td.File,
				}
			}
			i++
		}
	}
	return out
}

func specType(td *runtime.TypeDef) ast.Expr {
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// resolveTypeRef resolves a type expression embedded in a decl of typedef
// `from` to a *TypeDef: package-local names via the index, pkg.Name via
// import refs, *T / T[...] peel to the base. Anything else fails —
// constraints and underlying-only types don't participate in method sets.
func (e *Engine) resolveTypeRef(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	switch t := x.(type) {
	case *ast.StarExpr:
		return e.resolveTypeRef(from, t.X)
	case *ast.ParenExpr:
		return e.resolveTypeRef(from, t.X)
	case *ast.IndexExpr:
		base, err := e.resolveTypeRef(from, t.X)
		if err != nil {
			return nil, err
		}
		return e.instantiateRef(from, base, []ast.Expr{t.Index}), nil
	case *ast.IndexListExpr:
		base, err := e.resolveTypeRef(from, t.X)
		if err != nil {
			return nil, err
		}
		return e.instantiateRef(from, base, t.Indices), nil
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
	case *ast.MapType:
		return &runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
	case *ast.ChanType:
		return &runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
	case *ast.StructType:
		td := &runtime.TypeDef{Kind: runtime.KindStruct, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}
		td.FTags = runtime.StructFieldTags(t)
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				td.EmbedSpecs = append(td.EmbedSpecs, f.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, embedBaseName(f.Type))
				continue
			}
			for _, n := range f.Names {
				td.Fields = append(td.Fields, n.Name)
			}
		}
		return td, nil
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Kind: runtime.KindInterface, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				td.IEmbeds = append(td.IEmbeds, m.Type)
				continue
			}
			for _, n := range m.Names {
				td.MReqs = append(td.MReqs, n.Name)
			}
		}
		return td, nil
	case *ast.FuncType:
		return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
	case *ast.Ident:
		// an ident may name a bound type parameter — the element of `[]T`
		// inside an instantiated `type Wrap[T any] []T` resolves to the
		// argument typedef carried on the typedef's Binds.
		if from.Binds != nil {
			if bv, ok := from.Binds[t.Name]; ok {
				if btd, ok := bv.(*runtime.TypeDef); ok {
					return btd, nil
				}
			}
		}
		// a type declared inside a function shadows package-level names —
		// embedded specs on local typedefs resolve through their decl-time
		// local-type snapshot (package indexes never see function scopes).
		if td, ok := from.LocalTypes[t.Name]; ok {
			return td, nil
		}
		if from.Pkg != nil && from.Pkg.Index != nil {
			if info, ok := from.Pkg.Index.Types[t.Name]; ok && info.Decl != nil {
				vv, err := e.materialize(from.Pkg, info.Decl)
				if err != nil {
					return nil, err
				}
				if td, ok := vv.(*runtime.TypeDef); ok {
					return td, nil
				}
				return nil, fmt.Errorf("%s is not a type", t.Name)
			}
		}
		if bv, ok := e.builtins.Get(t.Name); ok {
			if td, ok := bv.(*runtime.TypeDef); ok {
				return td, nil
			}
		}
		return nil, fmt.Errorf("cannot resolve type %s", t.Name)
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok || from.Pkg == nil {
			return nil, fmt.Errorf("cannot resolve embedded type")
		}
		var scope map[string]*runtime.ImportRef
		if from.File != nil {
			scope = from.Pkg.Scopes[from.File]
		}
		ref, ok := scope[id.Name]
		if !ok {
			// minireflect's exprOf qualifies a named typedef by package
			// PATH (e.g. <dir>/prog/x.T, reflect.Value) — the selector's
			// qualifier is a path, not a file-scope import alias. The
			// local package resolves through its own index; anything
			// else loads by path.
			if from.Pkg.Path == id.Name {
				return e.resolveTypeRef(from, ast.NewIdent(t.Sel.Name))
			}
			ref = &runtime.ImportRef{
				Path: id.Name,
				Load: func(path string) (*runtime.Package, error) {
					return e.loadPath(context.Background(), path)
				},
			}
		}
		p, err := ref.Materialize()
		if err != nil {
			return nil, err
		}
		m, err := p.Member(t.Sel.Name, e.materialize)
		if err != nil {
			return nil, err
		}
		td, ok := m.(*runtime.TypeDef)
		if !ok {
			return nil, fmt.Errorf("%s.%s is not a type", id.Name, t.Sel.Name)
		}
		return td, nil
	}
	return nil, fmt.Errorf("unsupported embedded type expression %T", x)
}

// elemOf implements the Hooks.ElemOf hook: the element typedef of a
// container typedef — or the pointee typedef of a pointer typedef —
// resolved from its underlying type AST (Anon or Spec.Type). Used by
// elided composite literal elements and pointer member dispatch.
func (e *Engine) elemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if td.Elem != nil {
		return td.Elem, nil
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	for {
		switch t := x.(type) {
		case *ast.ParenExpr:
			x = t.X
			continue
		case *ast.Ellipsis:
			x = t.Elt
			continue
		case *ast.StarExpr:
			// pointer typedef: element is the pointee typedef
			return e.resolveTypeRef(td, t.X)
		case *ast.ArrayType:
			return e.elemTypeRef(td, t.Elt)
		case *ast.MapType:
			return e.elemTypeRef(td, t.Value)
		case *ast.ChanType:
			return e.elemTypeRef(td, t.Value)
		}
		// bare underlying (e.g. `type T MyStruct`): the elem is that type
		return e.resolveTypeRef(td, x)
	}
}

// instantiateRef builds the specialized typedef for `base[args...]` like
// the VM's specializeType does for a runtime instantiation: the type
// parameters bind to the argument typedefs and every method is re-bound
// so its body sees the concrete arguments. Arguments that do not resolve
// (an unbound type parameter in a generic context) bind to a placeholder
// named typedef so `List[T]` inside a decl stays a stable shape.
func (e *Engine) instantiateRef(from *runtime.TypeDef, base *runtime.TypeDef, args []ast.Expr) *runtime.TypeDef {
	if base == nil || len(base.TParams) == 0 {
		return base
	}
	binds := map[string]runtime.Value{}
	for i, tp := range base.TParams {
		if i >= len(args) {
			break
		}
		// type args are full type expressions: a `*T` argument binds the
		// pointer typedef, not its pointee — `mapping[string, *routingNode]`
		// holds V=*routingNode (net/http's routing tree).
		at, err := e.elemTypeRef(from, args[i])
		if err != nil || at == nil {
			if id, ok := args[i].(*ast.Ident); ok {
				at = &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: id.Name, Pkg: from.Pkg, File: from.File}
			} else {
				continue
			}
		}
		binds[tp] = at
	}
	td := &runtime.TypeDef{
		Pkg: base.Pkg, Name: base.Name, File: base.File, Spec: base.Spec, Kind: base.Kind,
		Fields: base.Fields, FTags: base.FTags, Anon: base.Anon, TParams: base.TParams,
		TConstraints: base.TConstraints, Binds: binds,
		MReqs: base.MReqs, IEmbeds: base.IEmbeds,
		EmbedSpecs: base.EmbedSpecs, EmbedIdx: base.EmbedIdx, Embeds: base.Embeds,
		LocalTypes: base.LocalTypes, Elem: base.Elem, HostNew: base.HostNew,
		Local: base.Local,
	}
	if len(base.Methods) > 0 {
		td.Methods = make(map[string]*runtime.Function, len(base.Methods))
		for name, m := range base.Methods {
			mbinds := map[string]runtime.Value{}
			for k, bv := range m.Binds {
				mbinds[k] = bv
			}
			for tp, tv := range binds {
				mbinds[tp] = tv
			}
			// the receiver may rename the type's parameters — bind them too
			for i, rp := range recvParamNames(m.Decl) {
				if i < len(base.TParams) {
					mbinds[rp] = binds[base.TParams[i]]
				}
			}
			td.Methods[name] = &runtime.Function{
				Pkg: m.Pkg, File: m.File, Decl: m.Decl, Name: m.Name,
				Recv: m.Recv, PtrRecv: m.PtrRecv,
				TParams: m.TParams, TConstraints: m.TConstraints,
				Binds: mbinds, Compile: m.Compile,
			}
		}
	}
	return td
}

// recvParamNames extracts the type parameter names a method receiver
// declares — `func (l List[E, F])` yields [E, F]. Non-generic receivers
// yield nil.
func recvParamNames(d *ast.FuncDecl) []string {
	if d == nil || d.Recv == nil || len(d.Recv.List) == 0 {
		return nil
	}
	var idxs []ast.Expr
	switch t := d.Recv.List[0].Type.(type) {
	case *ast.IndexExpr:
		idxs = []ast.Expr{t.Index}
	case *ast.IndexListExpr:
		idxs = t.Indices
	}
	var out []string
	for _, x := range idxs {
		if id, ok := x.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
	}
	return out
}

// elemTypeRef resolves a container's element type expression: `*T`
// elements stay pointers (KindPointer), everything else resolves to its
// typedef. Named pointer types (`*P` where P is `type P *Sq`) resolve
// through resolveTypeRef to P's own typedef, which memberOfType peels.
func (e *Engine) elemTypeRef(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if st, ok := x.(*ast.StarExpr); ok {
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: st, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
	}
	return e.resolveTypeRef(from, x)
}

// ---- shared helpers ----

// typeParamNames extracts parameter names from a type parameter list.
func typeParamNames(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// typeParamConstraints extracts the constraint expression per type
// parameter name, parallel to typeParamNames.
func typeParamConstraints(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		for range f.Names {
			out = append(out, f.Type)
		}
	}
	return out
}

// embedBaseName derives the field name of an anonymous (embedded) struct
// field: the base type name, ignoring pointers, packages and type args.
func embedBaseName(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embedBaseName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embedBaseName(t.X)
	case *ast.IndexListExpr:
		return embedBaseName(t.X)
	}
	return ""
}
