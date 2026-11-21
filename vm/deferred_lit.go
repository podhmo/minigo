package vm

import (
	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// deferredLitTyp resolves a literal-type deferred fact: the site's Expr
// resolved, then Depth element peels, the map-key typedef when Key, and
// After more element peels — the container an elided literal or a
// literal key actually lands in.
func (v *VM) deferredLitTyp(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, error) {
	td, err := v.deferredType(f, site)
	if err != nil || td == nil {
		return td, err
	}
	for i := 0; i < site.Depth && td != nil; i++ {
		td = v.deferredElem(f, td)
	}
	if site.Key && td != nil {
		td = v.mapKeyTypedef(f, td)
	}
	for i := 0; i < site.After && td != nil; i++ {
		td = v.deferredElem(f, td)
	}
	return td, nil
}

// deferredElem peels one container level; elemTypedef already resolves
// named types to their underlying container first (peelNamed).
func (v *VM) deferredElem(f *frame, td *runtime.TypeDef) *runtime.TypeDef {
	return v.elemTypedef(f, td)
}

// litKeyExprKey answers an OpLitKey's question: whether the resolved
// literal type is a map or an array/slice, where an ident key is a key
// expression — anything else makes the ident a field name.
func (v *VM) litKeyExprKey(f *frame, site *bytecode.CallSite) bool {
	td, err := v.deferredLitTyp(f, site)
	if err != nil {
		panic(&runtime.Trap{Pos: site.Pos, Reason: err.Error(), Err: err})
	}
	for td != nil {
		td = v.peelNamed(td)
		switch td.Kind {
		case runtime.KindMap, runtime.KindSlice:
			return true
		case runtime.KindPointer:
			// an elided `&T` literal reads keys by T's shape.
			td = v.elemTypedef(f, td)
		default:
			return false
		}
	}
	return false
}
