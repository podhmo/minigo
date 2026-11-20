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
	"fmt"
	"go/ast"
	"unsafe"

	"github.com/podhmo/minigo/internal/hostbridge"
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
		Materialize:  materializeDefault,
		ConstArg:     hostConstArg,
		AnchorCaller: callbackContext,
		AnchorMember: callbackMemberContext,
		HostErr: func(vc runtime.CallerRef) func(runtime.MemberCaller, runtime.Value) error {
			m := vmOf(vc)
			if m == nil {
				return nil
			}
			return m.H.HostErr
		},
		HasVM: func(vc runtime.VMCaller) bool { return vmOf(vc) != nil },
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
		GoroutineID:          goroutineID,
	}
}

// funcMapElemFor returns funcMapElem when t is a registered FuncMap
// type, else nil.
func funcMapElemFor(t hostbridge.Type) func(runtime.Value, runtime.VMCaller) (hostbridge.Value, error) {
	if funcMapTypes[t] {
		return funcMapElem
	}
	return nil
}

// hostTypOf returns the host reflect type a bound typedef stands in
// for — the fresh value HostNew mints, or the raw scalar HostScalar
// marks — so its method set can be probed where the typedef's own
// declared Methods stay empty. nil when the typedef binds no host type.
func hostTypOf(td *runtime.TypeDef) hostbridge.Type {
	if td == nil {
		return nil
	}
	if td.HostScalar != nil {
		return hostbridge.TypeOf(td.HostScalar)
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
		rt = hostbridge.TypeOf(et.HostNew())
		if rt.Kind() != hostbridge.KindPointer {
			rt = hostbridge.PointerTo(rt)
		}
	}
	return hostZeroMethod(rt, name)
}

// hostZeroMethod binds method name on the zero (nil) value of host type
// rt, or reports false when rt has no such method.
func hostZeroMethod(rt hostbridge.Type, name string) (runtime.Value, bool) {
	m := hostbridge.ZeroOf(rt).MethodByName(name)
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

// hostField finds a field by name on a host value: pointer and
// interface chains are dereferenced to the struct underneath. nil or
// non-struct roots report not-found. Exported fields always resolve;
// unexported ones resolve only for callers in the field's own package
// (Go's visibility rule), read through the field's address so
// interpreted stdlib code can see what it stamped — net/http's
// ServeMux reading back the r.pat it wrote on a host *http.Request.
func hostField(v any, name, pkg string) (hostbridge.Value, bool) {
	rv := hostbridge.RVOf(v)
	for rv.Kind() == hostbridge.KindPointer || rv.Kind() == hostbridge.KindInterface {
		if rv.IsNil() {
			return hostbridge.Value{}, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != hostbridge.KindStruct {
		return hostbridge.Value{}, false
	}
	fv := rv.FieldByName(name)
	if !fv.IsValid() {
		return hostbridge.Value{}, false
	}
	if fv.CanInterface() {
		return fv, true
	}
	sf, _ := rv.Type().FieldByName(name)
	if pkg == "" || sf.PkgPath != pkg || !fv.CanAddr() {
		return hostbridge.Value{}, false
	}
	return hostbridge.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem(), true
}

// initHostLiteral writes keyed composite-literal fields onto a
// host-created value (`&sync.Pool{New: f}`): the pointer chain is
// dereferenced to a settable struct, each key must name an exported
// field, and the value marshals to the field's type (a script func for
// New, via toReflectValue's adaptFunc).
func (v *VM) initHostLiteral(f *frame, td *runtime.TypeDef, hv any, raw []runtime.Value) {
	rv := hostbridge.RVOf(hv)
	for rv.Kind() == hostbridge.KindPointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != hostbridge.KindStruct {
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
	rv := hostbridge.RVOf(hv)
	for rv.Kind() == hostbridge.KindPointer {
		if rv.IsNil() {
			f.trap("cannot initialize host type %s: nil struct", td.Name)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != hostbridge.KindStruct {
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
var hostConstKindNames = map[hostbridge.Kind]string{
	hostbridge.KindInt: "int", hostbridge.KindInt8: "int8", hostbridge.KindInt16: "int16",
	hostbridge.KindInt32: "int32", hostbridge.KindInt64: "int64",
	hostbridge.KindUint: "uint", hostbridge.KindUint8: "uint8", hostbridge.KindUint16: "uint16",
	hostbridge.KindUint32: "uint32", hostbridge.KindUint64: "uint64", hostbridge.KindUintptr: "uintptr",
	hostbridge.KindFloat32: "float32", hostbridge.KindFloat64: "float64",
}

// vmOf reports the VM behind a caller (nil for a foreign caller).
func vmOf(vc runtime.CallerRef) *VM {
	if b, ok := vc.(ownerVMBearer); ok {
		return b.ownerVM()
	}
	return nil
}

// hostSeqFunc adapts a script iter.Seq / iter.Seq2 value to the host
// func(yield func(any) bool) / func(yield func(any, any) bool) shape: the
// host's yield receives each element marshaled like any other `any`
// data, and its false return stops the script producer. A failure inside
// the seq propagates as a panic, as a callback's would. ok is false for a
var (
	anyType = hostbridge.TypeForOf[any]()
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
// marshaling element-wise would box a hostbridge.Value per byte. Elements
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

// funcMapTypes are host map[string]any types whose values the host
// requires to be real funcs — text/template's and html/template's
// FuncMap, which Template.Funcs checks with reflect. Registered at init
// by the engine's bindings ([RegisterFuncMapType]); read-only after.
var funcMapTypes = map[hostbridge.Type]bool{}

// RegisterFuncMapType marks t (a map with `any` values) as a FuncMap:
// converting a script map to t adapts each script func value to a host
// func of its own signature. Call it from an init func only.
func RegisterFuncMapType(t hostbridge.Type) { funcMapTypes[t] = true }

// funcMapElem converts one FuncMap value. A bound host func hands over
// its Target; a script func adapts to a host func whose signature comes
// from its declaration — predeclared basic and error types keep their
// Go type (so the template engine converts and checks arguments like
// gc's would), any other parameter or result type is `any`, and each
// argument converts to its declared script type before the call (see
// funcMapArgs). A value that is no func at all crosses as an ordinary
// `any`, so Template.Funcs itself rejects it with gc's recoverable
// `value for NAME not a function` panic.
func funcMapElem(x runtime.Value, vc runtime.VMCaller) (hostbridge.Value, error) {
	vc = bindCaller(vc)
	wrap := func(fv hostbridge.Value) hostbridge.Value {
		out := hostbridge.NewRV(anyType).Elem()
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
		if rv := hostbridge.RVOf(fx.Target); rv.Kind() == hostbridge.KindFunc {
			return wrap(rv), nil
		}
		// an intrinsic without a host Target (strings.Split): a
		// func(...any) (any, error) that calls it on the VM.
		return wrap(hostbridge.MakeFunc(builtinFuncMapType, func(in []hostbridge.Value) []hostbridge.Value {
			return callBuiltinFuncMap(fx, in[0], vc)
		})), nil
	case *runtime.GoValue:
		if rv := hostbridge.RVOf(fx.V); rv.Kind() == hostbridge.KindFunc {
			return wrap(rv), nil
		}
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
		sig, pkg, file, binds := runtime.FuncSigOf(x)
		if sig == nil {
			return hostbridge.Value{}, fmt.Errorf("FuncMap value %T has no declared signature", x)
		}
		fv, err := marshalHost.AdaptFuncWith(x, sigHostType(sig), vc, hostbridge.AdaptOpts{
			FuncMap: funcMapArgs(vmOf(vc), sig, &runtime.TypeDef{Pkg: pkg, File: file, Binds: binds}),
		})
		if err != nil {
			return hostbridge.Value{}, err
		}
		return wrap(fv), nil
	}
	return marshalHost.ToReflect(x, anyType, vc)
}

var builtinFuncMapType = hostbridge.TypeForOf[func(...any) (any, error)]()

// callBuiltinFuncMap runs a Target-less intrinsic for a FuncMap call:
// the args arrive packed in one []any, a (value, error) tuple result
// splits into the template's value and error, a single value has a nil
// error.
func callBuiltinFuncMap(fx *runtime.BuiltinFunc, packed hostbridge.Value, vc runtime.VMCaller) []hostbridge.Value {
	vc = callbackContext(vc)
	sargs := make([]runtime.Value, packed.Len())
	for i := range sargs {
		sargs[i] = hostbridge.GoSlotValueOf(packed.Index(i))
	}
	fail := func(err error) []hostbridge.Value {
		return []hostbridge.Value{hostbridge.ZeroOf(anyType), hostbridge.RVOf(&err).Elem()}
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
	errv := hostbridge.ZeroOf(hostbridge.ErrorType)
	if len(rs) == 2 {
		if errv, err = marshalHost.ToReflect(rs[1], hostbridge.ErrorType, vc); err != nil {
			return fail(fmt.Errorf("%s: second result is not an error: %w", fx.Name, err))
		}
	}
	return []hostbridge.Value{val, errv}
}

// sigHostType builds a host func type for a script signature: each
// predeclared basic and `error` keeps its Go type, everything else is
// `any`. A trailing ...T parameter stays variadic.
func sigHostType(sig *ast.FuncType) hostbridge.Type {
	slot := func(e ast.Expr) hostbridge.Type {
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
	var in, out []hostbridge.Type
	variadic := false
	if sig.Params != nil {
		for _, fl := range sig.Params.List {
			e := fl.Type
			var rt hostbridge.Type
			if el, ok := e.(*ast.Ellipsis); ok {
				variadic = true
				rt = hostbridge.SliceOf(slot(el.Elt))
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
	return hostbridge.FuncOf(in, out, variadic)
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

// Guard wraps callee so the conversion of the arguments runs on the VM
// that executes the call — not the one that registered the func when the
// template runs on another goroutine (an http handler, a `go`
// statement): there the call reroutes to a child VM, and the conversion
// (type hooks, the interface memo) belongs to that VM.
func (fs *funcMapSig) Guard(callee runtime.Value) runtime.Value {
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

// hostNamedInt reports whether x is a host value of a defined integer
// type — a hostbridge.Kind or parse.NodeType, not a bare int or int64.
// hostStringOperand is what eqlValue compares for a host-boxed string:
// its string contents. Any other box compares as itself.

// callHooksOf bundles the process- and caller-aware operations the
// hostbridge call path needs — all resolved from the CallerRef, so a
// foreign VMCaller works too.
func callHooksOf(vc runtime.CallerRef) *hostbridge.CallHooks {
	return &hostbridge.CallHooks{
		BindCaller: bindCaller,
		Parks: func(name string) (bool, bool) {
			w, ok := parkingHostMethods[name]
			return w, ok
		},
		ProcDone:    procDoneOf,
		ProcOf:      procOf,
		Park:        vcPark,
		Exit:        func() any { return procExit{} },
		MaskInt:     func(iv int64, td *runtime.TypeDef) int64 { return maskInt(iv, sizedNameOf(td)) },
		Materialize: materializeDefault,
		GoroutineID: goroutineID,
	}
}

// callReflectFunc / callOpaqueStore delegate to the hostbridge call
// path (internal/hostbridge/callhost.go); they exist so vm callers keep
// their (name, m, vc, args) spelling.
func callReflectFunc(name string, m hostbridge.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	return marshalHost.CallReflectFunc(callHooksOf(vc), name, m, vc, args)
}

func callOpaqueStore(recv, name string, m hostbridge.Value, vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	return marshalHost.CallOpaqueStore(callHooksOf(vc), recv, name, m, vc, args)
}

// SnapshotOf delegates to hostbridge.SnapshotOf.
func SnapshotOf(x any) runtime.Value { return hostbridge.SnapshotOf(x) }
