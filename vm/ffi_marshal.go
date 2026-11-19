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
	"io/fs"
	"reflect"
	"unsafe"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

// marshalHost is the process-wide hostbridge.Marshal wired to this
// package's VM-dependent pieces: constant folding, callback/iface
// adaptation and the VM intern tables resolved through the caller.
// Built in init — the hook funcs reference marshalHost through their
// bodies, so a literal var initializer would be an init cycle.
var marshalHost *hostbridge.Marshal

func init() {
	marshalHost = &hostbridge.Marshal{
		Materialize: materializeDefault,
		ConstArg:    hostConstArg,
		AdaptFunc:   adaptFunc,
		AdaptFuncOpaque: func(x runtime.Value, ft reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
			return adaptFuncOut(x, ft, vc, true)
		},
		AdaptIface: adaptIface,
		HostErr: func(vc runtime.VMCaller, orig runtime.Value) error {
			m := vmOf(vc)
			if m == nil || m.H.HostErr == nil {
				return nil
			}
			return m.H.HostErr(vc, orig)
		},
		ElemOf: func(vc runtime.VMCaller, td *runtime.TypeDef) (*runtime.TypeDef, error) {
			if m := vmOf(vc); m != nil && m.H.ElemOf != nil {
				return m.H.ElemOf(td)
			}
			return nil, nil
		},
		MapKeyOf: func(vc runtime.VMCaller, td *runtime.TypeDef) *runtime.TypeDef {
			if m := vmOf(vc); m != nil {
				return m.mapKeyTypedef(nil, td)
			}
			return nil
		},
		AliasOf: func(vc runtime.VMCaller, td *runtime.TypeDef) *runtime.TypeDef {
			if m := vmOf(vc); m != nil {
				return m.peelAlias(td)
			}
			return td
		},
		HostTypOf:            hostTypOf,
		ScriptBytes:          scriptBytes,
		FuncMapElem:          funcMapElemFor,
		TemplateCallableNout: templateCallableNout,
	}
}

// funcMapElemFor returns funcMapElem when t is a registered FuncMap
// type, else nil.
func funcMapElemFor(t reflect.Type) func(runtime.Value, runtime.VMCaller) (reflect.Value, error) {
	if funcMapTypes[t] {
		return funcMapElem
	}
	return nil
}

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
	if rt := hostbridge.HostNilType(td); rt != nil {
		// a nil named host slice ([hostbridge.HostNil]): IP's methods on nil IP
		return hostZeroMethod(rt, name)
	}
	if td == nil || td.Spec != nil || td.Kind != runtime.KindPointer {
		return nil, false
	}
	rt := hostbridge.HostNilPtrType(td)
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

// SnapshotOf converts a host Go value into a [VM.Call] argument. The value
// crosses as a snapshot — a copy shaped for the script world — so script
// writes to it never reach the host value. An unnamed host map becomes a
// [runtime.Map] that a map-typed parameter can index, range and write; a
// named map or struct keeps its host box so its methods stay callable.
// Script-native values pass through unchanged.
func SnapshotOf(x any) runtime.Value {
	// The shape is hostbridge.GoValueOf's plus one wider unbox: unnamed maps unbox
	// here while host call results still box them — an arg has a declared
	// param shape to satisfy, a result does not. Named maps and structs
	// keep their box for member dispatch, the rule hostbridge.GoValueOf applies to
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
	// int, so it enters bare — tagging it (like hostbridge.GoValueOf does for a
	// reflect result) would make `int`-typed params reject the ints
	// callers already pass.
	if v, ok := x.(int64); ok {
		return v
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() == reflect.Map && rv.Type().Name() == "" {
		// recursing through snapshotValue unboxes nested maps too —
		// m["a"]["b"] keeps working at any depth.
		td := hostbridge.AnonMapTypX(hostbridge.AnonExprOf(rv.Type().Key()), hostbridge.AnonExprOf(rv.Type().Elem()))
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
			// arrival; primitives keep hostbridge.GoValueOf's tagged shape (an
			// int64 in an `any` slot stays `int64`, not bare `int`).
			// The typedef spells the composite element correctly
			// (`[]map[string]interface{}` — an unparseable one-identifier
			// element name never reaches the param check).
			var td *runtime.TypeDef
			if rv.Kind() == reflect.Slice {
				td = hostbridge.AnonSliceTypX(hostbridge.AnonExprOf(rv.Type().Elem()))
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
				td = hostbridge.AnonArrayTypX(rv.Len(), hostbridge.AnonExprOf(rv.Type().Elem()))
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
						el[i] = hostbridge.GoValueOf(pv)
					}
				} else if et.Kind() == reflect.Map || et.Kind() == reflect.Slice || et.Kind() == reflect.Array {
					el[i] = snapshotValue(ev.Interface(), visiting)
				} else {
					el[i] = hostbridge.GoValueOf(ev)
				}
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
	}
	return hostbridge.GoValueOf(rv)
}

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
		val, err := marshalHost.HostFieldValue(raw[i+1], rv.Type(), name, fv.Type(), v)
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
		val, err := marshalHost.ToReflect(e, fv.Type(), v)
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

// adaptIface reports a scriptIface-family proxy for v when v declares
// every method interface t requires. Pure reflect cannot fabricate
// interface impls, so the adapter's static method set must contain t's
// — and the proxy variant is picked by whether the script declares the
// OPTIONAL-probe methods (io.WriterTo/io.ReaderFrom, asserted by
// io.Copy and friends): a Read-only script value adapted to io.Reader
// must NOT satisfy WriterTo, or io.Copy calls a method that does not
// exist and returns its error instead of copying through Read.
func adaptIface(v runtime.Value, t reflect.Type, vc runtime.MemberCaller) (reflect.Value, bool) {
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
func errOut(vc runtime.MemberCaller, rs []runtime.Value, i int) error {
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
func hostErr(vc runtime.MemberCaller, v runtime.Value) error {
	vc = callbackMemberContext(vc)
	if m := vmOf(vc); m != nil && m.H.HostErr != nil {
		return m.H.HostErr(vc, v)
	}
	return &scriptIface{vc: vc, recv: v}
}

// vmOf reports the VM behind a caller (nil for a foreign caller).
func vmOf(vc runtime.CallerRef) *VM {
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

// hostSeqFunc adapts a script iter.Seq / iter.Seq2 value to the host
// func(yield func(any) bool) / func(yield func(any, any) bool) shape: the
// host's yield receives each element marshaled like any other `any`
// data, and its false return stops the script producer. A failure inside
// the seq propagates as a panic, as a callback's would. ok is false for a
var (
	anyType = reflect.TypeFor[any]()
)

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
				if rv, err := marshalHost.ToReflect(a, pt, vc); err == nil {
					in[i] = rv
					useSlice = true
					backs = append(backs, argBack{arg: a, rv: rv, maps: mapElemPtrs(rv)})
					continue
				}
			}
			pt = pt.Elem()
		}
		rv, err := marshalHost.ToReflect(a, pt, vc)
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
				// a []byte arg commits in one pass — hostbridge.GoValueOf per
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
					ss.Elems[i] = retagElem(ss.Elems[i], hostbridge.GoValueOf(ev))
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
		val := hostbridge.GoValueOf(wb.rv.Elem())
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
// is NIL, one unboxes via hostbridge.GoValueOf, several become a Tuple.
func goResults(out []reflect.Value) (runtime.Value, error) {
	switch len(out) {
	case 0:
		return runtime.NIL, nil
	case 1:
		return hostbridge.GoSlotValueOf(out[0]), nil
	default:
		el := make([]runtime.Value, len(out))
		for i, o := range out {
			el[i] = hostbridge.GoSlotValueOf(o)
		}
		return &runtime.Tuple{Elems: el}, nil
	}
}

// callOpaqueStore calls a container method (hostbridge.OpaqueContainers) whose
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
		rv, err := marshalHost.ToReflect(a, pt, vc)
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
	return marshalHost.ToReflect(x, anyType, vc)
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
		sargs[i] = hostbridge.GoSlotValueOf(packed.Index(i))
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
	val, err := marshalHost.ToReflect(rs[0], anyType, vc)
	if err != nil {
		return fail(err)
	}
	errv := reflect.Zero(hostbridge.ErrorType)
	if len(rs) == 2 {
		if errv, err = marshalHost.ToReflect(rs[1], hostbridge.ErrorType, vc); err != nil {
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
				return hostbridge.ErrorType
			}
			if rt := hostbridge.HostBasicTypes[id.Name]; rt != nil {
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
		if t.Name == "any" || t.Name == "error" || hostbridge.HostBasicTypes[t.Name] != nil {
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
	return marshalHost.DeepHost(p.Value, vc, marshalHost.NewWalk())
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
					sargs = append(sargs, hostbridge.GoSlotValueOf(a.Index(j)))
				}
				continue
			}
			sargs = append(sargs, hostbridge.GoSlotValueOf(a))
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
				rv, err = marshalHost.ToReflect(rs[i], t.Out(i), vc)
			} else {
				rv, err = marshalHost.ToReflect(runtime.NIL, t.Out(i), vc)
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
