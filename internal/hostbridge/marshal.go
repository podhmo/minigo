// marshal.go converts script values to host values: the reflect.Value
// marshal for typed parameters (toReflectValueDepth), the eager `any`
// projection (deepHost and its HostMarshal state), the host-shape table
// that types slices/maps as real Go containers, and the struct twin
// packing for interpreted host structs.
//
// The VM-dependent pieces (constant materialization, callback/iface
// adaptation, the VM's intern tables) arrive through the Marshal hooks,
// wired once per process by the vm package.
package hostbridge

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/podhmo/minigo/runtime"
)

// PoolType is sync.Pool's reflect type — hostFieldValue keys Pool.New's
// opaque-result rule on it, and vm's composite literal path does too.
var PoolType = reflect.TypeFor[sync.Pool]()

var (
	hostShapes    sync.Map // *runtime.TypeDef -> reflect.Type (nil: no shape)
	hostShapeSize atomic.Int64
)

// Marshal carries the VM hooks the host marshal needs. The vm package
// builds one per process; nil hooks disable the paths that need them.
type Marshal struct {
	// Materialize folds an untyped constant to its default type's value.
	Materialize func(u *runtime.UConst) (runtime.Value, error)
	// ConstArg converts a UConst at the target type without folding.
	ConstArg func(u *runtime.UConst, t reflect.Type) (rv reflect.Value, ok bool, err error)
	// AnchorCaller re-roots a callback caller on an idle anchor VM for
	// work outside Call (method selection, marshaling results).
	AnchorCaller func(vc runtime.VMCaller) runtime.VMCaller
	// AnchorMember is AnchorCaller for MemberCaller-shaped callers.
	AnchorMember func(vc runtime.MemberCaller) runtime.MemberCaller
	// HostErr returns the VM's script-error box for the caller, nil
	// when the caller carries no VM or the VM has no error binding —
	// the check must run before IfaceMember, which dereferences vc.
	HostErr func(vc runtime.CallerRef) func(runtime.MemberCaller, runtime.Value) error
	// HasVM reports whether vc reaches a live VM — a provisional nil
	// host shape must not cache when no VM can resolve the hooks.
	HasVM func(vc runtime.VMCaller) bool
	// ElemOf resolves a container typedef's element typedef.
	ElemOf func(vc runtime.VMCaller, td *runtime.TypeDef) (*runtime.TypeDef, error)
	// MapKeyOf resolves a map typedef's key typedef.
	MapKeyOf func(vc runtime.VMCaller, td *runtime.TypeDef) *runtime.TypeDef
	// AliasOf peels a `type A = B` typedef to B.
	AliasOf func(vc runtime.VMCaller, td *runtime.TypeDef) *runtime.TypeDef
	// HostTypOf returns the reflect type a bound typedef stands in.
	HostTypOf func(td *runtime.TypeDef) reflect.Type
	// ScriptBytes reads a script []byte's backing bytes in one pass.
	ScriptBytes func(x *runtime.Slice) ([]byte, bool)
	// FuncMapElem returns the elem converter when t is a registered
	// FuncMap (map[string]any) type, else nil.
	FuncMapElem func(t reflect.Type) func(runtime.Value, runtime.VMCaller) (reflect.Value, error)
	// TemplateCallableNout reports a script func's template-callable
	// result count (0 when it is not callable or not template-shaped).
	TemplateCallableNout func(m *runtime.Function) int
	// GoroutineID reports the running goroutine's id; it scopes the
	// call-projected container registry (callscope.go) so a host
	// callback arg crossing back resolves to the script value.
	GoroutineID func() int64
}

// NewWalk starts one marshal's walk/budget state.
func (m *Marshal) NewWalk() *HostMarshal {
	return &HostMarshal{M: m, Walk: runtime.NewWalk(), Budget: HostEvalBudget}
}

// maxHostConvertDepth caps the container/struct recursion a script
// value can drive into a host type: a cyclic twin (type T struct {
// next *T } with n.next = n) would otherwise recurse until the
// goroutine stack gives out — an uncatchable death, not a trap.
const maxHostConvertDepth = 128

// HostEvalBudget bounds the eager method calls one marshal may run.
// templateCallable methods are invoked at marshal time (see
// structDataHost), and a method returning a fresh object would expand
// that object's methods the same way, so without a cap a
// method-per-object chain never terminates — the earlier unbudgeted
// attempt hung on interpreted net/http structs.
const HostEvalBudget = 64

var ErrorType = reflect.TypeFor[error]()

// HostBasicTypes maps the predeclared basic type names to their Go types.
var HostBasicTypes = map[string]reflect.Type{
	"bool": reflect.TypeFor[bool](), "string": reflect.TypeFor[string](),
	"int": reflect.TypeFor[int](), "int8": reflect.TypeFor[int8](), "int16": reflect.TypeFor[int16](),
	"int32": reflect.TypeFor[int32](), "int64": reflect.TypeFor[int64](), "rune": reflect.TypeFor[rune](),
	"uint": reflect.TypeFor[uint](), "uint8": reflect.TypeFor[uint8](), "uint16": reflect.TypeFor[uint16](),
	"uint32": reflect.TypeFor[uint32](), "uint64": reflect.TypeFor[uint64](), "byte": reflect.TypeFor[byte](),
	"uintptr": reflect.TypeFor[uintptr](), "float32": reflect.TypeFor[float32](), "float64": reflect.TypeFor[float64](),
	"complex64": reflect.TypeFor[complex64](), "complex128": reflect.TypeFor[complex128](),
}

// OpaqueContainers are host container types whose `any` method args are
// pure storage: the callee never reflects over the value, it hands it
// back on a later load (Pool.Get, Map.Load, atomicBox.Load,
// context.Value) or compares it against another boxed arg
// (Map.CompareAndSwap). A script value
// crosses these boundaries verbatim inside the `any` box, exactly as
// gc's interface boxing keeps it — marshaling to a host projection
// would invoke the value's niladic methods on every Store (a json
// Decoder's ReadToken/SkipValue on every Put), calls gc never makes. Keyed by
// reflect.Type's string so *stdatomic.AtomicBox — the bound
// sync/atomic.Value/Pointer cell (it lives in internal/stdlib/atomic now) — can join; vm
// cannot name it without an import cycle.
var OpaqueContainers = map[string]bool{
	"*sync.Pool":           true,
	"*sync.Map":            true,
	"*context.valueCtx":    true,
	"*stdatomic.AtomicBox": true,
}

var stringAnyMapType = reflect.TypeFor[map[string]any]()

// hostMarshal carries the state one deepHost walk shares: walk folds
// cyclic values back to the projection already built for them — a host
// map or slice may legally contain itself — and budget caps the eager
// template-method calls described above.
type HostMarshal struct {
	// M resolves the VM-side hooks (materialize, adapt, intern tables).
	M      *Marshal
	Walk   *runtime.Walk
	Budget int
}

// toReflectValue marshals a runtime value to a reflect.Value of the
// requested type: empty interfaces carry the value verbatim (a *Cell stays
// a pointer, slices/maps cross unconverted), func types wrap the callable
// so the host can invoke it back on the calling VM (vc), named/cell
// wrappers unwrap, slices and maps convert element-wise, scalars assign or
// convert.
func (m *Marshal) ToReflect(v runtime.Value, t reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	return m.toReflectValueDepth(v, t, vc, 0)
}

func (m *Marshal) toReflectValueDepth(v runtime.Value, t reflect.Type, vc runtime.VMCaller, depth int) (reflect.Value, error) {
	if depth > maxHostConvertDepth {
		return reflect.Value{}, fmt.Errorf("cannot convert to %s: value nesting too deep", t)
	}
	if u, ok := v.(*runtime.UConst); ok {
		if rv, ok, err := m.ConstArg(u, t); ok {
			return rv, err
		}
		mv, err := m.Materialize(u)
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
		v = m.DeepHost(v, vc, m.NewWalk())
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
		return m.adaptFunc(v, t, vc)
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
			if rv, ok, err := m.ConstArg(x, t); ok {
				return rv, err
			}
			mv, err := m.Materialize(x)
			if err != nil {
				return reflect.Value{}, err
			}
			v = mv
			continue
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
			holder = v // last ref wins — the deepest pointer chain link
		}
		if _, isGV := v.(*runtime.GoValue); isGV {
			// a host value is the peel's terminal — the GoValue case
			// below reads its box; dereferencing a *T box would mint
			// an endless chain of pointee boxes.
			break
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
		if t == ErrorType && runtime.NilDeclaredTyp(orig) != nil {
			// `var e *E; error(e)` is a non-nil error carrying a
			// typed nil — boxing it as zero drops the cause (gc's
			// context.Cause returns the (*E)(nil), not Canceled).
			if he := m.HostErr(vc); he != nil {
				if err := he(vc, orig); err != nil {
					return reflect.ValueOf(&err).Elem(), nil
				}
			}
		}
		return reflect.Zero(t), nil
	case *runtime.GoValue:
		av = reflect.ValueOf(x.V)
	case *runtime.Slice:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return reflect.Value{}, fmt.Errorf("cannot convert script slice to %s", t)
		}
		// an unboxed host slice marshals back as its own backing —
		// `buf.Bytes()` handed to a FuncMap func shares the buffer, so
		// the callee's element writes reach it like gc's would.
		if x.Host.IsValid() && x.Host.Type().AssignableTo(t) {
			av = x.Host
			break
		}
		out := reflect.New(t).Elem()
		// byte slices marshal in one pass — the element loop below
		// builds a reflect.Value per byte otherwise (issue #362).
		if t.Elem().Kind() == reflect.Uint8 {
			if bs, ok := m.ScriptBytes(x); ok {
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
			ev, err := m.toReflectValueDepth(e, t.Elem(), vc, depth+1)
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
			kv, err := m.toReflectValueDepth(k, t.Key(), vc, depth+1)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("map key: %w", err)
			}
			var ev reflect.Value
			if fme := m.FuncMapElem(t); fme != nil {
				ev, err = fme(e, vc)
			} else {
				ev, err = m.toReflectValueDepth(e, t.Elem(), vc, depth+1)
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
		hostNamedIs := HostNamedIs
		if tw, ok := vc.(runtime.HostNamedTwinner); ok {
			hostNamedIs = tw.HostNamedTwin
		}
		if hst == nil || !hostNamedIs(hst, x.Def) || (namedTyp != nil && namedTyp != x.Def) {
			av = reflect.ValueOf(x)
			break
		}
		pv := reflect.New(hst)
		if err := m.fillHostStruct(pv.Elem(), x, vc, depth); err != nil {
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
	// a host facade unwraps to the value its API type names — a
	// *scriptFile fills a *os.File slot (cmd.ExtraFiles) with the file
	// it wraps.
	if gv, isGV := v.(*runtime.GoValue); isGV {
		if uw, ok := gv.V.(interface{ UnwrapHost() any }); ok {
			if uav := reflect.ValueOf(uw.UnwrapHost()); uav.IsValid() && uav.Type().AssignableTo(t) {
				return uav, nil
			}
		}
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
		if t == ErrorType {
			// a script error crossing into a host `error` slot (a
			// CancelCauseFunc's cause) boxes like every other script
			// error the host sees, so errors.Is/== agree on it.
			if he := m.HostErr(vc); he != nil {
				if _, ok := runtime.IfaceMember(vc, orig, "Error"); ok {
					if err := he(vc, orig); err != nil {
						return reflect.ValueOf(&err).Elem(), nil
					}
				}
			}
		}
		if pv, ok := m.adaptIface(orig, t, vc); ok {
			return pv, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", av.Type(), t)
}

// deepHost converts a script value for an `any` parameter: containers
// become real host values (Slice → []any, Map → map[any]any) so the
// callee can reflect over them; Named and GoValue unwrap; typed nils
// read as nil (a nil slice or map as its typed zero, see hostNilOf);
// structs marshal to fabricated *struct{...} values (see
// structDataHost).
// Everything else — funcs, non-struct pointers — stays verbatim.
func (m *Marshal) DeepHost(v runtime.Value, vc runtime.VMCaller, h *HostMarshal) runtime.Value {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return nil
	case *runtime.TypedNil:
		return m.hostNilOf(x.Typ, vc)
	case *runtime.IfaceNil:
		// `var a any = s` with a nil s []string: the same typed zero
		// (coerce turns the TypedNil into a concretely tagged IfaceNil).
		return m.hostNilOf(x.Typ, vc)
	case *runtime.UConst:
		mv, err := m.Materialize(x)
		if err != nil {
			panic(&runtime.Panic{Value: &runtime.GoValue{V: err}})
		}
		return m.DeepHost(mv, vc, h)
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
			if hf, ok := m.hostSeqFunc(x, vc); ok {
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
		if rt := HostBasicTypes[runtime.BasicNameOf(x.Typ)]; rt != nil && x.Typ.HostNew == nil {
			if rv, ok := convertBasic(m.DeepHost(x.V, vc, h), rt); ok {
				return rv.Interface()
			}
		}
		return m.DeepHost(x.V, vc, h)
	case int64:
		// a bare int64 is a script `int` (sized ints carry a tag)
		return int(x)
	case *runtime.GoValue:
		return x.V
	case *runtime.Struct:
		return structDataHost(x, x, vc, h)
	case *runtime.Slice, *runtime.Map:
		if sv, ok := v.(*runtime.Slice); ok && sv.Host.IsValid() && sv.Host.Len() == len(sv.Elems) {
			// an unboxed host slice crosses back as its own backing —
			// `buf.Bytes()` in a FuncMap arg shares the buffer, so the
			// callee's element writes reach it like gc's would.
			out := sv.Host.Interface()
			m.registerProjected(v, out)
			return out
		}
		if out, ok := h.typedContainer(v, vc); ok {
			m.registerProjected(v, out)
			return out
		}
		out, _ := h.Walk.Container(v, func(e runtime.Value) any {
			return m.DeepHost(e, vc, h)
		}, runtime.UnwrapKey)
		m.registerProjected(v, out)
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
func (m *Marshal) hostNilOf(td *runtime.TypeDef, vc runtime.VMCaller) any {
	if td == nil {
		return nil
	}
	if rt := HostNilType(td); rt != nil {
		// a nil named host map or slice (a request's Form, net.IP)
		return reflect.Zero(rt).Interface()
	}
	if td2 := m.AliasOf(vc, td); td2 != nil {
		td = td2
	}
	switch td.Kind {
	case runtime.KindSlice:
		if at, ok := td.Anon.(*ast.ArrayType); ok && at.Len != nil {
			return nil // an array has no nil
		}
		if rt := m.cachedHostShape(vc, td); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		return []any(nil)
	case runtime.KindMap:
		if rt := m.cachedHostShape(vc, td); rt != nil {
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
func (m *Marshal) hostSeqFunc(n *runtime.Named, vc runtime.VMCaller) (any, bool) {
	arity := runtime.IterSeqArity(n.Typ)
	if arity == 0 {
		return nil, false
	}
	seq := runtime.Value(n)
	toHost := func(e runtime.Value, c runtime.VMCaller) any {
		return m.DeepHost(e, c, m.NewWalk())
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
			if rt := runtime.HostReflectType(els[i]); rt != nil {
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
func (m *Marshal) hostShapeOf(vc runtime.VMCaller, td *runtime.TypeDef, depth int) reflect.Type {
	if td == nil || depth > 8 {
		return nil
	}
	if rt := m.HostTypOf(td); rt != nil {
		return rt
	}
	if rt := HostBasicTypes[runtime.BasicNameOf(td)]; rt != nil {
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
	if m.ElemOf == nil {
		return nil
	}
	switch td.Kind {
	case runtime.KindSlice:
		at, ok := x.(*ast.ArrayType)
		if !ok {
			return nil
		}
		et, err := m.ElemOf(vc, td)
		if err != nil {
			return nil
		}
		ert := m.hostShapeOf(vc, et, depth+1)
		if ert == nil {
			return nil
		}
		if at.Len == nil {
			return reflect.SliceOf(ert)
		}
		// the fold's owner is the array's declaring package, not
		// the element type's — `int` has no package to key on.
		lit, ok := runtime.ArrayLenExpr(td.Pkg, at).(*ast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return nil
		}
		n, err := strconv.ParseInt(lit.Value, 0, 64)
		if err != nil || n < 0 {
			return nil
		}
		return reflect.ArrayOf(int(n), ert)
	case runtime.KindMap:
		et, err := m.ElemOf(vc, td)
		if err != nil {
			return nil
		}
		ert := m.hostShapeOf(vc, et, depth+1)
		krt := m.hostShapeOf(vc, m.MapKeyOf(vc, td), depth+1)
		if ert == nil || krt == nil || !krt.Comparable() {
			return nil
		}
		return reflect.MapOf(krt, ert)
	}
	return nil
}

const hostShapeCap = 4096

func (m *Marshal) cachedHostShape(vc runtime.VMCaller, td *runtime.TypeDef) reflect.Type {
	if td == nil {
		return nil
	}
	if rt, ok := hostShapes.Load(td); ok {
		t, _ := rt.(reflect.Type)
		return t
	}
	rt := m.hostShapeOf(vc, td, 0)
	// without a VM the type hooks are out of reach and the answer is a
	// provisional nil — keep it out of the cache.
	if m.HasVM(vc) && hostShapeSize.Load() < hostShapeCap {
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
func (h *HostMarshal) typedContainer(v runtime.Value, vc runtime.VMCaller) (any, bool) {
	if old, ok := h.Walk.Seen(v); ok {
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
	rt := h.M.cachedHostShape(vc, td)
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
				h.Walk.Register(x, out.Interface())
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
			h.Walk.Register(x, out.Interface())
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

// stringAnyMap fills a native map[string]any — the common template data
// shape — with no reflect call per entry. ok is false (and nothing is
// registered) when a key is not a plain script string.
func (h *HostMarshal) stringAnyMap(x *runtime.Map, vc runtime.VMCaller) (map[string]any, bool) {
	n := x.Len()
	for i := 0; i < n; i++ {
		k, _ := x.At(i)
		if _, ok := k.(string); !ok {
			return nil, false
		}
	}
	out := make(map[string]any, n)
	h.Walk.Register(x, out)
	for i := 0; i < n; i++ {
		k, e := x.At(i)
		out[k.(string)] = h.M.DeepHost(e, vc, h)
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
func (h *HostMarshal) setHost(dst reflect.Value, e runtime.Value, vc runtime.VMCaller) bool {
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
	hv := h.M.DeepHost(e, vc, h)
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

// hostFieldValue converts x for a host struct field: toReflectValue,
// except that a script sync.Pool.New keeps its result opaque — the
// pool hands it back through Get, which returns a script value verbatim
// (GoValueOf), so it must not marshal: a struct would become a
// *struct{...} projection, invoking its niladic methods on every miss,
// and a named non-struct would lose its script type (the same rule as
// Pool.Put).
func (m *Marshal) HostFieldValue(x runtime.Value, owner reflect.Type, name string, ft reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if owner == PoolType && name == "New" && ft.Kind() == reflect.Func {
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod:
			if vc != nil {
				return m.adaptFuncOut(x, ft, vc, true)
			}
		}
	}
	return m.ToReflect(x, ft, vc)
}

// fillHostStruct copies a script struct's fields into the host struct
// out, name by name. The script type is the host type's source twin —
// [HostNamedIs] gates the conversion at the call site — so the
// field names line up; a field the host lacks is skipped (toolchain
// skew adds or drops internals). Unexported fields set through their
// address: out is freshly allocated, so the write stays private to the
// twin being built.
func (m *Marshal) fillHostStruct(out reflect.Value, s *runtime.Struct, vc runtime.VMCaller, depth int) error {
	st := out.Type()
	for i, name := range s.Def.Fields {
		fv := out.FieldByName(name)
		if !fv.IsValid() {
			continue
		}
		ev, err := m.toReflectValueDepth(s.Fields[i], fv.Type(), vc, depth+1)
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
// <no value> as the dropped map entry did before. Members promoted
// through embedded fields join the projection by promotedData — gc
// resolves them through the interface boundary — and unexported
// members are absent, matching Go's can't-evaluate and
// unexported-field errors as a missing field.
func structDataHost(recv runtime.Value, s *runtime.Struct, vc runtime.VMCaller, h *HostMarshal) any {
	if old, ok := h.Walk.Seen(s); ok {
		return old
	}
	td := s.Def
	var sfs []reflect.StructField
	var fidx []int
	var mnames []string
	declared := map[string]bool{}
	if td != nil {
		for i, name := range td.Fields {
			if i < len(s.Fields) && ast.IsExported(name) {
				sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
				fidx = append(fidx, i)
				declared[name] = true
			}
		}
		for name := range td.Methods {
			if ast.IsExported(name) {
				mnames = append(mnames, name)
				declared[name] = true
			}
		}
	}
	sort.Strings(mnames)
	for _, name := range mnames {
		sfs = append(sfs, reflect.StructField{Name: name, Type: anyType})
	}
	// a value receiver excludes pointer receivers from the method set,
	// exactly as Go does — a *runtime.Struct recv is the struct value,
	// anything else is a pointer carrying its ref.
	_, valueRecv := recv.(*runtime.Struct)
	// members promoted through embedded fields resolve through the
	// interface boundary in gc — `{{.C.Hidden}}` on an embed-receiving
	// *T — so they join the projection as direct members.
	pnames, pitems := promotedData(s, td, vc, !valueRecv, declared)
	for _, name := range pnames {
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
	h.Walk.Register(s, out.Interface())
	RecordStructOrig(out.Interface(), recv)
	ev := out.Elem()
	for j, i := range fidx {
		if fv := h.M.DeepHost(s.Fields[i], vc, h); fv != nil {
			ev.Field(j).Set(reflect.ValueOf(fv))
		}
	}
	if td == nil {
		return out.Interface()
	}
	for k, name := range mnames {
		h.projMethod(ev.Field(len(fidx)+k), td.Methods[name], recv, td.Methods[name].PtrRecv && valueRecv, vc)
	}
	for j, name := range pnames {
		fv := ev.Field(len(fidx) + len(mnames) + j)
		it := pitems[name]
		switch {
		case it.hm:
			h.projHostMethod(fv, it.hv)
		case it.hv.IsValid():
			fv.Set(it.hv)
		case it.m != nil:
			h.projMethod(fv, it.m, it.recv, false, vc)
		default:
			if hv := h.M.DeepHost(it.fv, vc, h); hv != nil {
				fv.Set(reflect.ValueOf(hv))
			}
		}
	}
	return out.Interface()
}

// nilishHost reports whether v reads as nil to a host — the same cases
// DeepHost collapses.
func nilishHost(v runtime.Value) bool {
	switch v.(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}
