// Stdlib intrinsics: a small native surface bound into every engine so
// interpreted code doesn't have to parse GOROOT sources for the common
// cases. Script values are marshalled to Go natives at the boundary
// (hostbridge.GoNative) so the real fmt/strconv/... implementations do the work.
// Errors return Go-style (value, err) tuples where the real API has one.
//
// Deliberately small — anything absent falls through to lazy GOROOT source
// interpretation, which stays the documented path for full fidelity.

package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"io"
	"os"
	"reflect"
	goruntime "runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/podhmo/minigo/builtin"
	"github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdcontext "github.com/podhmo/minigo/internal/stdlib/context"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	stdtime "github.com/podhmo/minigo/internal/stdlib/time"
	"github.com/podhmo/minigo/runtime"
)

var (
	memStatsOnce     sync.Once
	memStatsSnapshot goruntime.MemStats
)

// hostType delegates to the shared binding machinery in internal/stdlib.
func hostType(name string, new func() any) *runtime.TypeDef {
	return stdlib.HostType(name, new)
}

// hostHelpers builds BuiltinFuncs whose Fn marshals arguments to Go natives
// and results back to runtime values.
type hostHelpers struct {
	env bind.Env // the narrow host-environment surface the bindings see
}

// engineStdout is the script-visible os.Stdout: a *os.File facade whose
// writes forward to the engine's output (WithOutput) lazily, so a host
// callee receiving os.Stdout — template.Execute, fmt.Fprint's target —
// lands in the same stream the fmt intrinsics print to. The embedded
// *os.File promotes the rest of the file API (Name, Fd, Stat, Sync, the
// deadline family, Read/ReadAt/Seek, ...) onto the reflect method set,
// so member access keeps working like Go's.
type engineStdout struct {
	*os.File
	h *hostHelpers
}

func (w *engineStdout) Write(p []byte) (int, error) { return w.h.out().Write(p) }

func (w *engineStdout) WriteString(s string) (int, error) {
	if sw, ok := w.h.out().(io.StringWriter); ok {
		return sw.WriteString(s)
	}
	return w.h.out().Write([]byte(s))
}

// ReadFrom keeps io.Copy(os.Stdout, r) inside the engine's output: the
// promoted (*os.File).ReadFrom would splice straight to the process's
// fd 1 and escape WithOutput the way a bare os.Stdout did.
func (w *engineStdout) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(w.h.out(), r)
}

// out returns the environment's output writer (io.Discard when unset).
func (h *hostHelpers) out() io.Writer {
	if h.env == nil {
		return io.Discard
	}
	return h.env.Out()
}

func (h *hostHelpers) Fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fnvc(name, func(_ runtime.VMCaller, a []any) (any, error) { return f(a) }, target...)
}

// fnvc is fn for bindings that need the caller (e.g. deadlock-wait
// bookkeeping through the channel a host call hands to the script).
func (h *hostHelpers) Fnvc(name string, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(vc, a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fnvcRaw is an arity-checked fnvc that also hands f the raw script
// args — an error argument must reach hostErrOf unflattened so a script
// error keeps its identity through the host (hostbridge.GoNative would format it).
func (h *hostHelpers) FnvcRaw(name string, arity int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != arity {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, arity, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(vc, a, args)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// Unexported spellings keep the existing call sites; the exported
// variants satisfy stdlib.Helpers for the moved installers (P2-12c).
func (h *hostHelpers) fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fn(name, f, target...)
}

func (h *hostHelpers) fn1(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fn1(name, f, target...)
}

func (h *hostHelpers) fn2(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fn2(name, f, target...)
}

func (h *hostHelpers) fn3(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Fn3(name, f, target...)
}

// Fn1/Fn2/Fn3 are arity-checked variants.
func (h *hostHelpers) Fn1(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 1, f, target...)
}

func (h *hostHelpers) Fn2(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 2, f, target...)
}

func (h *hostHelpers) Fn3(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.Arity(name, 3, f, target...)
}

func (h *hostHelpers) Arity(name string, n int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < n {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, n, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = hostbridge.GoNative(v)
		}
		r, err := f(a)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// scanFn adapts fmt's Scan family: the out-params arrive as script refs,
// which host fmt cannot store through, so each is mirrored into a host
// variable of the pointee's type, the real fmt call scans into those, and
// each result is written back through the ref. heads counts the leading
// non-pointer args (a format string or reader); headFn converts any
// prefix of them it needs to (nil: none) and scanFn goNatives the rest,
// so a converter like scanReader never has to know the arg count.
func (h *hostHelpers) scanFn(name string, heads int, headFn func(runtime.MemberCaller, []runtime.Value) ([]any, error), target any, call func(head, ptrs []any) (int, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < heads {
			return nil, fmt.Errorf("%s needs at least %d args, got %d", name, heads, len(args))
		}
		var head []any
		if headFn != nil {
			var err error
			if head, err = headFn(v, args[:heads]); err != nil {
				return nil, err
			}
		}
		for i := len(head); i < heads; i++ {
			head = append(head, hostbridge.GoNative(args[i]))
		}
		ptrs := make([]any, len(args)-heads)
		tds := make([]*runtime.TypeDef, len(args)-heads)
		for i, ref := range args[heads:] {
			cur, ok := runtime.Deref(ref)
			if !ok {
				return retErr(0, fmt.Errorf("%s: argument %d is not a pointer", name, i+heads+1))
			}
			// the host mirror erases the pointee's declared tag —
			// `var m MyInt; Sscan("5", &m)` — so capture it for the
			// write-back below.
			tds[i] = runtime.TagOf(cur)
			t := reflect.TypeOf(hostbridge.GoNative(cur))
			if t == nil {
				return retErr(0, fmt.Errorf("%s: cannot infer scan target type: argument %d", name, i+heads+1))
			}
			rv := reflect.New(t)
			// Seed with the current pointee: gc leaves args the scan
			// never reached (or failed to store) untouched.
			rv.Elem().Set(reflect.ValueOf(hostbridge.GoNative(cur)))
			ptrs[i] = rv.Interface()
		}
		n, err := call(head, ptrs)
		// gc stores whatever it managed to scan even on error, so the
		// write-back runs unconditionally.
		for i, ref := range args[heads:] {
			out := scriptVal(reflect.ValueOf(ptrs[i]).Elem().Interface())
			if tds[i] != nil {
				out = runtime.Tag(tds[i], out)
			}
			runtime.SetRef(ref, out)
		}
		return retErr(n, err)
	}}
}

// scanReader converts an Fscan family's leading reader arg, accepting
// both host io.Reader values and script-defined readers.
func (h *hostHelpers) scanReader(v runtime.MemberCaller, args []runtime.Value) ([]any, error) {
	r, err := h.asReaderVM(v, args[0])
	if err != nil {
		return nil, err
	}
	return []any{r}, nil
}

// sortInPlace sorts a *runtime.Slice's elements directly — going through
func (h *hostHelpers) sortInPlace(name string) *runtime.BuiltinFunc {
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
			sortScript(s.Elems)
		case *runtime.TypedNil:
			// sorting a nil slice is a no-op in Go; nil pointers and
			// other typed nils still fail the reflect type check.
			if !nilSliceArg(s) {
				return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
			}
		default:
			return nil, fmt.Errorf("%s: arg must be a slice, got %T", name, args[0])
		}
		return runtime.NIL, nil
	}}
}

func numOf(v runtime.Value) (float64, bool) {
	switch n := v.(type) {
	case *runtime.Named:
		return numOf(n.V)
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

// sortInterface adapts a script value's Len/Less/Swap members into a
// host sort.Interface for sort.Sort: the callbacks run on the calling
// VM (the usual host-callback hazard — results are only read during the
// call).
func (h *hostHelpers) sortInterface(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
	sort.Sort(&scriptSortable{vc: vc, lenFn: lenFn, lessFn: lessFn, swapFn: swapFn})
	return runtime.NIL, nil
}

func (h *hostHelpers) sortSlice(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if hostSortSlice(v, args[0], args[1], false) {
		return runtime.NIL, nil // a boxed host slice (net.IP)
	}
	s, ok := sliceOf(args[0])
	if !ok {
		if nilSliceArg(args[0]) {
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

// sortByCmpFunc implements slices.SortFunc and slices.SortStableFunc: cmp is
// a script callable returning negative/zero/positive. The host sort is the
// real algorithm (pdqsort / stable pdqsort), so element order under a cmp
// that reports ties matches go's even past the 12-element insertion bound.
func (h *hostHelpers) sortByCmpFunc(name string) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		s, ok := sliceOf(args[0])
		if !ok {
			if nilish(args[0]) {
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

// sizeDeclTyp recovers the operand's declared typedef for
// unsafe.Sizeof/Alignof — the layout unsafe.Offsetof already computes
// from declared field types. A value without a stamped typedef (an
// untyped literal, a host box) reports nil and the caller falls back
// to runtime-value widths.
func sizeDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch v.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		// a pointer value: the operand's declared type is *T where T
		// is the pointee's declared type — the tag stamped on the box
		// belongs under the star (OpBox/OpLocalRef stamp the POINTEE's
		// typedef), it never answers the pointee's layout itself.
		var elem *runtime.TypeDef
		if c, ok := v.(*runtime.Cell); ok {
			elem = c.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: elem}
	}
	return valueDeclTyp(v)
}

// retErr wraps a (n int, err error) or single-value+error result into the
// script-visible tuple (value, err-or-nil).
func retErr(n int, err error) (any, error) {
	return stdlib.RetErr(n, err)
}

func retErr2[T any](v T, err error) (any, error) {
	return stdlib.RetErr2(v, err)
}

// errVal delegates to the shared binding machinery in internal/stdlib.
func errVal(err error) runtime.Value {
	return stdlib.ErrVal(err)
}

// scriptVal delegates to the shared binding machinery in internal/stdlib.
func scriptVal(v any) runtime.Value {
	return stdlib.ScriptVal(v)
}

func str(v any) string {
	return stdlib.Str(v)
}

// timerChanBox/tickerChanBox/afterFuncTimer moved with the time
// installer (P2-12c); the aliases keep typeid_test.go's references.
type timerChanBox = stdtime.TimerChanBox
type afterFuncTimer = stdtime.AfterFuncTimer

// scriptCoro emulates runtime's coro for the iter.Pull/Pull2 linknames;
// the type lives with the moved context installer (P2-12c).
type scriptCoro = stdcontext.ScriptCoro

// asCtx coerces a script value to a context.Context (P2-12c).
func asCtx(v any) (context.Context, error) {
	return stdcontext.AsCtx(v)
}

// proxyHostType resolves the public spelling of a host type — the
// TypeAlias hook for the reflect facade (moved with the time
// installer, P2-12c).
func proxyHostType(rt reflect.Type) reflect.Type {
	return stdtime.ProxyHostType(rt)
}

// proxyUnwrapHost is the facade's UnwrapHost hook — the inverse of
// proxyHostType (P2-12c).
func proxyUnwrapHost(v any, t reflect.Type) (reflect.Value, bool) {
	return stdtime.ProxyUnwrapHost(v, t)
}

// proxyHostTypeOf is proxyHostType over a value — the fmt path's %T
// lookup (P2-12c).
func proxyHostTypeOf(v any) reflect.Type {
	return stdtime.ProxyHostTypeOf(v)
}

func intOf(v any) int {
	return stdlib.IntOf(v)
}

func int64Of(v any) int64 { return stdlib.Int64Of(v) }

// unsafeElems resolves unsafe.String/Slice's (ptr, len) to the n
// elements starting at an &s[i] ref, sharing the slice's backing.
// sliceTypFromPtr maps a pointer/array/slice typedef to the unnamed
// []E typedef unsafe.Slice's result carries — never the named slice or
// the [N]E array the pointer was taken from.
func sliceTypFromPtr(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	switch x := td.TypeExpr().(type) {
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.X}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.Elt}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	}
	return td
}

func byteSlice(v any) []byte {
	return stdlib.ByteSlice(v)
}

func strSlice(v any) []string {
	if n, ok := v.(*runtime.Named); ok {
		return strSlice(n.V)
	}
	if s, ok := v.(*runtime.Slice); ok {
		out := make([]string, len(s.Elems))
		for i, e := range s.Elems {
			out[i] = str(hostbridge.GoNative(e))
		}
		return out
	}
	if a, ok := v.([]any); ok {
		out := make([]string, len(a))
		for i, e := range a {
			out[i] = str(e)
		}
		return out
	}
	return nil
}

// nilSliceArg reports whether v is a typed nil whose type is a slice —
// sort's slice functions accept a nil slice (a no-op / sorted) but
// still reject nil pointers, maps, chans like Go's reflect-based check.
func nilSliceArg(v runtime.Value) bool {
	tn, ok := runtime.Unwrap(v).(*runtime.TypedNil)
	return ok && tn.Typ != nil && tn.Typ.Kind == runtime.KindSlice && !isArrayTyp(tn.Typ)
}

func sliceOf(v runtime.Value) (*runtime.Slice, bool) {
	return stdlib.SliceOf(v)
}

// retagSlice re-applies orig's named slice type (`type Buffer []byte`)
// to out, a new header over orig's elements; an unnamed orig returns out.
func retagSlice(orig runtime.Value, out *runtime.Slice) runtime.Value {
	if n, ok := orig.(*runtime.Named); ok {
		return runtime.Tag(n.Typ, out)
	}
	return out
}

// packSlice moves kept elements to the front of s's backing array and
// zeroes the vacated tail, then returns the len(kept) view sharing it —
// the same update Go's slices.DeleteFunc performs, so slices re-sliced
// from the original observe the deletion.
func (h *hostHelpers) packSlice(vc runtime.VMCaller, s *runtime.Slice, kept []runtime.Value) *runtime.Slice {
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

// seqElems collects a seq argument's elements: a *runtime.Slice reads
// verbatim, and an iter.Seq-shaped callable (seqOf's BuiltinFunc, or a
// script func used as a seq) is driven with a yield that gathers each
// element — the seq stops when yield reports false.
func seqElems(vc runtime.ScriptCaller, v runtime.Value) ([]runtime.Value, error) {
	if s, ok := sliceOf(v); ok {
		return append([]runtime.Value{}, s.Elems...), nil
	}
	if src := nativeSeq(v); src != nil && src.Collect != nil {
		// an intrinsic seq over a known source (maps.Keys/Values of a
		// script map, strings.SplitSeq) reads it in one pass.
		return src.Collect(vc), nil
	}
	var out []runtime.Value
	if err := driveSeq(vc, v, func(ya []runtime.Value) { out = append(out, ya...) }); err != nil {
		return nil, err
	}
	return out, nil
}

// strPieces boxes string pieces as script values for seqOf.
func strPieces(ss []string) []runtime.Value {
	out := make([]runtime.Value, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// seqOf builds an iter.Seq-shaped value: a callable the range-over-func
// loop drives through its yield builtin — the producer runs once and the
// loop body executes inside each yield call, so early exit just reports
// false back. Pieces are materialized up front; laziness is unobservable
// from a script.
func seqOf(name string, pieces []runtime.Value) *runtime.BuiltinFunc {
	return seqFn(name, sliceSource(pieces))
}

// nilish reports whether a value carries no concrete content — bare nil,
// a typed nil, or a named wrapper around one.
func nilish(v runtime.Value) bool {
	switch runtime.Unwrap(v).(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}

// sliceTypOf keeps a bound slice op's declared element type — a new
// []string built without the tag loses `[]string` reads downstream.
func sliceTypOf(v any) *runtime.TypeDef {
	if s, ok := runtime.Unwrap(v).(*runtime.Slice); ok {
		return s.Typ
	}
	return nil
}

// scriptSliceArg reads a script slice argument of the comparing slices
// helpers: its elements and element typedef (nil when unknown). A nil
// slice has no elements. ok is false for anything else — a host slice
// box — which the caller compares host-side.
func scriptSliceArg(vc runtime.ElemTyper, v runtime.Value) ([]runtime.Value, *runtime.TypeDef, bool) {
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
		if nilSliceArg(x) {
			return nil, nil, true
		}
	case *runtime.GoValue:
		// a boxed host named slice (net.IP) read element by element —
		// callers only read, so the elements may be copies.
		return hostSliceElems(x)
	}
	return nil, nil, false
}

// hostSliceElems reads a boxed host slice or array's elements as script
// values. The element typedef is the predeclared basic when the element
// type is an unnamed basic (net.IP's byte), else nil.
func hostSliceElems(x *runtime.GoValue) ([]runtime.Value, *runtime.TypeDef, bool) {
	rv := reflect.ValueOf(x.V)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, nil, false
	}
	el := make([]runtime.Value, rv.Len())
	for i := range el {
		el[i] = scriptVal(rv.Index(i).Interface())
	}
	var et *runtime.TypeDef
	if t := rv.Type().Elem(); t.PkgPath() == "" && t.Kind() <= reflect.Complex128 && t.Kind() != reflect.Invalid {
		et = runtime.BasicTypedef(t.Kind().String())
	}
	return el, et, true
}

// sliceIndexScript is slices.Index(s, x) over a script slice with Go's
// element equality ([runtime.VMCaller.Equal]): an untyped x converts to
// the element type first (`1<<n` truncates to the element width), a
// []any compares (dynamic type, value) pairs, and a boxed host named
// basic meets its source twin in a []fs.FileMode.
func sliceIndexScript(vc runtime.IndexTyper, name string, args []runtime.Value) (int, error) {
	if len(args) != 2 {
		return 0, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
	}
	el, et, ok := scriptSliceArg(vc, args[0])
	if !ok {
		return slices.Index(anySlice(hostbridge.GoNative(args[0])), hostbridge.GoNative(args[1])), nil
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

func anySlice(v any) []any {
	if n, ok := v.(*runtime.Named); ok {
		return anySlice(n.V)
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

func strsSlice(ss []string) runtime.Value {
	return stdlib.StrsSlice(ss)
}

// runePred adapts a script callback into a rune predicate — strings'
// TrimFunc/IndexFunc family and unicode's Is-print predicates call it.
func runePred(v runtime.ScriptCaller, fn runtime.Value) func(rune) bool {
	return func(r rune) bool {
		res := hostbridge.CallOrPanic(v, fn, []runtime.Value{int64(r)})
		b, _ := res.(bool)
		return b
	}
}

func runeOf(v any) rune { return rune(intOf(v)) }

// strArgs marshals trailing varargs (each already a script value) to strings.
func strArgs(a []any) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = str(v)
	}
	return out
}

// callOrPanic invokes a script callback from inside a host call and
// re-raises any failure as a Go panic: v.Call delivers a script panic
// (or trap) as an error across its boundary, but returning that as the
// builtin's error would turn a recoverable panic into a runtime trap —
// in Go a failure inside strings.Map's callback just propagates as a
// panic, and a trap or process exit keeps its own kind through
// asScriptPanic.
// walkCallback runs a filepath.Walk/WalkDir script callback for host
// path p: the walk ran from ap, the engine's resolution of the script's
// root, so p maps back onto root as written (gc hands the root over
// verbatim — "./a", "a/" — and joins descendants onto it), entry is the
// boxed fs.DirEntry/fs.FileInfo (NIL when the host passed none), and
// the result goes back through hostErrOf so the host walker compares
// fs.SkipDir/SkipAll by identity and a script error keeps its identity
// on the way out (errVal).
func walkCallback(v runtime.MemberCaller, cb runtime.Value, root, ap, p string, entry runtime.Value, werr error) error {
	sp := scriptPathOf(root, ap, p)
	r := hostbridge.CallOrPanic(v, cb, []runtime.Value{sp, entry, errVal(scriptPathErr(werr, root, ap))})
	return hostbridge.HostErrOf(v, r)
}

// derefTyp peels the pointer level off a value's typedef — json.Unmarshal
// decodes into the pointee, so `&cfg` (*Config) shapes as Config.
func derefTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td != nil && td.Kind == runtime.KindPointer {
		if td.Elem == nil {
			// `&v` where the pointee's typedef could not be recovered
			// (e.g. `var v any` carries an untyped iface nil) — decode
			// as `any` rather than wrapping the value in a cell.
			return nil
		}
		return td.Elem
	}
	return td
}

// elemAt indexes a prior container's stored values without failing — a
// prior field/element exists only when the old value matches the new
// shape's position.
func elemAt(vs []runtime.Value, i int) runtime.Value {
	if i < len(vs) {
		return vs[i]
	}
	return nil
}

// sliceExtreme is slices.Max/Min: the element better wins over best.
// It returns the slice's own element, so its type survives —
// slices.Max([]fs.FileMode{...}) prints a FileMode, not its payload.
func sliceExtreme(vc runtime.ElemTyper, name string, args []runtime.Value, better func(best, x runtime.Value) bool) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
	}
	el, _, ok := scriptSliceArg(vc, args[0])
	if !ok {
		el = scriptElems(hostbridge.GoNative(args[0]))
	}
	if len(el) == 0 {
		panic(emptyListPanic(name))
	}
	best := el[0]
	for _, x := range el[1:] {
		if better(best, x) {
			best = x
		}
	}
	return best, nil
}

// emptyListPanic is the string panic of slices.Max/Min/MaxFunc/MinFunc
// on an empty slice — a plain string, so `recover()` yields exactly
// Go's `slices.MaxFunc: empty list`.
func emptyListPanic(name string) *runtime.Panic {
	return &runtime.Panic{Value: name + ": empty list"}
}

// sliceParam resolves the slice parameter of the slices helpers: the
// slice itself, or nil for a nil slice (which holds no elements) — a
// bare `var s []int`, a typed or named nil, a nil read out of a struct
// field or an `any`. Every helper reading a slice argument goes through
// it, so a nil slice behaves as in Go instead of failing a type
// assertion. A slice of zero-size elements stores none of them, so the
// helpers that read elements do not take it; see [sliceParamAny].
func sliceParam(name string, v runtime.Value) (*runtime.Slice, error) {
	s, err := sliceParamAny(name, v)
	if err == nil && s != nil && s.Virtual() {
		return nil, fmt.Errorf("%s: slices of zero-size elements are not supported", name)
	}
	return s, err
}

// sliceParamAny is [sliceParam] that also accepts a slice of zero-size
// elements, for the helpers that never read its elements.
func sliceParamAny(name string, v runtime.Value) (*runtime.Slice, error) {
	if s, ok := sliceOf(v); ok {
		return s, nil
	}
	if nilish(v) {
		return nil, nil
	}
	return nil, fmt.Errorf("%s: arg must be a slice", name)
}

// sliceParamTyp is the declared slice type of a [sliceParam] result s
// that was read from v — the slice's own, or the nil slice's — nil when
// unknown.
func sliceParamTyp(s *runtime.Slice, v runtime.Value) *runtime.TypeDef {
	if s != nil {
		return s.Typ
	}
	if tn, ok := runtime.Unwrap(v).(*runtime.TypedNil); ok {
		return tn.Typ
	}
	return nil
}

// nilSliceLike is the nil slice of type typ, tagged with orig's named
// slice type when orig carries one (see [retagSlice]).
func nilSliceLike(orig runtime.Value, typ *runtime.TypeDef) runtime.Value {
	nl := &runtime.TypedNil{Typ: typ}
	if n, ok := orig.(*runtime.Named); ok {
		return runtime.Tag(n.Typ, nl)
	}
	return nl
}

// elemOperands prepares the variadic element operands of slices.Insert
// and Replace for storing into a slice of type typ: an untyped constant
// takes the element type (like sliceIndexScript's operand), and each
// operand is copied so an array or struct is not shared with its source.
func elemOperands(vc runtime.ElemOperandTyper, name string, typ *runtime.TypeDef, vals []runtime.Value) ([]runtime.Value, error) {
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

// fillHoles gives the slots of full that the host left empty — the
// tail slices.Delete cleared, the spare capacity of a freshly grown
// array — the zero value of typ's element, which is what a script slot
// holds there (a hole would print as <nil>).
func fillHoles(vc runtime.VMCaller, typ *runtime.TypeDef, full []runtime.Value) {
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

// cloneElems copies a slice's elements into a fresh backing array; an
// array or struct element is copied, not shared with the original.
func cloneElems(el []runtime.Value) []runtime.Value {
	out := append([]runtime.Value{}, el...)
	for i, v := range out {
		out[i] = runtime.Copy(v)
	}
	return out
}

// sliceElems is a [sliceParam] result's elements — none for nil.
func sliceElems(s *runtime.Slice) []runtime.Value {
	if s == nil {
		return nil
	}
	return s.Elems
}

// callInt runs a script cmp-style callback for its int result.
func callInt(vc runtime.ScriptCaller, fn runtime.Value, args ...runtime.Value) int64 {
	return int64Of(hostbridge.CallOrPanic(vc, fn, args))
}

// asCell peels Named tags to reach the addressable cell underneath —
// the pointee slot whose Typ is the declared (static) type. Ref kinds
// (FieldRef/IndexRef) are not cells and report nil.
func asCell(v runtime.Value) *runtime.Cell {
	for {
		switch x := v.(type) {
		case *runtime.Cell:
			return x
		case *runtime.Named:
			v = x.V
		default:
			return nil
		}
	}
}

// devisit marks a pair of composite values already under comparison.
// A reference cycle (a map or slice containing itself, two structures
// pointing at each other) reaches the same pair again, which Go's
// DeepEqual treats coinductively as equal — recording the pair both
// terminates the walk and answers the recurrence.
type devisit struct{ a, b runtime.Value }

// deepEql implements reflect.DeepEqual over script values: nil-ness and
// type identity are honored, composites compare recursively, and
// leaf/host values compare as marshaled natives. Named tags and
// pointer/ref layers peel on both sides in lockstep — a depth or named
// type mismatch is a type mismatch, so *T never equals T and a named
// type never equals a different name for the same underlying type.
//
// twin, when non-nil, reports whether two nil types differ only as a nil
// host *T and its source twin's (or bound) *T ([vm.VM.HostNilTwin]).
func deepEql(a, b runtime.Value, twin func(a, b *runtime.TypeDef) bool) bool {
	return deepEqlSeen(a, b, map[devisit]bool{}, twin)
}

func deepEqlSeen(a, b runtime.Value, seen map[devisit]bool, twin func(a, b *runtime.TypeDef) bool) bool {
	for {
		// a boxed host named basic (a FileMode from os.Stat) meets
		// its source twin's value (io/fs's ModeDir) as that Named;
		// a plain uint32 stays a different type.
		a, b = runtime.TwinOperands(a, b)
		an, aNamed := a.(*runtime.Named)
		bn, bNamed := b.(*runtime.Named)
		if aNamed != bNamed {
			// a raw bound host scalar (time.Second) against a tagged one
			// (time.Duration(1e9)): compare as host values.
			hn, raw := an, b
			if bNamed {
				hn, raw = bn, a
			}
			if hn.Typ != nil && hn.Typ.HostScalar != nil {
				hv, ok := runtime.HostScalarOf(hn.Typ, hn.V)
				return ok && reflect.TypeOf(raw) == reflect.TypeOf(hv) && any(raw) == hv
			}
			return false
		}
		if aNamed {
			if !runtime.TypIdenticalStrict(an.Typ, bn.Typ) {
				return false
			}
			if an.Typ != nil && an.Typ.HostScalar != nil {
				// a bound host scalar's payload is either the host value
				// or still a constant (time.Duration(1e9)): compare as
				// host values.
				ha, aok := runtime.HostScalarOf(an.Typ, an.V)
				hb, bok := runtime.HostScalarOf(bn.Typ, bn.V)
				if aok && bok {
					return ha == hb
				}
			}
			a, b = an.V, bn.V
			continue
		}
		ad, aRef := runtime.Deref(a)
		bd, bRef := runtime.Deref(b)
		if aRef != bRef {
			return false
		}
		if aRef {
			a, b = ad, bd
			continue
		}
		break
	}
	if an, bn := deepNilish(a), deepNilish(b); an || bn {
		if !(an && bn) {
			return false
		}
		// an untyped nil and an empty-interface nil are just nil —
		// equal to each other; a typed nil, bare or boxed in an
		// interface (`var a any = (*T)(nil)`), carries a dynamic type
		// so only another typed nil of the same type compares equal
		// (kind + spelling — anonymous typedefs all spell "" by name —
		// or a nil host *T against its source twin's *T).
		at, bt := deepNilTyp(a), deepNilTyp(b)
		if (at == nil) != (bt == nil) {
			return false
		}
		if at != nil {
			return runtime.TypIdenticalStrict(at, bt) || (twin != nil && twin(at, bt))
		}
		return true
	}
	switch av := a.(type) {
	case *runtime.Slice:
		bs, ok := b.(*runtime.Slice)
		// both arrays and slices are runtime.Slice — the typedef's
		// spelling carries the kind and element type ([]T != [N]T).
		if !ok || len(av.Elems) != len(bs.Elems) || !runtime.TypIdenticalStrict(av.Typ, bs.Typ) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Elems {
			if !deepEqlSeen(av.Elems[i], bs.Elems[i], seen, twin) {
				return false
			}
		}
		return true
	case *runtime.Struct:
		bs, ok := b.(*runtime.Struct)
		// struct defs need full type identity — field names alone let
		// struct{ X int } equal struct{ X any }. Named types match
		// only the same declaration; anonymous ones compare spelling
		// (field types, tags, order).
		if !ok || !runtime.TypIdenticalStrict(av.Def, bs.Def) || len(av.Fields) != len(bs.Fields) {
			return false
		}
		if av == bs {
			return true
		}
		v := devisit{av, bs}
		if seen[v] {
			return true
		}
		seen[v] = true
		for i := range av.Fields {
			if !deepEqlSeen(av.Fields[i], bs.Fields[i], seen, twin) {
				return false
			}
		}
		return true
	case *runtime.Map:
		bm, ok := b.(*runtime.Map)
		if !ok || len(av.Pairs) != len(bm.Pairs) || !runtime.TypIdenticalStrict(av.Typ, bm.Typ) {
			return false
		}
		if av == bm {
			return true
		}
		v := devisit{av, bm}
		if seen[v] {
			return true
		}
		seen[v] = true
		// keys match by the map's own equality — the canonical key in
		// Pairs — not by deep equality: two distinct pointer keys with
		// equal pointees are different keys in Go. Only the values
		// compare recursively.
		for ak, aval := range av.Pairs {
			bval, found := bm.LookupCanonical(ak)
			if !found {
				return false
			}
			if !deepEqlSeen(aval, bval, seen, twin) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(hostbridge.GoNative(a), hostbridge.GoNative(b))
}

// deepNilTyp is the dynamic type of a nil v: a typed nil's own type or
// the type boxed in an interface nil; nil for the nil interface.
func deepNilTyp(v runtime.Value) *runtime.TypeDef {
	if tn, ok := v.(*runtime.TypedNil); ok {
		return tn.Typ
	}
	return runtime.BoxedNilTyp(v)
}

// deepNilish reports whether v is any nil flavor (typed nil, iface nil,
// or the untyped nil).
func deepNilish(v runtime.Value) bool {
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil, runtime.Nil:
		return true
	}
	return v == nil || v == runtime.NIL
}

// ioAdapters bundles the env hooks the moved io package needs
// (P2-12): the declared method set and the fmt argument unwrap.
func (h *hostHelpers) ioAdapters() *stdio.Adapters {
	return &stdio.Adapters{
		MethodSetOf: func(v any) map[string]bool {
			set, _, _ := h.env.MethodSetOfValue(v)
			return set
		},
		UnwrapArg: func(v any) any {
			if fv, ok := v.(*fmtValue); ok {
				return fv.x
			}
			return v
		},
	}
}

func (h *hostHelpers) asReaderVM(vc runtime.MemberCaller, v any) (io.Reader, error) {
	return h.ioAdapters().AsReaderVM(vc, v)
}

func unwrapFmtArg(v any) any {
	if fv, ok := v.(*fmtValue); ok {
		return fv.x
	}
	return v
}

func asWriterVM(vc runtime.MemberCaller, v any) (io.Writer, error) {
	return defaultIOAdapters.AsWriterVM(vc, v)
}

var defaultIOAdapters = &stdio.Adapters{UnwrapArg: unwrapFmtArg}

// engineFileOf unwraps the script-visible os.Stdout facade to the real
// file for net's FileConn/FilePacketConn adapters (P2-12).
func engineFileOf(v any) *os.File {
	if s, ok := v.(*engineStdout); ok {
		return s.File
	}
	t, _ := v.(*os.File)
	return t
}
