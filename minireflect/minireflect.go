// Package minireflect implements a native facade for the part of the
// standard "reflect" package that scripts actually touch. Interpreting
// reflect's own source is a dead end for minigo: reflect.TypeOf bottoms
// out in internal/abi and unsafe.Pointer bit reinterpretation, which
// the host-object value model cannot execute.
//
// The facade splits values into two domains:
//
//   - script domain: an RValue views a runtime.Value together with its
//     settable location (a *runtime.Cell, *runtime.FieldRef or
//     *runtime.IndexRef). Field/Index/MapIndex walk the script value;
//     Set writes through the ref-view.
//   - host domain: a value produced from a *runtime.GoValue delegates
//     to a real reflect.Value, so host objects keep faithful semantics
//     (methods, unexported state) without leaving host land.
//
// Type identity is canonicalized: *RType values are interned by a key
// computed from the typedef's name or structural spelling (and from the
// real reflect.Type for host values), so `v.Type() == durationType`
// works across the domains.
package minireflect

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
	"sync"

	"github.com/podhmo/minigo/runtime"
)

// Hooks carries the engine's type machinery into the facade. The
// engine fills it with the same functions it installs on VMs; the
// facade never reaches into package minigo itself.
type Hooks struct {
	// ElemOf resolves the element type of a pointer, slice, map, or
	// chan typedef.
	ElemOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// ResolveType resolves a type expression in a declaring type's
	// context (used for map keys and anon element types).
	ResolveType func(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error)
	// FieldTypes returns the declared field types of a struct typedef.
	FieldTypes func(td *runtime.TypeDef) ([]*runtime.TypeDef, error)
	// TypeMethods returns the method names of a typedef's method set.
	TypeMethods func(td *runtime.TypeDef) (map[string]bool, error)
	// IfaceReqs returns the required methods of an interface typedef.
	IfaceReqs func(td *runtime.TypeDef) (map[string]bool, error)
	// Underlying resolves a typedef's underlying type.
	Underlying func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// AliasOf resolves a KindAlias typedef to its direct target typedef
	// (one hop — `type A = B` gives B itself). Nil leaves alias typedefs
	// keyed by their declared name.
	AliasOf func(td *runtime.TypeDef) (*runtime.TypeDef, error)
	// MethodSet returns the typedef's method FUNCTIONS — declared plus
	// promoted, honoring Go's receiver rule (pointer-receiver methods
	// join only through a pointer type or embedded pointer field) —
	// unexported members included; callers filter visibility. Interface
	// typedefs yield members carrying each required method's signature.
	MethodSet func(td *runtime.TypeDef) (map[string]*runtime.Function, error)
}

// Env is the facade's shared state: the type interner plus the engine
// hooks. One Env backs one engine's reflect bind.
type Env struct {
	h     Hooks
	types sync.Map // canonical key -> *RType
}

// Symbols returns the symbols bound under the "reflect" import path.
// The caller is responsible for merging DeepEqual (which already lives
// in the minigo package over script values).
func Symbols(h Hooks) map[string]runtime.Value {
	e := &Env{h: h}
	syms := map[string]runtime.Value{
		"ValueOf":         e.fn("reflect.ValueOf", e.valueOf),
		"TypeOf":          e.fn("reflect.TypeOf", e.typeOf),
		"Indirect":        e.fn("reflect.Indirect", e.indirect),
		"New":             e.fn("reflect.New", e.new_),
		"NewAt":           e.fn("reflect.NewAt", e.unsupported("reflect.NewAt")),
		"Zero":            e.fn("reflect.Zero", e.zero),
		"PtrTo":           e.fn("reflect.PtrTo", e.ptrTo),
		"PointerTo":       e.fn("reflect.PointerTo", e.ptrTo),
		"SliceOf":         e.fn("reflect.SliceOf", e.sliceOf),
		"MapOf":           e.fn("reflect.MapOf", e.mapOf),
		"ChanOf":          e.fn("reflect.ChanOf", e.chanOf),
		"ArrayOf":         e.fn("reflect.ArrayOf", e.arrayOf),
		"FuncOf":          e.fn("reflect.FuncOf", e.unsupported("reflect.FuncOf")),
		"StructOf":        e.fn("reflect.StructOf", e.unsupported("reflect.StructOf")),
		"MakeSlice":       e.fn("reflect.MakeSlice", e.makeSlice),
		"MakeMap":         e.fn("reflect.MakeMap", e.makeMap),
		"MakeMapWithSize": e.fn("reflect.MakeMapWithSize", e.makeMapWithSize),
		"MakeChan":        e.fn("reflect.MakeChan", e.unsupported("reflect.MakeChan")),
		"Append":          e.fn("reflect.Append", e.append_),
		"AppendSlice":     e.fn("reflect.AppendSlice", e.appendSlice),
		"Copy":            e.fn("reflect.Copy", e.copy_),
		"Select":          e.fn("reflect.Select", e.unsupported("reflect.Select")),
		"Swapper":         e.fn("reflect.Swapper", e.unsupported("reflect.Swapper")),
		"VisibleFields":   e.fn("reflect.VisibleFields", e.unsupported("reflect.VisibleFields")),
		"TypeAssert":      e.typeAssert(),
		"TypeFor":         e.typeFor(),
		// The two public shell types. `var v reflect.Value` produces a
		// host-boxed zero facade value; `var t reflect.Type` is an
		// interface typedef so `t == nil` and method checks behave.
		"Value": &runtime.TypeDef{
			Name:    "reflect.Value",
			Kind:    runtime.KindStruct,
			HostNew: func() any { return &RValue{} },
		},
		"Type": &runtime.TypeDef{
			Name: "reflect.Type",
			Kind: runtime.KindInterface,
			MReqs: []string{
				"Align", "AssignableTo", "Bits", "Comparable",
				"ConvertibleTo", "Elem", "Field", "FieldAlign",
				"FieldByIndex", "FieldByName", "Implements", "In",
				"IsVariadic", "Key", "Kind", "Len", "Method",
				"MethodByName", "Name", "NumField", "NumIn",
				"NumMethod", "NumOut", "Out", "PkgPath", "String",
			},
		},
		// StructTag and StructField delegate to real host types.
		"StructTag": &runtime.TypeDef{
			Name:    "reflect.StructTag",
			Kind:    runtime.KindNamedBasic,
			HostNew: func() any { return reflect.StructTag("") },
		},
		"StructField": &runtime.TypeDef{
			Name:    "reflect.StructField",
			Kind:    runtime.KindStruct,
			HostNew: func() any { return &StructField{} },
		},
		"MapIter": &runtime.TypeDef{
			Name:    "reflect.MapIter",
			Kind:    runtime.KindStruct,
			HostNew: func() any { return &MapIter{} },
		},
	}
	// Kind constants are the real reflect.Kind values: they format as
	// "int"/"struct" and compare equal to RValue.Kind() results.
	for _, k := range []struct {
		name string
		kind reflect.Kind
	}{
		{"Invalid", reflect.Invalid},
		{"Bool", reflect.Bool},
		{"Int", reflect.Int}, {"Int8", reflect.Int8}, {"Int16", reflect.Int16},
		{"Int32", reflect.Int32}, {"Int64", reflect.Int64},
		{"Uint", reflect.Uint}, {"Uint8", reflect.Uint8}, {"Uint16", reflect.Uint16},
		{"Uint32", reflect.Uint32}, {"Uint64", reflect.Uint64},
		{"Uintptr", reflect.Uintptr},
		{"Float32", reflect.Float32}, {"Float64", reflect.Float64},
		{"Complex64", reflect.Complex64}, {"Complex128", reflect.Complex128},
		{"Array", reflect.Array}, {"Chan", reflect.Chan}, {"Func", reflect.Func},
		{"Interface", reflect.Interface}, {"Map", reflect.Map},
		{"Ptr", reflect.Ptr}, {"Pointer", reflect.Pointer},
		{"Slice", reflect.Slice}, {"String", reflect.String},
		{"Struct", reflect.Struct}, {"UnsafePointer", reflect.UnsafePointer},
	} {
		syms[k.name] = &runtime.GoValue{V: k.kind}
	}
	for _, d := range []struct {
		name string
		dir  reflect.ChanDir
	}{
		{"RecvDir", reflect.RecvDir}, {"SendDir", reflect.SendDir}, {"BothDir", reflect.BothDir},
	} {
		syms[d.name] = &runtime.GoValue{V: d.dir}
	}
	return syms
}

// fn wraps a facade function as a BuiltinFunc.
func (e *Env) fn(name string, f func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: f}
}

// typeFor is the generic builtin behind reflect.TypeFor[T]: the type
// argument arrives through instantiation, not the call's value args.
func (e *Env) typeFor() *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{
		Name: "reflect.TypeFor",
		Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("reflect.TypeFor requires a type argument")
		},
		GenFn: func(vm runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			if len(targs) != 1 {
				return nil, fmt.Errorf("reflect.TypeFor takes 1 type argument, got %d", len(targs))
			}
			if len(args) != 0 {
				return nil, fmt.Errorf("reflect.TypeFor takes no value arguments")
			}
			td, ok := targs[0].(*runtime.TypeDef)
			if !ok {
				return nil, fmt.Errorf("reflect.TypeFor: type argument is %T, not a type", targs[0])
			}
			return &runtime.GoValue{V: e.rtypeOf(td)}, nil
		},
	}
}

// typeAssert is the generic builtin behind reflect.TypeAssert[T]: the
// comma-ok type assertion over a facade Value, matching Go 1.25's
// semantics — panic on a zero or read-only Value, otherwise return the
// payload typed as T with ok, or T's zero with !ok. Concrete T requires
// the payload's exact type; interface T requires the dynamic type to
// implement it; an interface-typed v asserts its dynamic payload (a nil
// interface asserts to nothing).
func (e *Env) typeAssert() *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{
		Name: "reflect.TypeAssert",
		Fn: func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("reflect.TypeAssert requires a type argument")
		},
		GenFn: func(vc runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			if len(targs) != 1 {
				return nil, fmt.Errorf("reflect.TypeAssert takes 1 type argument, got %d", len(targs))
			}
			if len(args) != 1 {
				return nil, fmt.Errorf("reflect.TypeAssert takes 1 value argument, got %d", len(args))
			}
			td, ok := targs[0].(*runtime.TypeDef)
			if !ok {
				return nil, fmt.Errorf("reflect.TypeAssert: type argument is %T, not a type", targs[0])
			}
			v := asRValue(args[0])
			if v == nil {
				return nil, fmt.Errorf("reflect.TypeAssert: arg is %T, not a reflect.Value", args[0])
			}
			if !v.IsValid() {
				trap("call of reflect.TypeAssert on zero Value")
			}
			if v.ro {
				plain("reflect.TypeAssert: cannot return value obtained from unexported field or method")
			}
			tR := e.rtypeOf(td)
			// the type checked against T: the payload's dynamic type.
			// An interface-typed v carries its concrete payload like Go's
			// v.Interface(), so Type() is enough for concrete v while an
			// interface-kind v needs the payload inside it.
			var vt *RType
			switch {
			case v.host():
				if v.rv.Kind() == reflect.Interface {
					if x := v.rv.Interface(); x != nil {
						vt = e.hostTypeOf(reflect.TypeOf(x))
					}
				} else {
					vt = v.Type()
				}
			case v.Kind() == reflect.Interface:
				if dt := typeOfValue(e, v.get()); dt != nil {
					vt = e.rtypeOf(dt)
				}
			default:
				vt = v.Type()
			}
			ok = false
			if vt != nil {
				if td.Kind == runtime.KindInterface {
					ok = vt.Implements(tR)
				} else {
					ok = vt == tR
				}
			}
			if !ok {
				return &runtime.Tuple{Elems: []runtime.Value{e.zeroOf(vc, td), false}}, nil
			}
			var out runtime.Value
			switch x := v.ifaceVal().(type) {
			case runtime.Value:
				out = x
			default:
				out = &runtime.GoValue{V: x}
			}
			return &runtime.Tuple{Elems: []runtime.Value{out, true}}, nil
		},
	}
}

// unsupported returns a builtin that traps loudly instead of silently
// doing the wrong thing — the "reject with a trap" rule for reflect
// surface the facade does not implement.
func (e *Env) unsupported(name string) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
	return func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
		return nil, fmt.Errorf("%s is not supported by minigo's reflect facade", name)
	}
}

// trap mirrors reflect's panic-on-misuse convention. Panicking a
// *runtime.Panic keeps the failure catchable by a script recover()
// while still reporting loudly at the top level.
func trap(format string, args ...any) {
	panic(&runtime.Panic{Value: fmt.Sprintf("reflect: "+format, args...)})
}

// plain panics like trap but without the `reflect:` prefix — some
// reflect runtime panics (`reflect.Value.Convert: ...`,
// `reflect.Value.Slice: ...`) carry their own spelling.
func plain(format string, args ...any) {
	panic(&runtime.Panic{Value: fmt.Sprintf(format, args...)})
}

// asRValue unwraps a script argument to its facade value: a GoValue
// boxing an *RValue returns the facade object itself.
func asRValue(v runtime.Value) *RValue {
	if gv, ok := runtime.Unwrap(v).(*runtime.GoValue); ok {
		if rv, ok := gv.V.(*RValue); ok {
			return rv
		}
	}
	return nil
}

// asRType unwraps a script argument to its facade type.
func asRType(v runtime.Value) *RType {
	if gv, ok := runtime.Unwrap(v).(*runtime.GoValue); ok {
		if rt, ok := gv.V.(*RType); ok {
			return rt
		}
	}
	return nil
}

// ---- package-level reflect functions ----

func (e *Env) valueOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.ValueOf needs 1 arg, got %d", len(args))
	}
	return &runtime.GoValue{V: e.valueOfValue(vc, args[0])}, nil
}

// valueOfValue is ValueOf over a raw runtime value.
func (e *Env) valueOfValue(vc runtime.VMCaller, v runtime.Value) *RValue {
	if v == nil || v == runtime.NIL {
		return &RValue{e: e, vc: vc}
	}
	switch x := v.(type) {
	case *runtime.IfaceNil:
		// an interface holding nil has no dynamic type
		return &RValue{e: e, vc: vc}
	case *runtime.Named:
		if gv, ok := x.V.(*runtime.GoValue); ok {
			if rv, ok := gv.V.(*RValue); ok {
				// a tagged facade box (reflect.Value{}) reflects to a real
				// reflect.Value struct, like the GoValue arm below
				return &RValue{e: e, vc: vc, rv: reflect.ValueOf(reflect.ValueOf(rv))}
			}
			// a tagged host box (host composite literal T{}): reflect
			// the addressable value inside — Type reads T, not *T.
			rv := reflect.ValueOf(gv.V)
			if rv.IsValid() && rv.Kind() == reflect.Pointer && !rv.IsNil() {
				rv = rv.Elem()
			}
			return &RValue{e: e, vc: vc, rv: rv}
		}
		// a tag on a script payload keeps its declared type — fall
		// through to the copy-and-td path below (Interface must hand
		// the named value back for TypeAssert/%T).
	case *runtime.GoValue:
		if rv, ok := x.V.(*RValue); ok {
			// reflecting a facade value itself yields Go's reflect.Value
			// struct (typ/ptr/flag), not the facade implementation struct
			// and not the script value it views
			return &RValue{e: e, vc: vc, rv: reflect.ValueOf(reflect.ValueOf(rv))}
		}
		if x.V == nil {
			return &RValue{e: e, vc: vc}
		}
		return &RValue{e: e, vc: vc, rv: reflect.ValueOf(x.V)}
	}
	// reflect.ValueOf copies its argument into the interface — a script
	// struct must be snapshotted, or later script writes leak into the
	// stored value (slices/maps/pointers keep sharing, like Go).
	return &RValue{e: e, vc: vc, val: runtime.Copy(v), td: typeOfValue(e, v)}
}

func (e *Env) typeOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.TypeOf needs 1 arg, got %d", len(args))
	}
	rv := e.valueOfValue(vc, args[0])
	if !rv.IsValid() {
		// reflect.TypeOf(nil) == nil — produce the interface nil, not
		// an invalid facade object, so `t == nil` checks work.
		return runtime.NIL, nil
	}
	return &runtime.GoValue{V: rv.Type()}, nil
}

func (e *Env) indirect(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.Indirect needs 1 arg, got %d", len(args))
	}
	v := asRValue(args[0])
	if v == nil {
		return nil, fmt.Errorf("reflect.Indirect: arg is %T, not a reflect.Value", args[0])
	}
	// Indirect dereferences exactly once like Go (v.Elem() when ptr) —
	// looping would skip one indirection too many for **T.
	out := v
	if out.Kind() == reflect.Ptr {
		out = out.Elem()
	}
	return &runtime.GoValue{V: out}, nil
}

func (e *Env) new_(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.New needs 1 arg, got %d", len(args))
	}
	t := asRType(args[0])
	if t == nil {
		return nil, fmt.Errorf("reflect.New: arg is %T, not a reflect.Type", args[0])
	}
	if t.rt != nil {
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: reflect.New(t.rt)}}, nil
	}
	// a pointer is a cell; New(t) is the cell holding t's zero.
	ptd := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: t.td,
		Anon: &ast.StarExpr{X: e.exprOf(t.td)}}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: &runtime.Cell{Elem: e.zeroOf(vc, t.td)}, td: ptd}}, nil
}

func (e *Env) zero(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.Zero needs 1 arg, got %d", len(args))
	}
	t := asRType(args[0])
	if t == nil {
		return nil, fmt.Errorf("reflect.Zero: arg is %T, not a reflect.Type", args[0])
	}
	if t.rt != nil {
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: reflect.Zero(t.rt)}}, nil
	}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: e.zeroOf(vc, t.td), td: t.td}}, nil
}

// zeroOf produces t's zero: delegate to the VM's zero when a caller is
// bound (it honors HostNew and co. via runtime.Zero anyway).
func (e *Env) zeroOf(vc runtime.VMCaller, td *runtime.TypeDef) runtime.Value {
	if vc != nil && td != nil && td.HostNew == nil {
		if z := vc.Zero(td); z != nil {
			return z
		}
	}
	return runtime.Zero(td)
}

func (e *Env) ptrTo(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.PtrTo needs 1 arg, got %d", len(args))
	}
	t := asRType(args[0])
	if t == nil {
		return nil, fmt.Errorf("reflect.PtrTo: arg is %T, not a reflect.Type", args[0])
	}
	if t.rt != nil {
		return &runtime.GoValue{V: e.hostTypeOf(reflect.PointerTo(t.rt))}, nil
	}
	return &runtime.GoValue{V: e.rtypeOf(&runtime.TypeDef{Kind: runtime.KindPointer, Elem: t.td,
		Anon: &ast.StarExpr{X: e.exprOf(t.td)}})}, nil
}

func (e *Env) sliceOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.SliceOf needs 1 arg, got %d", len(args))
	}
	t := asRType(args[0])
	if t == nil {
		return nil, fmt.Errorf("reflect.SliceOf: arg is %T, not a reflect.Type", args[0])
	}
	if t.rt != nil {
		return &runtime.GoValue{V: e.hostTypeOf(reflect.SliceOf(t.rt))}, nil
	}
	return &runtime.GoValue{V: e.rtypeOf(&runtime.TypeDef{Kind: runtime.KindSlice, Elem: t.td,
		Anon: &ast.ArrayType{Elt: e.exprOf(t.td)}})}, nil
}

func (e *Env) mapOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.MapOf needs 2 args, got %d", len(args))
	}
	k, v := asRType(args[0]), asRType(args[1])
	if k == nil || v == nil {
		return nil, fmt.Errorf("reflect.MapOf: args must be reflect.Type")
	}
	if k.rt != nil && v.rt != nil {
		return &runtime.GoValue{V: e.hostTypeOf(reflect.MapOf(k.rt, v.rt))}, nil
	}
	mtd := &runtime.TypeDef{Kind: runtime.KindMap, Elem: v.td,
		Anon: &ast.MapType{Key: e.exprOf(k.td), Value: e.exprOf(v.td)}}
	return &runtime.GoValue{V: e.internT(e.keyOf(mtd), &RType{td: mtd, keyTd: k.td})}, nil
}

func (e *Env) chanOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.ChanOf needs 2 args, got %d", len(args))
	}
	t := asRType(args[1])
	if t == nil {
		return nil, fmt.Errorf("reflect.ChanOf: arg 1 is %T, not a reflect.Type", args[1])
	}
	if t.rt != nil {
		if dir, ok := dirOf(args[0]); ok {
			return &runtime.GoValue{V: e.hostTypeOf(reflect.ChanOf(dir, t.rt))}, nil
		}
	}
	// direction is part of the type — it rides on the Anon ChanType.
	adir := ast.RECV | ast.SEND
	if dir, ok := dirOf(args[0]); ok {
		switch dir {
		case reflect.RecvDir:
			adir = ast.RECV
		case reflect.SendDir:
			adir = ast.SEND
		}
	}
	return &runtime.GoValue{V: e.rtypeOf(&runtime.TypeDef{Kind: runtime.KindChan, Elem: t.td,
		Anon: &ast.ChanType{Dir: adir, Value: e.exprOf(t.td)}})}, nil
}

func (e *Env) arrayOf(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.ArrayOf needs 2 args, got %d", len(args))
	}
	n, t := intOf(args[0]), asRType(args[1])
	if t == nil {
		return nil, fmt.Errorf("reflect.ArrayOf: arg 1 is %T, not a reflect.Type", args[1])
	}
	if n < 0 {
		panic(&runtime.Panic{Value: "reflect: negative length"})
	}
	if t.rt != nil {
		return &runtime.GoValue{V: e.hostTypeOf(reflect.ArrayOf(int(n), t.rt))}, nil
	}
	mtd := &runtime.TypeDef{Kind: runtime.KindSlice, Elem: t.td,
		Anon: &ast.ArrayType{Len: &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(n, 10)},
			Elt: e.exprOf(t.td)}}
	return &runtime.GoValue{V: e.internT(e.keyOf(mtd), &RType{td: mtd, isArray: true, alen: int(n)})}, nil
}

func (e *Env) makeSlice(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("reflect.MakeSlice needs 3 args, got %d", len(args))
	}
	t, l, c := asRType(args[0]), int(intOf(args[1])), int(intOf(args[2]))
	if t == nil {
		return nil, fmt.Errorf("reflect.MakeSlice: arg 0 is %T, not a reflect.Type", args[0])
	}
	if t.rt != nil {
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: reflect.MakeSlice(t.rt, l, c)}}, nil
	}
	if t.Kind() != reflect.Slice {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.MakeSlice of non-slice type %s", t.String())})
	}
	if l < 0 {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.MakeSlice: negative len %d", l)})
	}
	if c < 0 {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.MakeSlice: negative cap %d", c)})
	}
	if l > c {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.MakeSlice: len %d greater than cap %d", l, c)})
	}
	et := e.elemOf(t.td)
	// the requested cap lands in the Go backing so Cap() reports it
	elems := make([]runtime.Value, l, c)
	for i := range elems {
		elems[i] = e.zeroOf(vc, et)
	}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: &runtime.Slice{Elems: elems, Typ: t.td}, td: t.td}}, nil
}

func (e *Env) makeMap(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("reflect.MakeMap needs 1 arg, got %d", len(args))
	}
	return e.makeMapN(vc, args[0], 0)
}

func (e *Env) makeMapWithSize(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.MakeMapWithSize needs 2 args, got %d", len(args))
	}
	return e.makeMapN(vc, args[0], int(intOf(args[1])))
}

func (e *Env) makeMapN(vc runtime.VMCaller, arg runtime.Value, n int) (runtime.Value, error) {
	t := asRType(arg)
	if t == nil {
		return nil, fmt.Errorf("reflect.MakeMap: arg is %T, not a reflect.Type", arg)
	}
	if t.rt != nil {
		var rv reflect.Value
		if n > 0 {
			rv = reflect.MakeMapWithSize(t.rt, n)
		} else {
			rv = reflect.MakeMap(t.rt)
		}
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: rv}}, nil
	}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: &runtime.Map{Typ: t.td}, td: t.td}}, nil
}

func (e *Env) append_(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("reflect.Append needs at least 1 arg, got %d", len(args))
	}
	s := asRValue(args[0])
	if s == nil {
		return nil, fmt.Errorf("reflect.Append: arg 0 is %T, not a reflect.Value", args[0])
	}
	elems := make([]runtime.Value, 0, len(args)-1)
	for _, a := range args[1:] {
		rv := asRValue(a)
		if rv == nil {
			return nil, fmt.Errorf("reflect.Append: arg is %T, not a reflect.Value", a)
		}
		elems = append(elems, rv.ifaceVal())
	}
	if s.rv.IsValid() {
		if s.rv.Kind() != reflect.Slice {
			// Go checks the kind before touching the elements — let
			// reflect.Append raise its own 'unknown method' panic.
			reflect.Append(s.rv)
		}
		in := make([]reflect.Value, len(elems))
		for i, el := range elems {
			rv, err := toHost(el, s.rv.Type().Elem())
			if err != nil {
				return nil, fmt.Errorf("reflect.Append: %w", err)
			}
			in[i] = rv
		}
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: reflect.Append(s.rv, in...)}}, nil
	}
	// Go's MustBe(Slice) rejects every other kind — arrays included —
	// with 'reflect: call of unknown method on X Value'.
	if s.Kind() != reflect.Slice {
		trap("call of unknown method on %s Value", s.kindStr())
	}
	sl, ok := s.get().(*runtime.Slice)
	if !ok {
		return nil, fmt.Errorf("reflect.Append on %s", s.Kind())
	}
	// append into the live backing: spare capacity is reused, so writes
	// through the result's elements land in the caller's array like Go.
	out := &runtime.Slice{Elems: append(sl.Elems, elems...), Typ: sl.Typ}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: out, td: s.td}}, nil
}

func (e *Env) appendSlice(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.AppendSlice needs 2 args, got %d", len(args))
	}
	s, t := asRValue(args[0]), asRValue(args[1])
	if s == nil || t == nil {
		return nil, fmt.Errorf("reflect.AppendSlice: args must be reflect.Value")
	}
	if s.rv.IsValid() && t.rv.IsValid() {
		if s.rv.Kind() != reflect.Slice || t.rv.Kind() != reflect.Slice {
			// Go checks both kinds before copying — let AppendSlice
			// raise its own 'unknown method' panic.
			reflect.AppendSlice(s.rv, t.rv)
		}
		return &runtime.GoValue{V: &RValue{e: e, vc: vc, rv: reflect.AppendSlice(s.rv, t.rv)}}, nil
	}
	// Go's MustBe(Slice) fires on either operand before copying —
	// 'reflect: call of unknown method on X Value' names the bad kind.
	if s.Kind() != reflect.Slice {
		trap("call of unknown method on %s Value", s.kindStr())
	}
	if t.Kind() != reflect.Slice {
		trap("call of unknown method on %s Value", t.kindStr())
	}
	sl, ok1 := s.get().(*runtime.Slice)
	tl, ok2 := t.get().(*runtime.Slice)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("reflect.AppendSlice on non-slice")
	}
	elems := make([]runtime.Value, len(tl.Elems))
	for i, el := range tl.Elems {
		elems[i] = runtime.Copy(el)
	}
	out := &runtime.Slice{Elems: append(sl.Elems, elems...), Typ: sl.Typ}
	return &runtime.GoValue{V: &RValue{e: e, vc: vc, val: out, td: s.td}}, nil
}

func (e *Env) copy_(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("reflect.Copy needs 2 args, got %d", len(args))
	}
	d, s := asRValue(args[0]), asRValue(args[1])
	if d == nil || s == nil {
		return nil, fmt.Errorf("reflect.Copy: args must be reflect.Value")
	}
	if d.rv.IsValid() && s.rv.IsValid() {
		return int64(reflect.Copy(d.rv, s.rv)), nil
	}
	// Go's checks, in order: the destination must be a slice or an
	// addressable array, the source a slice/array (or a string into a
	// byte destination), and only then does the copy run.
	dk := d.Kind()
	if dk != reflect.Slice && dk != reflect.Array {
		trap("call of reflect.Copy on %s Value", d.kindStr())
	}
	if dk == reflect.Array && !d.CanAddr() {
		trap("unknown method using unaddressable value")
	}
	sk := s.Kind()
	if sk != reflect.Slice && sk != reflect.Array && sk != reflect.String {
		trap("call of reflect.Copy on %s Value", s.kindStr())
	}
	ds, dok := d.get().(*runtime.Slice)
	var ss []runtime.Value
	switch sv := s.get().(type) {
	case *runtime.Slice:
		ss = sv.Elems
	case string:
		if et := e.elemOf(d.td); et != nil && e.kindOfTd(et) != reflect.Uint8 {
			trap("call of reflect.Copy on string Value")
		}
		for i := 0; i < len(sv); i++ {
			ss = append(ss, int64(sv[i]))
		}
	}
	if !dok || ss == nil {
		return nil, fmt.Errorf("reflect.Copy on non-slice")
	}
	n := len(ss)
	if len(ds.Elems) < n {
		n = len(ds.Elems)
	}
	// each element is assigned by value — struct elements deep-copy.
	for i := 0; i < n; i++ {
		ds.Elems[i] = runtime.Copy(ss[i])
	}
	return int64(n), nil
}

// ---- arg helpers ----

func intOf(v runtime.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case *runtime.Named:
		return intOf(x.V)
	case *runtime.UConst:
		if i, ok := runtime.Unwrap(x).(int64); ok {
			return i
		}
	}
	return 0
}

func dirOf(v runtime.Value) (reflect.ChanDir, bool) {
	if gv, ok := v.(*runtime.GoValue); ok {
		if d, ok := gv.V.(reflect.ChanDir); ok {
			return d, true
		}
	}
	return reflect.BothDir, false
}
