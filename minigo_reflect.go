package minigo

import (
	"github.com/podhmo/minigo/minireflect"
)

// reflectHooks returns the engine's type machinery in the facade's
// hook shape — the wiring installReflect builds the reflect bind with,
// also used where intrinsics need declared-type layout (unsafe.Offsetof).
func (e *Engine) reflectHooks() minireflect.Hooks {
	return minireflect.Hooks{
		ElemOf:      e.td.ElemOf,
		ResolveType: e.td.ResolveTypeRef,
		FieldTypes:  e.td.FieldTypes,
		TypeMethods: e.td.TypeMethods,
		IfaceReqs:   e.ifaceReqs,
		Underlying:  e.td.Underlying,
		AliasOf:     e.td.AliasOf,
		MethodSet:   e.td.MethodSet,
		TypeAlias:   proxyHostType,
		UnwrapHost:  proxyUnwrapHost,
	}
}
