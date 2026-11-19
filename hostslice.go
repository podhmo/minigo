package minigo

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

// Boxed host named slices (a net.IP) through the builtins and the slices
// and sort intrinsics. They keep their box for method dispatch; the
// operations below either act on the host backing (append, copy, make,
// sort.Slice) or read the slice as a script snapshot, run the script
// implementation and write the result back (see hostSliceBoundary).

// hostSliceBase is the host slice an append starts from: a boxed one, or
// the typed nil of a bound host slice type (`var ip net.IP`).
func hostSliceBase(x runtime.Value) (runtime.Value, bool) {
	if _, _, ok := vm.HostSliceOf(x); ok {
		return x, true
	}
	var td *runtime.TypeDef
	switch n := runtime.Unwrap(x).(type) {
	case *runtime.TypedNil:
		td = n.Typ
	case *runtime.IfaceNil:
		td = n.Typ
	}
	rt := hostbridge.HostNilType(td)
	if rt == nil || rt.Kind() != reflect.Slice {
		return nil, false
	}
	zero := &runtime.GoValue{V: reflect.Zero(rt).Interface()}
	if td.HostNew != nil {
		return runtime.Tag(td, zero), true // a script-declared net.IP keeps its tag
	}
	return zero, true
}

// spreadElems is the element list of an append operand list whose last
// operand may be a spread marker.
func spreadElems(args []runtime.Value) ([]runtime.Value, error) {
	n := len(args)
	if n == 0 {
		return args, nil
	}
	sp, ok := args[n-1].(*runtime.Spread)
	if !ok {
		return args, nil
	}
	if sp.S.Virtual() {
		// a huge zero-size-element slice carries only its length
		return nil, fmt.Errorf("append of a %d-element virtual slice to a host slice", sp.S.Len())
	}
	return append(slices.Clone(args[:n-1]), sp.S.Elems...), nil
}

// appendHostSlice is append on a boxed host slice: the host slice grows
// itself, so the result keeps its host type.
func appendHostSlice(v runtime.VMCaller, base runtime.Value, add []runtime.Value) (runtime.Value, error) {
	add, err := spreadElems(add)
	if err != nil {
		return nil, err
	}
	return vm.AppendHostSlice(v, base, add)
}

// copyIntoHostSlice is copy(dst, src) with a boxed host slice dst; src
// holds the elements already read from a script slice, a host slice
// snapshot or a string.
func copyIntoHostSlice(v runtime.VMCaller, dst runtime.Value, src []runtime.Value) (runtime.Value, error) {
	n, err := vm.CopyIntoHostSlice(v, dst, src)
	if err != nil {
		return nil, err
	}
	return int64(n), nil
}

// hostSortSlice runs sort.Slice / sort.SliceStable on a boxed host slice:
// the host slice itself is reordered while the script less function reads
// it by index, as the slice the script passed. ok is false when x is no
// host slice.
func hostSortSlice(v runtime.ScriptCaller, x, less runtime.Value, stable bool) bool {
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

// hostSliceUse describes where the slices a function reads sit among its
// arguments (pos; nil means every argument) and whether it reorders them
// in place.
type hostSliceUse struct {
	pos     []int
	inPlace bool
}

var (
	firstSliceRead     = hostSliceUse{pos: []int{0}}
	firstSliceReorder  = hostSliceUse{pos: []int{0}, inPlace: true}
	firstTwoSlicesRead = hostSliceUse{pos: []int{0, 1}}
)

// slicesHostUse is the exception list of the slices package: every other
// function reads a slice from its first argument.
var slicesHostUse = map[string]hostSliceUse{
	"Concat":         {},
	"Equal":          firstTwoSlicesRead,
	"EqualFunc":      firstTwoSlicesRead,
	"Compare":        firstTwoSlicesRead,
	"CompareFunc":    firstTwoSlicesRead,
	"Sort":           firstSliceReorder,
	"SortFunc":       firstSliceReorder,
	"SortStableFunc": firstSliceReorder,
	"Reverse":        firstSliceReorder,
	"Compact":        firstSliceReorder,
	"CompactFunc":    firstSliceReorder,
	"Delete":         firstSliceReorder,
	"DeleteFunc":     firstSliceReorder,
}

// sortHostUse names the sort functions that take a plain slice argument
// (sort.Sort and friends take a sort.Interface, which a box satisfies
// as it is).
var sortHostUse = map[string]hostSliceUse{
	"Ints":     firstSliceReorder,
	"Float64s": firstSliceReorder,
	"Strings":  firstSliceReorder,
}

// genericFns marks a bound package's intrinsics as generic: every
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
func genericFns(pkg map[string]runtime.Value) map[string]runtime.Value {
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
				ti := instNilTarg(fname, i, len(targs))
				if ti < 0 || ti >= len(targs) {
					continue
				}
				if td, ok := targs[ti].(*runtime.TypeDef); ok && instNilable(td) && !instIface(td) {
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
			return instResult(fname, targs, res), nil
		}
		pkg[name] = &wrapped
	}
	return pkg
}

// instNilTarg reports which type argument the declared type of arg
// position `i` of `fname` is bound to: position i binds targ i except
// for the params that are not a type argument (a comparator, a count,
// a seq operand) or bind a different one — BinarySearchFunc[S,E,T]'s
// target is T, the third type argument, not the positional second. -1
// leaves the arg's nil bare; the variadic tail binds the last targ.
func instNilTarg(fname string, i int, ntargs int) int {
	if ntargs == 0 {
		return -1
	}
	if ti, ok := instNilTargs[fname][i]; ok {
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

// instNilTargs: bound func name → arg position → type-argument index.
// Only the params that are not themselves a type argument, or bind a
// different one, appear; -1 leaves the nil bare.
var instNilTargs = map[string]map[int]int{
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

// instResSelf names the bound funcs whose result type is their first
// type argument (S/M): only those re-tag a nil or container result —
// `Clone[S]` → S, `Concat[[]int]()` → `[]int(nil)`. `Collect` and the
// `Sorted` family return a slice OF a type argument (`[]E`, `[]K`), the
// seq helpers return a seq, the rest a scalar — absent on purpose.
var instResSelf = map[string]bool{
	"slices.Clone": true, "slices.Concat": true, "slices.Compact": true,
	"slices.CompactFunc": true, "slices.Grow": true, "slices.Clip": true,
	"slices.Repeat": true, "slices.Insert": true, "slices.Delete": true,
	"slices.DeleteFunc": true, "slices.Replace": true, "slices.AppendSeq": true,
	"maps.Clone": true,
}

// instIface reports whether td's underlying is an interface — a nil
// arg binds as the untyped nil the interface elements already hold,
// not a TypedNil (a nil element of []any is an IfaceNil and slotEql
// would miss it).
func instIface(td *runtime.TypeDef) bool {
	if td.Kind == runtime.KindInterface {
		return true
	}
	u := runtime.TypUnderlyingSpelling(td)
	return u == "interface{}" || u == "any" || u == "error"
}

// instNilable reports whether td's underlying is a nilable shape — a
// nil argument of an instantiated call binds as td's zero.
func instNilable(td *runtime.TypeDef) bool {
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

// instResult types an instantiated call's result by its first type
// argument — only for the instResSelf funcs whose result IS that
// argument: an untyped nil becomes that arg's nil when it is nilable
// (`Concat[[]int]()` is `[]int(nil)`), and a container value re-tags to
// a named argument of the same underlying (`Clone[S]` returns S, not
// the []int the argument carried). Other funcs return `[]E` of an arg
// (Collect, Sorted), a seq, or a scalar — re-tagging any of those to
// targs[0] would lie about the result's type.
func instResult(fname string, targs []runtime.Value, res runtime.Value) runtime.Value {
	if len(targs) == 0 || !instResSelf[fname] {
		return res
	}
	td, ok := targs[0].(*runtime.TypeDef)
	if !ok || td == nil {
		return res
	}
	if res == runtime.NIL {
		if instNilable(td) {
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

// hostSliceBoundary wraps the intrinsics of pkg so a boxed host slice
// among their slice arguments reads as a script slice: the intrinsic runs
// on a snapshot, an in-place one writes the reordered elements back to the
// host backing, and a slice result of the snapshot's type is boxed as the
// host type again (slices.Clone of a net.IP is a net.IP). With def set it
// also covers every function of pkg that uses leaves out; otherwise only
// the functions uses names.
func hostSliceBoundary(pkg map[string]runtime.Value, uses map[string]hostSliceUse, def *hostSliceUse) map[string]runtime.Value {
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
		wrapped.Fn = hostSliceCall(bf.Fn, use)
		pkg[name] = &wrapped
	}
	return pkg
}

func hostSliceCall(fn func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), use hostSliceUse) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
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
