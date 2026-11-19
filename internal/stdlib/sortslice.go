package stdlib

import (
	"cmp"
	"fmt"
	"go/ast"
	"maps"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/podhmo/minigo/builtin"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func NativeSeq(v runtime.Value) *runtime.SeqSource {
	if c, ok := runtime.Unwrap(v).(*runtime.BuiltinFunc); ok {
		return c.Seq
	}
	return nil
}

// DriveSeq drives seq with a Go-side consumer: an intrinsic seq runs its
// producer natively; anything else is called through the VM with a
// yield builtin wrapping take.

func DriveSeq(vc runtime.ScriptCaller, seq runtime.Value, take func(args []runtime.Value)) error {
	if src := NativeSeq(seq); src != nil {
		return src.Each(vc, func(args []runtime.Value) bool {
			take(args)
			return true
		})
	}
	yield := &runtime.BuiltinFunc{Name: "yield", SyncCallbacks: true, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		take(args)
		return true, nil
	}}
	hostbridge.CallOrPanic(vc, seq, []runtime.Value{yield})
	return nil
}

// MapSeqKind selects what a map seq yields.

type MapSeqKind int

const (
	MapSeqKeys MapSeqKind = iota
	MapSeqValues
	MapSeqAll
)

// HostMapSource is the native source of maps.Keys / Values / All over a
// host map: lazy over a live MapIter like Go's — an entry deleted
// before its turn is not produced and a value written meanwhile reads
// its new content. A nil map produces nothing.

func HostMapSource(rv reflect.Value, kind MapSeqKind) *runtime.SeqSource {
	src := &runtime.SeqSource{
		Each: func(_ runtime.ScriptCaller, yield func([]runtime.Value) bool) error {
			if !rv.IsValid() || rv.IsNil() {
				return nil
			}
			n := 1
			if kind == MapSeqAll {
				n = 2
			}
			buf := make([]runtime.Value, n)
			it := rv.MapRange()
			for it.Next() {
				switch kind {
				case MapSeqKeys:
					buf[0] = ScriptVal(it.Key().Interface())
				case MapSeqValues:
					buf[0] = ScriptVal(it.Value().Interface())
				default:
					buf[0] = ScriptVal(it.Key().Interface())
					buf[1] = ScriptVal(it.Value().Interface())
				}
				if !yield(buf) {
					return nil
				}
			}
			return nil
		},
	}
	switch kind {
	case MapSeqKeys:
		src.Collect = func(runtime.ScriptCaller) []runtime.Value {
			if !rv.IsValid() || rv.IsNil() {
				return nil
			}
			out := make([]runtime.Value, 0, rv.Len())
			for it := rv.MapRange(); it.Next(); {
				out = append(out, ScriptVal(it.Key().Interface()))
			}
			return out
		}
	case MapSeqValues:
		src.Collect = func(runtime.ScriptCaller) []runtime.Value {
			if !rv.IsValid() || rv.IsNil() {
				return nil
			}
			out := make([]runtime.Value, 0, rv.Len())
			// MapRange, not MapKeys+MapIndex: a NaN key is not equal to
			// itself, so a re-lookup comes back invalid and panics.
			for it := rv.MapRange(); it.Next(); {
				out = append(out, ScriptVal(it.Value().Interface()))
			}
			return out
		}
	}
	return src
}

// BasicTdOfReflect returns the element typedef of a host map's key or
// element type when it spells a predeclared basic (int, string, ...) or
// the empty interface (`any`) — nil for named host types, where the seq
// stays untyped.

func BasicTdOfReflect(rt reflect.Type) *runtime.TypeDef {
	if rt == nil {
		return nil
	}
	if rt.Kind() == reflect.Interface && rt.NumMethod() == 0 {
		return &runtime.TypeDef{Kind: runtime.KindInterface, Anon: &ast.InterfaceType{}}
	}
	if rt.PkgPath() == "" && rt.Name() != "" {
		if td := runtime.BasicTypedef(rt.Name()); td != nil {
			return td
		}
	}
	return nil
}

// HostMapSeqArg reads a host map argument of the maps seq helpers: the
// live reflect map plus the typedefs of its key and element types when
// they spell predeclared basics or `any` (nil when unspellable — the
// seq then stays untyped). ok is false for a non-host-map.

func HostMapSeqArg(v runtime.Value) (rv reflect.Value, kt, vt *runtime.TypeDef, ok bool) {
	nv := hostbridge.GoNative(v)
	if nv == nil {
		return reflect.Value{}, nil, nil, false
	}
	rv = reflect.ValueOf(nv)
	if !rv.IsValid() || rv.Kind() != reflect.Map {
		return reflect.Value{}, nil, nil, false
	}
	return rv, BasicTdOfReflect(rv.Type().Key()), BasicTdOfReflect(rv.Type().Elem()), true
}

// HostMapElemOf converts a yielded script pair element to the host
// map's key or element reflect type for maps.Insert.

func HostMapElemOf(v runtime.Value, rt reflect.Type) (reflect.Value, error) {
	return HostReflectOf(hostbridge.GoNative(v), rt)
}

// HostReflectOf converts a native value to reflect type rt for host map
// writes: the assignable/convertible scalar paths first, then slices
// and maps rebuilt element-wise — a script container arrives through
// hostbridge.GoNative as []any / map[any]any and a host map's declared element
// type ([]string, map[string]int, ...) is not one conversion away.

func HostReflectOf(nv any, rt reflect.Type) (reflect.Value, error) {
	if nv == nil {
		if rt.Kind() == reflect.Interface || rt.Kind() == reflect.Pointer ||
			rt.Kind() == reflect.Map || rt.Kind() == reflect.Slice ||
			rt.Kind() == reflect.Chan || rt.Kind() == reflect.Func {
			return reflect.Zero(rt), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot assign nil to %s", rt)
	}
	rv := reflect.ValueOf(nv)
	if rv.Type().AssignableTo(rt) {
		return rv, nil
	}
	if rv.Type().ConvertibleTo(rt) {
		return rv.Convert(rt), nil
	}
	switch rt.Kind() {
	case reflect.Slice:
		if rv.Kind() != reflect.Slice {
			break
		}
		out := reflect.MakeSlice(rt, rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			ev, err := HostReflectOf(rv.Index(i).Interface(), rt.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(ev)
		}
		return out, nil
	case reflect.Map:
		if rv.Kind() != reflect.Map {
			break
		}
		out := reflect.MakeMapWithSize(rt, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			kv, err := HostReflectOf(it.Key().Interface(), rt.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			vv, err := HostReflectOf(it.Value().Interface(), rt.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(kv, vv)
		}
		return out, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %T as %s in map entry", nv, rt)
}

// ScriptMapSeqArg reads a map argument of the maps seq helpers: the
// script map (nil for a nil map), its typedef, and the declared key and
// value typedefs (nil when unknown). ok is false for a non-map.

func ScriptMapSeqArg(vc runtime.SeqTyper, v runtime.Value) (m *runtime.Map, kt, vt *runtime.TypeDef, ok bool) {
	mt := runtime.TagOf(v)
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Map:
		m = x
		if mt == nil {
			mt = x.Typ
		}
	case *runtime.TypedNil:
		if mt == nil {
			mt = x.Typ
		}
		if MapTypeExpr(mt) == nil {
			return nil, nil, nil, false
		}
	case runtime.Nil:
	default:
		return nil, nil, nil, false
	}
	if mt != nil {
		kt, _ = MapKeyType(vc, mt)
		vt, _ = vc.ElemOf(mt)
	}
	return m, kt, vt, true
}

// MapKeyConv converts a script map's stored (untagged) key back to the
// declared key type — a map[fs.FileMode]bool yields FileModes, not their
// payloads. nil when keys need no conversion: an interface-keyed map's
// keys already carry their dynamic types, and a predeclared int, string,
// float64 or bool key is stored bare. A key already tagged with the key
// type passes through.

func MapKeyConv(vc runtime.Converter, m *runtime.Map, kt *runtime.TypeDef) func(runtime.Value) runtime.Value {
	if kt == nil || m == nil || m.IfaceKey || kt.Kind == runtime.KindInterface || PlainKeyTd(kt) {
		return nil
	}
	return func(k runtime.Value) runtime.Value {
		if n, ok := k.(*runtime.Named); ok && n.Typ == kt {
			return k
		}
		if ck, err := vc.Convert(kt, k); err == nil {
			return ck
		}
		return k
	}
}

// PlainKeyTd reports whether kt is a predeclared type whose values are
// stored bare (int, string, float64, bool) — its keys need no
// conversion back to the declared type.

func PlainKeyTd(kt *runtime.TypeDef) bool {
	for _, name := range [...]string{"int", "string", "float64", "bool"} {
		if kt == runtime.BasicTypedef(name) {
			return true
		}
	}
	return false
}

// SeqSliceArg reads the slice argument of the slices seq helpers: the
// slice (nil for a nil slice), the argument's own type (a named slice
// type stays named) and its element typedef. ok is false for a non-slice.

func SeqSliceArg(vc runtime.ElemTyper, v runtime.Value) (s *runtime.Slice, st, et *runtime.TypeDef, ok bool) {
	if d, isRef := runtime.Deref(v); isRef {
		v = d
	}
	st = runtime.TagOf(v)
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.Slice:
		s = x
		if st == nil {
			st = x.Typ
		}
	case *runtime.TypedNil:
		if !NilSliceArg(x) {
			return nil, nil, nil, false
		}
		if st == nil {
			st = x.Typ
		}
	default:
		return nil, nil, nil, false
	}
	if st != nil {
		et, _ = vc.ElemOf(st)
	}
	return s, st, et, true
}

// SliceElemAt reads s[i] — a virtual (zero-size element) slice stores
// none and reads its zero.

func SliceElemAt(s *runtime.Slice, i int) runtime.Value {
	if s.Virtual() {
		return s.Zero
	}
	return s.Elems[i]
}

// SliceLen is len(s) for a possibly nil slice.

func SliceLen(s *runtime.Slice) int {
	if s == nil {
		return 0
	}
	return int(s.Len())
}

// CollectSeq drives seq and returns its elements, boxed as a []E typed
// after the seq (a nil slice when it yields nothing, like
// slices.Collect), plus the raw elements and the slice typedef.

func CollectSeq(vc runtime.ScriptCaller, seq runtime.Value) (runtime.Value, []runtime.Value, *runtime.TypeDef, error) {
	el, err := SeqElems(vc, seq)
	if err != nil {
		return nil, nil, nil, err
	}
	st := SeqSliceTd(seq)
	return PackCollected(el, st), el, st, nil
}

// PackCollected boxes collected elements: a nil []E when there are none
// and the type is known, else a slice typed st.

func PackCollected(el []runtime.Value, st *runtime.TypeDef) runtime.Value {
	if len(el) == 0 && st != nil {
		return &runtime.TypedNil{Typ: st}
	}
	return &runtime.Slice{Elems: el, Typ: st}
}

// MapKeyOperand is the stored form of key k in a map typed mt.

// slicesSeqHelpers adds the iterator helpers of slices (Go 1.23's
// iter.Seq surface) to its symbol table.

func SlicesSeqHelpers(typedSeq func(*runtime.BuiltinFunc, ...*runtime.TypeDef) runtime.Value, slicesPkg map[string]runtime.Value) map[string]runtime.Value {
	sliceSeq := func(name string, two bool, each func(s *runtime.Slice, yield func([]runtime.Value) bool)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
			}
			s, _, et, ok := SeqSliceArg(vc, args[0])
			if !ok {
				return nil, fmt.Errorf("%s: arg must be a slice", name)
			}
			fn := SeqFn(name, &runtime.SeqSource{Each: func(_ runtime.ScriptCaller, yield func([]runtime.Value) bool) error {
				each(s, yield)
				return nil
			}})
			if two {
				return typedSeq(fn, runtime.BasicTypedef("int"), et), nil
			}
			return typedSeq(fn, et), nil
		}}
	}
	slicesPkg["Values"] = sliceSeq("slices.Values", false, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 1)
		for i, n := 0, SliceLen(s); i < n; i++ {
			buf[0] = SliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["All"] = sliceSeq("slices.All", true, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 2)
		for i, n := 0, SliceLen(s); i < n; i++ {
			buf[0], buf[1] = int64(i), SliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["Backward"] = sliceSeq("slices.Backward", true, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 2)
		for i := SliceLen(s) - 1; i >= 0; i-- {
			buf[0], buf[1] = int64(i), SliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["Collect"] = &runtime.BuiltinFunc{Name: "slices.Collect", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("slices.Collect needs 1 arg, got %d", len(args))
		}
		out, _, _, err := CollectSeq(vc, args[0])
		if err != nil {
			return nil, fmt.Errorf("slices.Collect: %w", err)
		}
		return out, nil
	}}
	slicesPkg["Sorted"] = &runtime.BuiltinFunc{Name: "slices.Sorted", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("slices.Sorted needs 1 arg")
		}
		_, el, st, err := CollectSeq(vc, args[0])
		if err != nil {
			return nil, fmt.Errorf("slices.Sorted: %w", err)
		}
		sort.Slice(el, func(i, j int) bool { return LessScript(el[i], el[j]) })
		return PackCollected(el, st), nil
	}}
	slicesPkg["AppendSeq"] = &runtime.BuiltinFunc{Name: "slices.AppendSeq", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("slices.AppendSeq needs 2 args, got %d", len(args))
		}
		s, st, _, ok := SeqSliceArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("slices.AppendSeq: first arg must be a slice")
		}
		el, err := SeqElems(vc, args[1])
		if err != nil {
			return nil, fmt.Errorf("slices.AppendSeq: %w", err)
		}
		if len(el) == 0 {
			return args[0], nil
		}
		// AppendSeq is append over a seq: the result shares the
		// backing while spare capacity suffices, and a grown array
		// copies every moved element (gc's growslice memcpy). The
		// appended elements are copied in either case — gc stores
		// element copies, not references to the yielded values.
		var base []runtime.Value
		if s != nil && !s.Virtual() {
			base = s.Elems
		} else {
			for i, n := 0, SliceLen(s); i < n; i++ {
				base = append(base, SliceElemAt(s, i))
			}
		}
		add := make([]runtime.Value, len(el))
		for i, e := range el {
			add[i] = runtime.Copy(e)
		}
		grown := append(base, add...)
		if cap(grown) > cap(base) {
			for i := range base {
				grown[i] = vc.Copy(grown[i])
			}
			FillHoles(vc, st, grown[len(grown):cap(grown)])
		}
		return RetagSlice(args[0], &runtime.Slice{Elems: grown, Typ: st}), nil
	}}
	sortedBy := func(name string, sorter func([]runtime.Value, func(a, b runtime.Value) int)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
			}
			_, el, st, err := CollectSeq(vc, args[0])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			cmp := args[1]
			sorter(el, func(a, b runtime.Value) int {
				n, _ := runtime.Unwrap(hostbridge.CallOrPanic(vc, cmp, []runtime.Value{a, b})).(int64)
				return int(n)
			})
			return PackCollected(el, st), nil
		}}
	}
	slicesPkg["SortedFunc"] = sortedBy("slices.SortedFunc", slices.SortFunc[[]runtime.Value])
	slicesPkg["SortedStableFunc"] = sortedBy("slices.SortedStableFunc", slices.SortStableFunc[[]runtime.Value])
	slicesPkg["Chunk"] = &runtime.BuiltinFunc{Name: "slices.Chunk", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("slices.Chunk needs 2 args, got %d", len(args))
		}
		s, st, _, ok := SeqSliceArg(vc, args[0])
		if !ok || s != nil && s.Virtual() {
			return nil, fmt.Errorf("slices.Chunk: arg must be a slice")
		}
		n := IntOf(args[1])
		if n < 1 {
			panic(&runtime.Panic{Value: "cannot be less than 1"})
		}
		// capture the element, not the args slice: Each may run after
		// Fn returns, when the caller's arg buffer is already rewritten.
		orig := args[0]
		fn := SeqFn("slices.Chunk", &runtime.SeqSource{Each: func(_ runtime.ScriptCaller, yield func([]runtime.Value) bool) error {
			buf := make([]runtime.Value, 1)
			for i, l := 0, SliceLen(s); i < l; i += n {
				end := min(i+n, l)
				buf[0] = RetagSlice(orig, &runtime.Slice{Elems: s.Elems[i:end:end], Typ: st})
				if !yield(buf) {
					return nil
				}
			}
			return nil
		}})
		return typedSeq(fn, st), nil
	}}
	return slicesPkg
}

// mapsSeqHelpers adds the iterator helpers of maps (Go 1.23's iter.Seq
// surface) to its symbol table.

func MapsSeqHelpers(typedSeq func(*runtime.BuiltinFunc, ...*runtime.TypeDef) runtime.Value, mapsPkg map[string]runtime.Value) map[string]runtime.Value {
	mapsPkg["Keys"] = &runtime.BuiltinFunc{Name: "maps.Keys", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.Keys needs 1 arg, got %d", len(args))
		}
		m, kt, _, ok := ScriptMapSeqArg(vc, args[0])
		if !ok {
			rv, hkt, _, hok := HostMapSeqArg(args[0])
			if !hok {
				return SeqFn("maps.Keys", SliceSource(MapKeys(hostbridge.GoNative(args[0])).Elems)), nil
			}
			return typedSeq(SeqFn("maps.Keys", HostMapSource(rv, MapSeqKeys)), hkt), nil
		}
		return typedSeq(SeqFn("maps.Keys", MapSource(m, MapSeqKeys, MapKeyConv(vc, m, kt))), kt), nil
	}}
	mapsPkg["Values"] = &runtime.BuiltinFunc{Name: "maps.Values", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.Values needs 1 arg, got %d", len(args))
		}
		m, _, vt, ok := ScriptMapSeqArg(vc, args[0])
		if !ok {
			rv, _, hvt, hok := HostMapSeqArg(args[0])
			if !hok {
				return SeqFn("maps.Values", SliceSource(MapValues(hostbridge.GoNative(args[0])).Elems)), nil
			}
			return typedSeq(SeqFn("maps.Values", HostMapSource(rv, MapSeqValues)), hvt), nil
		}
		return typedSeq(SeqFn("maps.Values", MapSource(m, MapSeqValues, nil)), vt), nil
	}}
	mapsPkg["All"] = &runtime.BuiltinFunc{Name: "maps.All", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.All needs 1 arg, got %d", len(args))
		}
		m, kt, vt, ok := ScriptMapSeqArg(vc, args[0])
		if !ok {
			rv, hkt, hvt, hok := HostMapSeqArg(args[0])
			if !hok {
				return nil, fmt.Errorf("maps.All: arg must be a map")
			}
			return typedSeq(SeqFn("maps.All", HostMapSource(rv, MapSeqAll)), hkt, hvt), nil
		}
		return typedSeq(SeqFn("maps.All", MapSource(m, MapSeqAll, MapKeyConv(vc, m, kt))), kt, vt), nil
	}}
	mapsPkg["Insert"] = &runtime.BuiltinFunc{Name: "maps.Insert", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("maps.Insert needs 2 args, got %d", len(args))
		}
		m, _, _, ok := ScriptMapSeqArg(vc, args[0])
		if !ok {
			rv, _, _, hok := HostMapSeqArg(args[0])
			if !hok {
				return nil, fmt.Errorf("maps.Insert: first arg must be a map")
			}
			krt, vrt := rv.Type().Key(), rv.Type().Elem()
			var bad bool
			err := DriveSeq(vc, args[1], func(ya []runtime.Value) {
				if len(ya) != 2 {
					bad = true
					return
				}
				if rv.IsNil() {
					panic(runtime.PlainPanic("assignment to entry in nil map"))
				}
				kv, kerr := HostMapElemOf(ya[0], krt)
				if kerr != nil {
					panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: kerr.Error()}})
				}
				vv, verr := HostMapElemOf(ya[1], vrt)
				if verr != nil {
					panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: verr.Error()}})
				}
				rv.SetMapIndex(kv, vv)
			})
			if err == nil && bad {
				err = fmt.Errorf("maps.Insert: seq must yield key and value")
			}
			if err != nil {
				return nil, err
			}
			return runtime.NIL, nil
		}
		mt := runtime.TagOf(args[0])
		if m != nil && mt == nil {
			mt = m.Typ
		}
		var bad bool
		err := DriveSeq(vc, args[1], func(ya []runtime.Value) {
			if len(ya) != 2 {
				bad = true
				return
			}
			if m == nil {
				panic(runtime.PlainPanic("assignment to entry in nil map"))
			}
			m.Insert(MapKeyOperand(vc, mt, ya[0]), ya[1])
		})
		if err == nil && bad {
			err = fmt.Errorf("maps.Insert: seq must yield key and value")
		}
		if err != nil {
			return nil, err
		}
		return runtime.NIL, nil
	}}
	mapsPkg["Collect"] = &runtime.BuiltinFunc{Name: "maps.Collect", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.Collect needs 1 arg, got %d", len(args))
		}
		var mt *runtime.TypeDef
		if els := IterSeqElems(args[0]); len(els) == 2 {
			mt = &runtime.TypeDef{
				Kind: runtime.KindMap, Anon: &ast.MapType{Key: ast.NewIdent("K"), Value: ast.NewIdent("V")},
				Binds: map[string]runtime.Value{"K": els[0], "V": els[1]}, Pkg: els[1].Pkg, File: els[1].File,
			}
		}
		var m *runtime.Map
		if mt != nil {
			m = vc.NewMap(mt)
		} else {
			m = runtime.NewMap(nil, false)
		}
		var bad bool
		err := DriveSeq(vc, args[0], func(ya []runtime.Value) {
			if len(ya) != 2 {
				bad = true
				return
			}
			m.Insert(MapKeyOperand(vc, mt, ya[0]), ya[1])
		})
		if err == nil && bad {
			err = fmt.Errorf("maps.Collect: seq must yield key and value")
		}
		if err != nil {
			return nil, err
		}
		return m, nil
	}}
	return mapsPkg
}

// scanFn adapts fmt's Scan family: the out-params arrive as script refs,
// which host fmt cannot store through, so each is mirrored into a host
// variable of the pointee's type, the real fmt call scans into those, and
// each result is written back through the ref. heads counts the leading
// non-pointer args (a format string or reader); headFn converts any
// prefix of them it needs to (nil: none) and scanFn goNatives the rest,
// so a converter like scanReader never has to know the arg count.

func SortInPlace(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		s := args[0]
		if n, ok := s.(*runtime.Named); ok {
			s = n.V
		}
		switch s := s.(type) {
		case *runtime.Slice:
			SortScript(s.Elems)
		case *runtime.TypedNil:
			// sorting a nil slice is a no-op in Go; nil pointers and
			// other typed nils still fail the reflect type check.
			if !NilSliceArg(s) {
				return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
			}
		default:
			return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
		}
		return runtime.NIL, nil
	}}
}

func NumOf(v runtime.Value) (float64, bool) {
	switch n := v.(type) {
	case *runtime.Named:
		return NumOf(n.V)
	case *runtime.UConst:
		// a constant ranks as its default-typed numeric —
		// `slices.BinarySearch(s, 5)` compares target 5 as a number.
		return builtin.ArgFloat(n)
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case time.Duration:
		return float64(n), true
	}
	return 0, false
}

// SortInterface adapts a script value's Len/Less/Swap members into a
// host sort.Interface for sort.Sort: the callbacks run on the calling
// VM (the usual host-callback hazard — results are only read during the
// call).

func SortInterface(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("sort.Sort needs 1 arg, got %d", len(args))
	}
	if ifc, ok := hostbridge.GoNative(args[0]).(sort.Interface); ok {
		sort.Sort(ifc)
		return runtime.NIL, nil
	}
	lenFn, lok := runtime.IfaceMember(vc, args[0], "Len")
	lessFn, sok := runtime.IfaceMember(vc, args[0], "Less")
	swapFn, wok := runtime.IfaceMember(vc, args[0], "Swap")
	if !lok || !sok || !wok {
		return nil, fmt.Errorf("sort.Sort: %T does not implement sort.Interface", args[0])
	}
	sort.Sort(&ScriptSortable{vc: vc, lenFn: lenFn, lessFn: lessFn, swapFn: swapFn})
	return runtime.NIL, nil
}

func SortSlice(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if HostSortSlice(v, args[0], args[1], false) {
		return runtime.NIL, nil // a boxed host slice (net.IP)
	}
	s, ok := SliceOf(args[0])
	if !ok {
		if NilSliceArg(args[0]) {
			return runtime.NIL, nil // sorting a nil slice is a no-op in Go
		}
		return nil, fmt.Errorf("sort.Slice: first arg must be a slice")
	}
	less := args[1]
	sort.Slice(s.Elems, func(i, j int) bool {
		r := hostbridge.CallOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
		b, _ := r.(bool)
		return b
	})
	return runtime.NIL, nil
}

// SortByCmpFunc implements slices.SortFunc and slices.SortStableFunc: cmp is
// a script callable returning negative/zero/positive. The host sort is the
// real algorithm (pdqsort / stable pdqsort), so element order under a cmp
// that reports ties matches go's even past the 12-element insertion bound.

func SortByCmpFunc(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		s, ok := SliceOf(args[0])
		if !ok {
			if Nilish(args[0]) {
				return args[0], nil // sorting a nil slice is a no-op
			}
			return nil, fmt.Errorf("%s: first arg must be a slice, got %T", name, args[0])
		}
		cmp := args[1]
		cf := func(a, b runtime.Value) int {
			r := hostbridge.CallOrPanic(v, cmp, []runtime.Value{a, b})
			n, _ := runtime.Unwrap(r).(int64)
			return int(n)
		}
		if name == "slices.SortFunc" {
			slices.SortFunc(s.Elems, cf)
		} else {
			slices.SortStableFunc(s.Elems, cf)
		}
		return runtime.NIL, nil
	}}
}

// NilSliceArg reports whether v is a typed nil whose type is a slice —
// sort's slice functions accept a nil slice (a no-op / sorted) but
// still reject nil pointers, maps, chans like Go's reflect-based check.

func NilSliceArg(v runtime.Value) bool {
	tn, ok := runtime.Unwrap(v).(*runtime.TypedNil)
	return ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindSlice && !IsArrayTyp(tn.Typ)
}

// RetagSlice re-applies orig's named slice type (`type Buffer []byte`)
// to out, a new header over orig's elements; an unnamed orig returns out.

func RetagSlice(orig runtime.Value, out *runtime.Slice) runtime.Value {
	if n, ok := orig.(*runtime.Named); ok {
		return runtime.Tag(n.Typ, out)
	}
	return out
}

// PackSlice moves kept elements to the front of s's backing array and
// zeroes the vacated tail, then returns the len(kept) view sharing it —
// the same update Go's slices.DeleteFunc performs, so slices re-sliced
// from the original observe the deletion.

func PackSlice(vc runtime.VMCaller, s *runtime.Slice, kept []runtime.Value) *runtime.Slice {
	copy(s.Elems, kept)
	// ElemZero, not runtime.Zero: a struct element's zero needs its
	// fields typed, and each vacated slot gets its own copy.
	var zero runtime.Value = runtime.NIL
	if s.Typ != nil {
		zero = vc.ElemZero(s.Typ)
	}
	for i := len(kept); i < len(s.Elems); i++ {
		s.Elems[i] = runtime.Copy(zero)
	}
	return &runtime.Slice{Elems: s.Elems[:len(kept)], Typ: s.Typ}
}

// SeqElems collects a seq argument's elements: a *runtime.Slice reads
// verbatim, and an iter.Seq-shaped callable (seqOf's BuiltinFunc, or a
// script func used as a seq) is driven with a yield that gathers each
// element — the seq stops when yield reports false.

func SeqElems(vc runtime.ScriptCaller, v runtime.Value) ([]runtime.Value, error) {
	if s, ok := SliceOf(v); ok {
		return append([]runtime.Value{}, s.Elems...), nil
	}
	if src := NativeSeq(v); src != nil && src.Collect != nil {
		// an intrinsic seq over a known source (maps.Keys/Values of a
		// script map, strings.SplitSeq) reads it in one pass.
		return src.Collect(vc), nil
	}
	var out []runtime.Value
	if err := DriveSeq(vc, v, func(ya []runtime.Value) { out = append(out, ya...) }); err != nil {
		return nil, err
	}
	return out, nil
}

// sliceTypOf keeps a bound slice op's declared element type — a new
// []string built without the tag loses `[]string` reads downstream.

func ScriptSliceArg(vc runtime.ElemTyper, v runtime.Value) ([]runtime.Value, *runtime.TypeDef, bool) {
	var typ *runtime.TypeDef
	if n, ok := v.(*runtime.Named); ok {
		typ, v = n.Typ, n.V
	}
	switch x := v.(type) {
	case *runtime.Slice:
		if typ == nil {
			typ = x.Typ
		}
		var et *runtime.TypeDef
		if typ != nil {
			et, _ = vc.ElemOf(typ)
		}
		return x.Elems, et, true
	case *runtime.TypedNil:
		if NilSliceArg(x) {
			return nil, nil, true
		}
	case *runtime.GoValue:
		// a boxed host named slice (net.IP) read element by element —
		// callers only read, so the elements may be copies.
		return HostSliceElems(x)
	}
	return nil, nil, false
}

// HostSliceElems reads a boxed host slice or array's elements as script
// values. The element typedef is the predeclared basic when the element
// type is an unnamed basic (net.IP's byte), else nil.

func HostSliceElems(x *runtime.GoValue) ([]runtime.Value, *runtime.TypeDef, bool) {
	rv := reflect.ValueOf(x.V)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, nil, false
	}
	el := make([]runtime.Value, rv.Len())
	for i := range el {
		el[i] = ScriptVal(rv.Index(i).Interface())
	}
	var et *runtime.TypeDef
	if t := rv.Type().Elem(); t.PkgPath() == "" && t.Kind() <= reflect.Complex128 && t.Kind() != reflect.Invalid {
		et = runtime.BasicTypedef(t.Kind().String())
	}
	return el, et, true
}

// SortScript orders int64/float64/string elements ascending — the element
// sets sort.Ints, sort.Strings, and slices.Sort support.

func SortScript(el []runtime.Value) {
	sort.Slice(el, func(i, j int) bool {
		return LessScript(el[i], el[j])
	})
}

// SortSlice implements sort.Slice: the less function is a script callable.
// ScriptElems exposes a slice argument's raw elements to Go-side helpers
// (the argument may arrive as a []any via hostbridge.GoNative).

func ScriptElems(v any) []runtime.Value {
	// a defined slice type arrives Named-wrapped — Clone and
	// instantiation results keep the declared-type tag.
	if n, ok := v.(*runtime.Named); ok {
		v = n.V
	}
	switch s := v.(type) {
	case *runtime.Slice:
		return s.Elems
	case []any:
		el := make([]runtime.Value, len(s))
		for i, x := range s {
			el[i] = ScriptVal(x)
		}
		return el
	}
	return nil
}

// LessScript orders int64/float64/string/bool (heterogeneous pairs rank by kind:
// numbers < strings < others, comparing numerically across int64/float64).

func LessScript(a, b runtime.Value) bool {
	an, aok := scalarNum(a)
	bn, bok := scalarNum(b)
	a = builtin.OrderScalar(a)
	b = builtin.OrderScalar(b)
	if aok && bok {
		return cmpNum(an, bn) < 0
	}
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			// false sorts before true, like fmtsort — never by a
			// named bool's String text.
			return !ab && bb
		}
	}
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return as < bs
		}
		return false // strings rank above numbers
	}
	if aok {
		return true
	}
	return false
}

// numOrd is a scalar payload classified for ordering: exact integers
// keep their full range (the float64 route folded a uint64 above
// MaxInt64 and merged neighbours past 2^53), floats compare under
// cmp.Compare's NaN-first rule.
type numOrd struct {
	cls int // 0 signed, 1 unsigned, 2 float
	i   int64
	u   uint64
	f   float64
}

// scalarNum reads a script value's scalar payload for ordering; ok is
// false for non-numeric payloads. The unsigned check runs before
// OrderScalar peels the typedef tag — a uint64-tagged element carries
// its bits as an int64 payload, and the typedef name, not the payload
// sign, picks the unsigned domain.
func scalarNum(v runtime.Value) (numOrd, bool) {
	if n, ok := v.(*runtime.Named); ok && unsignedScalarName(runtime.BasicNameOf(n.Typ)) {
		if i, ok := builtin.OrderScalar(n.V).(int64); ok {
			return numOrd{cls: 1, u: uint64(i)}, true
		}
	}
	v = builtin.OrderScalar(v)
	switch n := v.(type) {
	case *runtime.Named:
		return scalarNum(n.V)
	case *runtime.GoValue:
		// an unsigned conversion past MaxInt64 keeps its bits boxed
		// (vm.convertTo: `uint64(1.6e19)` reads via its host kind).
		return scalarNum(n.V)
	case *runtime.UConst:
		// a constant ranks as its default-typed numeric.
		if f, ok := builtin.ArgFloat(n); ok {
			return numOrd{cls: 2, f: f}, true
		}
	case int64:
		return numOrd{i: n}, true
	case float64:
		return numOrd{cls: 2, f: n}, true
	case uint64:
		return numOrd{cls: 1, u: n}, true
	case time.Duration:
		return numOrd{i: int64(n)}, true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return numOrd{i: rv.Int()}, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return numOrd{cls: 1, u: rv.Uint()}, true
	case reflect.Float32, reflect.Float64:
		return numOrd{cls: 2, f: rv.Float()}, true
	}
	return numOrd{}, false
}

// unsignedScalarName mirrors vm's unsignedName — the typedef spellings
// whose int64 payloads mean unsigned bits.
func unsignedScalarName(name string) bool {
	switch name {
	case "uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

// cmpNum orders two numOrds: a float anywhere pulls the pair onto
// float64 (NaN first, like cmp.Compare); a negative signed value
// ranks below every unsigned one; otherwise the payloads compare
// exactly. ±0 stay equal — slices.Max/Min apply the signed-zero
// preference on top.
func cmpNum(a, b numOrd) int {
	if a.cls == 2 || b.cls == 2 {
		fa, fb := a.f, b.f
		if a.cls != 2 {
			fa = float64(a.u)
			if a.cls == 0 {
				fa = float64(a.i)
			}
		}
		if b.cls != 2 {
			fb = float64(b.u)
			if b.cls == 0 {
				fb = float64(b.i)
			}
		}
		if math.IsNaN(fa) {
			if math.IsNaN(fb) {
				return 0
			}
			return -1
		}
		if math.IsNaN(fb) {
			return 1
		}
		return cmp.Compare(fa, fb)
	}
	if a.cls == 1 || b.cls == 1 {
		an, aok := a.u, a.cls == 1
		bn, bok := b.u, b.cls == 1
		if a.cls == 0 {
			aok = a.i >= 0
			an = uint64(a.i)
		}
		if b.cls == 0 {
			bok = b.i >= 0
			bn = uint64(b.i)
		}
		if aok != bok {
			if !aok {
				return -1
			}
			return 1
		}
		if aok {
			return cmp.Compare(an, bn)
		}
		return cmp.Compare(a.i, b.i)
	}
	return cmp.Compare(a.i, b.i)
}

// ScriptSortable is the sort.Interface view of a script object.

type ScriptSortable struct {
	vc                    runtime.ScriptCaller
	lenFn, lessFn, swapFn runtime.Value
}

// CmpScript is cmp.Compare over script values, with the ordering
// slices.Sort uses ([LessScript]: NaN before every other float, a host
// named basic ordered among its source twins). Two int64 payloads
// compare exactly — the float64 route would merge neighbours past 2^53.

func CmpScript(a, b runtime.Value) int64 {
	if an, aok := scalarNum(a); aok {
		if bn, bok := scalarNum(b); bok {
			return int64(cmpNum(an, bn))
		}
	}
	a, b = builtin.OrderScalar(a), builtin.OrderScalar(b)
	switch {
	case LessScript(a, b):
		return -1
	case LessScript(b, a):
		return 1
	}
	return 0
}

func (s *ScriptSortable) Len() int {
	r, err := s.vc.Call(s.lenFn, nil)
	if err != nil {
		panic(err)
	}
	return int(Int64Of(r))
}

func (s *ScriptSortable) Less(i, j int) bool {
	r, err := s.vc.Call(s.lessFn, []runtime.Value{int64(i), int64(j)})
	if err != nil {
		panic(err)
	}
	b, _ := r.(bool)
	return b
}

func (s *ScriptSortable) Swap(i, j int) {
	if _, err := s.vc.Call(s.swapFn, []runtime.Value{int64(i), int64(j)}); err != nil {
		panic(err)
	}
}

// slicesCompare is slices.Compare (elements ordered with cmp.Compare)
// and slices.CompareFunc (elements ordered by the script callback): the
// first non-zero element comparison decides, then the shorter slice is
// less. Either slice may be nil.

func SlicesCompactFunc() *runtime.BuiltinFunc {
	const name = "slices.CompactFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		if s == nil || len(s.Elems) < 2 {
			return args[0], nil
		}
		kept := make([]runtime.Value, 1, len(s.Elems))
		kept[0] = s.Elems[0]
		for i := 1; i < len(s.Elems); i++ {
			if !hostbridge.CallBool(vc, args[1], s.Elems[i], s.Elems[i-1]) {
				kept = append(kept, s.Elems[i])
			}
		}
		return RetagSlice(args[0], PackSlice(vc, s, kept)), nil
	}}
}

// Boxed host named slices (a net.IP) through the builtins and the slices
// and sort intrinsics. They keep their box for method dispatch; the
// operations below either act on the host backing (append, copy, make,
// sort.Slice) or read the slice as a script snapshot, run the script
// implementation and write the result back (see HostSliceBoundary).

// HostSortSlice runs sort.Slice / sort.SliceStable on a boxed host slice:
// the host slice itself is reordered while the script less function reads
// it by index, as the slice the script passed. ok is false when x is no
// host slice.

func HostSortSlice(v runtime.ScriptCaller, x, less runtime.Value, stable bool) bool {
	_, rv, ok := vm.HostSliceOf(x)
	if !ok {
		return false
	}
	cmp := func(i, j int) bool {
		r := hostbridge.CallOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
		b, _ := r.(bool)
		return b
	}
	if stable {
		sort.SliceStable(rv.Interface(), cmp)
	} else {
		sort.Slice(rv.Interface(), cmp)
	}
	return true
}

// HostSliceUse describes where the slices a function reads sit among its
// arguments (pos; nil means every argument) and whether it reorders them
// in place.

type HostSliceUse struct {
	pos     []int
	inPlace bool
}

var SlicesHostUse = map[string]HostSliceUse{
	"Concat":         {},
	"Equal":          FirstTwoSlicesRead,
	"EqualFunc":      FirstTwoSlicesRead,
	"Compare":        FirstTwoSlicesRead,
	"CompareFunc":    FirstTwoSlicesRead,
	"Sort":           FirstSliceReorder,
	"SortFunc":       FirstSliceReorder,
	"SortStableFunc": FirstSliceReorder,
	"Reverse":        FirstSliceReorder,
	"Compact":        FirstSliceReorder,
	"CompactFunc":    FirstSliceReorder,
	"Delete":         FirstSliceReorder,
	"DeleteFunc":     FirstSliceReorder,
}

// SortHostUse names the sort functions that take a plain slice argument
// (sort.Sort and friends take a sort.Interface, which a box satisfies
// as it is).

var SortHostUse = map[string]HostSliceUse{
	"Ints":     FirstSliceReorder,
	"Float64s": FirstSliceReorder,
	"Strings":  FirstSliceReorder,
}

// GenericFns marks a bound package's intrinsics as generic: every
// function in `slices` and `maps` is generic in gc, and the intrinsics
// are already parametric over the runtime value shapes, so an explicit
// instantiation (`slices.Contains[[]int]`) binds the same callee —
// without GenFn the index traps `index on *runtime.BuiltinFunc`. The
// type arguments still matter where they are the only type
// information: a nil argument reads as the arg's nilable type
// (`slices.Clone[[]int](nil)` is `[]int(nil)`, not an error) and a nil
// or named-container result keeps the argument's declared type
// (`slices.Concat[[]int]()` returns `[]int(nil)`, `slices.Clone[S]`
// returns S).

func GenericFns(pkg map[string]runtime.Value) map[string]runtime.Value {
	for name, val := range pkg {
		bf, ok := val.(*runtime.BuiltinFunc)
		if !ok || bf.Fn == nil || bf.GenFn != nil {
			continue
		}
		fname := bf.Name
		wrapped := *bf
		fn := bf.Fn
		wrapped.GenFn = func(vc runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			copied := false
			for i, a := range args {
				if a != runtime.NIL {
					continue
				}
				ti := InstNilTarg(fname, i, len(targs))
				if ti < 0 || ti >= len(targs) {
					continue
				}
				if td, ok := targs[ti].(*runtime.TypeDef); ok && InstNilable(td) && !InstIface(td) {
					if !copied {
						args = slices.Clone(args)
						copied = true
					}
					args[i] = &runtime.TypedNil{Typ: td}
				}
			}
			res, err := fn(vc, args)
			if err != nil {
				return nil, err
			}
			return InstResult(fname, targs, res), nil
		}
		pkg[name] = &wrapped
	}
	return pkg
}

// InstNilTarg reports which type argument the declared type of arg
// position `i` of `fname` is bound to: position i binds targ i except
// for the params that are not a type argument (a comparator, a count,
// a seq operand) or bind a different one — BinarySearchFunc[S,E,T]'s
// target is T, the third type argument, not the positional second. -1
// leaves the arg's nil bare; the variadic tail binds the last targ.

func InstNilTarg(fname string, i int, ntargs int) int {
	if ntargs == 0 {
		return -1
	}
	if ti, ok := InstNilTargs[fname][i]; ok {
		return ti
	}
	if i >= ntargs {
		// a nil past the fixed params belongs to the variadic tail,
		// which binds the last type argument (`Concat`'s `...S`,
		// `Replace`'s `...E`); extra args on a fixed arity are a
		// caller error either way.
		return ntargs - 1
	}
	return i
}

// InstNilTargs: bound func name → arg position → type-argument index.
// Only the params that are not themselves a type argument, or bind a
// different one, appear; -1 leaves the nil bare.

var InstNilTargs = map[string]map[int]int{
	"slices.BinarySearchFunc": {1: 2, 2: -1},
	"slices.SortFunc":         {1: -1},
	"slices.SortStableFunc":   {1: -1},
	"slices.DeleteFunc":       {1: -1},
	"slices.EqualFunc":        {2: -1},
	"slices.IndexFunc":        {1: -1},
	"slices.ContainsFunc":     {1: -1},
	"slices.IsSortedFunc":     {1: -1},
	"slices.MaxFunc":          {1: -1},
	"slices.MinFunc":          {1: -1},
	"slices.CompactFunc":      {1: -1},
	"slices.Replace":          {1: -1, 2: -1},
	"slices.Insert":           {1: -1},
	"slices.Delete":           {1: -1, 2: -1},
	"slices.Grow":             {1: -1},
	"slices.Repeat":           {1: -1},
	"slices.Chunk":            {1: -1},
	"slices.Collect":          {0: -1},
	"slices.Sorted":           {0: -1},
	"slices.SortedFunc":       {0: -1, 1: -1},
	"slices.SortedStableFunc": {0: -1, 1: -1},
	"slices.AppendSeq":        {1: -1},
	"maps.EqualFunc":          {2: -1},
	"maps.DeleteFunc":         {1: -1},
	"maps.Insert":             {1: -1},
	"maps.Collect":            {0: -1},
}

// InstResSelf names the bound funcs whose result type is their first
// type argument (S/M): only those re-tag a nil or container result —
// `Clone[S]` → S, `Concat[[]int]()` → `[]int(nil)`. `Collect` and the
// `Sorted` family return a slice OF a type argument (`[]E`, `[]K`), the
// seq helpers return a seq, the rest a scalar — absent on purpose.

var InstResSelf = map[string]bool{
	"slices.Clone": true, "slices.Concat": true, "slices.Compact": true,
	"slices.CompactFunc": true, "slices.Grow": true, "slices.Clip": true,
	"slices.Repeat": true, "slices.Insert": true, "slices.Delete": true,
	"slices.DeleteFunc": true, "slices.Replace": true, "slices.AppendSeq": true,
	"maps.Clone": true,
}

// InstIface reports whether td's underlying is an interface — a nil
// arg binds as the untyped nil the interface elements already hold,
// not a TypedNil (a nil element of []any is an IfaceNil and slotEql
// would miss it).

func InstIface(td *runtime.TypeDef) bool {
	if td.Kind == runtime.KindInterface {
		return true
	}
	u := runtime.TypUnderlyingSpelling(td)
	return u == "interface{}" || u == "any" || u == "error"
}

// InstNilable reports whether td's underlying is a nilable shape — a
// nil argument of an instantiated call binds as td's zero.

func InstNilable(td *runtime.TypeDef) bool {
	if td.Kind == runtime.KindPointer || td.Kind == runtime.KindMap ||
		td.Kind == runtime.KindChan || td.Kind == runtime.KindFunc ||
		td.Kind == runtime.KindInterface {
		return true
	}
	u := runtime.TypUnderlyingSpelling(td)
	return strings.HasPrefix(u, "[]") || strings.HasPrefix(u, "map[") ||
		strings.HasPrefix(u, "chan ") || strings.HasPrefix(u, "<-chan ") ||
		strings.HasPrefix(u, "*") || u == "interface{}" || u == "any" || u == "error"
}

// InstResult types an instantiated call's result by its first type
// argument — only for the InstResSelf funcs whose result IS that
// argument: an untyped nil becomes that arg's nil when it is nilable
// (`Concat[[]int]()` is `[]int(nil)`), and a container value re-tags to
// a named argument of the same underlying (`Clone[S]` returns S, not
// the []int the argument carried). Other funcs return `[]E` of an arg
// (Collect, Sorted), a seq, or a scalar — re-tagging any of those to
// targs[0] would lie about the result's type.

func InstResult(fname string, targs []runtime.Value, res runtime.Value) runtime.Value {
	if len(targs) == 0 || !InstResSelf[fname] {
		return res
	}
	td, ok := targs[0].(*runtime.TypeDef)
	if !ok || td == nil {
		return res
	}
	if res == runtime.NIL {
		if InstNilable(td) {
			return &runtime.TypedNil{Typ: td}
		}
		return res
	}
	if tn, ok := res.(*runtime.TypedNil); ok {
		if td.Name != "" && (tn.Typ == nil || runtime.TypUnderlyingSpelling(tn.Typ) == runtime.TypUnderlyingSpelling(td)) {
			return &runtime.TypedNil{Typ: td}
		}
		return res
	}
	if td.Name == "" {
		return res
	}
	u := runtime.TypUnderlyingSpelling(td)
	var rt *runtime.TypeDef
	switch res.(type) {
	case *runtime.Slice:
		if rt = runtime.TagOf(res); rt == nil && !strings.HasPrefix(u, "[]") {
			return res
		}
	case *runtime.Map:
		if rt = runtime.TagOf(res); rt == nil && !strings.HasPrefix(u, "map[") {
			return res
		}
	case *runtime.Named:
		rt = runtime.TagOf(res)
	default:
		return res
	}
	if rt == nil || runtime.TypUnderlyingSpelling(rt) == u {
		return runtime.Tag(td, res)
	}
	return res
}

// HostSliceBoundary wraps the intrinsics of pkg so a boxed host slice
// among their slice arguments reads as a script slice: the intrinsic runs
// on a snapshot, an in-place one writes the reordered elements back to the
// host backing, and a slice result of the snapshot's type is boxed as the
// host type again (slices.Clone of a net.IP is a net.IP). With def set it
// also covers every function of pkg that uses leaves out; otherwise only
// the functions uses names.

func HostSliceBoundary(pkg map[string]runtime.Value, uses map[string]HostSliceUse, def *HostSliceUse) map[string]runtime.Value {
	for name, val := range pkg {
		bf, ok := val.(*runtime.BuiltinFunc)
		if !ok || bf.Fn == nil || bf.GenFn != nil {
			continue
		}
		use, named := uses[name]
		if !named {
			if def == nil {
				continue
			}
			use = *def
		}
		wrapped := *bf
		wrapped.Fn = HostSliceCall(bf.Fn, use)
		pkg[name] = &wrapped
	}
	return pkg
}

func HostSliceCall(fn func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), use HostSliceUse) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
	return func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		var snaps []runtime.Value // parallel to args; nil where no host slice
		orig := args
		for i := range args {
			if use.pos != nil && !slices.Contains(use.pos, i) {
				continue
			}
			s, ok := vm.UnboxHostSlice(orig[i])
			if !ok {
				continue
			}
			if snaps == nil {
				snaps = make([]runtime.Value, len(orig))
				args = slices.Clone(orig) // the caller's buffer is not ours to rewrite
			}
			snaps[i], args[i] = s, s
		}
		if snaps == nil {
			return fn(vc, args)
		}
		res, err := fn(vc, args)
		if err != nil {
			return nil, err
		}
		for i, s := range snaps {
			if s == nil {
				continue
			}
			if use.inPlace {
				if err := vm.StoreHostSlice(vc, orig[i], s); err != nil {
					return nil, err
				}
			}
		}
		for i, s := range snaps {
			if s != nil {
				if r := vm.ReboxHostSlice(vc, orig[i], s, res); r != res {
					return r, nil
				}
			}
		}
		return res, nil
	}
}

// rewriteWrapVerbs rewrites %w verbs to %v (flags, width and explicit
// [n] indexes kept) and reports the operand index of every %w, in verb
// order, the way gc's doPrintf records p.wrappedErrs: explicit indexes
// move the cursor, `*` width/precision consume an operand, and the list
// may repeat an index (`%[1]w %[1]w`). Indexes past the operands are
// reported too; the caller drops them (gc prints %!w(MISSING) and
// records nothing).

func IsArrayTyp(td *runtime.TypeDef) bool {
	at, ok := td.TypeExpr().(*ast.ArrayType)
	return ok && at.Len != nil
}

var (
	FirstSliceRead     = HostSliceUse{pos: []int{0}}
	FirstSliceReorder  = HostSliceUse{pos: []int{0}, inPlace: true}
	FirstTwoSlicesRead = HostSliceUse{pos: []int{0, 1}}
)

// SliceIndexScript is slices.Index(s, x) over a script slice with Go's
// element equality ([runtime.VMCaller.Equal]): an untyped x converts to
// the element type first (`1<<n` truncates to the element width), a
// []any compares (dynamic type, value) pairs, and a boxed host named
// basic meets its source twin in a []fs.FileMode.
func SliceIndexScript(vc runtime.IndexTyper, name string, args []runtime.Value) (int, error) {
	if len(args) != 2 {
		return 0, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
	}
	el, et, ok := ScriptSliceArg(vc, args[0])
	if !ok {
		return slices.Index(AnySlice(hostbridge.GoNative(args[0])), hostbridge.GoNative(args[1])), nil
	}
	x := args[1]
	switch x.(type) {
	case *runtime.UConst, int64, float64:
		// an untyped operand (a call materializes `1<<n` to int) takes
		// the element type, truncating like Go's context typing.
		if et == nil || et.Kind == runtime.KindInterface {
			break
		}
		cx, err := vc.Convert(et, x)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		x = cx
	}
	for i, e := range el {
		if vc.Equal(et, e, x) {
			return i, nil
		}
	}
	return -1, nil
}
func AnySlice(v any) []any {
	if n, ok := v.(*runtime.Named); ok {
		return AnySlice(n.V)
	}
	if s, ok := v.(*runtime.Slice); ok {
		out := make([]any, len(s.Elems))
		for i, e := range s.Elems {
			out[i] = hostbridge.GoNative(e)
		}
		return out
	}
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// CloneElems copies a slice's elements into a fresh backing array; an
// array or struct element is copied, not shared with the original.
func CloneElems(el []runtime.Value) []runtime.Value {
	out := append([]runtime.Value{}, el...)
	for i, v := range out {
		out[i] = runtime.Copy(v)
	}
	return out
}

// SliceElems is a [sliceParam] result's elements — none for nil.
func SliceElems(s *runtime.Slice) []runtime.Value {
	if s == nil {
		return nil
	}
	return s.Elems
}

// CallInt invokes a script callable and unwraps the int result.
func CallInt(vc runtime.ScriptCaller, fn runtime.Value, args ...runtime.Value) int64 {
	return Int64Of(hostbridge.CallOrPanic(vc, fn, args))
}

// MapsEqualFunc is maps.EqualFunc: equal sizes, every key of m1 present
// in m2, and eq(v1, v2) true for it. Keys are looked up by their script
// value (the two maps may key differently), so a NaN key is never found.
func MapsEqualFunc() *runtime.BuiltinFunc {
	const name = "maps.EqualFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s needs 3 args, got %d", name, len(args))
		}
		m1, _, _, ok1 := ScriptMapSeqArg(vc, args[0])
		m2, _, _, ok2 := ScriptMapSeqArg(vc, args[1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s: first two args must be maps", name)
		}
		// a nil map holds no pairs
		var n1, n2 int
		if m1 != nil {
			n1 = m1.Len()
		}
		if m2 != nil {
			n2 = m2.Len()
		}
		if n1 != n2 {
			return false, nil
		}
		if n1 == 0 {
			return true, nil
		}
		w := m1.BeginWalk()
		for i := range w.Len() {
			k, v1, ok := w.At(i)
			if !ok {
				continue
			}
			v2, ok := m2.Get(k)
			if !ok || !hostbridge.CallBool(vc, args[2], v1, v2) {
				return false, nil
			}
		}
		return true, nil
	}}
}

// MapsDeleteFunc is maps.DeleteFunc: every entry for which del(k, v) is
// true is removed, the walk skipping entries del itself removed earlier.
func MapsDeleteFunc() *runtime.BuiltinFunc {
	const name = "maps.DeleteFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		m, kt, _, ok := ScriptMapSeqArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("%s: first arg must be a map", name)
		}
		if m == nil {
			return runtime.NIL, nil
		}
		conv := MapKeyConv(vc, m, kt)
		w := m.BeginWalk()
		for i := range w.Len() {
			k, v, ok := w.At(i)
			if !ok {
				continue
			}
			ak := k
			if conv != nil {
				ak = conv(k)
			}
			if hostbridge.CallBool(vc, args[1], ak, v) {
				m.Delete(k)
			}
		}
		return runtime.NIL, nil
	}}
}

// MapKeyType resolves the declared key type of a map typedef — the
// typedef and its type expression; nil when td spells no map type.
func MapKeyType(vc runtime.ExprResolver, td *runtime.TypeDef) (*runtime.TypeDef, ast.Expr) {
	mt := MapTypeExpr(td)
	if mt == nil {
		return nil, nil
	}
	kt, err := vc.ResolveType(td, mt.Key)
	if err != nil {
		return nil, nil
	}
	return kt, mt.Key
}

// MapTypeExpr is the map type expression a map typedef spells — its
// own anonymous `map[K]V` or a named type's declaration; nil otherwise.
func MapTypeExpr(td *runtime.TypeDef) *ast.MapType {
	if td == nil {
		return nil
	}
	mt, _ := td.TypeExpr().(*ast.MapType)
	return mt
}

func MapKeys(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return MapKeys(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		display, _ := m.SnapshotKeys()
		return &runtime.Slice{Elems: display}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Keys(m))}
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map {
		// a boxed host named map (http.Header, url.Values)
		el := make([]runtime.Value, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			el = append(el, ScriptVal(it.Key().Interface()))
		}
		return &runtime.Slice{Elems: el}
	}
	return &runtime.Slice{}
}

func MapValues(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return MapValues(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		el := make([]runtime.Value, m.Len())
		for i := range el {
			_, el[i] = m.At(i)
		}
		return &runtime.Slice{Elems: el}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Values(m))}
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map {
		// a boxed host named map (http.Header, url.Values)
		el := make([]runtime.Value, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			el = append(el, ScriptVal(it.Value().Interface()))
		}
		return &runtime.Slice{Elems: el}
	}
	return &runtime.Slice{}
}

// IterSeqElems reads the element typedefs off an iter.Seq / iter.Seq2
// value (an intrinsic's or a script's): [V] or [K, V]; nil for anything
// that is not typed as one.
func IterSeqElems(v runtime.Value) []*runtime.TypeDef {
	n, ok := v.(*runtime.Named)
	if !ok {
		return nil
	}
	return runtime.IterSeqElemTypes(n.Typ)
}

// SliceTdOf is the []E typedef over a resolved element typedef.
func SliceTdOf(et *runtime.TypeDef) *runtime.TypeDef {
	if et == nil {
		return nil
	}
	const tp = "E"
	return &runtime.TypeDef{
		Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent(tp)},
		Binds: map[string]runtime.Value{tp: et}, Pkg: et.Pkg, File: et.File, Elem: et,
	}
}

// SeqSliceTd is the []E typedef a collecting helper (slices.Sorted,
// slices.Collect) returns for its seq argument: read off the seq's
// iter.Seq[E] type, or kept from a slice argument; nil when unknown.
func SeqSliceTd(v runtime.Value) *runtime.TypeDef {
	if els := IterSeqElems(v); len(els) == 1 {
		return SliceTdOf(els[0])
	}
	return sliceTypOf(v)
}

// MapSource is the native source of maps.Keys / Values / All over a
// script map: lazy like Go's — it walks a snapshot of the key order and
// reads each pair live, so an entry deleted before its turn is not
// produced and a value written meanwhile reads its new content. conv
// converts a stored key back to the declared key type (nil: identity).
// A nil map produces nothing.
func MapSource(m *runtime.Map, kind MapSeqKind, conv func(runtime.Value) runtime.Value) *runtime.SeqSource {
	key := func(k runtime.Value) runtime.Value {
		if conv != nil {
			return conv(k)
		}
		return k
	}
	src := &runtime.SeqSource{
		Each: func(_ runtime.ScriptCaller, yield func([]runtime.Value) bool) error {
			if m == nil || m.Len() == 0 {
				return nil
			}
			w := m.BeginWalk()
			n := 1
			if kind == MapSeqAll {
				n = 2
			}
			buf := make([]runtime.Value, n)
			for i := range w.Len() {
				if kind == MapSeqKeys {
					k, ok := w.Key(i)
					if !ok {
						continue
					}
					buf[0] = key(k)
				} else {
					k, v, ok := w.At(i)
					if !ok {
						continue
					}
					if kind == MapSeqValues {
						buf[0] = v
					} else {
						buf[0], buf[1] = key(k), v
					}
				}
				if !yield(buf) {
					return nil
				}
			}
			return nil
		},
	}
	switch kind {
	case MapSeqKeys:
		src.Collect = func(runtime.ScriptCaller) []runtime.Value {
			if m == nil {
				return nil
			}
			out, _ := m.SnapshotKeys()
			if conv != nil {
				for i, k := range out {
					out[i] = conv(k)
				}
			}
			return out
		}
	case MapSeqValues:
		src.Collect = func(runtime.ScriptCaller) []runtime.Value {
			if m == nil {
				return nil
			}
			_, cks := m.SnapshotKeys()
			out := make([]runtime.Value, len(cks))
			for i, ck := range cks {
				out[i] = m.Pairs[ck]
			}
			return out
		}
	}
	return src
}

// MapKeyOperand is the stored form of key k in a map typed mt.
func MapKeyOperand(vc runtime.MapKeyer, mt *runtime.TypeDef, k runtime.Value) runtime.Value {
	if mt == nil {
		return k
	}
	return vc.MapKeyOperand(mt, k)
}

// sliceTypOf keeps a bound slice op's declared element type — a new
// []string built without the tag loses `[]string` reads downstream.
func sliceTypOf(v any) *runtime.TypeDef {
	if s, ok := runtime.Unwrap(v).(*runtime.Slice); ok {
		return s.Typ
	}
	return nil
}

// ElemAt indexes a prior container's stored values without failing — a
// prior field/element exists only when the old value matches the new
// shape's position.
func ElemAt(vs []runtime.Value, i int) runtime.Value {
	if i < len(vs) {
		return vs[i]
	}
	return nil
}

// SliceExtreme is slices.Max/Min: the element better wins over best.
// It returns the slice's own element, so its type survives —
// slices.Max([]fs.FileMode{...}) prints a FileMode, not its payload.
func SliceExtreme(vc runtime.ElemTyper, name string, args []runtime.Value, better func(best, x runtime.Value) bool) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
	}
	el, _, ok := ScriptSliceArg(vc, args[0])
	if !ok {
		el = ScriptElems(hostbridge.GoNative(args[0]))
	}
	if len(el) == 0 {
		panic(EmptyListPanic(name))
	}
	best := el[0]
	for _, x := range el[1:] {
		if better(best, x) {
			best = x
		}
	}
	return best, nil
}

// EmptyListPanic is the string panic of slices.Max/Min/MaxFunc/MinFunc
// on an empty slice — a plain string, so `recover()` yields exactly
// Go's `slices.MaxFunc: empty list`.
func EmptyListPanic(name string) *runtime.Panic {
	return &runtime.Panic{Value: name + ": empty list"}
}

// SliceParam resolves the slice parameter of the slices helpers: the
// slice itself, or nil for a nil slice (which holds no elements) — a
// bare `var s []int`, a typed or named nil, a nil read out of a struct
// field or an `any`. Every helper reading a slice argument goes through
// it, so a nil slice behaves as in Go instead of failing a type
// assertion. A slice of zero-size elements stores none of them, so the
// helpers that read elements do not take it; see [SliceParamAny].
func SliceParam(name string, v runtime.Value) (*runtime.Slice, error) {
	s, err := SliceParamAny(name, v)
	if err == nil && s != nil && s.Virtual() {
		return nil, fmt.Errorf("%s: slices of zero-size elements are not supported", name)
	}
	return s, err
}

// SliceParamAny is [SliceParam] that also accepts a slice of zero-size
// elements, for the helpers that never read its elements.
func SliceParamAny(name string, v runtime.Value) (*runtime.Slice, error) {
	if s, ok := SliceOf(v); ok {
		return s, nil
	}
	if Nilish(v) {
		return nil, nil
	}
	return nil, fmt.Errorf("%s: arg must be a slice", name)
}

// SliceParamTyp is the declared slice type of a [SliceParam] result s
// that was read from v — the slice's own, or the nil slice's — nil when
// unknown.
func SliceParamTyp(s *runtime.Slice, v runtime.Value) *runtime.TypeDef {
	if s != nil {
		return s.Typ
	}
	if tn, ok := runtime.Unwrap(v).(*runtime.TypedNil); ok {
		return tn.Typ
	}
	return nil
}

// NilSliceLike is the nil slice of type typ, tagged with orig's named
// slice type when orig carries one (see [RetagSlice]).
func NilSliceLike(orig runtime.Value, typ *runtime.TypeDef) runtime.Value {
	nl := &runtime.TypedNil{Typ: typ}
	if n, ok := orig.(*runtime.Named); ok {
		return runtime.Tag(n.Typ, nl)
	}
	return nl
}

// ElemOperands prepares the variadic element operands of slices.Insert
// and Replace for storing into a slice of type typ: an untyped constant
// takes the element type (like SliceIndexScript's operand), and each
// operand is copied so an array or struct is not shared with its source.
func ElemOperands(vc runtime.ElemOperandTyper, name string, typ *runtime.TypeDef, vals []runtime.Value) ([]runtime.Value, error) {
	var et *runtime.TypeDef
	if typ != nil {
		et, _ = vc.ElemOf(typ)
	}
	out := make([]runtime.Value, len(vals))
	for k, v := range vals {
		switch v.(type) {
		case *runtime.UConst, int64, float64:
			if et != nil && et.Kind != runtime.KindInterface {
				cv, err := vc.Convert(et, v)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", name, err)
				}
				v = cv
			}
		}
		out[k] = runtime.Copy(v)
	}
	return out, nil
}

// FillHoles gives the slots of full that the host left empty — the
// tail slices.Delete cleared, the spare capacity of a freshly grown
// array — the zero value of typ's element, which is what a script slot
// holds there (a hole would print as <nil>).
func FillHoles(vc runtime.VMCaller, typ *runtime.TypeDef, full []runtime.Value) {
	var zero runtime.Value = runtime.NIL
	if typ != nil {
		zero = vc.ElemZero(typ)
	}
	for k, v := range full {
		if v == nil {
			full[k] = runtime.Copy(zero)
		}
	}
}
