// errors.go carries the host error side of the errors package: the
// ScriptError box that lets errors.Is/As and fmt's %w walk a script
// error's own Unwrap/Is/As methods, the hostErrTwins table that
// rebuilds concrete host error structs (*fs.PathError) from their
// interpreted source twins, and the error-tree walkers (ErrIs,
// FindAsTarget) the script errors.Is/As intrinsics call.
package hostbridge

import (
	"fmt"
	"io/fs"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// ffn is the fmt-package variant of fn: script values reach host fmt as
// fmtValue wrappers so composites render like Go's %v — a *runtime.Slice
// prints [1 2], a struct {x y}, a pointer &{...}, and a String()/Error()/
// GoString() method declared on the type is honored.
// ScriptError boxes a script error value (a struct carrying an Error
// method) as a host error, so fmt.Errorf's %w chains and errors.Is/As
// walk it through host errors.Unwrap.
type ScriptError struct {
	C runtime.MemberCaller
	V runtime.Value
}

// hostErrOf converts a script error value to a host error: boxed Go
// errors pass through, script values get a ScriptError that calls back
// into the VM for Error().
func HostErrOf(c runtime.MemberCaller, v runtime.Value) error {
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
			return HostErrOf(c, x.V)
		}
		// keep the tag: a named script type's methods (Error,
		// Timeout, Is) live on it, and boxing the bare payload
		// would drop them.
		return &ScriptError{C: c, V: v}
	case runtime.Nil, *runtime.IfaceNil, *runtime.TypedNil:
		// an untyped nil error unwraps to nothing — but a typed nil
		// (`var err error = (*E)(nil)`, or the TypedNil `errors.As`
		// stores through reflect.Value.Set) is a real error value:
		// its concrete tag unwraps to the TypedNil As/Is match on.
		if td := runtime.NilDeclaredTyp(v); td != nil {
			return &ScriptError{C: c, V: &runtime.TypedNil{Typ: td}}
		}
		return nil
	default:
		return &ScriptError{C: c, V: v}
	}
}

// hostErrArg is hostErrOf for host helpers that type-switch on concrete
// host error types: a pointer to a script struct that is the source twin
// of a hostErrTwins entry converts to the host type (a fresh value — the
// helpers only read it); anything else boxes like hostErrOf. The arg is
// unwrapped first — a var of pointer type arrives as a Cell whose
// hostErrOf would otherwise box the cell itself as a script error.
func HostErrArg(c runtime.MemberCaller, v runtime.Value) error {
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
					if hostNamedIsOf(c, t.rt, s.Def) {
						return t.build(c, s)
					}
				}
			}
			if tn, ok := inner.(*runtime.TypedNil); ok {
				if err := twinNilHost(c, tn); err != nil {
					return err
				}
			}
			return HostErrOf(c, u)
		case *runtime.Cell:
			inner := runtime.Unwrap(x.Elem)
			if s, ok := inner.(*runtime.Struct); ok && s.Def != nil {
				for _, t := range hostErrTwins {
					if hostNamedIsOf(c, t.rt, s.Def) {
						return t.build(c, s)
					}
				}
				// a script struct pointer: the cell is the *T its
				// pointer-receiver methods (Error, Timeout, Is) hang
				// on — unwrapping to the struct would drop them.
				return HostErrOf(c, u)
			}
			u = inner
		case *runtime.Struct:
			if x.Def != nil {
				for _, t := range hostErrTwins {
					if hostNamedIsOf(c, t.rt, x.Def) {
						return t.build(c, x)
					}
				}
			}
			return HostErrOf(c, u)
		case *runtime.TypedNil:
			// a typed nil of a twin pointer type hands the host a
			// real (*T)(nil): gc's underlyingError type-switches on
			// it and panics dereferencing the field, the same crash
			// the script should see rather than a quietly false
			// predicate.
			if err := twinNilHost(c, x); err != nil {
				return err
			}
			return HostErrOf(c, u)
		default:
			return HostErrOf(c, u)
		}
	}
}

// hostErrTwins lists the host error structs whose source twin a script
// can build while the host helpers that inspect them stay host-bound:
// io/fs is interpreted from source, but os.IsNotExist & co. type-switch
// on the host *fs.PathError. Each entry rebuilds the host value from the
// script struct's fields (by name), the error fields via hostErrOf.
var hostErrTwins = []struct {
	rt    reflect.Type
	build func(c runtime.MemberCaller, s *runtime.Struct) error
}{{reflect.TypeFor[fs.PathError](), func(c runtime.MemberCaller, s *runtime.Struct) error {
	return &fs.PathError{
		Op:   nativeStr(GoNative(structField(s, "Op"))),
		Path: nativeStr(GoNative(structField(s, "Path"))),
		Err:  HostErrOf(c, structField(s, "Err")),
	}
}},
}

// hostNamedIsOf answers source-twin identity through the caller's intern
// table when it has one ([runtime.HostNamedTwinner]); thinner callers
// keep the name+path residual compare ([HostNamedIs]).
func hostNamedIsOf(c any, rt reflect.Type, td *runtime.TypeDef) bool {
	if tw, ok := c.(runtime.HostNamedTwinner); ok {
		return tw.HostNamedTwin(rt, td)
	}
	return HostNamedIs(rt, td)
}

// twinNilHost returns the real host (*T)(nil) for a script typed nil
// of a hostErrTwins pointer type, nil for any other typed nil.
func twinNilHost(c runtime.CallerRef, tn *runtime.TypedNil) error {
	et := tn.Typ
	if et == nil {
		return nil
	}
	for et.Kind == runtime.KindPointer {
		et = et.Elem
	}
	for _, t := range hostErrTwins {
		if hostNamedIsOf(c, t.rt, et) {
			return reflect.Zero(reflect.PointerTo(t.rt)).Interface().(error)
		}
	}
	return nil
}

// asMatchVal maps a chain element to the value errors.As matches on — a
// ScriptError's inner script value (a typed nil stays bare so its tag
// matches the target), anything else boxed.
func asMatchVal(e error) runtime.Value {
	if se, ok := e.(*ScriptError); ok {
		return se.V
	}
	return errValOf(e)
}

// findAsTarget walks an error's tree the way errors.As/AsType do —
// the node itself, its own As(any) bool, then Unwrap() error and
// Unwrap() []error children depth-first — and reports the first
// element satisfying the target. want is the target's element typedef
// (nil accepts anything); reqs is want's required method set for an
// interface target; target is the destination pointer a node's As
// method writes through (errors.As's user cell, AsType's result cell).
func FindAsTarget(v runtime.ErrMatchCaller, err error, want *runtime.TypeDef, reqs map[string]bool, target runtime.Value) (runtime.Value, bool) {
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
				if rv, ok := FindAsTarget(v, kid, want, reqs, target); ok {
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
func ErrIs(err, target error) bool {
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
				if ErrIs(kid, target) {
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
	if se, ok := target.(*ScriptError); ok {
		_, ok := runtime.TryOpaqueKey(se.V)
		return ok
	}
	return reflect.TypeOf(target).Comparable()
}

// errEqual is gc's `err == target` for a chain that may hold script
// errors: two ScriptError boxes compare their script values canonically
// (OpaqueKey — the same mapKey equality host containers use), a script
// error never equals a native one, and two natives compare as errors.
func errEqual(err, target error) bool {
	s0, ok0 := err.(*ScriptError)
	s1, ok1 := target.(*ScriptError)
	if ok0 || ok1 {
		if !ok0 || !ok1 {
			return false
		}
		k0, g0 := runtime.TryOpaqueKey(s0.V)
		k1, g1 := runtime.TryOpaqueKey(s1.V)
		return g0 && g1 && k0 == k1
	}
	return err == target
}

// errKids returns an error node's children — Unwrap() error's single
// cause or Unwrap() []error's several, checked in Go's order.
// ScriptError answers through the script Unwrap member itself so a
// script `Unwrap() []error` still fans out.
func errKids(err error) []error {
	if se, ok := err.(*ScriptError); ok {
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
func matchAsTarget(v runtime.ErrMatchCaller, sv runtime.Value, want *runtime.TypeDef, reqs map[string]bool) bool {
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
func sameErrTypHost(v runtime.ElemZeroTyper, rt reflect.Type, want *runtime.TypeDef) bool {
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
	return wt.Name == rt.PkgPath()+"."+rt.Name() || hostNamedIsOf(v, rt, wt)
}

// sameErrTyp compares a chain element's dynamic typedef with the As
// target's element typedef: pointer depth must match (Go requires
// reflect.TypeOf(err) == reflect.TypeOf(target).Elem()), and leaf
// identity is name + package path — leaf spelling alone lets two
// packages declaring the same error type name match each other.
func sameErrTyp(v runtime.ElemZeroTyper, st, want *runtime.TypeDef) bool {
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

// errKids returns the error node's children: a script `Unwrap()
// []error` fans out like errors.Join's elements (a shape Unwrap()
// error cannot express alongside it on one Go type), a plain
// `Unwrap() error` is the single cause.
func (e *ScriptError) errKids() []error {
	m, ok := runtime.IfaceMember(e.C, e.V, "Unwrap")
	if !ok {
		return nil
	}
	r, err := e.C.Call(m, nil)
	if err != nil {
		return nil
	}
	switch x := runtime.Unwrap(r).(type) {
	case *runtime.Slice:
		kids := make([]error, 0, len(x.Elems))
		for _, elem := range x.Elems {
			if ke := HostErrOf(e.C, elem); ke != nil {
				kids = append(kids, ke)
			}
		}
		return kids
	case *runtime.TypedNil:
		if x.Typ != nil && x.Typ.Kind == runtime.KindSlice {
			return nil // `Unwrap() []error` returned a nil slice
		}
	}
	if ke := HostErrOf(e.C, r); ke != nil {
		return []error{ke}
	}
	return nil
}

// ScriptValue is the script-visible error the box carries: a typed nil
// (`var err error = (*E)(nil)`) reads back boxed in its interface so it
// stays non-nil.
func (e *ScriptError) ScriptValue() runtime.Value {
	if tn, ok := e.V.(*runtime.TypedNil); ok {
		return &runtime.IfaceNil{Typ: tn.Typ}
	}
	return e.V
}

// Unwrap lets a script-declared `Unwrap() error` method join the host
// errors chain — errors.Unwrap/Is/As walk through it like Go's. A
// script `Unwrap() []error` reports no single cause — like
// errors.Join, the type has no Unwrap() error.
func (e *ScriptError) Error() string {
	// A typed-nil error still runs its Error(): the method may succeed
	// on a nil receiver (`func (e *E) Error() string { return "x" }`,
	// `errors.Join`'s joinError) and gc prints its result; only a
	// panic lands on fmt's `<nil>` fallback, which the call's own
	// recovery covers.
	s, ok := runtime.IfaceCallString(e.C, e.V, "Error")
	if !ok {
		switch e.V.(type) {
		case *runtime.TypedNil, *runtime.IfaceNil:
			return "<nil>"
		}
		return fmt.Sprintf("%v", GoNative(e.V))
	}
	return s
}

func (e *ScriptError) Unwrap() error {
	kids := e.errKids()
	if len(kids) != 1 {
		return nil
	}
	return kids[0]
}

// Is lets a script-declared `Is(error) bool` method join host
// errors.Is — the error's own equality override like Go's custom-Is
// rule. The host error target maps back to its script value the way
// hostErrOf boxed it, so the script side compares what it sees.
func (e *ScriptError) Is(target error) bool {
	m, ok := runtime.IfaceMember(e.C, e.V, "Is")
	if !ok {
		return false
	}
	r, err := e.C.Call(m, []runtime.Value{errValOf(target)})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// As lets a script-declared `As(any) bool` method join host
// errors.As/AsType — the error's own As assigns the target like Go's
// custom-As rule. A non-pointer target has nothing to write to and
// simply fails the script side's assert.
func (e *ScriptError) As(target any) bool {
	m, ok := runtime.IfaceMember(e.C, e.V, "As")
	if !ok {
		return false
	}
	r, err := e.C.Call(m, []runtime.Value{target})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// Timeout lets a script-declared `Timeout() bool` method answer host
// checks like os.IsTimeout; without one the error is not a timeout.
func (e *ScriptError) Timeout() bool {
	m, ok := runtime.IfaceMember(e.C, e.V, "Timeout")
	if !ok {
		return false
	}
	r, err := e.C.Call(m, nil)
	if err != nil {
		return false
	}
	b, _ := runtime.Unwrap(r).(bool)
	return b
}

// errValOf boxes a host error the way script intrinsics read it back:
// a script error that went through host code (a %w cause, a callback's
// result) reads as itself, anything else as a *runtime.GoValue whose
// method calls dispatch via reflection.
func errValOf(err error) runtime.Value {
	if err == nil {
		return runtime.NIL
	}
	if sb, ok := err.(runtime.ScriptBoxed); ok {
		return sb.ScriptValue()
	}
	return &runtime.GoValue{V: err}
}

// structField reads a script struct's field by name (NIL when absent).
func structField(s *runtime.Struct, name string) runtime.Value {
	for i, f := range s.Def.Fields {
		if f == name && i < len(s.Fields) {
			return s.Fields[i]
		}
	}
	return runtime.NIL
}

// nativeStr spells a native payload: the str() fold the root package
// applies to hostErrTwins fields — Named and UConst tags peel, anything
// else prints with fmt.
func nativeStr(v any) string {
	if n, ok := v.(*runtime.Named); ok {
		return nativeStr(n.V)
	}
	if u, ok := v.(*runtime.UConst); ok {
		return nativeStr(MustNativeConst(u))
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
