package stdjson

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

// jsonMarshalProbe spells the value the marshaler probes run on: an
// addressable operand (gc's CanAddr) sees pointer-receiver
// MarshalJSON/MarshalText too, which a Cell wrapper surfaces through
// IfaceMember. A value that already carries a reference shape needs
// no wrapper.
func jsonMarshalProbe(v any, addr bool) any {
	if !addr {
		return v
	}
	switch v.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return v
	}
	return &runtime.Cell{Elem: v}
}

// jsonTypIsIface reports whether td is declared as an interface type —
// the Kind flag for resolved typdefs, the AST for name-only ones
// (`interface{}` literal or an `any` spelling).
func jsonTypIsIface(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Kind == runtime.KindInterface {
		return true
	}
	// ident-spelled interface underlyings (`type T any`, `type E error`)
	// resolve through IfaceBase — reading the spelling instead would
	// misjudge a shadowed `any` (`type any = int`) as an interface.
	if td.IfaceBase {
		return true
	}
	_, ok := td.TypeExpr().(*ast.InterfaceType)
	return ok
}

// jsonIfaceElem reports whether a slice/array typedef's element is
// interface-declared: its stored values are copies, never addressable.
func jsonIfaceElem(c runtime.ElemTyper, td *runtime.TypeDef) bool {
	if td == nil || c == nil {
		return false
	}
	et, err := c.ElemOf(td)
	return err == nil && jsonTypIsIface(et)
}

// jsonIsPtrValue reports whether v is pointer-ish storage — a Cell,
// field/index/deref ref, possibly inside a named tag.
func jsonIsPtrValue(v runtime.Value) bool {
	for {
		switch x := v.(type) {
		case *runtime.Named:
			v = x.V
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			return true
		default:
			return false
		}
	}
}
