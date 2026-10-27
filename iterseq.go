package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"slices"
	"sort"

	"github.com/podhmo/minigo/runtime"
)

// iterSeqKey keys the instantiated iter.Seq / iter.Seq2 typedef cache
// by the element typedefs (b is nil for Seq).
type iterSeqKey struct{ a, b *runtime.TypeDef }

// iterSeqBaseKey keys the generic iter.Seq / iter.Seq2 typedefs in the
// same cache.
type iterSeqBaseKey string

// sharedTd reports whether td is a canonical, shared typedef — a
// predeclared or declared non-generic type, materialized once — so that
// keying a cache on its pointer stays bounded. An anonymous composite
// ([]int, struct{...}) or a generic instantiation (G[int]) is built
// afresh per resolution.
func sharedTd(td *runtime.TypeDef) bool {
	return td != nil && td.Name != "" && len(td.Binds) == 0 && td.TParams == nil
}

// iterSeqTd instantiates iter.Seq[V] (one elem) or iter.Seq2[K, V] (two)
// over resolved element typedefs, so an intrinsic seq spells and infers
// like one a script returns: %T prints iter.Seq[string], a script
// `func f[T any](iter.Seq[T])` binds T from it, and slices.Sorted reads
// the element type back off it. nil when an element is unknown or iter
// does not load (a deny policy, no GOROOT) — the seq then stays an
// untyped callable.
func (e *Engine) iterSeqTd(elems ...*runtime.TypeDef) *runtime.TypeDef {
	key := iterSeqKey{}
	switch len(elems) {
	case 1:
		key.a = elems[0]
	case 2:
		key.a, key.b = elems[0], elems[1]
		if key.b == nil {
			return nil
		}
	default:
		return nil
	}
	if key.a == nil {
		return nil
	}
	cacheable := sharedTd(key.a) && (key.b == nil || sharedTd(key.b))
	if cacheable {
		if td, ok := e.iterSeqTds.Load(key); ok {
			return td.(*runtime.TypeDef)
		}
	}
	base := e.iterSeqBase(len(elems))
	if base == nil {
		return nil
	}
	binds := make(map[string]runtime.Value, len(elems))
	for i, tp := range base.TParams {
		binds[tp] = elems[i]
	}
	td := e.instantiateBinds(base, binds)
	if cacheable {
		if prev, loaded := e.iterSeqTds.LoadOrStore(key, td); loaded {
			return prev.(*runtime.TypeDef)
		}
	}
	return td
}

// iterSeqBase is the generic iter.Seq (n=1) or iter.Seq2 (n=2) typedef,
// loaded from the iter package once per engine; nil when it does not
// load.
func (e *Engine) iterSeqBase(n int) *runtime.TypeDef {
	name := "Seq"
	if n == 2 {
		name = "Seq2"
	}
	if td, ok := e.iterSeqTds.Load(iterSeqBaseKey(name)); ok {
		return td.(*runtime.TypeDef)
	}
	p, err := e.loadPath(context.Background(), "iter")
	if err != nil {
		return nil
	}
	m, err := p.Member(name, e.materialize)
	if err != nil {
		return nil
	}
	base, ok := m.(*runtime.TypeDef)
	if !ok || len(base.TParams) != n {
		return nil
	}
	e.iterSeqTds.Store(iterSeqBaseKey(name), base)
	return base
}

// iterSeqElems reads the element typedefs off an iter.Seq / iter.Seq2
// value (an intrinsic's or a script's): [V] or [K, V]; nil for anything
// that is not typed as one.
func iterSeqElems(v runtime.Value) []*runtime.TypeDef {
	n, ok := v.(*runtime.Named)
	if !ok || n.Typ == nil || n.Typ.Pkg == nil || n.Typ.Pkg.Path != "iter" {
		return nil
	}
	var names []string
	switch n.Typ.Name {
	case "Seq", "iter.Seq":
		names = []string{"V"}
	case "Seq2", "iter.Seq2":
		names = []string{"K", "V"}
	default:
		return nil
	}
	if len(n.Typ.TParams) == len(names) {
		names = n.Typ.TParams
	}
	out := make([]*runtime.TypeDef, len(names))
	for i, tp := range names {
		td, _ := n.Typ.Binds[tp].(*runtime.TypeDef)
		if td == nil {
			return nil
		}
		out[i] = td
	}
	return out
}

// sliceTdOf is the []E typedef over a resolved element typedef.
func sliceTdOf(et *runtime.TypeDef) *runtime.TypeDef {
	if et == nil {
		return nil
	}
	const tp = "E"
	return &runtime.TypeDef{
		Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent(tp)},
		Binds: map[string]runtime.Value{tp: et}, Pkg: et.Pkg, File: et.File, Elem: et,
	}
}

// seqSliceTd is the []E typedef a collecting helper (slices.Sorted,
// slices.Collect) returns for its seq argument: read off the seq's
// iter.Seq[E] type, or kept from a slice argument; nil when unknown.
func seqSliceTd(v runtime.Value) *runtime.TypeDef {
	if els := iterSeqElems(v); len(els) == 1 {
		return sliceTdOf(els[0])
	}
	return sliceTypOf(v)
}

// typedSeq tags an intrinsic seq callable with its iter.Seq / Seq2 type
// when the element typedefs are known.
func (e *Engine) typedSeq(fn *runtime.BuiltinFunc, elems ...*runtime.TypeDef) runtime.Value {
	if td := e.iterSeqTd(elems...); td != nil {
		return runtime.Tag(td, fn)
	}
	return fn
}

// yieldOK reports whether a yield call's result lets the producer go on.
func yieldOK(r runtime.Value) bool {
	goon, ok := r.(bool)
	return !ok || goon
}

// seqFn builds an intrinsic seq builtin over a native producer: the
// VM-facing Fn drives each with the script's yield func, and Go-side
// consumers read src directly (see nativeSeq).
func seqFn(name string, src *runtime.SeqSource) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: "iter.Seq(" + name + ")", SyncCallbacks: true, Seq: src, Fn: func(vc runtime.VMCaller, ya []runtime.Value) (runtime.Value, error) {
		if len(ya) != 1 {
			return nil, fmt.Errorf("%s: Seq needs a yield func", name)
		}
		return runtime.NIL, src.Each(vc, scriptYield(vc, ya[0]))
	}}
}

// scriptYield adapts a script yield func to a native producer's yield.
// The range-over-func loop body (DirectYield) runs straight from Go; any
// other callable goes through the VM with a fresh argument slice, and a
// panic in it propagates as a Go panic so a recover around the range
// sees it, like in Go.
func scriptYield(vc runtime.VMCaller, yield runtime.Value) func(args []runtime.Value) bool {
	if c, ok := yield.(*runtime.BuiltinFunc); ok && c.DirectYield {
		return func(args []runtime.Value) bool {
			r, err := c.Fn(vc, args)
			if err != nil {
				panic(err)
			}
			return yieldOK(r)
		}
	}
	return func(args []runtime.Value) bool {
		return yieldOK(callOrPanic(vc, yield, slices.Clone(args)))
	}
}

// nativeSeq is the native source of an intrinsic seq value (possibly
// tagged iter.Seq[...]); nil for any other value.
func nativeSeq(v runtime.Value) *runtime.SeqSource {
	if c, ok := runtime.Unwrap(v).(*runtime.BuiltinFunc); ok {
		return c.Seq
	}
	return nil
}

// driveSeq drives seq with a Go-side consumer: an intrinsic seq runs its
// producer natively; anything else is called through the VM with a
// yield builtin wrapping take.
func driveSeq(vc runtime.VMCaller, seq runtime.Value, take func(args []runtime.Value)) error {
	if src := nativeSeq(seq); src != nil {
		return src.Each(vc, func(args []runtime.Value) bool {
			take(args)
			return true
		})
	}
	yield := &runtime.BuiltinFunc{Name: "yield", SyncCallbacks: true, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		take(args)
		return true, nil
	}}
	callOrPanic(vc, seq, []runtime.Value{yield})
	return nil
}

// sliceSource is the native source over pieces materialized up front
// (strings.SplitSeq and friends, maps over host values).
func sliceSource(pieces []runtime.Value) *runtime.SeqSource {
	return &runtime.SeqSource{
		Each: func(_ runtime.VMCaller, yield func([]runtime.Value) bool) error {
			buf := make([]runtime.Value, 1)
			for _, p := range pieces {
				buf[0] = p
				if !yield(buf) {
					return nil
				}
			}
			return nil
		},
		Collect: func(runtime.VMCaller) []runtime.Value { return slices.Clone(pieces) },
	}
}

// mapSeqKind selects what a map seq yields.
type mapSeqKind int

const (
	mapSeqKeys mapSeqKind = iota
	mapSeqValues
	mapSeqAll
)

// mapSource is the native source of maps.Keys / Values / All over a
// script map: lazy like Go's — it walks a snapshot of the key order and
// reads each pair live, so an entry deleted before its turn is not
// produced and a value written meanwhile reads its new content. conv
// converts a stored key back to the declared key type (nil: identity).
// A nil map produces nothing.
func mapSource(m *runtime.Map, kind mapSeqKind, conv func(runtime.Value) runtime.Value) *runtime.SeqSource {
	key := func(k runtime.Value) runtime.Value {
		if conv != nil {
			return conv(k)
		}
		return k
	}
	src := &runtime.SeqSource{
		Each: func(_ runtime.VMCaller, yield func([]runtime.Value) bool) error {
			if m == nil || m.Len() == 0 {
				return nil
			}
			w := m.BeginWalk()
			defer w.End()
			n := 1
			if kind == mapSeqAll {
				n = 2
			}
			buf := make([]runtime.Value, n)
			for i := range w.Len() {
				if kind == mapSeqKeys {
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
					if kind == mapSeqValues {
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
	case mapSeqKeys:
		src.Collect = func(runtime.VMCaller) []runtime.Value {
			if m == nil {
				return nil
			}
			out := slices.Clone(m.Order)
			if conv != nil {
				for i, k := range out {
					out[i] = conv(k)
				}
			}
			return out
		}
	case mapSeqValues:
		src.Collect = func(runtime.VMCaller) []runtime.Value {
			if m == nil {
				return nil
			}
			out := make([]runtime.Value, len(m.Keys))
			for i, ck := range m.Keys {
				out[i] = m.Pairs[ck]
			}
			return out
		}
	}
	return src
}

// scriptMapSeqArg reads a map argument of the maps seq helpers: the
// script map (nil for a nil map), its typedef, and the declared key and
// value typedefs (nil when unknown). ok is false for a non-map.
func scriptMapSeqArg(vc runtime.VMCaller, v runtime.Value) (m *runtime.Map, kt, vt *runtime.TypeDef, ok bool) {
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
		if mapTypeExpr(mt) == nil {
			return nil, nil, nil, false
		}
	case runtime.Nil:
	default:
		return nil, nil, nil, false
	}
	if mt != nil {
		kt, _ = mapKeyType(vc, mt)
		vt, _ = vc.ElemOf(mt)
	}
	return m, kt, vt, true
}

// mapKeyConv converts a script map's stored (untagged) key back to the
// declared key type — a map[fs.FileMode]bool yields FileModes, not their
// payloads. nil when keys need no conversion: an interface-keyed map's
// keys already carry their dynamic types, and a predeclared int, string,
// float64 or bool key is stored bare. A key already tagged with the key
// type passes through.
func mapKeyConv(vc runtime.VMCaller, m *runtime.Map, kt *runtime.TypeDef) func(runtime.Value) runtime.Value {
	if kt == nil || m == nil || m.IfaceKey || kt.Kind == runtime.KindInterface || plainKeyTd(kt) {
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

// plainKeyTd reports whether kt is a predeclared type whose values are
// stored bare (int, string, float64, bool) — its keys need no
// conversion back to the declared type.
func plainKeyTd(kt *runtime.TypeDef) bool {
	for _, name := range [...]string{"int", "string", "float64", "bool"} {
		if kt == runtime.BasicTypedef(name) {
			return true
		}
	}
	return false
}

// seqSliceArg reads the slice argument of the slices seq helpers: the
// slice (nil for a nil slice), the argument's own type (a named slice
// type stays named) and its element typedef. ok is false for a non-slice.
func seqSliceArg(vc runtime.VMCaller, v runtime.Value) (s *runtime.Slice, st, et *runtime.TypeDef, ok bool) {
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
		if !nilSliceArg(x) {
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

// sliceElemAt reads s[i] — a virtual (zero-size element) slice stores
// none and reads its zero.
func sliceElemAt(s *runtime.Slice, i int) runtime.Value {
	if s.Virtual() {
		return s.Zero
	}
	return s.Elems[i]
}

// sliceLen is len(s) for a possibly nil slice.
func sliceLen(s *runtime.Slice) int {
	if s == nil {
		return 0
	}
	return int(s.Len())
}

// collectSeq drives seq and returns its elements, boxed as a []E typed
// after the seq (a nil slice when it yields nothing, like
// slices.Collect), plus the raw elements and the slice typedef.
func collectSeq(vc runtime.VMCaller, seq runtime.Value) (runtime.Value, []runtime.Value, *runtime.TypeDef, error) {
	el, err := seqElems(vc, seq)
	if err != nil {
		return nil, nil, nil, err
	}
	st := seqSliceTd(seq)
	return packCollected(el, st), el, st, nil
}

// packCollected boxes collected elements: a nil []E when there are none
// and the type is known, else a slice typed st.
func packCollected(el []runtime.Value, st *runtime.TypeDef) runtime.Value {
	if len(el) == 0 && st != nil {
		return &runtime.TypedNil{Typ: st}
	}
	return &runtime.Slice{Elems: el, Typ: st}
}

// mapKeyOperand is the stored form of key k in a map typed mt.
func mapKeyOperand(vc runtime.VMCaller, mt *runtime.TypeDef, k runtime.Value) runtime.Value {
	if mt == nil {
		return k
	}
	return vc.MapKeyOperand(mt, k)
}

// slicesSeqHelpers adds the iterator helpers of slices (Go 1.23's
// iter.Seq surface) to its symbol table.
func (e *Engine) slicesSeqHelpers(slicesPkg map[string]runtime.Value) map[string]runtime.Value {
	sliceSeq := func(name string, two bool, each func(s *runtime.Slice, yield func([]runtime.Value) bool)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
			}
			s, _, et, ok := seqSliceArg(vc, args[0])
			if !ok {
				return nil, fmt.Errorf("%s: arg must be a slice", name)
			}
			fn := seqFn(name, &runtime.SeqSource{Each: func(_ runtime.VMCaller, yield func([]runtime.Value) bool) error {
				each(s, yield)
				return nil
			}})
			if two {
				return e.typedSeq(fn, runtime.BasicTypedef("int"), et), nil
			}
			return e.typedSeq(fn, et), nil
		}}
	}
	slicesPkg["Values"] = sliceSeq("slices.Values", false, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 1)
		for i, n := 0, sliceLen(s); i < n; i++ {
			buf[0] = sliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["All"] = sliceSeq("slices.All", true, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 2)
		for i, n := 0, sliceLen(s); i < n; i++ {
			buf[0], buf[1] = int64(i), sliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["Backward"] = sliceSeq("slices.Backward", true, func(s *runtime.Slice, yield func([]runtime.Value) bool) {
		buf := make([]runtime.Value, 2)
		for i := sliceLen(s) - 1; i >= 0; i-- {
			buf[0], buf[1] = int64(i), sliceElemAt(s, i)
			if !yield(buf) {
				return
			}
		}
	})
	slicesPkg["Collect"] = &runtime.BuiltinFunc{Name: "slices.Collect", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("slices.Collect needs 1 arg, got %d", len(args))
		}
		out, _, _, err := collectSeq(vc, args[0])
		if err != nil {
			return nil, fmt.Errorf("slices.Collect: %w", err)
		}
		return out, nil
	}}
	slicesPkg["Sorted"] = &runtime.BuiltinFunc{Name: "slices.Sorted", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("slices.Sorted needs 1 arg")
		}
		_, el, st, err := collectSeq(vc, args[0])
		if err != nil {
			return nil, fmt.Errorf("slices.Sorted: %w", err)
		}
		sort.Slice(el, func(i, j int) bool { return lessScript(el[i], el[j]) })
		return packCollected(el, st), nil
	}}
	slicesPkg["AppendSeq"] = &runtime.BuiltinFunc{Name: "slices.AppendSeq", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("slices.AppendSeq needs 2 args, got %d", len(args))
		}
		s, st, _, ok := seqSliceArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("slices.AppendSeq: first arg must be a slice")
		}
		el, err := seqElems(vc, args[1])
		if err != nil {
			return nil, fmt.Errorf("slices.AppendSeq: %w", err)
		}
		if len(el) == 0 {
			return args[0], nil
		}
		base := make([]runtime.Value, 0, sliceLen(s)+len(el))
		for i, n := 0, sliceLen(s); i < n; i++ {
			base = append(base, sliceElemAt(s, i))
		}
		return retagSlice(args[0], &runtime.Slice{Elems: append(base, el...), Typ: st}), nil
	}}
	sortedBy := func(name string, sorter func([]runtime.Value, func(a, b runtime.Value) int)) *runtime.BuiltinFunc {
		return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
			}
			_, el, st, err := collectSeq(vc, args[0])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			cmp := args[1]
			sorter(el, func(a, b runtime.Value) int {
				n, _ := runtime.Unwrap(callOrPanic(vc, cmp, []runtime.Value{a, b})).(int64)
				return int(n)
			})
			return packCollected(el, st), nil
		}}
	}
	slicesPkg["SortedFunc"] = sortedBy("slices.SortedFunc", slices.SortFunc[[]runtime.Value])
	slicesPkg["SortedStableFunc"] = sortedBy("slices.SortedStableFunc", slices.SortStableFunc[[]runtime.Value])
	slicesPkg["Chunk"] = &runtime.BuiltinFunc{Name: "slices.Chunk", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("slices.Chunk needs 2 args, got %d", len(args))
		}
		s, st, _, ok := seqSliceArg(vc, args[0])
		if !ok || s != nil && s.Virtual() {
			return nil, fmt.Errorf("slices.Chunk: arg must be a slice")
		}
		n := intOf(args[1])
		if n < 1 {
			panic(&runtime.Panic{Value: "cannot be less than 1"})
		}
		fn := seqFn("slices.Chunk", &runtime.SeqSource{Each: func(_ runtime.VMCaller, yield func([]runtime.Value) bool) error {
			buf := make([]runtime.Value, 1)
			for i, l := 0, sliceLen(s); i < l; i += n {
				end := min(i+n, l)
				buf[0] = retagSlice(args[0], &runtime.Slice{Elems: s.Elems[i:end:end], Typ: st})
				if !yield(buf) {
					return nil
				}
			}
			return nil
		}})
		return e.typedSeq(fn, st), nil
	}}
	return slicesPkg
}

// mapsSeqHelpers adds the iterator helpers of maps (Go 1.23's iter.Seq
// surface) to its symbol table.
func (e *Engine) mapsSeqHelpers(mapsPkg map[string]runtime.Value) map[string]runtime.Value {
	mapsPkg["Keys"] = &runtime.BuiltinFunc{Name: "maps.Keys", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.Keys needs 1 arg, got %d", len(args))
		}
		m, kt, _, ok := scriptMapSeqArg(vc, args[0])
		if !ok {
			return seqFn("maps.Keys", sliceSource(mapKeys(goNative(args[0])).Elems)), nil
		}
		return e.typedSeq(seqFn("maps.Keys", mapSource(m, mapSeqKeys, mapKeyConv(vc, m, kt))), kt), nil
	}}
	mapsPkg["Values"] = &runtime.BuiltinFunc{Name: "maps.Values", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.Values needs 1 arg, got %d", len(args))
		}
		m, _, vt, ok := scriptMapSeqArg(vc, args[0])
		if !ok {
			return seqFn("maps.Values", sliceSource(mapValues(goNative(args[0])).Elems)), nil
		}
		return e.typedSeq(seqFn("maps.Values", mapSource(m, mapSeqValues, nil)), vt), nil
	}}
	mapsPkg["All"] = &runtime.BuiltinFunc{Name: "maps.All", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("maps.All needs 1 arg, got %d", len(args))
		}
		m, kt, vt, ok := scriptMapSeqArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("maps.All: arg must be a map")
		}
		return e.typedSeq(seqFn("maps.All", mapSource(m, mapSeqAll, mapKeyConv(vc, m, kt))), kt, vt), nil
	}}
	mapsPkg["Insert"] = &runtime.BuiltinFunc{Name: "maps.Insert", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("maps.Insert needs 2 args, got %d", len(args))
		}
		m, _, _, ok := scriptMapSeqArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("maps.Insert: first arg must be a map")
		}
		mt := runtime.TagOf(args[0])
		if m != nil && mt == nil {
			mt = m.Typ
		}
		var bad bool
		err := driveSeq(vc, args[1], func(ya []runtime.Value) {
			if len(ya) != 2 {
				bad = true
				return
			}
			if m == nil {
				panic(runtime.PlainPanic("assignment to entry in nil map"))
			}
			m.Insert(mapKeyOperand(vc, mt, ya[0]), ya[1])
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
		if els := iterSeqElems(args[0]); len(els) == 2 {
			mt = &runtime.TypeDef{
				Kind: runtime.KindMap, Anon: &ast.MapType{Key: ast.NewIdent("K"), Value: ast.NewIdent("V")},
				Binds: map[string]runtime.Value{"K": els[0], "V": els[1]}, Pkg: els[1].Pkg, File: els[1].File,
			}
		}
		m := &runtime.Map{}
		if mt != nil {
			m = vc.NewMap(mt)
		}
		var bad bool
		err := driveSeq(vc, args[0], func(ya []runtime.Value) {
			if len(ya) != 2 {
				bad = true
				return
			}
			m.Insert(mapKeyOperand(vc, mt, ya[0]), ya[1])
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
