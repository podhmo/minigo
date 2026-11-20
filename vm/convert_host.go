package vm

import (
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
