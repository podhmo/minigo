package errors

import (
	"errors"
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Install binds the errors intrinsics: New plus the chain-walking
// Is/As/Unwrap and the AsType generic. ifaceReqSet supplies the
// interface typedef's requirement keys (see iface_reqs.go in the root
// package) — an engine responsibility the caller hands in.
func Install(bind stdlib.Bind, h stdlib.Helpers, ifaceReqSet func(*runtime.TypeDef) map[string]bool) {
	bind("errors", map[string]runtime.Value{
		"New": h.Fn("errors.New", func(a []any) (any, error) { return errors.New(stdlib.Str(a[0])), nil }, errors.New),
		// sentinel vars ride GoValue boxes so `err == errors.ErrUnsupported`
		// and errors.Is against the chain see the real objects.
		"ErrUnsupported": &runtime.GoValue{V: errors.ErrUnsupported},
		"Join": &runtime.BuiltinFunc{Name: "errors.Join", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			var errs []error
			for _, x := range args {
				if err := hostbridge.HostErrOf(v, x); err != nil {
					errs = append(errs, err)
				}
			}
			return stdlib.ErrVal(errors.Join(errs...)), nil
		}},
		// Is/Unwrap/As take raw runtime args: h.fn's hostbridge.GoNative would flatten
		// a script error value to its printed string, losing the type the
		// chain walk needs. hostErrOf keeps the script value inside a
		// scriptError wrapper instead.
		"Is": &runtime.BuiltinFunc{Name: "errors.Is", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.Is needs 2 args")
			}
			e0, e1 := hostbridge.HostErrOf(v, args[0]), hostbridge.HostErrOf(v, args[1])
			if e0 == nil || e1 == nil {
				return e0 == e1, nil
			}
			// script-side walk — host errors.Is's `==` compares error
			// pointers and only walks Unwrap() error, so it misses a
			// scriptError's canonical equality and its []error kids.
			// No `err == target` fast path here: gc only compares when
			// the target is comparable, which is errIs's own gate — a
			// pointer-identity shortcut would answer true on a same-box
			// uncomparable target where gc skips `==` entirely.
			return hostbridge.ErrIs(e0, e1), nil
		}},
		"Unwrap": &runtime.BuiltinFunc{Name: "errors.Unwrap", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.Unwrap needs 1 arg")
			}
			return stdlib.ErrVal(errors.Unwrap(hostbridge.HostErrOf(v, args[0]))), nil
		}},
		// As walks the error tree the way gc's errors.As does — each
		// node's dynamic type, its own As(any) bool, then Unwrap() error
		// and Unwrap() []error children depth-first — and assigns the
		// first cause satisfying the target cell's declared element type.
		"As": &runtime.BuiltinFunc{Name: "errors.As", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.As needs 2 args")
			}
			want := cellElemTyp(args[1])
			sv, ok := hostbridge.FindAsTarget(v, hostbridge.HostErrOf(v, args[0]), want, ifaceReqSet(want), args[1])
			if ok && runtime.SetRef(args[1], sv) {
				return true, nil
			}
			return false, nil
		}},
		// AsType is the generic form of As: GenFn makes the bind
		// instantiable (`errors.AsType[*E](err)` → a plain builtin that
		// returns the first matching chain element with its bool). The
		// walk shares As's matching rule: an interface E accepts any
		// error, a concrete E matches the element's declared typedef.
		"AsType": &runtime.BuiltinFunc{Name: "errors.AsType", GenFn: func(v runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			if len(targs) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 type argument")
			}
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 arg")
			}
			want, _ := targs[0].(*runtime.TypeDef)
			// gc returns E's zero on a miss — a typed nil so a
			// pointer-receiver Error() on the result still runs.
			var zero runtime.Value = runtime.NIL
			if want != nil {
				zero = v.Zero(want)
			}
			// target stands in for errors.As's user cell: a node's own
			// As method writes its answer through this pointer.
			target := &runtime.Cell{Elem: zero}
			sv, ok := hostbridge.FindAsTarget(v, hostbridge.HostErrOf(v, args[0]), want, ifaceReqSet(want), target)
			if ok {
				return &runtime.Tuple{Elems: []runtime.Value{sv, true}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{zero, false}}, nil
		}},
	})
}

// cellElemTyp reads the declared type held by a cell (the target of
// errors.As's &x): its TypedNil/IfaceNil/Struct tag.
func cellElemTyp(v runtime.Value) *runtime.TypeDef {
	c, ok := v.(*runtime.Cell)
	if !ok {
		return nil
	}
	switch e := c.Elem.(type) {
	case *runtime.Cell:
		// the variable's VALUE is itself a pointer (a cell): `&p` where
		// p is declared `*T` — the assignable target type is T.
		if e.Typ != nil {
			return e.Typ
		}
		if s, ok := e.Elem.(*runtime.Struct); ok {
			return s.Def
		}
		return nil
	case *runtime.Slice, *runtime.Map, *runtime.Chan:
		// a container-typed target carries no usable element typedef for
		// As — nil keeps the matcher permissive (a nil want accepts
		// anything).
		return nil
	}
	return stdlib.ValueDeclTyp(c.Elem)
}
