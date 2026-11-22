package typedef

import (
	"fmt"
	"go/ast"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// respath records where a type resolution walked — the call-local state
// go-scan carries as ctx's ResolutionPathKey: the public chase entry
// seeds it with the starting typedef, each hop appends the decl it
// resolves to, and a failure annotates its error with the walk. The
// record lives only for the call — nothing persists on the typedefs
// or the resolver.
//
// The record is lazy: seeding costs a pointer store, hops append to
// small slices (walks stay short), and the key spelling exists only
// when String/errf needs it — Underlying runs once per coerce on the
// VM hot path, so the earlier eager map + key-concat showed up in
// profiles. ids hold the decl identities — two decls sharing a name
// (a package `type A []int` and a function's `type A B` both spell
// "m.A") are different hops.
type respath struct {
	seed *runtime.TypeDef
	hops []*runtime.TypeDef
	ids  []any
}

// respathName is the decl name behind a typedef, or "" when there is
// none — anonymous types open no record (a real cycle can only
// revisit a named decl).
func respathName(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if td.Name != "" {
		return td.Name
	}
	if td.Spec != nil && td.Spec.Name != nil {
		return td.Spec.Name.Name
	}
	return ""
}

// key names one hop: the declaring package's path and the decl name,
// or a marker for a typedef with no decl behind it (synthesized or
// anonymous types keep the walk interesting only through their hops).
func respathKey(td *runtime.TypeDef) string {
	name := respathName(td)
	switch {
	case td == nil:
		return "<nil>"
	case name == "":
		return "<expr>"
	case td.Pkg != nil && td.Pkg.Path != "":
		return td.Pkg.Path + "." + name
	default:
		return name
	}
}

// identityOf names the decl a typedef carries: its spec when one was
// declared (fresh typedef objects around the same decl still share it)
// and the typedef itself otherwise (bound types are interned anyway).
func identityOf(td *runtime.TypeDef) any {
	if td.Spec != nil {
		return td.Spec
	}
	return td
}

// seedPath starts the record at the typedef the public entry was
// called on. Unnamed typedefs open no record: a real cycle can only
// revisit a named decl, and two anonymous hops would collide on the
// "<expr>" marker.
func seedPath(td *runtime.TypeDef) respath {
	if respathName(td) == "" {
		return respath{}
	}
	return respath{seed: td}
}

// push appends the typedef a hop resolved to. Unnamed hops are not
// recorded — synthesized typedefs are fresh objects each hop and can
// never be the revisit a cycle is looking for.
func (p respath) push(td *runtime.TypeDef) respath {
	if respathName(td) == "" {
		return p
	}
	// copy-on-write: the walk is value-semantic like go-scan's ctx value,
	// so a sub-chase's hops never leak into the caller's seen set — a
	// resolveTypeRef inside aliasOf pushing `int` must not read as a
	// revisit when the outer Underlying later arrives at `int` itself.
	hops := make([]*runtime.TypeDef, len(p.hops)+1)
	copy(hops, p.hops)
	hops[len(p.hops)] = td
	p.hops = hops
	ids := make([]any, len(p.ids)+1)
	copy(ids, p.ids)
	ids[len(p.ids)] = identityOf(td)
	p.ids = ids
	return p
}

// seen reports whether the decl this typedef carries already appears
// on the path — a resolution arriving at it again is a cycle when the
// chase would keep walking it.
func (p respath) seen(td *runtime.TypeDef) bool {
	id := identityOf(td)
	if p.seed != nil && identityOf(p.seed) == id {
		return true
	}
	for _, x := range p.ids {
		if x == id {
			return true
		}
	}
	return false
}

func (p respath) String() string {
	parts := make([]string, 0, len(p.hops)+1)
	if p.seed != nil {
		parts = append(parts, respathKey(p.seed))
	}
	for _, td := range p.hops {
		parts = append(parts, respathKey(td))
	}
	return strings.Join(parts, " -> ")
}

// errf annotates a resolution failure with the walk that reached it.
func (p respath) errf(format string, args ...any) error {
	return fmt.Errorf("%s (resolving %s)", fmt.Sprintf(format, args...), p)
}

// ---- the chases that carry the path ----

func (r *Resolver) AliasOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	return r.aliasOf(td, seedPath(td))
}

func (r *Resolver) aliasOf(td *runtime.TypeDef, p respath) (*runtime.TypeDef, error) {
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
	elem, err := r.resolveTypeRef(td, x, p)
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
	return r.underlying(td, seedPath(td))
}

func (r *Resolver) underlying(td *runtime.TypeDef, p respath) (*runtime.TypeDef, error) {
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
			elem, err := r.resolveTypeRef(td, x, p)
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
		next, err := r.resolveTypeRef(td, td.Anon, p)
		if err != nil || next == nil || next == td {
			return nil, err
		}
		// a revisit only cycles while the revisited decl still peels —
		// `type T []A; type A = T` walks T -> A -> T and stops: T is
		// the answer, not a loop.
		if p.seen(next) && (next.Kind == runtime.KindAlias || next.Kind == runtime.KindNamedBasic) {
			return nil, p.errf("type underlying cycle through %s", respathKey(next))
		}
		p = p.push(next)
		td = next
	}
	return td, nil
}

// peelAliasTd follows a typedef's alias links — `type A = B` IS B, so
// method sets, interface requirements and signatures all read through
// the alias to the aliased declaration. Named basics stay put: `type A B`
// declares a distinct method set, not B's.
func (r *Resolver) PeelAliasTd(td *runtime.TypeDef) *runtime.TypeDef {
	out, _ := r.peelAliasTd(td, seedPath(td))
	return out
}

// peelAliasTd is the path-carrying walk: hops append to p so a caller
// reporting a resolution failure can name the chain, and a chain
// revisiting a decl it would keep peeling reports the cycle. Reaching
// a walked named decl is fine — `type T []A; type A = T` ends at T,
// which the loop leaves alone.
func (r *Resolver) peelAliasTd(td *runtime.TypeDef, p respath) (*runtime.TypeDef, error) {
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		next, err := r.aliasOf(td, p)
		if err != nil || next == nil || next == td {
			break
		}
		if next.Kind == runtime.KindAlias && p.seen(next) {
			return td, p.errf("type alias cycle through %s", respathKey(next))
		}
		p = p.push(next)
		td = next
	}
	return td, nil
}
