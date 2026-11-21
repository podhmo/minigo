// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"math"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

// hostTypOf returns the host reflect type a bound typedef stands in
// for — the fresh value HostNew mints, or the raw scalar HostScalar
// marks — so its method set can be probed where the typedef's own
// declared Methods stay empty. nil when the typedef binds no host type.
func hostTypOf(td *runtime.TypeDef) reflect.Type {
	if td == nil {
		return nil
	}
	if td.HostScalar != nil {
		return reflect.TypeOf(td.HostScalar)
	}
	if td.HostNew != nil {
		return td.HostType()
	}
	return nil
}

func isGoValue(x runtime.Value) bool {
	_, ok := x.(*runtime.GoValue)
	return ok
}

// uncontendedLock returns the non-blocking attempt matching a blocking
// lock method — TryLock for Lock, TryRLock for RLock — or nil when hv
// has none (or name is not a lock). A successful attempt is exactly the
// effect of the blocking call.
func uncontendedLock(hv any, name string) func() bool {
	switch name {
	case "Lock":
		if l, ok := hv.(interface{ TryLock() bool }); ok {
			return l.TryLock
		}
	case "RLock":
		if l, ok := hv.(interface{ TryRLock() bool }); ok {
			return l.TryRLock
		}
	}
	return nil
}

// hostNilMethod binds a host-backed method to a nil *T receiver: Go
// dispatches pointer methods on nil receivers, so `var b *bytes.Buffer;
// b.String()` calls String on (*bytes.Buffer)(nil) and the method's own
// nil handling decides the outcome (Buffer.String reports "<nil>").
// td is the anonymous *T typedef; a declared pointer typedef
// (`type P *T`) carries no promoted set, matching Go. The receiver
// reflect type is *U where U is the element typedef's boxed value type
// — a HostNew that returns a pointer already is the *T box.
func (v *VM) hostNilMethod(td *runtime.TypeDef, name string) (runtime.Value, bool) {
	if rt := HostNilType(td); rt != nil {
		// a nil named host slice ([HostNil]): IP's methods on nil IP
		return hostZeroMethod(rt, name)
	}
	if td == nil || td.Spec != nil || td.Kind != runtime.KindPointer {
		return nil, false
	}
	rt := HostNilPtrType(td)
	if rt == nil {
		if v.H.ElemOf == nil {
			return nil, false
		}
		et, err := v.H.ElemOf(td)
		if err != nil || et == nil || et.HostNew == nil {
			return nil, false
		}
		rt = reflect.TypeOf(et.HostNew())
		if rt.Kind() != reflect.Pointer {
			rt = reflect.PointerTo(rt)
		}
	}
	return hostZeroMethod(rt, name)
}

// hostZeroMethod binds method name on the zero (nil) value of host type
// rt, or reports false when rt has no such method.
func hostZeroMethod(rt reflect.Type, name string) (runtime.Value, bool) {
	m := reflect.Zero(rt).MethodByName(name)
	if !m.IsValid() {
		return nil, false
	}
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		return callReflectFunc(name, m, vc, args)
	}}
	bf.Target = m.Interface()
	if tm, ok := rt.MethodByName(name); ok {
		tm := tm
		bf.Method = &tm
	}
	return bf, true
}

// sizedIntTyp names the builtin typedef for a sized int kind — the
// reflect kind string spells the Go name exactly ("int8", "uint32").
func sizedIntTyp(k reflect.Kind) *runtime.TypeDef {
	return &runtime.TypeDef{Name: k.String(), Kind: runtime.KindNamedBasic}
}

// goValueOf adapts a reflect result to a runtime value: script-native types
// pass through, everything else stays boxed as a host GoValue. Value is
// `any`, so the pass-through list names the concrete runtime types.
func goValueOf(rv reflect.Value) runtime.Value {
	if !rv.IsValid() {
		return runtime.NIL
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			// a nil *T of a named host type, or a nil chan/func of a
			// predeclared spelling, is a typed nil ([HostNil]): it
			// still compares equal to nil, but keeps its dynamic type
			// when stored into an interface and dispatches T's pointer
			// methods on the nil receiver, as in Go. Other nil
			// pointer-ish host values read as plain nil — boxing them
			// would make `v == nil` comparisons lie.
			if rv.Kind() != reflect.Interface {
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
			return runtime.NIL
		}
	}
	x := rv.Interface()
	// a canonical opaque-store key coming back from a container
	// (sync.Map Range callback, a loaded CAS comparand) restores the
	// script value it was rendered from; everything else is not a
	// canonical form and falls through to marshal as usual.
	if orig, ok := runtime.OpaqueKeyOrig(x); ok {
		return orig
	}
	// an anonymous *struct{...} is the projection structDataHost
	// fabricates for a script struct; a host handing it back (a FuncMap
	// func's argument, an `any` load) restores the script value.
	if orig, ok := StructOrig(x); ok {
		return orig
	}
	switch v := x.(type) {
	case nil:
		return runtime.NIL
	case int:
		return int64(v)
	case int64:
		// a concrete int64 keeps its width so %T spells `int64`; a
		// payload unwrapped from an interface is a script int, spelled
		// `int` (both store int64 — only rv's own kind can tell them
		// apart).
		if rv.Kind() == reflect.Interface {
			return v
		}
		return runtime.Tag(sizedIntTyp(reflect.Int64), v)
	case int8, int16, int32:
		// sized ints keep their declared width so %T spells them
		// like Go (int32 also covers rune — an alias). The tag reads
		// the payload's kind: rv may be interface-shaped while x is
		// the concrete payload.
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), pv.Int())
	case uint, uint8, uint16, uint32, uintptr:
		pv := reflect.ValueOf(v)
		return runtime.Tag(sizedIntTyp(pv.Kind()), int64(pv.Uint()))
	case uint64:
		if v <= math.MaxInt64 {
			if rv.Kind() == reflect.Interface {
				return int64(v)
			}
			return runtime.Tag(sizedIntTyp(reflect.Uint64), int64(v))
		}
		return &runtime.GoValue{V: x}
	case string:
		return v
	case bool:
		return v
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(&runtime.TypeDef{Name: "float32", Kind: runtime.KindNamedBasic}, float64(v))
	case float64:
		return v
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return v
	case []byte:
		// []byte unmarshals to a slice of int64s so `string(b)` and
		// indexing behave like Go source suggests. A nil slice is a
		// typed nil so `b == nil` reads like Go.
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("byte")}
		}
		el := make([]runtime.Value, len(v))
		for i, b := range v {
			el[i] = runtime.ByteValue(b)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []string:
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("string")}
		}
		el := make([]runtime.Value, len(v))
		for i, s := range v {
			el[i] = s
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("string")}
	case []any:
		// a `chan any` send deep-hosts script containers (see deepHost);
		// the receive rehydrates the flat slice shape so indexing works.
		if v == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(v))
		for i, e := range v {
			el[i] = goValueOf(reflect.ValueOf(e))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("any")}
	case map[any]any:
		if v == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := runtime.NewMap(nil, false)
		for k, e := range v {
			m.Insert(goValueOf(reflect.ValueOf(k)), goValueOf(reflect.ValueOf(e)))
		}
		return m
	case runtime.ScriptBoxed:
		// a script value the host carried in a proxy (a script error
		// returned by a host Unwrap) reads back as itself.
		return v.ScriptValue()
	case error:
		// errors stay boxed — Error() and Unwrap() dispatch via reflection;
		// errors.Is/As unwrap through goNative.
		return &runtime.GoValue{V: x}
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.TypedNil, *runtime.IfaceNil,
		*runtime.Named, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return v
	case runtime.Named:
		// opaqueStoreArg stored a Named by value so `any` storage keys
		// compare by content — a loaded value re-wraps to the pointer
		// form, keeping its tag for script type asserts.
		return hostbridge.NamedLoadValue(v)
	default:
		// an unnamed host slice/array ([N]T, []T) unboxes element-wise so
		// indexing and range work; a named slice type keeps its box to
		// preserve method dispatch. A nil slice is a typed nil so
		// `v == nil` reads like Go.
		switch rv.Kind() {
		case reflect.Slice:
			if rv.IsNil() && rv.Type().Name() != "" {
				// a nil named host slice (net.ParseIP's IP on bad
				// input) is a typed nil of the host type, as a nil *T
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
			if rv.Type().Name() == "" {
				td := anonSliceTypX(anonExprOf(rv.Type().Elem()))
				td.Elem = hostPkgElemTypedef(rv.Type().Elem())
				if rv.IsNil() {
					return &runtime.TypedNil{Typ: td}
				}
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = goValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: td}
			}
		case reflect.Map:
			if rv.IsNil() && rv.Type().Name() != "" {
				// a nil named host map (a request's Form, Trailer) is a
				// typed nil of the host type, like a nil named slice
				if tn := HostNil(rv.Type()); tn != nil {
					return tn
				}
			}
		case reflect.Array:
			if rv.Type().Name() == "" {
				el := make([]runtime.Value, rv.Len())
				for i := range el {
					el[i] = goValueOf(rv.Index(i))
				}
				return &runtime.Slice{Elems: el, Typ: anonArrayTypX(rv.Len(), anonExprOf(rv.Type().Elem()))}
			}
		}
		return &runtime.GoValue{V: x}
	}
}

// goSlotValueOf is goValueOf for a value read out of a host interface
// slot — a host call's `any` result (sync.Map.Load, context.Value,
// atomic.Value.Load), a callback's `any` param (sync.Map.Range), an
// `any` field. A typed nil held there is a non-nil interface, so it
// reads back boxed as an [runtime.IfaceNil] that keeps its dynamic type:
// `v == nil` is false and `v.(*T)` still matches. goValueOf alone would
// yield the bare typed nil, which equals nil. Script channels also ride
// on `chan any` but keep their element type, so they use goValueOf.
func goSlotValueOf(rv reflect.Value) runtime.Value {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		e := rv.Elem()
		switch e.Kind() {
		case reflect.Chan, reflect.Func:
			// a nil host chan/func of a predeclared spelling
			if e.IsNil() {
				if tn := HostNil(e.Type()); tn != nil {
					return &runtime.IfaceNil{Typ: tn.Typ}
				}
			}
		case reflect.Pointer:
			if e.IsNil() {
				// a nil host *T of a named type (os.Open's file on error)
				if tn := HostNil(e.Type()); tn != nil {
					return &runtime.IfaceNil{Typ: tn.Typ}
				}
			} else if tn, ok := e.Interface().(*runtime.TypedNil); ok && tn.Typ != nil {
				// a script typed nil stored verbatim
				return &runtime.IfaceNil{Typ: tn.Typ}
			}
		}
		// a typed nil stored as a canonical opaque key (a sync.Map
		// value, an atomic.Value) restores to its typed nil — box it
		if orig, ok := runtime.OpaqueKeyOrig(e.Interface()); ok {
			if tn, isTN := orig.(*runtime.TypedNil); isTN && tn.Typ != nil {
				return &runtime.IfaceNil{Typ: tn.Typ}
			}
			return orig
		}
	}
	return goValueOf(rv)
}

// SnapshotOf converts a host Go value into a [VM.Call] argument. The value
// crosses as a snapshot — a copy shaped for the script world — so script
// writes to it never reach the host value. An unnamed host map becomes a
// [runtime.Map] that a map-typed parameter can index, range and write; a
// named map or struct keeps its host box so its methods stay callable.
// Script-native values pass through unchanged.
func SnapshotOf(x any) runtime.Value {
	// The shape is goValueOf's plus one wider unbox: unnamed maps unbox
	// here while host call results still box them — an arg has a declared
	// param shape to satisfy, a result does not. Named maps and structs
	// keep their box for member dispatch, the rule goValueOf applies to
	// named slices.
	return snapshotValue(x, map[uintptr]struct{}{})
}

// snapshotValue is SnapshotOf's worker: it threads the set of
// containers currently being unboxed so one that contains itself
// (reachable only through an `any` slot, since a Go map or slice
// type can't name itself anonymously) boxes at the repeat instead of
// recursing forever. Shared subcontainers still unbox — the set
// forgets each container once its elements are built. Arrays skip
// the check: an `any` slot stores an array by copy, so one can never
// reach itself, and a cycle that passes through an array always
// crosses a guarded slice or map on the way back.
func snapshotValue(x any, visiting map[uintptr]struct{}) runtime.Value {
	// the script's int domain is int64: a host int64 stands for a script
	// int, so it enters bare — tagging it (like goValueOf does for a
	// reflect result) would make `int`-typed params reject the ints
	// callers already pass.
	if v, ok := x.(int64); ok {
		return v
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() == reflect.Map && rv.Type().Name() == "" {
		// recursing through snapshotValue unboxes nested maps too —
		// m["a"]["b"] keeps working at any depth.
		td := anonMapTypX(anonExprOf(rv.Type().Key()), anonExprOf(rv.Type().Elem()))
		if rv.IsNil() {
			return &runtime.TypedNil{Typ: td}
		}
		if _, dup := visiting[rv.Pointer()]; dup {
			return &runtime.GoValue{V: x}
		}
		visiting[rv.Pointer()] = struct{}{}
		m := runtime.NewMap(td, rv.Type().Key().Kind() == reflect.Interface)
		for _, k := range rv.MapKeys() {
			m.Insert(snapshotValue(k.Interface(), visiting), snapshotValue(rv.MapIndex(k).Interface(), visiting))
		}
		delete(visiting, rv.Pointer())
		return m
	}
	if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) && rv.Type().Name() == "" {
		switch rv.Type() {
		case reflect.TypeOf([]byte(nil)), reflect.TypeOf([]string(nil)):
			// the []byte/[]string shapes keep their element-tagged unbox —
			// byte elements arrive tagged uint8, not bare int64s.
		default:
			// every other anonymous slice/array unboxes element-wise.
			// Elements that can carry a nested anonymous container —
			// a map-typed slot, or an `any` slot holding one — route
			// through snapshotValue so `rs[0]["k"]` and `m[k]` index on
			// arrival; primitives keep goValueOf's tagged shape (an
			// int64 in an `any` slot stays `int64`, not bare `int`).
			// The typedef spells the composite element correctly
			// (`[]map[string]interface{}` — an unparseable one-identifier
			// element name never reaches the param check).
			var td *runtime.TypeDef
			if rv.Kind() == reflect.Slice {
				td = anonSliceTypX(anonExprOf(rv.Type().Elem()))
				if rv.IsNil() {
					return &runtime.TypedNil{Typ: td}
				}
				// the same repeat guard the map branch keeps: a slice
				// that holds itself through an `any` slot (s[0] = s)
				// boxes at the repeat instead of overflowing the stack.
				// A len-0 slice can neither recurse nor be a real
				// repeat — s[:0] shares s's base pointer, so it must
				// skip the guard instead of boxing as a false cycle.
				if rv.Len() > 0 {
					if _, dup := visiting[rv.Pointer()]; dup {
						return &runtime.GoValue{V: x}
					}
					visiting[rv.Pointer()] = struct{}{}
					defer delete(visiting, rv.Pointer())
				}
			} else {
				td = anonArrayTypX(rv.Len(), anonExprOf(rv.Type().Elem()))
			}
			et := rv.Type().Elem()
			el := make([]runtime.Value, rv.Len())
			for i := range el {
				ev := rv.Index(i)
				if et.Kind() == reflect.Interface {
					pv := reflect.ValueOf(ev.Interface())
					switch {
					case !pv.IsValid():
						el[i] = runtime.NIL
					case pv.Kind() == reflect.Map || pv.Kind() == reflect.Slice || pv.Kind() == reflect.Array:
						el[i] = snapshotValue(pv.Interface(), visiting)
					default:
						el[i] = goValueOf(pv)
					}
				} else if et.Kind() == reflect.Map || et.Kind() == reflect.Slice || et.Kind() == reflect.Array {
					el[i] = snapshotValue(ev.Interface(), visiting)
				} else {
					el[i] = goValueOf(ev)
				}
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
	}
	return goValueOf(rv)
}

// hostRValueType is the facade's host type: a host []*RValue (MapKeys,
// Call results) spells its elements reflect.Value like Go.
var hostRValueType = reflect.TypeOf((*minireflect.RValue)(nil))

// hostField finds a field by name on a host value: pointer and
// interface chains are dereferenced to the struct underneath. nil or
// non-struct roots report not-found. Exported fields always resolve;
// unexported ones resolve only for callers in the field's own package
// (Go's visibility rule), read through the field's address so
// interpreted stdlib code can see what it stamped — net/http's
// ServeMux reading back the r.pat it wrote on a host *http.Request.
func hostField(v any, name, pkg string) (reflect.Value, bool) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	fv := rv.FieldByName(name)
	if !fv.IsValid() {
		return reflect.Value{}, false
	}
	if fv.CanInterface() {
		return fv, true
	}
	sf, _ := rv.Type().FieldByName(name)
	if pkg == "" || sf.PkgPath != pkg || !fv.CanAddr() {
		return reflect.Value{}, false
	}
	return reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem(), true
}

// initHostLiteral writes keyed composite-literal fields onto a
// host-created value (`&sync.Pool{New: f}`): the pointer chain is
// dereferenced to a settable struct, each key must name an exported
// field, and the value marshals to the field's type (a script func for
// New, via toReflectValue's adaptFunc).
func (v *VM) initHostLiteral(f *frame, td *runtime.TypeDef, hv any, raw []runtime.Value) {
	rv := reflect.ValueOf(hv)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		f.trap("cannot initialize host type %s with fields", td.Name)
	}
	for i := 0; i < len(raw); i += 2 {
		name, ok := raw[i].(string)
		if !ok {
			f.trap("struct literal key %T", raw[i])
		}
		fv := rv.FieldByName(name)
		if !fv.IsValid() {
			f.trap("%s has no field %s", td.Name, name)
		}
		if !fv.CanSet() {
			f.trap("cannot set unexported field %s of host type %s", name, td.Name)
		}
		val, err := hostFieldValue(raw[i+1], rv.Type(), name, fv.Type(), v)
		if err != nil {
			f.trap("%s.%s: %s", td.Name, name, err)
		}
		fv.Set(val)
	}
}

// initHostPositional fills a host-backed struct literal `T{v1, v2, ...}`:
// Go requires exactly NumField values, each assignable to the field in
// declaration order (unicode.RangeTable entries arrive this way).
func (v *VM) initHostPositional(f *frame, td *runtime.TypeDef, hv any, raw []runtime.Value) {
	rv := reflect.ValueOf(hv)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		f.trap("cannot initialize host type %s with fields", td.Name)
	}
	if len(raw) != rv.NumField() {
		f.trap("wrong number of fields in literal of host type %s: %d != %d", td.Name, len(raw), rv.NumField())
	}
	for i, e := range raw {
		fv := rv.Field(i)
		if !fv.CanSet() {
			f.trap("implicit assignment to unexported field %s of host type %s", rv.Type().Field(i).Name, td.Name)
		}
		val, err := toReflectValue(e, fv.Type(), v)
		if err != nil {
			f.trap("%s.%s: %s", td.Name, rv.Type().Field(i).Name, err)
		}
		fv.Set(val)
	}
}

// hostConstKindNames names the basic type an untyped constant converts
// to for a host parameter of each integer or float kind.
var hostConstKindNames = map[reflect.Kind]string{
	reflect.Int: "int", reflect.Int8: "int8", reflect.Int16: "int16",
	reflect.Int32: "int32", reflect.Int64: "int64",
	reflect.Uint: "uint", reflect.Uint8: "uint8", reflect.Uint16: "uint16",
	reflect.Uint32: "uint32", reflect.Uint64: "uint64", reflect.Uintptr: "uintptr",
	reflect.Float32: "float32", reflect.Float64: "float64",
}

// maxHostConvertDepth caps the container/struct recursion a script
// value can drive into a host type: a cyclic twin (type T struct {
// next *T } with n.next = n) would otherwise recurse until the
// goroutine stack gives out — an uncatchable death, not a trap.
const maxHostConvertDepth = 128

// toReflectValue marshals a runtime value to a reflect.Value of the
// requested type: empty interfaces carry the value verbatim (a *Cell stays
// a pointer, slices/maps cross unconverted), func types wrap the callable
// so the host can invoke it back on the calling VM (vc), named/cell
// wrappers unwrap, slices and maps convert element-wise, scalars assign or
// convert.
func toReflectValue(v runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	return toReflectValueDepth(v, t, vc, 0)
}

func toReflectValueDepth(v runtime.Value, t reflect.Type, vc runtime.VMCaller, depth int) (reflect.Value, error) {
	if depth > maxHostConvertDepth {
		return reflect.Value{}, fmt.Errorf("cannot convert to %s: value nesting too deep", t)
	}
	if u, ok := v.(*runtime.UConst); ok {
		if rv, ok, err := hostConstArg(u, t); ok {
			return rv, err
		}
		mv, err := materializeDefault(u)
		if err != nil {
			return reflect.Value{}, err
		}
		v = mv
	}
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
		if v == nil || v == runtime.NIL {
			return reflect.Zero(t), nil
		}
		// a host func taking `any` can only consume the value through
		// reflect, so script containers marshal to real host values —
		// slices as []any, maps as map[any]any. Pointer-like values stay
		// verbatim: their address-ness is the point. callReflectFunc
		// copies a converted slice's elements back after the call.
		v = deepHost(v, vc, &hostMarshal{
			walk:   runtime.NewWalk(),
			budget: hostEvalBudget,
		})
		if v == nil || v == runtime.NIL {
			return reflect.Zero(t), nil
		}
		av := reflect.ValueOf(v)
		if !av.IsValid() {
			return reflect.Zero(t), nil
		}
		out := reflect.New(t).Elem()
		out.Set(av)
		return out, nil
	}
	if t.Kind() == reflect.Func {
		return adaptFunc(v, t, vc)
	}
	// holder keeps the pointer-like node (Cell/FieldRef/IndexRef) the
	// final value was loaded through: when the parameter wants a pointer
	// the callee can write through, a fresh host pointer is built here
	// and callReflectFunc copies the pointee back into holder.
	// orig keeps the arg as passed — a `&w` cell, a field ref — so the
	// interface adaptation below sees the arg's method set under Go's
	// receiver rule (a pointer's set includes pointer receivers).
	// namedTyp keeps the outermost named tag the unwrap peels: a script
	// struct reached through `type P pattern` is typed P, not pattern,
	// so the source-twin conversion below must not claim it for
	// pattern's host type. Unnamed typedefs (a *T wrapper) name nothing,
	// so only a named tag counts.
	var holder runtime.Value
	var namedTyp *runtime.TypeDef
	orig := v
	for {
		if n, ok := v.(*runtime.Named); ok {
			if namedTyp == nil && n.Typ != nil && n.Typ.Name != "" {
				namedTyp = n.Typ
			}
			v = n.V
			continue
		}
		switch x := v.(type) {
		case *runtime.UConst:
			// a typed constant keeps its constant-domain payload under
			// the type tag — time.Duration(5) is Named{time.Duration,
			// UConst} — so it converts at the parameter's type the same
			// way a bare untyped constant does above.
			if rv, ok, err := hostConstArg(x, t); ok {
				return rv, err
			}
			mv, err := materializeDefault(x)
			if err != nil {
				return reflect.Value{}, err
			}
			v = mv
			continue
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			holder = v // last ref wins — the deepest pointer chain link
		}
		if dv, ok := runtime.Deref(v); ok {
			v = dv
			continue
		}
		break
	}
	var av reflect.Value
	switch x := v.(type) {
	case nil, runtime.Nil:
		return reflect.Zero(t), nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		return reflect.Zero(t), nil
	case *runtime.GoValue:
		av = reflect.ValueOf(x.V)
	case *runtime.Slice:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return reflect.Value{}, fmt.Errorf("cannot convert script slice to %s", t)
		}
		out := reflect.New(t).Elem()
		// byte slices marshal in one pass — the element loop below
		// builds a reflect.Value per byte otherwise (issue #362).
		if t.Elem().Kind() == reflect.Uint8 {
			if bs, ok := scriptBytes(x); ok {
				if t.Kind() == reflect.Slice {
					av = reflect.ValueOf(bs)
				} else {
					reflect.Copy(out, reflect.ValueOf(bs))
					av = out
				}
				break
			}
		}
		if t.Kind() == reflect.Slice {
			out.Set(reflect.MakeSlice(t, len(x.Elems), len(x.Elems)))
		}
		for i, e := range x.Elems {
			if i >= out.Len() {
				break
			}
			ev, err := toReflectValueDepth(e, t.Elem(), vc, depth+1)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(ev)
		}
		av = out
	case *runtime.Map:
		if t.Kind() != reflect.Map {
			return reflect.Value{}, fmt.Errorf("cannot convert script map to %s", t)
		}
		out := reflect.MakeMapWithSize(t, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			kv, err := toReflectValueDepth(k, t.Key(), vc, depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("map key: %w", err)
			}
			var ev reflect.Value
			if funcMapTypes[t] {
				ev, err = funcMapElem(e, vc)
			} else {
				ev, err = toReflectValueDepth(e, t.Elem(), vc, depth+1)
			}
			if err != nil {
				return reflect.Value{}, fmt.Errorf("map elem: %w", err)
			}
			out.SetMapIndex(kv, ev)
		}
		av = out
	case *runtime.Struct:
		// a source twin of a host struct type — an interpreted
		// net/http.pattern for the real net/http.pattern — packs into a
		// fresh host struct field-by-field, so interpreted stdlib writes
		// like `r.pat = pat` land real values host code reads back
		// (Request.PathValue). The fresh struct is a snapshot, not an
		// alias: mutating the script value later does not reach the
		// stamped copy. A value tagged by a different named type — a
		// `type P pattern` conversion — is not a twin either, matching
		// gc's named-type unassignability; and a look-alike of another
		// package stays unconvertible, like gc's type error.
		var hst reflect.Type
		if t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct {
			hst = t.Elem()
		} else if t.Kind() == reflect.Struct {
			hst = t
		}
		hostNamedIs := runtime.HostNamedIs
		if tw, ok := vc.(runtime.HostNamedTwinner); ok {
			hostNamedIs = tw.HostNamedTwin
		}
		if hst == nil || !hostNamedIs(hst, x.Def) || (namedTyp != nil && namedTyp != x.Def) {
			av = reflect.ValueOf(x)
			break
		}
		pv := reflect.New(hst)
		if err := fillHostStruct(pv.Elem(), x, vc, depth); err != nil {
			return reflect.Value{}, err
		}
		if t.Kind() == reflect.Pointer {
			av = pv
		} else {
			av = pv.Elem()
		}
	case string, bool, int64, float64:
		av = reflect.ValueOf(x)
	default:
		av = reflect.ValueOf(x)
	}
	if !av.IsValid() {
		return reflect.Zero(t), nil
	}
	if av.Type().AssignableTo(t) {
		return av, nil
	}
	// a host-typed composite literal boxes `*T`; placing it inside a
	// `T` field/element (e.g. []unicode.Range16{{...}}) needs the pointee.
	if av.Kind() == reflect.Pointer && !av.IsNil() && av.Type().Elem().AssignableTo(t) {
		return av.Elem(), nil
	}
	if av.Type().ConvertibleTo(t) {
		return av.Convert(t), nil
	}
	// `&b` / pointer-var args cross as the pointee value — rebuild a real
	// host pointer when the parameter wants one (a *T param or an
	// interface the pointer type implements, like io.Writer). The temp
	// points at a copy; callReflectFunc writes it back to holder.
	if holder != nil {
		if t.Kind() == reflect.Pointer && av.Type().ConvertibleTo(t.Elem()) {
			pv := reflect.New(t.Elem())
			pv.Elem().Set(av.Convert(t.Elem()))
			return pv, nil
		}
		if pt := reflect.PointerTo(av.Type()); pt.AssignableTo(t) {
			pv := reflect.New(av.Type())
			pv.Elem().Set(av)
			return pv, nil
		}
	}
	// a script value that declares the interface's methods adapts to a
	// live proxy: host code calling back into script — bufio.Reset on an
	// interpreted chunkWriter, or io.Copy fed an interpreted body — can
	// reach the script methods through it.
	if t.Kind() == reflect.Interface && t.NumMethod() > 0 {
		if t == errorType {
			// a script error crossing into a host `error` slot (a
			// CancelCauseFunc's cause) boxes like every other script
			// error the host sees, so errors.Is/== agree on it.
			if m := vmOf(vc); m != nil && m.H.HostErr != nil {
				if _, ok := runtime.IfaceMember(vc, orig, "Error"); ok {
					if err := m.H.HostErr(vc, orig); err != nil {
						return reflect.ValueOf(&err).Elem(), nil
					}
				}
			}
		}
		if pv, ok := adaptIface(orig, t, vc); ok {
			return pv, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", av.Type(), t)
}

// adaptIface reports a scriptIface-family proxy for v when v declares
// every method interface t requires. Pure reflect cannot fabricate
// interface impls, so the adapter's static method set must contain t's
// — and the proxy variant is picked by whether the script declares the
// OPTIONAL-probe methods (io.WriterTo/io.ReaderFrom, asserted by
// io.Copy and friends): a Read-only script value adapted to io.Reader
// must NOT satisfy WriterTo, or io.Copy calls a method that does not
// exist and returns its error instead of copying through Read.
func adaptIface(v runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, bool) {
	if vc == nil {
		return reflect.Value{}, false
	}
	// script containers only — a struct value or a pointer box (cell,
	// field/index ref) or named tag carrying one. The IfaceMember probes
	// below apply the receiver rule to whatever box arrived.
	switch v.(type) {
	case *runtime.Struct, *runtime.Named,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
	default:
		return reflect.Value{}, false
	}
	// v is the arg as passed — a bare *Struct or a pointer box wrapping
	// it. Go stores the pointer when the caller passes &s, so the
	// adapter's declared surface must come from that box's method set.
	si := &scriptIface{vc: vc, recv: v}
	// the probe answers "does the arg offer this method" — the value's
	// method set under Go's receiver rule, so a bare struct no longer
	// advertises a WriteTo it could never satisfy.
	_, hasWT := runtime.IfaceMember(vc, v, "WriteTo")
	_, hasRF := runtime.IfaceMember(vc, v, "ReadFrom")
	var proxy any = si
	switch {
	case hasWT && hasRF:
		proxy = scriptIfaceWTRF{si}
	case hasWT:
		proxy = scriptIfaceWT{si}
	case hasRF:
		proxy = scriptIfaceRF{si}
	}
	st := reflect.TypeOf(proxy)
	if !st.Implements(t) {
		// the fs.FS family sits beside the io surface above: a script
		// value declaring Open — an interpreted embed.FS, or a script
		// type with an Open method — adapts through scriptFS. It is
		// probed only when the io proxy cannot serve t, so a script
		// io.Reader keeps the honest WriteTo/ReadFrom surface.
		if fsv := scriptFSFor(vc, v); fsv != nil && reflect.TypeOf(fsv).Implements(t) {
			proxy = fsv
			st = reflect.TypeOf(proxy)
		}
		if !st.Implements(t) {
			// fs.File beside it: a script value declaring Stat — an
			// embed openFile, a script file type, or a file a host
			// handed back — adapts through the same per-file probe
			// Open results get.
			if fv := scriptFileFor(vc, v); fv != nil && reflect.TypeOf(fv).Implements(t) {
				proxy = fv
				st = reflect.TypeOf(proxy)
			}
		}
	}
	if !st.Implements(t) {
		return reflect.Value{}, false
	}
	for i := 0; i < t.NumMethod(); i++ {
		if _, ok := runtime.IfaceMember(vc, v, t.Method(i).Name); !ok {
			return reflect.Value{}, false
		}
	}
	out := reflect.New(t).Elem()
	out.Set(reflect.ValueOf(proxy))
	return out, true
}

// errOut extracts the error slot of a script (x, err) pair: host error
// sentinels (io.EOF) arrive as GoValues and pass through verbatim so
// identity comparisons host-side still hold; a script error value (a
// boxed typed nil included — it is a non-nil error) goes through
// hostErr so it reads back as itself once the host returns it.
func errOut(vc runtime.VMCaller, rs []runtime.Value, i int) error {
	if i >= len(rs) {
		return nil
	}
	switch e := runtime.Unwrap(rs[i]).(type) {
	case nil, runtime.Nil, *runtime.TypedNil:
		return nil
	case *runtime.IfaceNil:
		if runtime.BoxedNilTyp(e) == nil {
			return nil
		}
		return hostErr(vc, rs[i])
	case *runtime.GoValue:
		if err, ok := e.V.(error); ok {
			return err
		}
		return fmt.Errorf("%v", e.V)
	default:
		return hostErr(vc, rs[i])
	}
}

// hostErr boxes a script error value for host code through the
// engine's HostErr hook, falling back to the scriptIface proxy (whose
// Error forwards to the script method).
func hostErr(vc runtime.VMCaller, v runtime.Value) error {
	vc = callbackContext(vc)
	if m := vmOf(vc); m != nil && m.H.HostErr != nil {
		return m.H.HostErr(vc, v)
	}
	return &scriptIface{vc: vc, recv: v}
}

var errorType = reflect.TypeFor[error]()

// vmOf reports the VM behind a caller (nil for a foreign caller).
func vmOf(vc runtime.VMCaller) *VM {
	if b, ok := vc.(ownerVMBearer); ok {
		return b.ownerVM()
	}
	return nil
}

func intOut(rs []runtime.Value, i int) int64 {
	if i >= len(rs) {
		return 0
	}
	switch n := runtime.Unwrap(rs[i]).(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// byteSliceArg boxes a host []byte as a script slice of byte-tagged
// elements — the shape a script `[]byte` parameter receives.
func byteSliceArg(p []byte) *runtime.Slice {
	el := make([]runtime.Value, len(p))
	for i, b := range p {
		el[i] = runtime.ByteValue(b)
	}
	return &runtime.Slice{Elems: el, Typ: &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("byte")}}}
}

// backBytes mirrors a script callee's writes to the boxed slice back
// into the host buffer — the []byte crossing is element-copied, so
// fills the callee performed would otherwise be invisible to the host.
func backBytes(dst []byte, src runtime.Value, n int) {
	sl, ok := src.(*runtime.Slice)
	if !ok {
		return
	}
	for i := 0; i < n && i < len(sl.Elems) && i < len(dst); i++ {
		if b, ok := runtime.Unwrap(sl.Elems[i]).(int64); ok {
			dst[i] = byte(b)
		}
	}
}

// strOut/boolOut/fileModeOut extract scalar slots of a script method's
// results — intOut's siblings for the fs proxies' non-int returns. A
// constant-domain or host-boxed FileMode still reads correctly.
func strOut(rs []runtime.Value, i int) string {
	if i >= len(rs) {
		return ""
	}
	str, _ := runtime.Unwrap(rs[i]).(string)
	return str
}

func boolOut(rs []runtime.Value, i int) bool {
	if i >= len(rs) {
		return false
	}
	b, _ := runtime.Unwrap(rs[i]).(bool)
	return b
}

func fileModeOut(rs []runtime.Value, i int) fs.FileMode {
	if i >= len(rs) {
		return 0
	}
	v := runtime.Unwrap(rs[i])
	if u, ok := v.(*runtime.UConst); ok {
		if mv, err := materializeDefault(u); err == nil {
			v = runtime.Unwrap(mv)
		}
	}
	switch m := v.(type) {
	case int64:
		return fs.FileMode(uint32(m))
	case *runtime.GoValue:
		if fm, ok := m.V.(fs.FileMode); ok {
			return fm
		}
	}
	return 0
}

// hostEvalBudget bounds the eager method calls one marshal may run.
// templateCallable methods are invoked at marshal time (see
// structDataHost), and a method returning a fresh object would expand
// that object's methods the same way, so without a cap a
// method-per-object chain never terminates — the earlier unbudgeted
// attempt hung on interpreted net/http structs.
const hostEvalBudget = 64

// hostMarshal carries the state one deepHost walk shares: walk folds
// cyclic values back to the projection already built for them — a host
// map or slice may legally contain itself — and budget caps the eager
// template-method calls described above.
type hostMarshal struct {
	walk   *runtime.Walk
	budget int
}

// deepHost converts a script value for an `any` parameter: containers
// become real host values (Slice → []any, Map → map[any]any) so the
// callee can reflect over them; Named and GoValue unwrap; typed nils
// read as nil (a nil slice or map as its typed zero, see hostNilOf);
// structs marshal to fabricated *struct{...} values (see
// structDataHost).
// Everything else — funcs, non-struct pointers — stays verbatim.
func deepHost(v runtime.Value, vc runtime.VMCaller, h *hostMarshal) runtime.Value {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return nil
	case *runtime.TypedNil:
		return hostNilOf(x.Typ, vc)
	case *runtime.IfaceNil:
		// `var a any = s` with a nil s []string: the same typed zero
		// (coerce turns the TypedNil into a concretely tagged IfaceNil).
		return hostNilOf(x.Typ, vc)
	case *runtime.UConst:
		mv, err := materializeDefault(x)
		if err != nil {
			panic(&runtime.Panic{Value: &runtime.GoValue{V: err}})
		}
		return deepHost(mv, vc, h)
	case *runtime.Named:
		// a named func value keeps its tag across the boundary: `any`
		// storage (sync.Map and friends) must hand back the same
		// interface value, so `Decompressor(f)` stored and loaded
		// still asserts to zip.Decompressor — an unnamed func value
		// would not. Other payloads marshal through like before.
		switch x.V.(type) {
		case *runtime.Function, *runtime.Closure,
			*runtime.BoundMethod, *runtime.BuiltinFunc:
			// an iter.Seq / iter.Seq2 crosses as a real host seq func,
			// so a host ranging over it (text/template's {{range}})
			// can drive it through reflect.
			if hf, ok := hostSeqFunc(x, vc); ok {
				return hf
			}
			return v
		}
		// a bound host scalar (time.Duration, time.Month) crosses as
		// the host type — its int64 or constant payload alone would
		// reach the callee as a bare int64, losing String and %T.
		if x.Typ != nil && x.Typ.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(x.Typ, x.V); ok {
				return hv
			}
		}
		// a sized basic (int8, float32, rune) or a script type defined
		// over one (`type N int8`) crosses at that basic Go type — the
		// payload alone is the widened int64/float64 the VM computes in.
		// A script type's own name has no host equivalent.
		if rt := hostBasicTypes[runtime.BasicNameOf(x.Typ)]; rt != nil && x.Typ.HostNew == nil {
			if rv, ok := convertBasic(deepHost(x.V, vc, h), rt); ok {
				return rv.Interface()
			}
		}
		return deepHost(x.V, vc, h)
	case int64:
		// a bare int64 is a script `int` (sized ints carry a tag)
		return int(x)
	case *runtime.GoValue:
		return x.V
	case *runtime.Struct:
		return structDataHost(x, x, vc, h)
	case *runtime.Slice, *runtime.Map:
		if out, ok := h.typedContainer(v, vc); ok {
			return out
		}
		out, _ := h.walk.Container(v, func(e runtime.Value) any {
			return deepHost(e, vc, h)
		}, runtime.UnwrapKey)
		return out
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		// a script pointer to a struct can't offer the host its
		// address — only data — so it marshals like the struct value,
		// except the pointer keeps its wider method set (pointer
		// receivers) exactly as Go's *T does.
		if dv, ok := runtime.Deref(x); ok {
			if s, ok := dv.(*runtime.Struct); ok {
				return structDataHost(x, s, vc, h)
			}
		}
		return v
	}
	return v
}

// hostNilOf is the host value a nil slice or map of type td crosses an
// `any` as: its typed zero, as in Go, where a nil []Pt in an interface is
// not a nil interface — a nil []string field reaches text/template's
// {{join .Tags}} as a []string, and {{len .Users}} counts a nil []Pt as 0.
// A type with a host shape ([hostShapeOf]) keeps it; any other falls back
// to the generic projection's []any / map[any]any, the shape a non-nil
// value of the same type takes. Every other nil reads as a bare nil.
func hostNilOf(td *runtime.TypeDef, vc runtime.VMCaller) any {
	if td == nil {
		return nil
	}
	if rt := HostNilType(td); rt != nil {
		// a nil named host map or slice (a request's Form, net.IP)
		return reflect.Zero(rt).Interface()
	}
	v := vmOf(vc)
	if v != nil {
		td = v.peelAlias(td)
	}
	switch td.Kind {
	case runtime.KindSlice:
		if at, ok := td.Anon.(*ast.ArrayType); ok && at.Len != nil {
			return nil // an array has no nil
		}
		if rt := cachedHostShape(v, td); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		return []any(nil)
	case runtime.KindMap:
		if rt := cachedHostShape(v, td); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		return map[any]any(nil)
	}
	return nil
}

// hostSeqFunc adapts a script iter.Seq / iter.Seq2 value to the host
// func(yield func(any) bool) / func(yield func(any, any) bool) shape: the
// host's yield receives each element marshaled like any other `any`
// data, and its false return stops the script producer. A failure inside
// the seq propagates as a panic, as a callback's would. ok is false for a
// value not typed as a seq.
func hostSeqFunc(n *runtime.Named, vc runtime.VMCaller) (any, bool) {
	arity := runtime.IterSeqArity(n.Typ)
	if arity == 0 {
		return nil, false
	}
	seq := runtime.Value(n)
	toHost := func(e runtime.Value, c runtime.VMCaller) any {
		return deepHost(e, c, &hostMarshal{walk: runtime.NewWalk(), budget: hostEvalBudget})
	}
	drive := func(yield func(runtime.VMCaller, []runtime.Value) bool) {
		// a host panic out of yield (text/template unwinds {{break}}
		// with one) must reach the host's recover unchanged: stop the
		// producer and re-raise it once the VM call is out of the way.
		var hostPanic any
		panicked := false
		yb := &runtime.BuiltinFunc{Name: "host yield", Fn: func(c runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			goon := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						hostPanic, panicked = r, true
					}
				}()
				goon = yield(c, args)
			}()
			return goon && !panicked, nil
		}}
		_, err := vc.Call(seq, []runtime.Value{yb})
		if panicked {
			panic(hostPanic)
		}
		if err != nil {
			panic(err)
		}
	}
	at := func(args []runtime.Value, i int) runtime.Value {
		if i < len(args) {
			return args[i]
		}
		return runtime.NIL
	}
	// each element crosses at its host type when it has one (a
	// predeclared basic, so a template sees int, not interface {}),
	// else as any; the seq itself is the host's real iter.Seq[...] type
	// when the binary has that instantiation (%T then matches).
	elemTs := make([]reflect.Type, arity)
	els := runtime.IterSeqElemTypes(n.Typ)
	for i := range elemTs {
		elemTs[i] = anyType
		if i < len(els) {
			if rt := runtime.HostBasicType(els[i]); rt != nil {
				elemTs[i] = rt
			}
		}
	}
	seqT := runtime.HostIterSeqType(n.Typ)
	if seqT == nil {
		yieldT := reflect.FuncOf(elemTs, []reflect.Type{reflect.TypeFor[bool]()}, false)
		seqT = reflect.FuncOf([]reflect.Type{yieldT}, nil, false)
	}
	return reflect.MakeFunc(seqT, func(in []reflect.Value) []reflect.Value {
		y := in[0]
		vals := make([]reflect.Value, arity)
		drive(func(c runtime.VMCaller, args []runtime.Value) bool {
			for i, et := range elemTs {
				vals[i] = reflect.Zero(et)
				if rv := reflect.ValueOf(toHost(at(args, i), c)); rv.IsValid() {
					if rv.Type().AssignableTo(et) {
						vals[i] = rv
					} else if rv.Type().ConvertibleTo(et) {
						vals[i] = rv.Convert(et)
					}
				}
			}
			return y.Call(vals)[0].Bool()
		})
		return nil
	}).Interface(), true
}

var (
	poolType = reflect.TypeFor[sync.Pool]()
	anyType  = reflect.TypeFor[any]()
)

// hostBasicTypes maps the predeclared basic type names to their Go types.
var hostBasicTypes = map[string]reflect.Type{
	"bool": reflect.TypeFor[bool](), "string": reflect.TypeFor[string](),
	"int": reflect.TypeFor[int](), "int8": reflect.TypeFor[int8](), "int16": reflect.TypeFor[int16](),
	"int32": reflect.TypeFor[int32](), "int64": reflect.TypeFor[int64](), "rune": reflect.TypeFor[rune](),
	"uint": reflect.TypeFor[uint](), "uint8": reflect.TypeFor[uint8](), "uint16": reflect.TypeFor[uint16](),
	"uint32": reflect.TypeFor[uint32](), "uint64": reflect.TypeFor[uint64](), "byte": reflect.TypeFor[byte](),
	"uintptr": reflect.TypeFor[uintptr](), "float32": reflect.TypeFor[float32](), "float64": reflect.TypeFor[float64](),
	"complex64": reflect.TypeFor[complex64](), "complex128": reflect.TypeFor[complex128](),
}

// basicClass groups reflect kinds whose values convert into each other
// without changing meaning: the VM computes every integer in int64 and
// every float in float64, so only the width (or signedness) differs.
func basicClass(k reflect.Kind) int {
	switch {
	case k >= reflect.Int && k <= reflect.Uintptr:
		return 1
	case k == reflect.Float32 || k == reflect.Float64:
		return 2
	case k == reflect.Complex64 || k == reflect.Complex128:
		return 3
	case k == reflect.String:
		return 4
	case k == reflect.Bool:
		return 5
	}
	return 0
}

// convertBasic converts hv, a marshaled scalar, to the basic or host
// scalar type rt when both sit in the same basic class; ok is false for
// anything else.
func convertBasic(hv any, rt reflect.Type) (reflect.Value, bool) {
	rv := reflect.ValueOf(hv)
	if !rv.IsValid() {
		return reflect.Value{}, false
	}
	if rv.Type() == rt {
		return rv, true
	}
	if c := basicClass(rv.Kind()); c != 0 && c == basicClass(rt.Kind()) {
		return rv.Convert(rt), true
	}
	return reflect.Value{}, false
}

// hostShapeOf returns the Go type a script value of type td can cross a
// host `any` parameter as: a basic type (or a script type defined over
// one), a bound host type, the empty interface, and slices, arrays and
// maps built from those. A script type defined over a basic yields that
// basic (its name and methods have no host equivalent). nil when some
// part has no host equivalent at all — a script struct, a pointer — and
// the value then marshals through the generic []any / map[any]any shape.
func hostShapeOf(v *VM, td *runtime.TypeDef, depth int) reflect.Type {
	if td == nil || depth > 8 {
		return nil
	}
	if rt := hostTypOf(td); rt != nil {
		return rt
	}
	if rt := hostBasicTypes[runtime.BasicNameOf(td)]; rt != nil {
		return rt
	}
	x := td.TypeExpr()
	switch t := x.(type) {
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return anyType
		}
		return nil
	case *ast.Ident:
		if t.Name == "any" && td.Spec == nil {
			return anyType
		}
	}
	if td.Name == "any" && td.Spec == nil {
		return anyType
	}
	if v == nil || v.H.ElemOf == nil {
		return nil
	}
	switch td.Kind {
	case runtime.KindSlice:
		at, ok := x.(*ast.ArrayType)
		if !ok {
			return nil
		}
		et, err := v.H.ElemOf(td)
		if err != nil {
			return nil
		}
		ert := hostShapeOf(v, et, depth+1)
		if ert == nil {
			return nil
		}
		if at.Len == nil {
			return reflect.SliceOf(ert)
		}
		lit, ok := runtime.ArrayLenExpr(at).(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return nil
		}
		n, err := strconv.ParseInt(lit.Value, 0, 64)
		if err != nil || n < 0 {
			return nil
		}
		return reflect.ArrayOf(int(n), ert)
	case runtime.KindMap:
		et, err := v.H.ElemOf(td)
		if err != nil {
			return nil
		}
		ert := hostShapeOf(v, et, depth+1)
		krt := hostShapeOf(v, v.mapKeyTypedef(nil, td), depth+1)
		if ert == nil || krt == nil || !krt.Comparable() {
			return nil
		}
		return reflect.MapOf(krt, ert)
	}
	return nil
}

// hostShapes caches [hostShapeOf] per container typedef — resolving the
// element and key types runs the type hooks, which costs more than
// marshaling a small map. Typedefs minted per value (host results) can
// be many, so the cache stops growing at hostShapeCap entries.
var (
	hostShapes    sync.Map // *runtime.TypeDef -> reflect.Type (nil: no shape)
	hostShapeSize atomic.Int64
)

const hostShapeCap = 4096

func cachedHostShape(v *VM, td *runtime.TypeDef) reflect.Type {
	if td == nil {
		return nil
	}
	if rt, ok := hostShapes.Load(td); ok {
		t, _ := rt.(reflect.Type)
		return t
	}
	rt := hostShapeOf(v, td, 0)
	// without a VM the type hooks are out of reach and the answer is a
	// provisional nil — keep it out of the cache.
	if v != nil && hostShapeSize.Load() < hostShapeCap {
		if _, loaded := hostShapes.LoadOrStore(td, rt); !loaded {
			hostShapeSize.Add(1)
		}
	}
	return rt
}

// typedContainer marshals a script slice, array or map whose type has a
// host shape ([hostShapeOf]) into a real Go value of that type —
// []int, [2]string, map[string]int — so the callee sees the same type gc
// would pass. ok is false when there is no shape or an element does not
// fit it; the caller then falls back to the generic []any / map[any]any.
// A container that can hold itself (an `any` element) registers with
// the walk before its elements marshal, so a cycle folds back to it.
func (h *hostMarshal) typedContainer(v runtime.Value, vc runtime.VMCaller) (any, bool) {
	if old, ok := h.walk.Seen(v); ok {
		return old, true
	}
	var td *runtime.TypeDef
	switch x := v.(type) {
	case *runtime.Slice:
		if x.Virtual() {
			return nil, false
		}
		td = x.Typ
	case *runtime.Map:
		td = x.Typ
	}
	rt := cachedHostShape(vmOf(vc), td)
	if rt == nil {
		return nil, false
	}
	switch x := v.(type) {
	case *runtime.Slice:
		switch rt.Kind() {
		case reflect.Slice:
			if out, ok := basicSliceFill(rt, x.Elems); ok {
				return out, true
			}
			out := reflect.MakeSlice(rt, len(x.Elems), len(x.Elems))
			if rt.Elem().Kind() == reflect.Interface {
				// only an `any` element can lead back to this slice, and
				// an `any` slot accepts every element: no fallback later.
				h.walk.Register(x, out.Interface())
			}
			for i, e := range x.Elems {
				if !h.setHost(out.Index(i), e, vc) {
					return nil, false
				}
			}
			return out.Interface(), true
		case reflect.Array:
			if rt.Len() != len(x.Elems) {
				return nil, false
			}
			out := reflect.New(rt).Elem()
			for i, e := range x.Elems {
				if !h.setHost(out.Index(i), e, vc) {
					return nil, false
				}
			}
			return out.Interface(), true
		}
	case *runtime.Map:
		if rt.Kind() != reflect.Map {
			return nil, false
		}
		if rt == stringAnyMapType {
			if out, ok := h.stringAnyMap(x, vc); ok {
				return out, true
			}
		}
		ifaceKey := rt.Key().Kind() == reflect.Interface
		// keys convert first: a key that does not fit makes the whole
		// map fall back before anything registers with the walk.
		keys := make([]reflect.Value, x.Len())
		for i := range keys {
			k, _ := x.At(i)
			kv := reflect.New(rt.Key()).Elem()
			if ifaceKey {
				// an `any` key keeps the generic projection's canonical
				// key form — a script struct key must stay hashable.
				if uk := runtime.UnwrapKey(k); uk != nil {
					kv.Set(reflect.ValueOf(uk))
				}
			} else if !h.setHost(kv, k, vc) {
				return nil, false
			}
			keys[i] = kv
		}
		out := reflect.MakeMapWithSize(rt, x.Len())
		if rt.Elem().Kind() == reflect.Interface {
			h.walk.Register(x, out.Interface())
		}
		ev := reflect.New(rt.Elem()).Elem()
		for i, kv := range keys {
			_, e := x.At(i)
			ev.SetZero()
			if !h.setHost(ev, e, vc) {
				// only a concrete element can fail, and then nothing
				// registered this map
				return nil, false
			}
			out.SetMapIndex(kv, ev)
		}
		return out.Interface(), true
	}
	return nil, false
}

var stringAnyMapType = reflect.TypeFor[map[string]any]()

// stringAnyMap fills a native map[string]any — the common template data
// shape — with no reflect call per entry. ok is false (and nothing is
// registered) when a key is not a plain script string.
func (h *hostMarshal) stringAnyMap(x *runtime.Map, vc runtime.VMCaller) (map[string]any, bool) {
	n := x.Len()
	for i := 0; i < n; i++ {
		k, _ := x.At(i)
		if _, ok := k.(string); !ok {
			return nil, false
		}
	}
	out := make(map[string]any, n)
	h.walk.Register(x, out)
	for i := 0; i < n; i++ {
		k, e := x.At(i)
		out[k.(string)] = deepHost(e, vc, h)
	}
	return out, true
}

// basicSliceFill builds the common basic slices ([]int, []string,
// []float64, []bool) natively when every element is a raw script scalar
// — no reflect call per element. ok is false for any other shape.
func basicSliceFill(rt reflect.Type, elems []runtime.Value) (any, bool) {
	switch rt {
	case reflect.TypeFor[[]int]():
		out := make([]int, len(elems))
		for i, e := range elems {
			n, ok := e.(int64)
			if !ok {
				return nil, false
			}
			out[i] = int(n)
		}
		return out, true
	case reflect.TypeFor[[]string]():
		out := make([]string, len(elems))
		for i, e := range elems {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	case reflect.TypeFor[[]float64]():
		out := make([]float64, len(elems))
		for i, e := range elems {
			f, ok := e.(float64)
			if !ok {
				return nil, false
			}
			out[i] = f
		}
		return out, true
	case reflect.TypeFor[[]bool]():
		out := make([]bool, len(elems))
		for i, e := range elems {
			b, ok := e.(bool)
			if !ok {
				return nil, false
			}
			out[i] = b
		}
		return out, true
	}
	return nil, false
}

// setHost stores script value e into dst, a slot of a typed host
// container: raw script scalars store straight into a basic slot (no
// boxing), anything else marshals through deepHost and converts within
// its basic class. ok is false when the value does not fit the slot.
func (h *hostMarshal) setHost(dst reflect.Value, e runtime.Value, vc runtime.VMCaller) bool {
	raw := e
	if n, ok := e.(*runtime.Named); ok && dst.Kind() != reflect.Interface {
		// a tagged scalar (int8, `type U8 uint8`, time.Month) stores its
		// payload into a concrete slot directly; the slot's type is the
		// host shape of the tag.
		raw = n.V
	}
	switch ev := raw.(type) {
	case int64:
		switch k := dst.Kind(); {
		case k >= reflect.Int && k <= reflect.Int64:
			dst.SetInt(ev)
			return true
		case k >= reflect.Uint && k <= reflect.Uintptr:
			dst.SetUint(uint64(ev))
			return true
		}
	case float64:
		if k := dst.Kind(); k == reflect.Float32 || k == reflect.Float64 {
			dst.SetFloat(ev)
			return true
		}
	case string:
		if dst.Kind() == reflect.String {
			dst.SetString(ev)
			return true
		}
	case bool:
		if dst.Kind() == reflect.Bool {
			dst.SetBool(ev)
			return true
		}
	}
	hv := deepHost(e, vc, h)
	if dst.Kind() == reflect.Interface {
		if hv != nil {
			dst.Set(reflect.ValueOf(hv))
		}
		return true
	}
	cv, ok := convertBasic(hv, dst.Type())
	if !ok {
		rv := reflect.ValueOf(hv)
		if !rv.IsValid() || !rv.Type().AssignableTo(dst.Type()) {
			return false
		}
		cv = rv
	}
	dst.Set(cv)
	return true
}

// opaqueContainers are host container types whose `any` method args are
// pure storage: the callee never reflects over the value, it hands it
// back on a later load (Pool.Get, Map.Load, atomicBox.Load,
// context.Value) or compares it against another boxed arg
// (Map.CompareAndSwap). A script value
// crosses these boundaries verbatim inside the `any` box, exactly as
// gc's interface boxing keeps it — marshaling to a host projection
// would invoke the value's niladic methods on every Store (a json
// Decoder's ReadToken/SkipValue on every Put), calls gc never makes. Keyed by
// reflect.Type's string so *minigo.atomicBox — the bound
// sync/atomic.Value/Pointer cell in package minigo — can join; vm
// cannot name it without an import cycle.
var opaqueContainers = map[string]bool{
	"*sync.Pool":        true,
	"*sync.Map":         true,
	"*context.valueCtx": true,
	"*minigo.atomicBox": true,
}

// hostFieldValue converts x for a host struct field: toReflectValue,
// except that a script sync.Pool.New keeps its result opaque — the
// pool hands it back through Get, which returns a script value verbatim
// (goValueOf), so it must not marshal: a struct would become a
// *struct{...} projection, invoking its niladic methods on every miss,
// and a named non-struct would lose its script type (the same rule as
// Pool.Put).
func hostFieldValue(x runtime.Value, owner reflect.Type, name string, ft reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if owner == poolType && name == "New" && ft.Kind() == reflect.Func {
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
			if vc != nil {
				return adaptFuncOut(x, ft, vc, true)
			}
		}
	}
	return toReflectValue(x, ft, vc)
}

// fillHostStruct copies a script struct's fields into the host struct
// out, name by name. The script type is the host type's source twin —
// [runtime.HostNamedIs] gates the conversion at the call site — so the
// field names line up; a field the host lacks is skipped (toolchain
// skew adds or drops internals). Unexported fields set through their
// address: out is freshly allocated, so the write stays private to the
// twin being built.
func fillHostStruct(out reflect.Value, s *runtime.Struct, vc runtime.VMCaller, depth int) error {
	st := out.Type()
	for i, name := range s.Def.Fields {
		fv := out.FieldByName(name)
		if !fv.IsValid() {
			continue
		}
		ev, err := toReflectValueDepth(s.Fields[i], fv.Type(), vc, depth+1)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", st, name, err)
		}
		if fv.CanSet() {
			fv.Set(ev)
			continue
		}
		if !fv.CanAddr() {
			return fmt.Errorf("%s.%s: field is not writable", st, name)
		}
		reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem().Set(ev)
	}
	return nil
}

// structOrigs records the script value each fabricated *struct{...}
// projection stands for: a host that hands one back — a FuncMap func's
// argument, an `any` round-trip — restores the script object it
// started from rather than surfacing the anonymous host struct.
// Entries live for the process, the same trade-off as
// runtime.opaqueKeyOrigs.
var structOrigs sync.Map

// StructOrig reports the script value a *struct{...} projection
// minted by structDataHost stands for — for recoverers outside the vm
// package (minigo's jsonHostArg). The type gate precedes the lookup:
// an unhashable x (a slice or map payload) would panic in sync.Map.
func StructOrig(x any) (runtime.Value, bool) {
	rt := reflect.TypeOf(x)
	if rt == nil || rt.Kind() != reflect.Pointer {
		return nil, false
	}
	if et := rt.Elem(); et.Kind() != reflect.Struct || et.Name() != "" {
		return nil, false
	}
	if orig, ok := structOrigs.Load(x); ok {
		return orig, true
	}
	return nil, false
}

// structDataHost marshals a script struct (or a pointer to one) for a
// host `any` parameter as a *struct{...} fabricated by
// reflect.StructOf: the type's exported fields plus its exported
// methods as `any` fields, so a member the type does not offer reports
// like gc — "can't evaluate field X in type ..." — where a map
// projection would read the miss as an absent key and print
// <no value>.
//
// A field carries its member deepHost'd; a method matching the
// template-callable shape (niladic; 1 result, or 2 with error last) is
// invoked at marshal time and its result stands in — text/template's
// evalField does not call what it finds, so Go's field-access call is
// replayed here. The marshal's budget caps these calls; on exhaustion,
// a pointer-receiver method on a value receiver, and any non-matching
// shape, the bound function value stays verbatim. A call that fails —
// panic, or a non-nil error result — leaves the field nil, printing
// <no value> as the dropped map entry did before. Only declared
// methods bind here — promoted ones stay out — and unexported members
// are absent, matching Go's can't-evaluate and unexported-field errors
// as a missing field.
func structDataHost(recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller, h *hostMarshal) any {
	if old, ok := h.walk.Seen(s); ok {
		return old
	}
	td := s.Def
	var sfs []reflect.StructField
	var fidx []int
	var mnames []string
	if td != nil {
		for i, name := range td.Fields {
			if i < len(s.Fields) && ast.IsExported(name) {
				sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
				fidx = append(fidx, i)
			}
		}
		for name := range td.Methods {
			if ast.IsExported(name) {
				mnames = append(mnames, name)
			}
		}
	}
	sort.Strings(mnames)
	for _, name := range mnames {
		sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
	}
	if len(sfs) == 0 {
		// a zero-size struct projects to a shared address (reflect.New
		// of struct{} returns zerobase), so every empty-exported type
		// would collide in structOrigs and fold to the newest orig — a
		// blank field keeps each projection's pointer distinct without
		// adding a member template field syntax can reach.
		sfs = append(sfs, reflect.StructField{Name: "_", Type: reflect.TypeFor[int64](), PkgPath: "github.com/podhmo/minigo/vm"})
	}
	out := reflect.New(reflect.StructOf(sfs))
	h.walk.Register(s, out.Interface())
	structOrigs.Store(out.Interface(), recv)
	ev := out.Elem()
	for j, i := range fidx {
		if fv := deepHost(s.Fields[i], vc, h); fv != nil {
			ev.Field(j).Set(reflect.ValueOf(fv))
		}
	}
	if td == nil {
		return out.Interface()
	}
	// a value receiver excludes pointer-receiver methods from the method
	// set, exactly as Go does — a *runtime.Struct recv is the struct
	// value, anything else is a pointer carrying its ref.
	_, valueRecv := recv.(*runtime.Struct)
	for k, name := range mnames {
		m := td.Methods[name]
		r := recv
		if !m.PtrRecv {
			if dv, ok := runtime.Deref(r); ok {
				r = dv
			}
		}
		bm := &runtime.BoundMethod{Recv: r, Fn: m}
		fv := ev.Field(len(fidx) + k)
		nout := templateCallableNout(m)
		if vc == nil || h.budget <= 0 || nout == 0 || (m.PtrRecv && valueRecv) {
			fv.Set(reflect.ValueOf(bm))
			continue
		}
		h.budget--
		res, err := vc.Call(bm, nil)
		if err != nil {
			continue
		}
		if nout == 2 {
			t, ok := res.(*runtime.Tuple)
			if !ok || len(t.Elems) != 2 || !nilish(t.Elems[1]) {
				continue
			}
			res = t.Elems[0]
		}
		if hv := deepHost(res, vc, h); hv != nil {
			fv.Set(reflect.ValueOf(hv))
		}
	}
	return out.Interface()
}

// templateCallableNout reports whether a method matches the shape
// text/template's evalField can invoke — niladic with 1 result, or 2
// results whose second is error — and returns that result count, or 0
// when the shape does not match.
func templateCallableNout(m *runtime.Function) int {
	if m == nil || m.Decl == nil || m.Decl.Type == nil {
		return 0
	}
	ft := m.Decl.Type
	if ft.Params != nil && len(ft.Params.List) > 0 {
		return 0
	}
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return 0
	}
	nout := 0
	var last ast.Expr
	for _, f := range ft.Results.List {
		if n := len(f.Names); n > 0 {
			nout += n
		} else {
			nout++
		}
		last = f.Type
	}
	if nout > 2 {
		return 0
	}
	if nout == 2 {
		id, ok := last.(*ast.Ident)
		if !ok || id.Name != "error" {
			return 0
		}
	}
	return nout
}

// nilish reports whether v reads as nil to a host — the same cases
// deepHost collapses.
func nilish(v runtime.Value) bool {
	switch v.(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}

// scriptBytes reads a script slice's elements as bytes in one pass —
// marshaling element-wise would box a reflect.Value per byte. Elements
// must all unwrap to an in-range int64; anything else reports false so
// the caller falls back to the generic loop (issue #362).
func scriptBytes(x *runtime.Slice) ([]byte, bool) {
	bs := make([]byte, len(x.Elems))
	for i, e := range x.Elems {
		n, ok := runtime.Unwrap(e).(int64)
		if !ok || n < 0 || n > 255 {
			return nil, false
		}
		bs[i] = byte(n)
	}
	return bs, true
}

// argBack pairs an argument with the host value it marshaled to so a
// callee's writes through it can mirror back into the script value.
type argBack struct {
	arg runtime.Value
	rv  reflect.Value
	// maps records each map element's identity before the call when rv
	// is a slice/array of maps: an element the callee left alone is the
	// host copy of the caller's script map, one it replaced is new data.
	maps []uintptr
}

// mapElemPtrs snapshots the map identities of a slice/array arg's
// elements (nil for any other shape).
func mapElemPtrs(rv reflect.Value) []uintptr {
	if rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) || rv.Type().Elem().Kind() != reflect.Map {
		return nil
	}
	ps := make([]uintptr, rv.Len())
	for i := range ps {
		ps[i] = rv.Index(i).Pointer()
	}
	return ps
}

// scalarPayload is a script scalar's payload: the value itself, or a
// Named's payload.
func scalarPayload(v runtime.Value) runtime.Value {
	if n, ok := v.(*runtime.Named); ok {
		return n.V
	}
	return v
}

// unchangedScalar reports whether the host element ev still holds the
// basic value the script element old carried — the common case after a
// read-only host call, which then skips the copy-back (no boxing per
// element, and old keeps its declared tag).
func unchangedScalar(old runtime.Value, ev reflect.Value) bool {
	p := scalarPayload(old)
	switch k := ev.Kind(); {
	case k >= reflect.Int && k <= reflect.Int64:
		n, ok := p.(int64)
		return ok && n == ev.Int()
	case k >= reflect.Uint && k <= reflect.Uintptr:
		n, ok := p.(int64)
		return ok && uint64(n) == ev.Uint()
	case k == reflect.Float32 || k == reflect.Float64:
		f, ok := p.(float64)
		return ok && f == ev.Float()
	case k == reflect.String:
		s, ok := p.(string)
		return ok && s == ev.String()
	case k == reflect.Bool:
		b, ok := p.(bool)
		return ok && b == ev.Bool()
	}
	return false
}

// retagElem keeps a script slice element's declared type across the
// copy-back after a host call: the host sees `type U8 uint8` elements as
// plain uint8, and the value coming back carries that basic tag. When
// the old element was tagged and the new payload has the same Go shape,
// the old tag wins; anything else takes the host value as is.
func retagElem(old, nv runtime.Value) runtime.Value {
	on, ok := old.(*runtime.Named)
	if !ok || on.Typ == nil {
		return nv
	}
	p := runtime.Unwrap(nv)
	if reflect.TypeOf(p) != reflect.TypeOf(runtime.Unwrap(on.V)) {
		return nv
	}
	if nn, ok := nv.(*runtime.Named); ok && nn.Typ == on.Typ {
		return nv
	}
	return runtime.Tag(on.Typ, p)
}

// sliceArg resolves an argument to the script slice it carries, through
// Named tags and one level of pointer-like deref.
func sliceArg(a runtime.Value) *runtime.Slice {
	u := runtime.Unwrap(a)
	if dv, ok := runtime.Deref(u); ok {
		u = runtime.Unwrap(dv)
	}
	s, _ := u.(*runtime.Slice)
	return s
}

// refArg resolves an argument to the pointer-like node (Cell/FieldRef/
// IndexRef) nearest its leaf value — the write-back target for a
// pointer crossed by address — plus the leaf it resolved to.
func refArg(a runtime.Value) (ref runtime.Value, leaf runtime.Value) {
	u := a
	var seen map[runtime.Value]bool
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		switch u.(type) {
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if _, isDR := u.(*runtime.DerefRef); !isDR {
				ref = u
			}
			// a cyclic pointer (x := any(nil); x = &x) resolves to
			// itself forever — stop on a revisit.
			if seen == nil {
				seen = map[runtime.Value]bool{u: true}
			} else if seen[u] {
				return ref, u
			} else {
				seen[u] = true
			}
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	return ref, u
}

// callReflectFunc invokes a host func reflect.Value with script args:
// arity checks, variadic/CallSlice handling, and result marshaling —
// shared by host method values and callable GoValue funcs.
func callReflectFunc(name string, m reflect.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	vc = bindCaller(vc)
	mt := m.Type()
	nin := mt.NumIn()
	if !mt.IsVariadic() && len(args) != nin {
		return nil, fmt.Errorf("%s needs %d args, got %d", name, nin, len(args))
	}
	if mt.IsVariadic() && len(args) < nin-1 {
		return nil, fmt.Errorf("%s needs at least %d args, got %d", name, nin-1, len(args))
	}
	// a trailing slice assignable to the variadic parameter calls
	// through CallSlice — `f(xs...)`-style forwarding on natives.
	in := make([]reflect.Value, len(args))
	var backs []argBack
	useSlice := false
	for i, a := range args {
		pt := mt.In(min(i, nin-1))
		if mt.IsVariadic() && i >= nin-1 {
			if i == nin-1 && len(args) == nin {
				if rv, err := toReflectValue(a, pt, vc); err == nil {
					in[i] = rv
					useSlice = true
					backs = append(backs, argBack{arg: a, rv: rv, maps: mapElemPtrs(rv)})
					continue
				}
			}
			pt = pt.Elem()
		}
		rv, err := toReflectValue(a, pt, vc)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
		}
		in[i] = rv
		backs = append(backs, argBack{arg: a, rv: rv, maps: mapElemPtrs(rv)})
	}
	var out []reflect.Value
	watched := false
	wakeable := false
	if w, parks := parkingHostMethods[name]; parks {
		if done := procDoneOf(vc); done != nil {
			watched = true
			wakeable = w
			// a method that can park indefinitely must not outlive its
			// process: run it on a helper goroutine and select on
			// proc.done, so a sibling's death unwinds this frame with
			// procExit instead of hanging the process (mu held by a
			// dead goroutine never releases its Lock waiters). The
			// helper keeps running the real call — a leaked goroutine,
			// the same documented limit as any host park.
			type callRes struct {
				out []reflect.Value
				p   any
			}
			resCh := make(chan callRes, 1)
			p := procOf(vc)
			go func() {
				// register before m.Call can invoke a script callback:
				// a Call arriving from this goroutine re-enters the VM
				// synchronously, so its panic propagates back through the
				// join rather than failing the process. p is captured
				// before the goroutine starts — it may already be dead,
				// which marking is harmless for; a nil receiver is not.
				if p != nil {
					defer p.WatchCallFrom(goroutineID())()
				}
				defer func() {
					if r := recover(); r != nil {
						resCh <- callRes{p: r}
					}
				}()
				if useSlice {
					resCh <- callRes{out: m.CallSlice(in)}
				} else {
					resCh <- callRes{out: m.Call(in)}
				}
			}()
			// the join can park indefinitely — wakeable only when the
			// call itself can time out (see blockingHostMethods).
			unpark := vcPark(vc, process.Wake{Misc: wakeable})
			defer unpark()
			select {
			case <-done:
				panic(procExit{})
			case r := <-resCh:
				unpark()
				if r.p != nil {
					panic(r.p)
				}
				out = r.out
			}
		}
	}
	if !watched {
		if useSlice {
			out = m.CallSlice(in)
		} else {
			out = m.Call(in)
		}
	}
	// callee writes propagate back: a script slice crossed as a fresh
	// host slice shares nothing (Reader.Read's buffer), and an
	// addressable arg crossed as a fresh host pointer (a *T param or an
	// interface the pointer implements) needs its pointee mirrored.
	for _, wb := range backs {
		handled := false
		if ss := sliceArg(wb.arg); ss != nil {
			rv := wb.rv
			if rv.Kind() == reflect.Interface {
				rv = rv.Elem()
			}
			if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
				// a []byte arg commits in one pass — goValueOf per
				// element would box and tag every byte again (#362).
				// Unaddressable arrays can't view as Bytes — they keep
				// the generic element loop.
				if rv.Type().Elem().Kind() == reflect.Uint8 && (rv.Kind() == reflect.Slice || rv.CanAddr()) {
					bs := rv.Bytes()
					for i := 0; i < len(bs) && i < len(ss.Elems); i++ {
						if p, ok := scalarPayload(ss.Elems[i]).(int64); ok && p == int64(bs[i]) {
							continue // unchanged: keep the element (and its tag)
						}
						ss.Elems[i] = retagElem(ss.Elems[i], runtime.ByteValue(bs[i]))
					}
					handled = true
					continue
				}
				for i := 0; i < rv.Len() && i < len(ss.Elems); i++ {
					ev := rv.Index(i)
					if unchangedScalar(ss.Elems[i], ev) {
						continue // keep the element (and its tag) as is
					}
					if ev.Kind() == reflect.Interface && !ev.IsNil() {
						if k := ev.Elem().Kind(); k == reflect.Slice || k == reflect.Map {
							// an `any` element holding a container is the
							// marshaled copy of a script container, which
							// may be this very slice: keep the script
							// element rather than rebuilding (and looping
							// on) the copy.
							continue
						}
					}
					if ev.Kind() == reflect.Map {
						if _, ok := runtime.Unwrap(ss.Elems[i]).(*runtime.Map); ok {
							// a map element crossed as a host copy: left
							// alone, the script map is still the caller's
							// value — swapping in the copy's GoValue box
							// would break index/assign on it
							// (`rows[0]["a"]`). A map the callee put in its
							// place lands as a script map, not a box.
							if i < len(wb.maps) && wb.maps[i] == ev.Pointer() {
								continue
							}
							ss.Elems[i] = retagElem(ss.Elems[i], SnapshotOf(ev.Interface()))
							continue
						}
					}
					ss.Elems[i] = retagElem(ss.Elems[i], goValueOf(ev))
				}
				handled = true
			}
		}
		if handled {
			continue
		}
		ref, leaf := refArg(wb.arg)
		if ref == nil || wb.rv.Kind() != reflect.Pointer {
			continue
		}
		// the arg's own pointer crossing directly isn't a temp —
		// copying its pointee back would un-box the cell.
		if gv, ok := leaf.(*runtime.GoValue); ok {
			if pv := reflect.ValueOf(gv.V); pv.IsValid() && pv.Kind() == reflect.Pointer && pv.Pointer() == wb.rv.Pointer() {
				continue
			}
		}
		val := goValueOf(wb.rv.Elem())
		if cell, ok := ref.(*runtime.Cell); ok {
			if n, ok := cell.Elem.(*runtime.Named); ok {
				// a declared cell keeps its tag through the round trip
				vv := runtime.Unwrap(val)
				if iv, ok := vv.(int64); ok {
					vv = maskInt(iv, sizedNameOf(n.Typ))
				}
				val = runtime.Tag(n.Typ, vv)
			}
		}
		runtime.SetRef(ref, val)
	}
	return goResults(out)
}

// goResults converts a host call's results to script values: no result
// is NIL, one unboxes via goValueOf, several become a Tuple.
func goResults(out []reflect.Value) (runtime.Value, error) {
	switch len(out) {
	case 0:
		return runtime.NIL, nil
	case 1:
		return goSlotValueOf(out[0]), nil
	default:
		el := make([]runtime.Value, len(out))
		for i, o := range out {
			el[i] = goSlotValueOf(o)
		}
		return &runtime.Tuple{Elems: el}, nil
	}
}

// callOpaqueStore calls a container method (opaqueContainers) whose
// `any` parameters are pure storage: each `any` arg crosses inside the
// interface box — what gc's boxing stores — so the struct's host
// projection, and the niladic method calls it runs at marshal time,
// never happen. A position gc hashes or compares as a key (a sync.Map
// key, a context key, CompareAndSwap's comparand) crosses in its
// canonical key form: equal script content hits and unhashable payloads
// panic with a hash-type error, exactly where gc runs the check. Other
// params marshal as usual, and no arg joins the write-back pass: a
// store shares nothing addressable.
func callOpaqueStore(recv, name string, m reflect.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	mt := m.Type()
	nin := mt.NumIn()
	if !mt.IsVariadic() && len(args) != nin {
		return nil, fmt.Errorf("%s needs %d args, got %d", name, nin, len(args))
	}
	if mt.IsVariadic() && len(args) < nin-1 {
		return nil, fmt.Errorf("%s needs at least %d args, got %d", name, nin-1, len(args))
	}
	in := make([]reflect.Value, len(args))
	for i, a := range args {
		pt := mt.In(min(i, nin-1))
		if mt.IsVariadic() && i >= nin-1 {
			pt = pt.Elem()
		}
		if pt == anyType {
			rv, err := hostbridge.OpaqueStoreArg(recv, name, i, a, materializeDefault)
			if err != nil {
				return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
			}
			in[i] = rv
			continue
		}
		rv, err := toReflectValue(a, pt, vc)
		if err != nil {
			return nil, fmt.Errorf("%s arg %d: %w", name, i, err)
		}
		in[i] = rv
	}
	return goResults(m.Call(in))
}

// funcMapTypes are host map[string]any types whose values the host
// requires to be real funcs — text/template's and html/template's
// FuncMap, which Template.Funcs checks with reflect. Registered at init
// by the engine's bindings ([RegisterFuncMapType]); read-only after.
var funcMapTypes = map[reflect.Type]bool{}

// RegisterFuncMapType marks t (a map with `any` values) as a FuncMap:
// converting a script map to t adapts each script func value to a host
// func of its own signature. Call it from an init func only.
func RegisterFuncMapType(t reflect.Type) { funcMapTypes[t] = true }

// funcMapElem converts one FuncMap value. A bound host func hands over
// its Target; a script func adapts to a host func whose signature comes
// from its declaration — predeclared basic and error types keep their
// Go type (so the template engine converts and checks arguments like
// gc's would), any other parameter or result type is `any`, and each
// argument converts to its declared script type before the call (see
// funcMapArgs). A value that is no func at all crosses as an ordinary
// `any`, so Template.Funcs itself rejects it with gc's recoverable
// `value for NAME not a function` panic.
func funcMapElem(x runtime.Value, vc runtime.VMCaller) (reflect.Value, error) {
	vc = bindCaller(vc)
	wrap := func(fv reflect.Value) reflect.Value {
		out := reflect.New(anyType).Elem()
		out.Set(fv)
		return out
	}
	if n, ok := x.(*runtime.Named); ok {
		switch n.V.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			x = n.V // a value of a named func type adapts like its func
		}
	}
	switch fx := x.(type) {
	case *runtime.BuiltinFunc:
		if rv := reflect.ValueOf(fx.Target); rv.Kind() == reflect.Func {
			return wrap(rv), nil
		}
		// an intrinsic without a host Target (strings.Split): a
		// func(...any) (any, error) that calls it on the VM.
		return wrap(reflect.MakeFunc(builtinFuncMapType, func(in []reflect.Value) []reflect.Value {
			return callBuiltinFuncMap(fx, in[0], vc)
		})), nil
	case *runtime.GoValue:
		if rv := reflect.ValueOf(fx.V); rv.Kind() == reflect.Func {
			return wrap(rv), nil
		}
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
		sig, pkg, file, binds := runtime.FuncSigOf(x)
		if sig == nil {
			return reflect.Value{}, fmt.Errorf("FuncMap value %T has no declared signature", x)
		}
		fv, err := adaptFuncWith(x, sigHostType(sig), vc, adaptOpts{
			funcMap: funcMapArgs(vmOf(vc), sig, &runtime.TypeDef{Pkg: pkg, File: file, Binds: binds}),
		})
		if err != nil {
			return reflect.Value{}, err
		}
		return wrap(fv), nil
	}
	return toReflectValue(x, anyType, vc)
}

var builtinFuncMapType = reflect.TypeFor[func(...any) (any, error)]()

// callBuiltinFuncMap runs a Target-less intrinsic for a FuncMap call:
// the args arrive packed in one []any, a (value, error) tuple result
// splits into the template's value and error, a single value has a nil
// error.
func callBuiltinFuncMap(fx *runtime.BuiltinFunc, packed reflect.Value, vc runtime.VMCaller) []reflect.Value {
	vc = callbackContext(vc)
	sargs := make([]runtime.Value, packed.Len())
	for i := range sargs {
		sargs[i] = goSlotValueOf(packed.Index(i))
	}
	fail := func(err error) []reflect.Value {
		return []reflect.Value{reflect.Zero(anyType), reflect.ValueOf(&err).Elem()}
	}
	r, err := vc.Call(fx, sargs)
	if err != nil {
		return fail(err)
	}
	rs := []runtime.Value{r}
	if tup, ok := r.(*runtime.Tuple); ok {
		rs = tup.Elems
	}
	if len(rs) == 0 || len(rs) > 2 {
		return fail(fmt.Errorf("%s returns %d values; a FuncMap func returns 1, or 2 with an error", fx.Name, len(rs)))
	}
	val, err := toReflectValue(rs[0], anyType, vc)
	if err != nil {
		return fail(err)
	}
	errv := reflect.Zero(errorType)
	if len(rs) == 2 {
		if errv, err = toReflectValue(rs[1], errorType, vc); err != nil {
			return fail(fmt.Errorf("%s: second result is not an error: %w", fx.Name, err))
		}
	}
	return []reflect.Value{val, errv}
}

// sigHostType builds a host func type for a script signature: each
// predeclared basic and `error` keeps its Go type, everything else is
// `any`. A trailing ...T parameter stays variadic.
func sigHostType(sig *ast.FuncType) reflect.Type {
	slot := func(e ast.Expr) reflect.Type {
		if id, ok := e.(*ast.Ident); ok {
			if id.Name == "error" {
				return errorType
			}
			if rt := hostBasicTypes[id.Name]; rt != nil {
				return rt
			}
		}
		return anyType
	}
	var in, out []reflect.Type
	variadic := false
	if sig.Params != nil {
		for _, fl := range sig.Params.List {
			e := fl.Type
			var rt reflect.Type
			if el, ok := e.(*ast.Ellipsis); ok {
				variadic = true
				rt = reflect.SliceOf(slot(el.Elt))
			} else {
				rt = slot(e)
			}
			for range max(1, len(fl.Names)) {
				in = append(in, rt)
			}
		}
	}
	if sig.Results != nil {
		for _, fl := range sig.Results.List {
			for range max(1, len(fl.Names)) {
				out = append(out, slot(fl.Type))
			}
		}
	}
	return reflect.FuncOf(in, out, variadic)
}

// funcMapSig is what a FuncMap func needs besides its host signature
// ([sigHostType]): the script type each parameter declares, so the
// arguments the template engine hands over as `any` bind like gc's typed
// ones would.
type funcMapSig struct {
	// params holds one typedef per declared parameter — a trailing
	// ...T contributes its element type T — nil where the host type
	// already carries it (basics, error, `any`) or the declaration does
	// not resolve (a function-local type).
	params  []*runtime.TypeDef
	spell   []string // Go spelling of each param, for the type-mismatch error
	convert bool     // some param has a typedef: guard converts the arguments
}

// funcMapArgs resolves the declared parameter types of a script func
// registered through FuncMap. When no parameter needs converting — the
// common case of basics only — calls skip the conversion step.
func funcMapArgs(v *VM, sig *ast.FuncType, ctx *runtime.TypeDef) *funcMapSig {
	fs := &funcMapSig{}
	if v == nil || v.H.ResolveType == nil || sig.Params == nil {
		return fs
	}
	for _, fl := range sig.Params.List {
		e := fl.Type
		if el, ok := e.(*ast.Ellipsis); ok {
			e = el.Elt
		}
		td := v.funcMapParamType(ctx, e)
		fs.convert = fs.convert || td != nil
		for range max(1, len(fl.Names)) {
			fs.params = append(fs.params, td)
			fs.spell = append(fs.spell, runtime.TypGoSpelling(e, ctx))
		}
	}
	return fs
}

// funcMapParamType resolves a parameter's declared type, or nil when the
// host slot already says all there is to say: a predeclared basic or
// `error` keeps its Go type, `any` takes whatever arrives.
func (v *VM) funcMapParamType(ctx *runtime.TypeDef, e ast.Expr) *runtime.TypeDef {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	switch t := e.(type) {
	case *ast.Ident:
		if t.Name == "any" || t.Name == "error" || hostBasicTypes[t.Name] != nil {
			return nil
		}
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return nil
		}
	case *ast.StarExpr:
		// resolveTypeRef peels *T to T: the pointer typedef is built
		// the way the compiler spells one.
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: t, Pkg: ctx.Pkg, File: ctx.File, Binds: ctx.Binds}
	}
	td, err := v.H.ResolveType(ctx, e)
	if err != nil {
		return nil
	}
	return td
}

// guard wraps callee so the conversion of the arguments runs on the VM
// that executes the call — not the one that registered the func when the
// template runs on another goroutine (an http handler, a `go`
// statement): there the call reroutes to a child VM, and the conversion
// (type hooks, the interface memo) belongs to that VM.
func (fs *funcMapSig) guard(callee runtime.Value) runtime.Value {
	if !fs.convert {
		return callee
	}
	return &runtime.BuiltinFunc{Name: "template func", Forwards: true, SyncCallbacks: true, Fn: func(c runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		v := vmOf(c)
		conv := make([]runtime.Value, len(args))
		for i, a := range args {
			j := min(i, len(fs.params)-1) // the variadic tail
			if v == nil || j < 0 || fs.params[j] == nil {
				conv[i] = a
				continue
			}
			r, err := v.hostArgAsChecked(a, fs.params[j], fs.spell[j])
			if err != nil {
				return nil, err
			}
			conv[i] = r
		}
		return c.Call(callee, conv)
	}}
}

// funcMapPanicValue maps the error a script FuncMap func came back with
// to the value the host panics with. text/template recovers that panic
// in safeCall — an error stands as is, anything else prints with %v —
// so a script panic must cross as its payload: panic("boom") reads
// `error calling p: boom`, not the `panic: boom` the *runtime.Panic
// renders. Other failures (a trap, a mismatched argument) keep their
// error.
func funcMapPanicValue(err error, vc runtime.VMCaller) any {
	var p *runtime.Panic
	if !errors.As(err, &p) || p == nil {
		return err
	}
	switch x := p.Value.(type) {
	case *runtime.GoValue:
		return x.V
	case *runtime.Named:
		if gv, ok := x.V.(*runtime.GoValue); ok {
			return gv.V
		}
	}
	if _, ok := runtime.IfaceMember(vc, p.Value, "Error"); ok {
		if e := hostErr(vc, p.Value); e != nil {
			return e
		}
	}
	if s, ok := runtime.IfaceCallString(vc, p.Value, "String"); ok {
		return s
	}
	return deepHost(p.Value, vc, &hostMarshal{walk: runtime.NewWalk(), budget: hostEvalBudget})
}

// adaptFunc wraps a script callable as a host-typed func so methods taking
// a func parameter (sync.Once.Do, sort callbacks, WalkDir-style visitors)
// can invoke it: calls run back on vc — the VM the host call is executing
// on, which is the right goroutine for synchronous host callbacks. A host
// that retains the func and calls it later from another goroutine
// (sync.WaitGroup.Go, time.AfterFunc) lands on vc.Call from a foreign
// goroutine, which reroutes to a spawned child VM — see Call.
func adaptFunc(x runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	return adaptFuncOut(x, t, vc, false)
}

// adaptFuncOut is adaptFunc; opaque hands a result to an `any` result
// slot as is rather than marshaling it (see hostFieldValue).
func adaptFuncOut(x runtime.Value, t reflect.Type, vc runtime.VMCaller, opaque bool) (reflect.Value, error) {
	return adaptFuncWith(x, t, vc, adaptOpts{opaque: opaque})
}

// adaptOpts tunes adaptFuncWith beyond adaptFunc's defaults.
type adaptOpts struct {
	// opaque hands a result to an `any` result slot as is (see
	// adaptFuncOut).
	opaque bool
	// funcMap marks a func registered through template.FuncMap: its
	// arguments convert to the declared script types, and a script
	// panic reaches the host as its payload (see funcMapPanicValue).
	funcMap *funcMapSig
}

// adaptFuncWith is adaptFuncOut with the full option set.
func adaptFuncWith(x runtime.Value, t reflect.Type, vc runtime.VMCaller, o adaptOpts) (reflect.Value, error) {
	opaque := o.opaque
	if vc == nil {
		return reflect.Value{}, fmt.Errorf("cannot adapt %T to %s off-VM", x, t)
	}
	switch x.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.Named:
	default:
		return reflect.Value{}, fmt.Errorf("cannot use %T as %s", x, t)
	}
	fv := reflect.MakeFunc(t, func(in []reflect.Value) []reflect.Value {
		vc := callbackContext(vc)
		callee, call := x, vc.Call
		if o.funcMap != nil {
			callee = o.funcMap.guard(x)
			if cc, ok := vc.(runtime.CatchCaller); ok {
				call = cc.CallCatching
			}
		}
		sargs := make([]runtime.Value, 0, len(in))
		for i, a := range in {
			if t.IsVariadic() && i == len(in)-1 {
				// the host packs a ...T call into one slice; the script
				// callee collects separate args back into its own ...T.
				for j := range a.Len() {
					sargs = append(sargs, goSlotValueOf(a.Index(j)))
				}
				continue
			}
			sargs = append(sargs, goSlotValueOf(a))
		}
		r, err := call(callee, sargs)
		if err != nil {
			if o.funcMap != nil {
				panic(funcMapPanicValue(err, vc))
			}
			panic(err)
		}
		nout := t.NumOut()
		var rs []runtime.Value
		if tup, ok := r.(*runtime.Tuple); ok {
			rs = tup.Elems
		} else {
			rs = []runtime.Value{r}
		}
		out := make([]reflect.Value, nout)
		for i := range out {
			var rv reflect.Value
			var err error
			if opaque && i < len(rs) && t.Out(i) == anyType {
				// the pool stores the result verbatim, exactly as Put
				// does: marshaling would lose the script type of a
				// named non-struct (slog's `(*buffer.Buffer)(&b)`
				// reading back as *[]uint8) and run a struct's
				// niladic methods via its host projection.
				rv, err = hostbridge.OpaqueStoreArg("*sync.Pool", "Put", 0, rs[i], materializeDefault)
			} else if i < len(rs) {
				rv, err = toReflectValue(rs[i], t.Out(i), vc)
			} else {
				rv, err = toReflectValue(runtime.NIL, t.Out(i), vc)
			}
			if err != nil {
				panic(err)
			}
			out[i] = rv
		}
		return out
	})
	return fv, nil
}

// hostNamedInt reports whether x is a host value of a defined integer
// type — a reflect.Kind or parse.NodeType, not a bare int or int64.
func hostNamedInt(x any) bool {
	t := reflect.TypeOf(x)
	if t == nil || t.PkgPath() == "" {
		return false
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

// hostStringOperand is what eqlValue compares for a host-boxed string:
// its string contents. Any other box compares as itself.
func hostStringOperand(gv *runtime.GoValue) runtime.Value {
	if rv := reflect.ValueOf(gv.V); rv.IsValid() && rv.Kind() == reflect.String {
		return rv.String()
	}
	return gv
}
