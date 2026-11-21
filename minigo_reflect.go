package minigo

import (
	"github.com/podhmo/minigo/minireflect"
)

// reflectHooks returns the engine's type machinery in the facade's
// hook shape — the wiring installReflect builds the reflect bind with,
// also used where intrinsics need declared-type layout (unsafe.Offsetof).
func (e *Engine) reflectHooks() minireflect.Hooks {
	return minireflect.Hooks{
		ElemOf:      e.elemOf,
		ResolveType: e.resolveTypeRef,
		FieldTypes:  e.fieldTypes,
		TypeMethods: e.typeMethods,
		IfaceReqs:   e.ifaceReqs,
		Underlying:  e.underlying,
		AliasOf:     e.aliasOf,
		MethodSet:   e.methodSet,
		TypeAlias:   proxyHostType,
		UnwrapHost:  proxyUnwrapHost,
	}
}
