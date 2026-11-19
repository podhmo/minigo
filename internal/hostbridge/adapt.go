// adapt.go wraps script callables and values for host func/interface
// parameters: AdaptFuncWith runs script functions back on the calling
// VM through reflect.MakeFunc, adaptIface boxes struct-shaped values in
// the scriptIface/scriptFS proxies from iface.go, and
// funcMapPanicValue maps a FuncMap func's script panic to its payload.
package hostbridge

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// FuncMapGuard converts a FuncMap func's arguments on the calling VM.
// vm's *funcMapSig implements it; nil disables the FuncMap path.
type FuncMapGuard interface {
	Guard(callee runtime.Value) runtime.Value
}

// adaptIface reports a scriptIface-family proxy for v when v declares
// every method interface t requires. Pure reflect cannot fabricate
// interface impls, so the adapter's static method set must contain t's
// — and the proxy variant is picked by whether the script declares the
// OPTIONAL-probe methods (io.WriterTo/io.ReaderFrom, asserted by
// io.Copy and friends): a Read-only script value adapted to io.Reader
// must NOT satisfy WriterTo, or io.Copy calls a method that does not
// exist and returns its error instead of copying through Read.
func (m *Marshal) adaptIface(v runtime.Value, t reflect.Type, vc runtime.MemberCaller) (reflect.Value, bool) {
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
	si := &scriptIface{vc: vc, recv: v, m: m}
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
		if fsv := m.scriptFSFor(vc, v); fsv != nil && reflect.TypeOf(fsv).Implements(t) {
			proxy = fsv
			st = reflect.TypeOf(proxy)
		}
		if !st.Implements(t) {
			// fs.File beside it: a script value declaring Stat — an
			// embed openFile, a script file type, or a file a host
			// handed back — adapts through the same per-file probe
			// Open results get.
			if fv := m.scriptFileFor(vc, v); fv != nil && reflect.TypeOf(fv).Implements(t) {
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

// funcMapPanicValue maps the error a script FuncMap func came back with
// to the value the host panics with. text/template recovers that panic
// in safeCall — an error stands as is, anything else prints with %v —
// so a script panic must cross as its payload: panic("boom") reads
// `error calling p: boom`, not the `panic: boom` the *runtime.Panic
// renders. Other failures (a trap, a mismatched argument) keep their
// error.
func (m *Marshal) funcMapPanicValue(err error, vc runtime.VMCaller) any {
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
		if e := m.hostErr(vc, p.Value); e != nil {
			return e
		}
	}
	if s, ok := runtime.IfaceCallString(vc, p.Value, "String"); ok {
		return s
	}
	return m.DeepHost(p.Value, vc, m.NewWalk())
}

// adaptFunc wraps a script callable as a host-typed func so methods taking
// a func parameter (sync.Once.Do, sort callbacks, WalkDir-style visitors)
// can invoke it: calls run back on vc — the VM the host call is executing
// on, which is the right goroutine for synchronous host callbacks. A host
// that retains the func and calls it later from another goroutine
// (sync.WaitGroup.Go, time.AfterFunc) lands on vc.Call from a foreign
// goroutine, which reroutes to a spawned child VM — see Call.
func (m *Marshal) adaptFunc(x runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	return m.adaptFuncOut(x, t, vc, false)
}

// adaptFuncOut is adaptFunc; opaque hands a result to an `any` result
// slot as is rather than marshaling it (see hostFieldValue).
func (m *Marshal) adaptFuncOut(x runtime.Value, t reflect.Type, vc runtime.VMCaller, opaque bool) (reflect.Value, error) {
	return m.AdaptFuncWith(x, t, vc, AdaptOpts{Opaque: opaque})
}

// adaptOpts tunes adaptFuncWith beyond adaptFunc's defaults.
type AdaptOpts struct {
	// Opaque hands a result to an `any` result slot as is (see
	// adaptFuncOut).
	Opaque bool
	// FuncMap marks a func registered through template.FuncMap: its
	// arguments convert to the declared script types, and a script
	// panic reaches the host as its payload (see funcMapPanicValue).
	FuncMap FuncMapGuard
}

// adaptFuncWith is adaptFuncOut with the full option set.
func (m *Marshal) AdaptFuncWith(x runtime.Value, t reflect.Type, vc runtime.VMCaller, o AdaptOpts) (reflect.Value, error) {
	opaque := o.Opaque
	if vc == nil {
		return reflect.Value{}, fmt.Errorf("cannot adapt %T to %s off-VM", x, t)
	}
	switch x.(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.Named:
	default:
		return reflect.Value{}, fmt.Errorf("cannot use %T as %s", x, t)
	}
	fv := reflect.MakeFunc(t, func(in []reflect.Value) []reflect.Value {
		vc = m.AnchorCaller(vc)
		callee, call := x, vc.Call
		if o.FuncMap != nil {
			callee = o.FuncMap.Guard(x)
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
					sargs = append(sargs, GoSlotValueOf(a.Index(j)))
				}
				continue
			}
			sargs = append(sargs, GoSlotValueOf(a))
		}
		r, err := call(callee, sargs)
		if err != nil {
			if o.FuncMap != nil {
				panic(m.funcMapPanicValue(err, vc))
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
				rv, err = OpaqueStoreArg("*sync.Pool", "Put", 0, rs[i], m.Materialize)
			} else if i < len(rs) {
				rv, err = m.ToReflect(rs[i], t.Out(i), vc)
			} else {
				rv, err = m.ToReflect(runtime.NIL, t.Out(i), vc)
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
