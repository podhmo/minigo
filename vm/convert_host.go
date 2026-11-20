package vm

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// hostPtrConv reports whether x is a host pointer box whose type is
// exactly the pointer type td spells: td's element is a bound host
// type, so `P(x)` is Go's identical-underlying-type conversion.
func (v *VM) hostPtrConv(td *runtime.TypeDef, x runtime.Value) bool {
	if !hostPtrBox(x) || v.H.ElemOf == nil {
		return false
	}
	et, err := v.H.ElemOf(v.peelNamed(td))
	if err != nil || et == nil {
		return false
	}
	if a := v.peelAlias(et); a != nil {
		et = a
	}
	// a declared pointee (`type MyBuf bytes.Buffer`) peels to the host
	// type it names before the identity check — `(*MyBuf)(p)` shares
	// p's box like gc shares the pointer.
	if u := v.peelNamed(et); u != nil {
		et = u
	}
	if et.HostNew == nil {
		return false
	}
	rt := hostbridge.TypeOf(et.HostNew())
	if rt == nil {
		return false
	}
	if rt.Kind() != hostbridge.KindPointer {
		rt = hostbridge.PointerTo(rt)
	}
	return rt == hostbridge.TypeOf(x.(*runtime.GoValue).V)
}

// convertStruct implements `S(x)` on struct typedefs: legal when the
// source is a struct whose field list matches the target's (the
// approximation of Go's identical-underlying rule — field types and tags
// are not modeled). The result is a copy carrying the target's typedef.
func (v *VM) convertStruct(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	s, ok := x.(*runtime.Struct)
	if !ok {
		if dv, isRef := runtime.Deref(x); isRef {
			s, ok = dv.(*runtime.Struct)
		}
	}
	if !ok {
		// a host struct box converts to a declared type over the same
		// host struct — `type MyBuf bytes.Buffer; MyBuf(b)` — or to
		// the bound type itself; gc copies the struct, so the tagged
		// result wraps a snapshot, not the shared box.
		if u := v.peelNamed(td); u != nil && u.HostNew != nil {
			if gv, isGV := runtime.Unwrap(x).(*runtime.GoValue); isGV &&
				hostbridge.TypeOf(gv.V) == hostbridge.TypeOf(u.HostNew()) {
				if nv, ok := runtime.HostStructCopy(gv); ok {
					return &runtime.Named{Typ: td, V: nv}, nil
				}
			}
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if s.Def != nil && s.Def != td && !runtime.StructFieldsEq(s.Def, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Def), tdName(td))
	}
	return &runtime.Struct{Def: td, Fields: append([]runtime.Value{}, s.Fields...)}, nil
}

// convShapeEq reports whether two typedefs have identical underlying
// shape for a conversion: both peel through declared chains to their
// structural spelling — `type C B` gives B's shape, `Wrap[int]` gives
// []int — while element positions compare by written identity, so []MyInt
// does not spell []int.
func (v *VM) convShapeEq(a, b *runtime.TypeDef) bool {
	return runtime.NewShapeEq(sigResolver{v}).Conv(a, b)
}

// underlyingShape spells a typedef's underlying shape. Declared chain
// heads (type C B, aliases) peel away; composite shapes spell their type
// expression with generic binds substituted.
func (v *VM) underlyingShape(td *runtime.TypeDef) string {
	return runtime.NewShapeEq(sigResolver{v}).UnderlyingShape(td)
}

// elemFamily classifies a slice element typedef for the special string
// conversions: 'b' when its underlying is byte-family (byte, uint8, or a
// named type on them like `type MyByte byte`), 'r' for rune-family, else
// 0 — mirroring Go's `[]byte(s)` / `[]rune(s)` rule.
func (v *VM) elemFamily(et *runtime.TypeDef) byte {
	u := v.peelNamed(et)
	if u == nil {
		return 0
	}
	name := u.Name
	if name == "" {
		if id, ok := u.Anon.(*ast.Ident); ok {
			name = id.Name
		}
	}
	switch name {
	case "byte", "uint8":
		return 'b'
	case "rune", "int32":
		return 'r'
	}
	return 0
}

