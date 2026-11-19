package typedef

import (
	"context"

	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/podhmo/minigo/index"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Resolver carries the typedef-dispatch machinery — method sets,
// interface satisfaction, alias/underlying resolution and the deferred
// type-of query — extracted from the root Engine (P2-14). The root wires
// its few engine-level deps through New.
type Resolver struct {
	materialize   func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)
	internTypedef func(td *runtime.TypeDef) *runtime.TypeDef
	hostTwin      func(rt reflect.Type) *runtime.TypeDef
	builtins      *runtime.Env
	loadPath      func(ctx context.Context, path string) (*runtime.Package, error)
	iterSeqTds    sync.Map
}

// Deps are the engine operations the resolver reaches back for.
type Deps struct {
	Materialize   func(pkg *runtime.Package, d *index.Decl) (runtime.Value, error)
	InternTypedef func(td *runtime.TypeDef) *runtime.TypeDef
	HostTwin      func(rt reflect.Type) *runtime.TypeDef
	Builtins      *runtime.Env
	LoadPath      func(ctx context.Context, path string) (*runtime.Package, error)
}

func New(d Deps) *Resolver {
	return &Resolver{materialize: d.Materialize, internTypedef: d.InternTypedef,
		hostTwin: d.HostTwin, builtins: d.Builtins, loadPath: d.LoadPath}
}

// reqKey spells a method name in the requirement/method-set key space:
// exported names are package-free, but an unexported name belongs to
// its declaring package — `interface{ m() }` declared in pkgA is not
// satisfied by pkgB's `m`. The pkg-qualified key rides Names/Depths/
// Funcs alongside the plain name, which member selection keeps using.
func ReqKey(pkg *runtime.Package, name string) string {
	if pkg == nil || ast.IsExported(name) {
		return name
	}
	return pkg.Path + "." + name
}

// plainReqName recovers the method name a pkg-qualified requirement
// key encodes (pkg `a`'s `m` is keyed "a.m"); exported keys carry no
// package part and return unchanged. Method names never contain '.',
// so the last dot marks the join.
func PlainReqName(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

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
func (r *Resolver) MethodsOfValue(v runtime.Value) (map[string]bool, error) {
	ms, err := r.MethodInfoOfValue(v)
	if ms == nil {
		return nil, err
	}
	return ms.Names, err
}

// methodSetOfValue implements the Hooks.MethodSetOf hook: methodsOfValue
// plus an "unsure" flag — true when an embedded type failed to resolve
// (e.g. a vendored stdlib package), so the returned set may be missing
// promoted methods that the embed would have contributed.
func (r *Resolver) MethodSetOfValue(v runtime.Value) (map[string]bool, bool, error) {
	ms, err := r.MethodInfoOfValue(v)
	if ms == nil {
		return nil, false, err
	}
	return ms.Names, ms.Unsure, err
}

// methodInfoOfValue is the single value walk behind the method-set
// hooks: it resolves a dynamic value to the typedef its methods come
// from (dereferencing pointers, honoring Named tags and host boxes) and
// reports the callable names, the signature-bearing member functions,
// and the unsure report in one pass — MethodSetOf, MethodFuncsOf and
// MethodInfoOf each pick their view.
func (r *Resolver) MethodInfoOfValue(v runtime.Value) (*runtime.MethodSet, error) {
	ptr := false
	var seen map[runtime.Value]bool
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
					return r.TypeMethodInfoU(n.Typ, ptr)
				}
				return &runtime.MethodSet{Names: hostMethodSet(gv.V)}, nil
			}
			if n.Typ != nil && n.Typ.HostScalar != nil && len(n.Typ.Methods) == 0 {
				// a bound host scalar (time.Duration) exposes the raw
				// host value's reflect method set. A payload still in
				// script shape (time.Month's int64) materializes to the
				// host type first so String and friends show up.
				if hv, ok := runtime.HostScalarOf(n.Typ, n.V); ok {
					return &runtime.MethodSet{Names: hostMethodSet(hv)}, nil
				}
				return &runtime.MethodSet{Names: hostMethodSet(runtime.Unwrap(n.V))}, nil
			}
			if n.Typ != nil && n.Typ.HostNew != nil && n.Typ.Kind == runtime.KindNamedBasic && len(n.Typ.Methods) == 0 {
				// a bound host named basic keeps a script payload
				// (reflect.StructTag's string) — materialize it as
				// the host type to see its reflect method set.
				rt := reflect.TypeOf(n.Typ.HostNew())
				if rv := reflect.ValueOf(runtime.Unwrap(n.V)); rv.IsValid() && rv.Type().ConvertibleTo(rt) {
					return &runtime.MethodSet{Names: hostMethodSet(rv.Convert(rt).Interface())}, nil
				}
			}
			return r.TypeMethodInfoU(n.Typ, ptr)
		}
		// a cyclic pointer (x := any(nil); x = &x) never lands on a
		// method-bearing value — stop rather than spin. Only ref kinds
		// can repeat; other values are skipped (some are unhashable).
		switch v.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if seen == nil {
				seen = map[runtime.Value]bool{v: true}
			} else if seen[v] {
				v = nil
			} else {
				seen[v] = true
			}
		}
		if v == nil {
			break
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
		if ms := r.MethodWalkU(x.Def, ptr, map[walkKey]bool{}); ms != nil {
			return ms, nil
		}
		return &runtime.MethodSet{}, nil
	case *runtime.TypedNil:
		return r.TypeMethodInfoU(x.Typ, ptr)
	case *runtime.IfaceNil:
		return r.TypeMethodInfoU(x.Typ, ptr)
	case *runtime.Slice:
		// a slice carrying a declared typedef (`type htmlSig []byte`)
		// exposes that type's methods — same for maps and channels.
		return r.TypeMethodInfoU(x.Typ, ptr)
	case *runtime.Map:
		return r.TypeMethodInfoU(x.Typ, ptr)
	case *runtime.Chan:
		return r.TypeMethodInfoU(x.Typ, ptr)
	case time.Duration:
		// a raw duration exposes the host method set the same way the
		// GoValue box below does — selectMember special-cases it too.
		return &runtime.MethodSet{Names: hostMethodSet(x)}, nil
	case *runtime.GoValue:
		// host values satisfy requirements by name alone — reflect
		// methods carry no declared signature for the func view.
		return &runtime.MethodSet{Names: r.HostTwinMethodSet(x.V)}, nil
	default:
		return nil, nil
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

// hostTwinMethodSet is hostMethodSet plus the unexported methods the
// source twin of x's type declares ([Engine.hostTwin]): reflect never
// lists unexported methods, so a host *ast.BinaryExpr would miss
// exprNode() and fail go/ast's own Expr when go/ast is interpreted from
// source. Both sides come from the same source, so the twin's set is the
// host type's.
func (r *Resolver) HostTwinMethodSet(x any) map[string]bool {
	set := hostMethodSet(x)
	rt := reflect.TypeOf(x)
	if rt == nil {
		return set
	}
	ptr := false
	if rt.Kind() == reflect.Pointer && rt.Name() == "" {
		rt, ptr = rt.Elem(), true
	}
	twin := r.hostTwin(rt)
	if twin == nil || twin.Pkg == nil || twin.Pkg.Index == nil {
		return set
	}
	names, _, err := r.TypeMethodsU(twin, ptr)
	if err != nil {
		return set
	}
	for m := range names {
		if !token.IsExported(m) {
			set[m] = true
		}
	}
	return set
}

// typeMethods implements the Hooks.TypeMethods hook: the method set of a
// typedef (a typed nil still dispatches its declared methods, like Go).
func (r *Resolver) TypeMethods(td *runtime.TypeDef) (map[string]bool, error) {
	// a typedef queried on its own reports the VALUE method set — the
	// pointer method set asks through the *T typedef, which peels to ptr.
	set, _, err := r.TypeMethodsU(td, false)
	return set, err
}

// typeMethodsU is typeMethods plus an "unsure" report: true when a
// pointer's pointee or an embedded type failed to resolve, so the set
// may be missing methods the unresolved type would have contributed.
// ptr reports whether the set is computed through a pointer — Go's
// method set for *T includes pointer receivers while T's does not.
func (r *Resolver) TypeMethodsU(td *runtime.TypeDef, ptr bool) (map[string]bool, bool, error) {
	ms, err := r.TypeMethodInfoU(td, ptr)
	if ms == nil {
		return nil, false, err
	}
	return ms.Names, ms.Unsure, err
}

// typeMethodInfoU is the typedef-level dispatch behind typeMethodsU:
// one resolution (alias peel → anonymous-*T peel → interface and host
// checks → embedded walk) producing both views — the callable name set
// and the signature-bearing member functions.
func (r *Resolver) TypeMethodInfoU(td *runtime.TypeDef, ptr bool) (*runtime.MethodSet, error) {
	// resolve aliases first — `type A = sync.Mutex` carries A's typedef
	// but its method set is the host type's reflect set; checking
	// HostNew on the unresolved alias would drop it.
	td = r.PeelAliasTd(td)
	if td == nil {
		return nil, nil
	}
	if rt := hostbridge.HostNilPtrType(td); rt != nil {
		// the type of a nil host *T offers *T's reflect method set,
		// like a non-nil box of it.
		return &runtime.MethodSet{Names: r.HostTwinMethodSet(reflect.Zero(rt).Interface())}, nil
	}
	var unsure bool
	// an anonymous *T typedef sees T's method set including pointer
	// receivers; a declared pointer typedef (`type P *Sq`) keeps only
	// methods declared on P itself — Go forbids those outright, so in
	// valid programs the set is empty.
	if td.Kind == runtime.KindPointer && td.Spec == nil {
		if et, err := r.ElemOf(td); err == nil && et != nil {
			td, ptr = et, true
		} else {
			unsure = true
		}
	}
	// the pointee may itself be an alias (`type A = sync.Mutex` in `*A`)
	// — peel again so the interface and host checks see the real type.
	td = r.PeelAliasTd(td)
	if td == nil {
		return &runtime.MethodSet{Unsure: unsure}, nil
	}
	ms := r.MethodWalkU(td, ptr, map[walkKey]bool{})
	if ms == nil {
		ms = &runtime.MethodSet{}
	}
	ms.Unsure = unsure || ms.Unsure
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
		return &runtime.MethodSet{Names: set, Funcs: ms.Funcs, Unsure: ms.Unsure}, nil
	}
	if td.HostScalar != nil && len(td.Methods) == 0 {
		// a bound host scalar (time.Month, time.Duration) offers its
		// host type's reflect set, so dynamic checks on a constant or
		// conversion (`any(time.March).(fmt.Stringer)`) see String
		// just like a value a host call returned. A script `type M
		// time.Month` is a fresh typedef without HostScalar.
		return &runtime.MethodSet{Names: hostMethodSet(td.HostScalar), Funcs: ms.Funcs, Unsure: ms.Unsure}, nil
	}
	return ms, nil
}

// aliasOf implements the Hooks.AliasOf hook: a KindAlias typedef resolves
// its target expression — one hop only, so `type A = B` gives B's own
// typedef even when B is itself a declared type (unlike underlying).
func (r *Resolver) AliasOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if td == nil || td.Kind != runtime.KindAlias || td.Anon == nil {
		return td, nil
	}
	x := td.Anon
	n := 0
	for {
		for {
			p, ok := x.(*ast.ParenExpr)
			if !ok {
				break
			}
			x = p.X
		}
		st, ok := x.(*ast.StarExpr)
		if !ok {
			break
		}
		n++
		x = st.X
	}
	// resolveTypeRef peels `*T` to T (it serves embed lookup), so an
	// alias `type A = *T` would otherwise lose the pointer: rebuild it
	// like the underlying hook does — A IS *T, not T.
	expr := x
	elem, err := r.ResolveTypeRef(td, x)
	if err != nil || elem == nil || n == 0 {
		return elem, err
	}
	for ; n > 0; n-- {
		expr = &ast.StarExpr{X: expr}
		elem = &runtime.TypeDef{Kind: runtime.KindPointer, Elem: elem, Anon: expr, Pkg: td.Pkg, File: td.File, TParams: td.TParams, Binds: td.Binds, LocalTypes: td.LocalTypes}
	}
	return elem, nil
}

// underlying implements the Hooks.Underlying hook: a KindAlias typedef
// resolves through its aliased expression to the real typedef.
func (r *Resolver) Underlying(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	// aliases and named basics both peel to their underlying typedef —
	// `type S string` bottoms out at the builtin "string" typedef so a
	// zero value picks the right literal kind.
	for td != nil && (td.Kind == runtime.KindAlias || td.Kind == runtime.KindNamedBasic) && td.Anon != nil {
		x := td.Anon
		for {
			p, ok := x.(*ast.ParenExpr)
			if !ok {
				break
			}
			x = p.X
		}
		if _, isStar := x.(*ast.StarExpr); isStar {
			// the declared underlying is a pointer type (`type P *S`,
			// `type P A` with `type A = *S`): resolveTypeRef peels the
			// star for embed lookup, so rebuild it here — a defined
			// pointer type's underlying IS the pointer, and it promotes
			// nothing the pointee carries.
			n := 0
			for {
				for {
					p, ok := x.(*ast.ParenExpr)
					if !ok {
						break
					}
					x = p.X
				}
				st, ok := x.(*ast.StarExpr)
				if !ok {
					break
				}
				n++
				x = st.X
			}
			elem, err := r.ResolveTypeRef(td, x)
			if err != nil || elem == nil {
				return nil, err
			}
			// the rebuilt pointer keeps an underlying spelling so
			// anonymous comparisons (*S vs *R) stay distinct, and the
			// generic binds so *T spells *int when P[int]'s underlying
			// compares against an anonymous *int (convShapeEq).
			expr := ast.Expr(x)
			pt := elem
			for ; n > 0; n-- {
				expr = &ast.StarExpr{X: expr}
				pt = &runtime.TypeDef{Kind: runtime.KindPointer, Elem: pt, Anon: expr, Pkg: td.Pkg, File: td.File, TParams: td.TParams, Binds: td.Binds, LocalTypes: td.LocalTypes}
			}
			return pt, nil
		}
		next, err := r.ResolveTypeRef(td, td.Anon)
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
func (r *Resolver) PeelAliasTd(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		next, err := r.AliasOf(td)
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
//
// The set's Depths map each name to the smallest embedding depth it
// appears at inside the returned subtree (0 for members the type
// declares): a caller embedding td sees every promotion one level
// deeper, and a member survives only when its shallowest promotion is
// unique — contested members move to Ambiguous at that depth.
// Members locates each promoted member's receiver slot so a selector
// can bind it directly.
// walkKey is the per-path cycle guard's identity for a typedef. A
// declared type keys on its Spec plus, for a generic instantiation,
// the resolved argument typedefs: instantiateBinds mints a fresh
// TypeDef per call, so pointer identity cannot catch the recursive
// embed `type Rec[T any] struct{ *Rec[T] }` — a legal declaration
// whose method set is a fixpoint. Synthesized typedefs (no Spec)
// keep pointer identity.
type walkKey struct {
	spec *ast.TypeSpec
	td   *runtime.TypeDef
	args string
}

func (r *Resolver) MethodWalkU(td *runtime.TypeDef, ptr bool, seen map[walkKey]bool) *runtime.MethodSet {
	td = r.PeelAliasTd(td)
	if td == nil {
		return nil
	}
	key := walkKey{td: td}
	if td.Spec != nil {
		key = walkKey{spec: td.Spec}
		if len(td.TParams) > 0 {
			var b strings.Builder
			for _, tp := range td.TParams {
				fmt.Fprintf(&b, "%p;", td.Binds[tp])
			}
			key.args = b.String()
		}
	}
	if seen[key] {
		return nil
	}
	// seen guards cycles along a path only — a sibling embed path that
	// reaches the same type under a different ptr condition still has
	// methods to contribute (`struct{ A; *T }` where A embeds T:
	// visiting A.T as a value must not hide the *T embed's pointer
	// receivers), so the mark lifts when the path unwinds like
	// methodInner's.
	seen[key] = true
	defer delete(seen, key)
	// an anonymous *T typedef sees T's method set including pointer
	// receivers; a declared pointer typedef keeps only its own decls.
	if td.Kind == runtime.KindPointer && td.Spec == nil {
		if et, err := r.ElemOf(td); err == nil && et != nil {
			td, ptr = et, true
		}
	}
	if td.Kind == runtime.KindInterface {
		// the requirement set carries pkg-qualified keys for unexported
		// names (M4-2b); the facade's method set also answers under the
		// plain name so `x.m` and `I.m` keep resolving. Never write
		// back into the cached req set — copy.
		names := map[string]bool{}
		for m := range r.IfaceReqSet(td) {
			names[m] = true
			if p := PlainReqName(m); p != m {
				names[p] = true
			}
		}
		funcs := r.IfaceSigFuncs(td)
		depths := map[string]int{}
		for m := range names {
			depths[m] = 0
		}
		return &runtime.MethodSet{Names: names, Funcs: funcs, Depths: depths}
	}
	if td.HostNew != nil {
		// a bound host type's methods live on its reflect method set,
		// not in td.Methods: enumerate them by name so a script struct
		// embedding one (`struct{ bytes.Buffer }`, `struct{ *os.File }`)
		// satisfies interfaces through the promoted methods. The func
		// map stays empty — host members keep their name-only pass and
		// resolve through member selection at call time.
		rt := td.HostType()
		if rt == nil {
			return &runtime.MethodSet{Unsure: true}
		}
		if !ptr && rt.Kind() == reflect.Pointer {
			// a value embed promotes only the embedded type's value
			// receivers; the host box is always the pointer type.
			rt = rt.Elem()
		}
		depths := hostDepths(rt)
		names := map[string]bool{}
		members := map[string]*runtime.PromotedMember{}
		for m, d := range depths {
			names[m] = true
			// host members take the embed slot itself as receiver —
			// selectMember resolves method and field alike on the
			// unwrapped box, nested promotion included.
			members[m] = &runtime.PromotedMember{Depth: d, Via: "host", Method: true}
		}
		for m, fd := range hostFieldDepths(rt) {
			members[m] = &runtime.PromotedMember{Depth: fd, Via: "host"}
		}
		return &runtime.MethodSet{Names: names, Depths: depths, Members: members}
	}
	names := map[string]bool{}
	funcs := map[string]*runtime.Function{}
	for name, m := range td.Methods {
		if m == nil || len(m.TParams) > 0 {
			continue // a generic method contributes no interface method
		}
		if m.PtrRecv && !ptr {
			continue // pointer receivers live only on *T's method set
		}
		names[name] = true
		funcs[name] = m
		if k := ReqKey(td.Pkg, name); k != name {
			// an unexported method also answers under its declaring
			// package's key so interface requirements compare per
			// package; promotion carries the key unchanged.
			names[k] = true
			funcs[k] = m
		}
	}
	members := map[string]*runtime.PromotedMember{}
	ambig := map[string]int{}
	// a declared field at this depth shadows every promoted member of
	// the same name (`struct{ *bytes.Buffer; Write int }` keeps the
	// field's Write, not the embed's method) — field names, including
	// the embedded fields' own, are td.Fields.
	var shadowed map[string]bool
	if len(td.Fields) > 0 {
		shadowed = map[string]bool{}
		for _, f := range td.Fields {
			shadowed[f] = true
		}
	}
	// a name declared at this depth — field or method alike — is not
	// promoted: embeds cannot contest it (the declared member always
	// wins, so its smin entry would only poison Depths).
	promoted := func(m string) bool {
		p := PlainReqName(m)
		if !shadowed[p] && td.Methods[p] == nil {
			return true
		}
		// a member `p` declared at this depth shadows the candidate —
		// for a pkg-qualified unexported key only when this type is in
		// the same package: gc keeps `struct{ a.T; m int }` satisfying
		// a's interface{ m() } (m in main is a different identifier).
		return m != p && ReqKey(td.Pkg, p) != m
	}
	// A member promoted through more than one source survives only
	// when its shallowest promotion is unique — gc's selector is
	// ambiguous where two paths offer the member at the same smallest
	// depth, and the member drops out of the method set entirely
	// rather than first-wins (gc reports "does not implement" too,
	// not only "ambiguous selector" on access). smin is that smallest
	// depth per member, dup the number of sources promoting it there,
	// winner the member func of a unique-shallowest path. Every
	// candidate competes at its promotion depth: an interface embed's
	// requirements at depth 1, promoted members one deeper than their
	// inner depth, a defined type's underlying promotions at the
	// depth they hold inside the shared field layout. A member
	// declared at this depth dominates any promotion.
	smin := map[string]int{}
	dup := map[string]int{}
	winner := map[string]*runtime.Function{}
	winnerPath := map[string][]int{}
	winnerVia := map[string]string{}
	winnerMethod := map[string]bool{}
	unsure := false
	contest := func(m string, d int, fn *runtime.Function, path []int, via string, method bool) {
		if c, ok := smin[m]; !ok || d < c {
			smin[m] = d
			dup[m] = 1
			winner[m] = fn
			winnerPath[m] = path
			winnerVia[m] = via
			winnerMethod[m] = method
		} else if d == c {
			dup[m]++
		}
	}
	if td.Kind == runtime.KindNamedBasic {
		// a defined type's method set starts empty — nothing the
		// underlying named type DECLARES transfers — but members the
		// underlying struct promotes through its own anonymous fields
		// still come along (gc promotes Inner.M for `type Wrap Base`
		// where Base = struct{ Inner }), at the depth they hold inside
		// the shared field layout. Over a host type the same rule
		// keeps its exported fields (a defined type never gains host
		// methods).
		u, uerr := r.Underlying(td)
		if uerr != nil || u == nil {
			unsure = true
		} else if u != td && u.Kind == runtime.KindInterface {
			// a defined type over an interface type is itself an
			// interface type (`type E error`, `type W E`): its method
			// set adds the underlying interface's requirements at this
			// level. The members ride as iface dispatch so a struct
			// embedding the type promotes them exactly like an
			// embedded `error` — `struct{ E }.Error()` selects on the
			// stored interface value.
			for m := range r.IfaceReqSet(u) {
				names[m] = true
				if p := PlainReqName(m); p != m {
					names[p] = true
				}
				if td.Methods[m] == nil && td.Methods[PlainReqName(m)] == nil {
					members[m] = &runtime.PromotedMember{Depth: 0, Via: "iface", Method: true}
				}
			}
			for m, sf := range r.IfaceSigFuncs(u) {
				if funcs[m] == nil {
					funcs[m] = sf
				}
			}
		} else if u != td && u.Kind == runtime.KindStruct {
			if u.HostNew != nil {
				for m, fd := range hostFieldDepths(u.HostType()) {
					if promoted(m) {
						contest(m, fd, nil, nil, "host", false)
					}
				}
				// methods promoted through the host type's anonymous
				// fields carry to the defined type too (gc: Wrap.M on
				// `type Wrap Base` for Base = struct{ Inner }) — at
				// the depth they hold inside the shared field layout.
				for m, md := range hostDepthsIn(u.HostType(), map[reflect.Type]bool{}) {
					if md > 0 && promoted(m) {
						contest(m, md, nil, nil, "host", true)
					}
				}
			} else if sub2 := r.MethodWalkU(u, ptr, seen); sub2 != nil {
				unsure = unsure || sub2.Unsure
				for m := range sub2.Names {
					if sub2.Depths[m] == 0 || !promoted(m) {
						continue // declared on the underlying — not promoted
					}
					path, via := []int(nil), "method"
					if pm := sub2.Members[m]; pm != nil {
						path, via = pm.Path, pm.Via
					}
					contest(m, sub2.Depths[m], sub2.Funcs[m], path, via, true)
				}
				for m, pm := range sub2.Members {
					if sub2.Names[m] || !promoted(m) {
						continue
					}
					contest(m, pm.Depth, nil, pm.Path, pm.Via, pm.Method)
				}
				for m, ad := range sub2.Ambiguous {
					if promoted(m) {
						if c, ok := ambig[m]; !ok || ad < c {
							ambig[m] = ad
						}
					}
				}
			}
		}
	}
	for k, spec := range td.EmbedSpecs {
		emb, err := r.ResolveTypeRef(td, spec)
		// the embed's package failed its init (a trap or a panic): the
		// program is already broken there, and treating the set as
		// unsure would make every interface assertion succeed silently.
		var tr *runtime.Trap
		if errors.As(err, &tr) {
			panic(tr)
		}
		var pn *runtime.Panic
		if errors.As(err, &pn) {
			panic(pn)
		}
		if err != nil || emb == nil {
			unsure = true
			continue
		}
		fieldIdx := -1
		if k < len(td.EmbedIdx) {
			fieldIdx = td.EmbedIdx[k]
		}
		peeled := r.PeelAliasTd(emb)
		if peeled != nil && peeled.Kind == runtime.KindInterface {
			// an embedded interface field satisfies its own requirements:
			// the name set covers every required name (facade typedefs
			// without AST still count) while the func map keeps only the
			// members a signature can be synthesized for. Requirements
			// compete at depth 1 like every other member — an iface
			// promise beats a deeper promotion and ties a same-depth
			// one; the receiver is the stored interface value itself.
			// The signature shell contests with the name so a promoted
			// requirement keeps its signature for assertion checks.
			sigs := r.IfaceSigFuncs(peeled)
			for m := range r.IfaceReqSet(peeled) {
				if promoted(m) {
					contest(m, 1, sigs[m], []int{fieldIdx}, "iface", true)
				}
				// a pkg-qualified unexported requirement (M4-2b) also
				// promotes under its plain name — `x.m` keeps selecting
				// it; satisfaction only counts the qualified key.
				if p := PlainReqName(m); p != m && promoted(p) {
					contest(p, 1, sigs[p], []int{fieldIdx}, "iface", true)
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
		sub := r.MethodWalkU(emb, embPtr, seen)
		if sub == nil {
			continue
		}
		unsure = unsure || sub.Unsure
		for m := range sub.Names {
			if !promoted(m) {
				continue
			}
			d := sub.Depths[m] + 1
			via, path := "method", []int{fieldIdx}
			if pm := sub.Members[m]; pm != nil {
				// a member the subwalk already located keeps its
				// dispatch kind — interface members stay dynamic,
				// host members keep reselecting on the box — and
				// its receiver slot one embed level deeper.
				via = pm.Via
				path = append([]int{fieldIdx}, pm.Path...)
			}
			contest(m, d, sub.Funcs[m], path, via, true)
		}
		// member-only entries — a host embed's promoted fields and
		// anything else the name set never counts — still compete
		// for the selector at their promotion depth.
		for m, pm := range sub.Members {
			if sub.Names[m] || !promoted(m) {
				continue
			}
			contest(m, pm.Depth+1, nil, append([]int{fieldIdx}, pm.Path...), pm.Via, pm.Method)
		}
		// a member contested inside an embed stays contested one level
		// up — unless a shallower unique path wins.
		for m, ad := range sub.Ambiguous {
			if !promoted(m) {
				continue
			}
			if c, ok := ambig[m]; !ok || ad+1 < c {
				ambig[m] = ad + 1
			}
		}
	}
	depths := map[string]int{}
	for m, d := range smin {
		if td.Methods[m] != nil {
			continue // a declared member dominates every promotion
		}
		if dup[m] > 1 {
			// the shallowest promotion is not unique: the member is
			// absent from the method set, name and func alike — gc
			// reports "does not implement" for an ambiguous method
			// too, not only "ambiguous selector" on access. Ambiguous
			// keeps the contested depth so a selector can still
			// diagnose.
			ambig[m] = d
			continue
		}
		members[m] = &runtime.PromotedMember{Path: winnerPath[m], Depth: d, Via: winnerVia[m], Method: winnerMethod[m]}
		if !winnerMethod[m] {
			// a promoted FIELD wins the selector at its depth but is
			// no method — gc reports "x.M is a field, not a method" on
			// interface satisfaction, so the name stays out.
			continue
		}
		names[m] = true
		if f := winner[m]; f != nil {
			funcs[m] = f
		}
	}
	for m := range names {
		depths[m] = smin[m]
	}
	// a member contested deeper inside an embed is absent unless a
	// shallower unique promotion outranks the contested depth. The
	// unique winner counts even when it is not a method — a winning
	// field still shadows the contested name below it.
	for m, ad := range ambig {
		if td.Methods[m] != nil {
			continue // a declared member dominates any inner ambiguity
		}
		if sd, ok := smin[m]; ok && dup[m] == 1 && sd < ad {
			delete(ambig, m) // the unique shallower path wins
			continue
		}
		delete(names, m)
		delete(funcs, m)
		delete(members, m)
		delete(depths, m)
	}
	return &runtime.MethodSet{Names: names, Funcs: funcs, Depths: depths, Members: members, Ambiguous: ambig, Unsure: unsure}
}

// hostDepths maps each method in a bound host type's method set to the
// depth it is promoted at inside the host struct. reflect flattens the
// method table, so the depth is rebuilt by walking anonymous fields —
// a member only reachable through one counts one deeper than inside
// that field's type (bufio.ReadWriter.Write sits at depth 1 via the
// embedded *bufio.Writer, so a struct embedding ReadWriter sees it at
// depth 2 like gc's selector). Members ambiguous inside the host type
// are already absent from rt's method set. A member a host type both
// declares and receives from an anonymous field reads as promoted —
// reflect cannot tell them apart, and shadowing declarations are the
// rarer case.
func hostDepths(rt reflect.Type) map[string]int {
	return hostDepthsIn(rt, map[reflect.Type]bool{})
}

// hostFieldDepths maps each exported field a host type promotes to its
// smallest promotion depth — 0 for a declared field, one deeper per
// anonymous field crossed. A field a host embed exposes competes with
// methods and declared fields like the value walk counts it. Unexported
// fields never promote (reflect's PkgPath rule); same-depth duplicates
// inside the host type keep the first found, mirroring hostDepths.
func hostFieldDepths(rt reflect.Type) map[string]int {
	out := map[string]int{}
	if rt == nil {
		return out
	}
	st := rt
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	var walk func(t reflect.Type, d int, seen map[reflect.Type]bool)
	walk = func(t reflect.Type, d int, seen map[reflect.Type]bool) {
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		defer delete(seen, t)
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" {
				continue // unexported fields never promote
			}
			if _, ok := out[sf.Name]; !ok || d < out[sf.Name] {
				out[sf.Name] = d
			}
			if sf.Anonymous {
				et := sf.Type
				for et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				walk(et, d+1, seen)
			}
		}
	}
	walk(st, 0, map[reflect.Type]bool{})
	return out
}

func hostDepthsIn(rt reflect.Type, seen map[reflect.Type]bool) map[string]int {
	out := map[string]int{}
	for i := 0; i < rt.NumMethod(); i++ {
		out[rt.Method(i).Name] = 0
	}
	st := rt
	if st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct || seen[st] {
		return out
	}
	seen[st] = true
	defer delete(seen, st)
	// the smallest internal depth each member is promoted at through
	// an anonymous field; a revisit means a cycle, which contributes
	// nothing a shallower level does not already declare.
	sub := map[string]int{}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.Anonymous {
			continue
		}
		ft := f.Type
		if ft.Kind() != reflect.Pointer && ft.Kind() != reflect.Interface && rt.Kind() == reflect.Pointer {
			// under a pointer receiver view an embedded value field
			// also promotes its pointer receivers; an embedded
			// interface contributes its own method set as is (a
			// pointer to it has none).
			ft = reflect.PointerTo(ft)
		}
		for m, d := range hostDepthsIn(ft, seen) {
			if c, ok := sub[m]; !ok || d < c {
				sub[m] = d
			}
		}
	}
	for m := range out {
		if d, ok := sub[m]; ok {
			out[m] = d + 1
		}
	}
	return out
}

// methodSet implements the minireflect MethodSet hook and the
// Hooks.TypeMethodFuncs hook: the signature-bearing method set of a
// typedef.
func (r *Resolver) MethodSet(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	return r.MethodFuncs(td, false), nil
}

// methodFuncs collects the method FUNCTIONS of a typedef — declared plus
// promoted — honoring Go's receiver rule: pointer-receiver methods join
// the set only when the type is reached through a pointer (an anonymous
// *T typedef or an embedded pointer field). Unexported members stay in
// the set; callers apply their own visibility rules (reflect exposes
// exported methods only). An interface typedef yields synthesized
// members carrying each required method's declared signature.
func (r *Resolver) MethodFuncs(td *runtime.TypeDef, ptr bool) map[string]*runtime.Function {
	if ms := r.MethodWalkU(td, ptr, map[walkKey]bool{}); ms != nil {
		return ms.Funcs
	}
	return nil
}

// ifaceSigFunc, ifaceSigFuncs, ifaceSigReqs, ifaceSig and ifaceSigsOf
// live in iface_reqs.go — the pkg-qualified requirement keys (M4-2b)
// shared by requirements, signatures and method sets are spelled there.

// methodFuncsOfValue implements the Hooks.MethodFuncsOf hook: the
// signature-bearing twin of methodSetOfValue — declared and promoted
// script methods plus synthesized interface members. Host reflect
// methods carry no decl signature, so host boxes report nil here and
// satisfy requirements by name alone.
func (r *Resolver) MethodFuncsOfValue(v runtime.Value) (map[string]*runtime.Function, error) {
	ms, err := r.MethodInfoOfValue(v)
	if ms == nil {
		return nil, err
	}
	return ms.Funcs, err
}

// ifaceReqs, ifaceReqSet, ifaceReqsRec and reqKey live in iface_reqs.go —
// the pkg-qualified requirement keys (M4-2b) share one key space with the
// method sets methodWalkU builds above.

// fieldTypes implements the Hooks.FieldTypes hook: the declared type of
// each field, parallel to td.Fields, resolved from the struct's field
// ASTs (embedded fields count once, like td.Fields itself). Unresolvable
// types yield nil entries; generic binds resolve `T`-style names first.
// The result is cached on td: every field store consults it, and
// resolving each field's type from AST again dominated allocation in
// field-heavy scripts (go/parser over a large package).
func (r *Resolver) FieldTypes(td *runtime.TypeDef) ([]*runtime.TypeDef, error) {
	if fts, ok := td.CachedFieldTypes(); ok {
		return fts, nil
	}
	fts := r.ResolveFieldTypes(td)
	td.SetFieldTypes(fts)
	return fts, nil
}

func (r *Resolver) ResolveFieldTypes(td *runtime.TypeDef) []*runtime.TypeDef {
	var st *ast.StructType
	for _, x := range []ast.Expr{td.Anon, SpecType(td)} {
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
			ft, err := r.ElemTypeRef(td, fld.Type)
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

func SpecType(td *runtime.TypeDef) ast.Expr {
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// resolveTypeRef resolves a type expression embedded in a decl of typedef
// `from` to a *TypeDef: package-local names via the index, pkg.Name via
// import refs, *T / T[...] peel to the base. Anything else fails —
// constraints and underlying-only types don't participate in method sets.
func (r *Resolver) ResolveTypeRef(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	switch t := x.(type) {
	case *ast.StarExpr:
		return r.ResolveTypeRef(from, t.X)
	case *ast.ParenExpr:
		return r.ResolveTypeRef(from, t.X)
	case *ast.IndexExpr:
		base, err := r.ResolveTypeRef(from, t.X)
		if err != nil {
			return nil, err
		}
		return r.InstantiateRef(from, base, []ast.Expr{t.Index}), nil
	case *ast.IndexListExpr:
		base, err := r.ResolveTypeRef(from, t.X)
		if err != nil {
			return nil, err
		}
		return r.InstantiateRef(from, base, t.Indices), nil
	case *ast.ArrayType:
		r.foldArrayLens(from, t)
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
	case *ast.MapType:
		return &runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
	case *ast.ChanType:
		return &runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
	case *ast.StructType:
		td := &runtime.TypeDef{Kind: runtime.KindStruct, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}
		td.FTags = runtime.StructFieldTags(t)
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 {
				td.EmbedSpecs = append(td.EmbedSpecs, f.Type)
				td.EmbedIdx = append(td.EmbedIdx, len(td.Fields))
				td.Fields = append(td.Fields, runtime.AnonFieldName(f.Type))
				continue
			}
			for _, n := range f.Names {
				td.Fields = append(td.Fields, n.Name)
			}
		}
		return td, nil
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Kind: runtime.KindInterface, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}
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
		return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
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
				vv, err := r.materialize(from.Pkg, info.Decl)
				if err != nil {
					return nil, err
				}
				if td, ok := vv.(*runtime.TypeDef); ok {
					// `type A = T` — an alias IS its target, so a type
					// reference resolves straight to the aliased decl
					// rather than the KindAlias handle. A generic alias
					// stays put: its target mentions the decl's own type
					// parameters, which only instantiateRef can bind.
					if td.Kind == runtime.KindAlias && len(td.TParams) == 0 {
						return r.PeelAliasTd(td), nil
					}
					return td, nil
				}
				return nil, fmt.Errorf("%s is not a type", t.Name)
			}
		}
		// a materialized typedef may sit in package globals without an
		// index decl yet — a REPL binds `type User` into the session
		// package's globals the moment its decl step runs, while the
		// index only learns the name at the next build.
		if from.Pkg != nil && from.Pkg.Globals != nil {
			if gv, ok := from.Pkg.Globals.Get(t.Name); ok {
				if td, ok := gv.(*runtime.TypeDef); ok && td != nil {
					return td, nil
				}
			}
		}
		// dot imports: the REPL's :cd enters a package as a pseudo
		// dot-import, so a bare `User` resolves through the entered
		// package's members like the VM's resolveGlobal does.
		if from.Pkg != nil && from.File != nil {
			var itd *runtime.TypeDef
			for _, ref := range from.Pkg.Imports[from.File] {
				if ref.Alias != "." {
					continue
				}
				if !token.IsExported(t.Name) && !ref.AllNames {
					continue
				}
				p, err := ref.Materialize()
				if err != nil || p == nil {
					continue
				}
				_, inGlobals := p.Globals.Get(t.Name)
				inIndex := false
				if p.Index != nil {
					_, inIndex = p.Index.Lookup(t.Name)
				}
				if !inGlobals && !inIndex {
					continue
				}
				m, err := p.Member(t.Name, r.materialize)
				if err != nil {
					return nil, fmt.Errorf("dot import %s: %s", ref.Path, err)
				}
				td, _ := m.(*runtime.TypeDef)
				if td == nil {
					continue
				}
				if itd != nil {
					return nil, fmt.Errorf("ambiguous dot-imported name: %s", t.Name)
				}
				itd = td
			}
			if itd != nil {
				return itd, nil
			}
		}
		if bv, ok := r.builtins.Get(t.Name); ok {
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
		p, err := r.ImportPackageAt(from, id.Name)
		if err != nil {
			return nil, err
		}
		if p == from.Pkg {
			return r.ResolveTypeRef(from, ast.NewIdent(t.Sel.Name))
		}
		m, err := p.Member(t.Sel.Name, r.materialize)
		if err != nil {
			return nil, err
		}
		td, ok := m.(*runtime.TypeDef)
		if !ok {
			return nil, fmt.Errorf("%s.%s is not a type", id.Name, t.Sel.Name)
		}
		// same alias transparency as the ident case: pkg.A where A is
		// `type A = T` resolves to T's own typedef.
		if td.Kind == runtime.KindAlias && len(td.TParams) == 0 {
			td = r.PeelAliasTd(td)
		}
		return td, nil
	}
	return nil, fmt.Errorf("unsupported embedded type expression %T", x)
}

// deferredTypeOf implements the Hooks.DeferredType hook (M4-7a): a
// deferred fact's type expression resolved under AllowIndex. Unlike
// resolveTypeRef — a reference resolver that strips `*` — the answer
// keeps the expression's own shape: `*int` resolves to a pointer
// typedef, parens count their inner stars the same way.
func (r *Resolver) DeferredTypeOf(site *bytecode.CallSite, ctx *runtime.TypeDef) (*runtime.TypeDef, error) {
	return r.StarKeepingTypedef(ctx, site.Expr)
}

// starKeepingTypedef resolves a type expression while keeping its
// leading `*` levels as pointer typedefs — resolveTypeRef strips them,
// and a nilability or spelling judgment needs the pointer.
func (r *Resolver) StarKeepingTypedef(ctx *runtime.TypeDef, expr ast.Expr) (*runtime.TypeDef, error) {
	var stars []*ast.StarExpr
	for {
		switch t := expr.(type) {
		case *ast.ParenExpr:
			expr = t.X
		case *ast.StarExpr:
			stars = append(stars, t)
			expr = t.X
		default:
			goto peeled
		}
	}
peeled:
	td, err := r.ResolveTypeRef(ctx, expr)
	if err != nil {
		return nil, err
	}
	// wrap innermost star first so `**int`'s *int level spells `*int`,
	// and keep the context's local-type snapshot: typeExpr-minted
	// pointers carry LocalTypes, and TypSpelling needs it to name
	// function-local element types alike.
	for i := len(stars) - 1; i >= 0 && td != nil; i-- {
		td = &runtime.TypeDef{Kind: runtime.KindPointer, Anon: stars[i], Elem: td, Pkg: ctx.Pkg, File: ctx.File, Binds: ctx.Binds, LocalTypes: ctx.LocalTypes}
	}
	return td, nil
}

// elemOf implements the Hooks.ElemOf hook: the element typedef of a
// container typedef — or the pointee typedef of a pointer typedef —
// resolved from its underlying type AST (Anon or Spec.Type). Used by
// elided composite literal elements and pointer member dispatch.
func (r *Resolver) ElemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	return r.elemOfFuel(td, 8)
}

// elemOfFuel is elemOf with a bound on the named-container hops a bare
// underlying (`type N Ms`, `type N AS` with an alias AS) takes to reach
// the container whose element it shares.
func (r *Resolver) elemOfFuel(td *runtime.TypeDef, fuel int) (*runtime.TypeDef, error) {
	if td.Elem != nil {
		return td.Elem, nil
	}
	x := td.TypeExpr()
	for {
		switch t := x.(type) {
		case *ast.ParenExpr:
			x = t.X
			continue
		case *ast.Ellipsis:
			x = t.Elt
			continue
		case *ast.StarExpr:
			// pointer typedef: element is the pointee typedef — kept as
			// a pointer when the pointee is itself a star (`**T`'s
			// element is `*T`, not T: resolveTypeRef would collapse the
			// inner star and lose the pointer level).
			return r.ElemTypeRef(td, t.X)
		case *ast.ArrayType:
			return r.ElemTypeRef(td, t.Elt)
		case *ast.MapType:
			return r.ElemTypeRef(td, t.Value)
		case *ast.ChanType:
			return r.ElemTypeRef(td, t.Value)
		}
		// bare underlying naming a container (`type N Ms`, `type N AS`
		// with an alias AS = []map[K]V): N shares that container's
		// element — the named type itself is not its element.
		etd, err := r.ResolveTypeRef(td, x)
		if err == nil && etd != nil && etd != td && fuel > 0 {
			switch etd.Kind {
			case runtime.KindSlice, runtime.KindMap, runtime.KindChan, runtime.KindPointer, runtime.KindAlias:
				return r.elemOfFuel(etd, fuel-1)
			}
		}
		// any other bare underlying (e.g. `type T MyStruct`): the elem
		// is that type
		return etd, err
	}
}

// instantiateRef builds the specialized typedef for `base[args...]` like
// the VM's specializeType does for a runtime instantiation: the type
// parameters bind to the argument typedefs and every method is re-bound
// so its body sees the concrete arguments. Arguments that do not resolve
// (an unbound type parameter in a generic context) bind to a placeholder
// named typedef so `List[T]` inside a decl stays a stable shape.
func (r *Resolver) InstantiateRef(from *runtime.TypeDef, base *runtime.TypeDef, args []ast.Expr) *runtime.TypeDef {
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
		at, err := r.ElemTypeRef(from, args[i])
		if err != nil || at == nil {
			if id, ok := args[i].(*ast.Ident); ok {
				at = &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: id.Name, Pkg: from.Pkg, File: from.File}
			} else {
				continue
			}
		}
		binds[tp] = at
	}
	return r.InstantiateBinds(base, binds)
}

// instantiateBinds builds the specialized typedef of generic base with
// its type parameters bound to binds (param name → typedef), re-binding
// every method so its body sees the concrete arguments.
func (r *Resolver) InstantiateBinds(base *runtime.TypeDef, binds map[string]runtime.Value) *runtime.TypeDef {
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
			var rpn []string
			if m.Decl != nil {
				rpn = syntax.RecvTypeParamNames(m.Decl.Recv)
			}
			for i, rp := range rpn {
				if rp == "_" {
					continue // `_` names no parameter to bind
				}
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
	// M4-3d: the instantiated descriptor is canonical — two mints of the
	// same instantiation are the same pointer.
	return r.internTypedef(td)
}

// elemTypeRef resolves a container's element type expression: `*T`
// elements stay pointers (KindPointer), everything else resolves to its
// typedef. Named pointer types (`*P` where P is `type P *Sq`) resolve
// through resolveTypeRef to P's own typedef, which memberOfType peels.
func (r *Resolver) ElemTypeRef(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if st, ok := x.(*ast.StarExpr); ok {
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: st, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
	}
	return r.ResolveTypeRef(from, x)
}

// ---- shared helpers ----

// typeParamNames extracts parameter names from a type parameter list,
// in declaration order — the shared walk lives in syntax.
func TypeParamNames(fl *ast.FieldList) []string {
	return syntax.TypeParamNames(fl)
}

// typeParamConstraints extracts the constraint expression per type
// parameter name, parallel to typeParamNames.
func TypeParamConstraints(fl *ast.FieldList) []ast.Expr {
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
