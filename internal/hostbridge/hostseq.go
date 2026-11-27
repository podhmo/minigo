package hostbridge

import (
	"go/ast"
	"go/token"
	"reflect"
	"strconv"

	"github.com/podhmo/minigo/runtime"
)

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
					} else if rv.Kind() == reflect.Pointer {
						// a host struct element arrives as the
						// allocator's *T while the seq spells T.
						if ev := rv.Elem(); ev.IsValid() && ev.Type().AssignableTo(et) {
							vals[i] = ev
						}
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
