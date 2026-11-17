package minigo

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installErrors(h *hostHelpers) {
	e.Bind("errors", map[string]runtime.Value{
		"New": h.fn("errors.New", func(a []any) (any, error) { return errors.New(str(a[0])), nil }, errors.New),
		// sentinel vars ride GoValue boxes so `err == errors.ErrUnsupported`
		// and errors.Is against the chain see the real objects.
		"ErrUnsupported": &runtime.GoValue{V: errors.ErrUnsupported},
		"Join": &runtime.BuiltinFunc{Name: "errors.Join", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			var errs []error
			for _, x := range args {
				if err := hostErrOf(v, x); err != nil {
					errs = append(errs, err)
				}
			}
			return errVal(errors.Join(errs...)), nil
		}},
		// Is/Unwrap/As take raw runtime args: h.fn's goNative would flatten
		// a script error value to its printed string, losing the type the
		// chain walk needs. hostErrOf keeps the script value inside a
		// scriptError wrapper instead.
		"Is": &runtime.BuiltinFunc{Name: "errors.Is", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.Is needs 2 args")
			}
			e0, e1 := hostErrOf(v, args[0]), hostErrOf(v, args[1])
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
			return errIs(e0, e1), nil
		}},
		"Unwrap": &runtime.BuiltinFunc{Name: "errors.Unwrap", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.Unwrap needs 1 arg")
			}
			return errVal(errors.Unwrap(hostErrOf(v, args[0]))), nil
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
			sv, ok := findAsTarget(v, hostErrOf(v, args[0]), want, e.ifaceReqSet(want), args[1])
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
			sv, ok := findAsTarget(v, hostErrOf(v, args[0]), want, e.ifaceReqSet(want), target)
			if ok {
				return &runtime.Tuple{Elems: []runtime.Value{sv, true}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{zero, false}}, nil
		}},
	})
}

// ffn is the fmt-package variant of fn: script values reach host fmt as
// fmtValue wrappers so composites render like Go's %v — a *runtime.Slice
// prints [1 2], a struct {x y}, a pointer &{...}, and a String()/Error()/
// GoString() method declared on the type is honored.
// scriptError boxes a script error value (a struct carrying an Error
// method) as a host error, so fmt.Errorf's %w chains and errors.Is/As
// walk it through host errors.Unwrap.
type scriptError struct {
	c runtime.VMCaller
	v runtime.Value
}

// errKids returns the error node's children: a script `Unwrap()
// []error` fans out like errors.Join's elements (a shape Unwrap()
// error cannot express alongside it on one Go type), a plain
// `Unwrap() error` is the single cause.
func (e *scriptError) errKids() []error {
	m, ok := runtime.IfaceMember(e.c, e.v, "Unwrap")
	if !ok {
		return nil
	}
	r, err := e.c.Call(m, nil)
	if err != nil {
		return nil
	}
	switch x := runtime.Unwrap(r).(type) {
	case *runtime.Slice:
		kids := make([]error, 0, len(x.Elems))
		for _, elem := range x.Elems {
			if ke := hostErrOf(e.c, elem); ke != nil {
				kids = append(kids, ke)
			}
		}
		return kids
	case *runtime.TypedNil:
		if x.Typ != nil && x.Typ.Kind == runtime.KindSlice {
			return nil // `Unwrap() []error` returned a nil slice
		}
	}
	if ke := hostErrOf(e.c, r); ke != nil {
		return []error{ke}
	}
	return nil
}

// wrapError is fmt.Errorf's %w product: message plus one cause.
type wrapError struct {
	msg string
	err error
}

// wrapOperand is gc's `a[n].(error)` on a %w operand: a boxed host
// error, a script value with an Error method, or a boxed typed nil
// (a non-nil error) converts through hostErrOf; anything else — an
// int, a string, a nil interface — wraps nothing.
func wrapOperand(c runtime.VMCaller, v runtime.Value) error {
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.GoValue:
		if _, ok := x.V.(error); !ok {
			return nil
		}
		return hostErrOf(c, v)
	case *runtime.IfaceNil:
		return hostErrOf(c, v)
	}
	if _, ok := runtime.IfaceMember(c, v, "Error"); ok {
		return hostErrOf(c, v)
	}
	return nil
}

// wrapErrors is fmt.Errorf's product for several %w verbs: message plus
// every cause, reachable through Unwrap() []error.
type wrapErrors struct {
	msg  string
	errs []error
}

// hostErrOf converts a script error value to a host error: boxed Go
// errors pass through, script values get a scriptError that calls back
// into the VM for Error().
func hostErrOf(c runtime.VMCaller, v runtime.Value) error {
	switch x := v.(type) {
	case *runtime.GoValue:
		if e, ok := x.V.(error); ok {
			return e
		}
		return fmt.Errorf("%v", x.V)
	case *runtime.Named:
		if _, ok := x.V.(*runtime.GoValue); ok {
			// a bound named type's payload is the host value —
			// peel to it so the helpers see the real error.
			return hostErrOf(c, x.V)
		}
		// keep the tag: a named script type's methods (Error,
		// Timeout, Is) live on it, and boxing the bare payload
		// would drop them.
		return &scriptError{c: c, v: v}
	case runtime.Nil, *runtime.IfaceNil:
		// an untyped nil error unwraps to nothing — but a boxed typed
		// nil (`var err error = (*E)(nil)`) is a real error value: its
		// concrete tag unwraps to the TypedNil As/Is match on.
		if td := runtime.BoxedNilTyp(v); td != nil {
			return &scriptError{c: c, v: &runtime.TypedNil{Typ: td}}
		}
		return nil
	default:
		return &scriptError{c: c, v: v}
	}
}

// osErrPred binds one of os.IsNotExist/IsExist/IsPermission/IsTimeout.
// These look through exactly one *fs.PathError / *os.LinkError /
// *os.SyscallError layer by host type switch, so the argument goes
// through hostErrArg: a script-built *fs.PathError (io/fs runs from
// source) reaches the host as a real *fs.PathError.
func osErrPred(name string, pred func(error) bool) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: pred, Fn: func(c runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		return pred(hostErrArg(c, args[0])), nil
	}}
}

// hostErrTwins lists the host error structs whose source twin a script
// can build while the host helpers that inspect them stay host-bound:
// io/fs is interpreted from source, but os.IsNotExist & co. type-switch
// on the host *fs.PathError. Each entry rebuilds the host value from the
// script struct's fields (by name), the error fields via hostErrOf.
var hostErrTwins = []struct {
	rt    reflect.Type
	build func(c runtime.VMCaller, s *runtime.Struct) error
}{
	{reflect.TypeFor[fs.PathError](), func(c runtime.VMCaller, s *runtime.Struct) error {
		return &fs.PathError{
			Op:   str(goNative(structField(s, "Op"))),
			Path: str(goNative(structField(s, "Path"))),
			Err:  hostErrOf(c, structField(s, "Err")),
		}
	}},
}

// hostErrArg is hostErrOf for host helpers that type-switch on concrete
// host error types: a pointer to a script struct that is the source twin
// of a hostErrTwins entry converts to the host type (a fresh value — the
// helpers only read it); anything else boxes like hostErrOf. The arg is
// unwrapped first — a var of pointer type arrives as a Cell whose
// hostErrOf would otherwise box the cell itself as a script error.
func hostErrArg(c runtime.VMCaller, v runtime.Value) error {
	u := v
	for {
		switch x := u.(type) {
		case *runtime.Named:
			if _, ok := x.V.(*runtime.GoValue); ok {
				u = x.V // a bound named type's payload is the host value
				continue
			}
			inner := runtime.Unwrap(x.V)
			if _, ok := inner.(*runtime.Cell); ok {
				u = inner // reach the pointer cell below
				continue
			}
			// a script named value: matching looks through the tag,
			// but the boxed error keeps the Named itself — its
			// methods (Error, Timeout, Is) live on the tag.
			if s, ok := inner.(*runtime.Struct); ok && s.Def != nil {
				for _, t := range hostErrTwins {
					if runtime.HostNamedIs(t.rt, s.Def) {
						return t.build(c, s)
					}
				}
			}
			if tn, ok := inner.(*runtime.TypedNil); ok {
				if err := twinNilHost(tn); err != nil {
					return err
				}
			}
			return hostErrOf(c, u)
		case *runtime.Cell:
			inner := runtime.Unwrap(x.Elem)
			if s, ok := inner.(*runtime.Struct); ok && s.Def != nil {
				for _, t := range hostErrTwins {
					if runtime.HostNamedIs(t.rt, s.Def) {
						return t.build(c, s)
					}
				}
				// a script struct pointer: the cell is the *T its
				// pointer-receiver methods (Error, Timeout, Is) hang
				// on — unwrapping to the struct would drop them.
				return hostErrOf(c, u)
			}
			u = inner
		case *runtime.Struct:
			if x.Def != nil {
				for _, t := range hostErrTwins {
					if runtime.HostNamedIs(t.rt, x.Def) {
						return t.build(c, x)
					}
				}
			}
			return hostErrOf(c, u)
		case *runtime.TypedNil:
			// a typed nil of a twin pointer type hands the host a
			// real (*T)(nil): gc's underlyingError type-switches on
			// it and panics dereferencing the field, the same crash
			// the script should see rather than a quietly false
			// predicate.
			if err := twinNilHost(x); err != nil {
				return err
			}
			return hostErrOf(c, u)
		default:
			return hostErrOf(c, u)
		}
	}
}

// twinNilHost returns the real host (*T)(nil) for a script typed nil
// of a hostErrTwins pointer type, nil for any other typed nil.
func twinNilHost(tn *runtime.TypedNil) error {
	et := tn.Typ
	if et == nil {
		return nil
	}
	for et.Kind == runtime.KindPointer {
		et = et.Elem
	}
	for _, t := range hostErrTwins {
		if runtime.HostNamedIs(t.rt, et) {
			return reflect.Zero(reflect.PointerTo(t.rt)).Interface().(error)
		}
	}
	return nil
}

// asMatchVal maps a chain element to the value errors.As matches on — a
// scriptError's inner script value (a typed nil stays bare so its tag
// matches the target), anything else boxed.
func asMatchVal(e error) runtime.Value {
	if se, ok := e.(*scriptError); ok {
		return se.v
	}
	return errVal(e)
}

// findAsTarget walks an error's tree the way errors.As/AsType do —
// the node itself, its own As(any) bool, then Unwrap() error and
// Unwrap() []error children depth-first — and reports the first
// element satisfying the target. want is the target's element typedef
// (nil accepts anything); reqs is want's required method set for an
// interface target; target is the destination pointer a node's As
// method writes through (errors.As's user cell, AsType's result cell).
func findAsTarget(v runtime.VMCaller, err error, want *runtime.TypeDef, reqs map[string]bool, target runtime.Value) (runtime.Value, bool) {
	for err != nil {
		sv := asMatchVal(err)
		if matchAsTarget(v, sv, want, reqs) {
			return sv, true
		}
		if xa, ok := err.(interface{ As(any) bool }); ok && xa.As(target) {
			// the node's own As wrote the answer through the target
			// pointer — read the cell back for the result value.
			if c, ok := target.(*runtime.Cell); ok {
				return c.Elem, true
			}
			return sv, true
		}
		switch kids := errKids(err); {
		case len(kids) == 0:
			return nil, false
		case len(kids) == 1:
			err = kids[0]
		default:
			for _, kid := range kids {
				if rv, ok := findAsTarget(v, kid, want, reqs, target); ok {
					return rv, true
				}
			}
			return nil, false
		}
	}
	return nil, false
}

// errIs walks an error tree the way gc's errors.Is does — each node's
// `err == target` equality on a comparable target, its own Is(error)
// bool, then Unwrap() error / Unwrap() []error children depth-first —
// sharing As's errKids so a script `Unwrap() []error` fans out like
// errors.Join's elements.
func errIs(err, target error) bool {
	targetComparable := errComparable(target)
	for {
		if targetComparable && errEqual(err, target) {
			return true
		}
		if x, ok := err.(interface{ Is(error) bool }); ok && x.Is(target) {
			return true
		}
		switch kids := errKids(err); {
		case len(kids) == 0:
			return false
		case len(kids) == 1:
			err = kids[0]
		default:
			for _, kid := range kids {
				if errIs(kid, target) {
					return true
				}
			}
			return false
		}
	}
}

// errComparable is gc's targetComparable: the target's dynamic type is
// comparable — a script value asks TryOpaqueKey (a struct containing a
// slice field is not), a host error its reflect type.
func errComparable(target error) bool {
	if se, ok := target.(*scriptError); ok {
		_, ok := runtime.TryOpaqueKey(se.v)
		return ok
	}
	return reflect.TypeOf(target).Comparable()
}

// errEqual is gc's `err == target` for a chain that may hold script
// errors: two scriptError boxes compare their script values canonically
// (OpaqueKey — the same mapKey equality host containers use), a script
// error never equals a native one, and two natives compare as errors.
func errEqual(err, target error) bool {
	s0, ok0 := err.(*scriptError)
	s1, ok1 := target.(*scriptError)
	if ok0 || ok1 {
		if !ok0 || !ok1 {
			return false
		}
		k0, g0 := runtime.TryOpaqueKey(s0.v)
		k1, g1 := runtime.TryOpaqueKey(s1.v)
		return g0 && g1 && k0 == k1
	}
	return err == target
}

// errKids returns an error node's children — Unwrap() error's single
// cause or Unwrap() []error's several, checked in Go's order.
// scriptError answers through the script Unwrap member itself so a
// script `Unwrap() []error` still fans out.
func errKids(err error) []error {
	if se, ok := err.(*scriptError); ok {
		return se.errKids()
	}
	if u, ok := err.(interface{ Unwrap() error }); ok {
		if e := u.Unwrap(); e != nil {
			return []error{e}
		}
		return nil
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		return u.Unwrap()
	}
	return nil
}

// matchAsTarget reports whether a chain element satisfies an errors.As
// target's element typedef: an interface target (`var e error; &e`)
// accepts an element implementing its full required method set, a
// concrete target matches the element's declared type exactly — name +
// package, the way reflect.TypeOf(err) == elem(target) works — and a
// GoValue-boxed host error, which carries no typedef of its own,
// matches on its reflect type (*strconv.NumError).
func matchAsTarget(v runtime.VMCaller, sv runtime.Value, want *runtime.TypeDef, reqs map[string]bool) bool {
	if want == nil {
		return true
	}
	if want.Kind == runtime.KindInterface {
		// interface targets check the element's method set —
		// AsType[Extra](E{}) where E offers only Error() misses, the
		// way gc's AssignableTo rejects it.
		for m := range reqs {
			if _, ok := runtime.IfaceMember(v, sv, m); !ok {
				return false
			}
		}
		return true
	}
	if sameErrTyp(v, v.TypeOf(sv), want) {
		return true
	}
	if gv, ok := sv.(*runtime.GoValue); ok {
		return sameErrTypHost(v, reflect.TypeOf(gv.V), want)
	}
	return false
}

// sameErrTypHost is the GoValue side of sameErrTyp: it compares a chain
// element's concrete host type with the As target's element typedef.
// Pointer depth must match first; the leaf compares a bound typedef's
// "pkg/path.Name" spelling with reflect's PkgPath+Name, which cannot
// collide with a script-declared type's short name.
func sameErrTypHost(v runtime.VMCaller, rt reflect.Type, want *runtime.TypeDef) bool {
	if rt == nil || want == nil {
		return false
	}
	wt := want
	for rt.Kind() == reflect.Pointer {
		if wt == nil || wt.Kind != runtime.KindPointer {
			return false
		}
		pt := wt
		wt = wt.Elem
		if wt == nil {
			// a typedef may carry the pointee only in Anon — let the VM
			// resolve the element zero instead of walking the AST here.
			wt = v.TypeOf(v.ElemZero(pt))
			if wt == nil {
				return false
			}
		}
		rt = rt.Elem()
	}
	if wt == nil || wt.Kind == runtime.KindPointer {
		return false
	}
	if wt.Name == "" || rt.Name() == "" {
		return false
	}
	// a type declared in a source-interpreted package (io/fs's
	// PathError) is named by its package and bare name instead.
	return wt.Name == rt.PkgPath()+"."+rt.Name() || runtime.HostNamedIs(rt, wt)
}

// sameErrTyp compares a chain element's dynamic typedef with the As
// target's element typedef: pointer depth must match (Go requires
// reflect.TypeOf(err) == reflect.TypeOf(target).Elem()), and leaf
// identity is name + package path — leaf spelling alone lets two
// packages declaring the same error type name match each other.
func sameErrTyp(v runtime.VMCaller, st, want *runtime.TypeDef) bool {
	if st == nil || want == nil || st.Kind != want.Kind {
		return false
	}
	if st == want {
		return true
	}
	stl, wl := st, want
	if st.Kind == runtime.KindPointer {
		stl, wl = st.Elem, want.Elem
		// a typedef may carry the pointee only in Anon — let the VM
		// resolve the element zero instead of walking the AST here.
		if stl == nil {
			stl = v.TypeOf(v.ElemZero(st))
		}
		if wl == nil {
			wl = v.TypeOf(v.ElemZero(want))
		}
	}
	if stl == nil || wl == nil {
		return false
	}
	if stl == wl {
		return true
	}
	if stl.Name == "" || stl.Name != wl.Name {
		return false
	}
	sp, wp := "", ""
	if stl.Pkg != nil {
		sp = stl.Pkg.Path
	}
	if wl.Pkg != nil {
		wp = wl.Pkg.Path
	}
	return sp == wp
}
