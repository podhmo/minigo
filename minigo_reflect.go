package minigo

import (
	"errors"

	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

// installReflect binds the reflect facade: interpreting reflect's own
// source is a dead end (reflect.TypeOf reaches internal/abi's
// unsafe.Pointer reinterpretation, which the host-object value model
// cannot execute), so the package is backed by the minireflect facade
// over runtime values instead. DeepEqual stays on the VM-side script
// implementation.
func (e *Engine) installReflect() {
	syms := minireflect.Symbols(minireflect.Hooks{
		ElemOf:      e.elemOf,
		ResolveType: e.resolveTypeRef,
		FieldTypes:  e.fieldTypes,
		TypeMethods: e.typeMethods,
		IfaceReqs:   e.ifaceReqs,
		Underlying:  e.underlying,
		AliasOf:     e.aliasOf,
		MethodSet:   e.methodSet,
		TypeAlias:   proxyHostType,
	})
	syms["DeepEqual"] = &runtime.BuiltinFunc{Name: "reflect.DeepEqual", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, errors.New("reflect.DeepEqual needs 2 args")
		}
		return deepEql(args[0], args[1]), nil
	}}
	e.Bind("reflect", syms)
}
