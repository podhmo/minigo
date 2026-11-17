// Stdlib intrinsics: a small native surface bound into every engine so
// interpreted code doesn't have to parse GOROOT sources for the common
// cases. Script values are marshalled to Go natives at the boundary
// (goNative) so the real fmt/strconv/... implementations do the work.
// Errors return Go-style (value, err) tuples where the real API has one.
//
// Deliberately small — anything absent falls through to lazy GOROOT source
// interpretation, which stays the documented path for full fidelity.

package minigo

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

var (
	memStatsOnce     sync.Once
	memStatsSnapshot goruntime.MemStats
)

// feedPump is one pump lifecycle: done retires it, exited closes when
// the goroutine returns, and dropped records a fire it held but never
// delivered (read only after exited closes).
type feedPump struct {
	done    chan struct{}
	exited  chan struct{}
	dropped bool
}

// pump forwards each src value into the proxy until p.done closes; a
// one-shot feed dies after its single delivery. A pump is retired only
// before the real timer is stopped or reset, so a fire it holds at that
// point predates the Stop/Reset and is dropped, as go1.23 discards it.
func (f *chanFeed) pump(p *feedPump) {
	defer close(p.exited)
	for {
		select {
		case v := <-f.src:
			if !f.once {
				// a repeating feed drops a tick nobody has collected
				// yet, like gc's ticker — the 1-buffer keeps only the
				// latest.
				select {
				case f.C <- v:
				default:
				}
				continue
			}
			select {
			case f.C <- v:
			case <-p.done:
				p.dropped = true
				return
			}
			f.dieIf(p)
			return
		case <-p.done:
			return
		}
	}
}

// start (re)arms the feed with a fresh pump: the managed channel reports
// alive again. Callers hold f.op (or own f exclusively).
func (f *chanFeed) start() {
	p := &feedPump{done: make(chan struct{}), exited: make(chan struct{})}
	f.mu.Lock()
	f.cur = p
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, false)
	}
	f.mu.Unlock()
	go f.pump(p)
}

// dieIf marks the feed dead after a one-shot delivery — but only while
// p is still the live pump; a Stop/Reset already owns the lifecycle.
func (f *chanFeed) dieIf(p *feedPump) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != p {
		return
	}
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
}

// retire stops the live pump and waits for it to exit, so no value read
// from src before the coming Stop/Reset can reach C afterwards. It
// reports whether the pump held an undelivered fire. Callers hold f.op;
// f.mu is released while waiting so the pump's dieIf cannot deadlock.
func (f *chanFeed) retire() bool {
	f.mu.Lock()
	p := f.cur
	f.cur = nil
	f.mu.Unlock()
	if p == nil {
		return false
	}
	close(p.done)
	<-p.exited
	return p.dropped
}

// drain discards a fire left in the proxy buffer, reporting whether one
// was there: go1.23 makes no pre-Stop/Reset value receivable afterwards.
func (f *chanFeed) drain() bool {
	select {
	case <-f.C:
		return true
	default:
		return false
	}
}

// stop retires the feed for good around the real stop: parked receivers
// join the asleep count. It reports whether a fire was still pending
// (the real timer's own stop result, a dropped fire, or a buffered one).
func (f *chanFeed) stop(realStop func() bool) bool {
	f.op.Lock()
	defer f.op.Unlock()
	dropped := f.retire()
	stopped := realStop()
	drained := f.drain()
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
	return stopped || dropped || drained
}

// reset retires the old pump, resets the real timer, discards any stale
// fire, and re-arms with a fresh pump. It reports whether a fire was
// still pending, like stop.
func (f *chanFeed) reset(realReset func() bool) bool {
	f.op.Lock()
	defer f.op.Unlock()
	dropped := f.retire()
	active := realReset()
	drained := f.drain()
	f.start()
	return active || dropped || drained
}

// Stop cancels the timer like time.Timer.Stop: the feed dies and parked
// receivers count asleep. As in go1.23, a fire not yet received counts
// as stopped (true) and is discarded.
func (b *timerChanBox) Stop() bool {
	return b.feed.stop(b.t.Stop)
}

// Reset reschedules the timer like time.Timer.Reset: a dead feed
// re-arms and a stale fire is discarded.
func (b *timerChanBox) Reset(d time.Duration) bool {
	return b.feed.reset(func() bool { return b.t.Reset(d) })
}

// Stop halts the ticker like time.Ticker.Stop: no more ticks can arrive,
// so the feed dies and parked receivers count asleep.
func (b *tickerChanBox) Stop() {
	b.feed.stop(func() bool { b.t.Stop(); return false })
}

// Reset restarts the ticker like time.Ticker.Reset: a stopped feed
// re-arms so parked receivers wake again.
//
// The interval is validated before the feed retires its pump: a
// recovered panic must leave the ticker running, as in gc.
func (b *tickerChanBox) Reset(d time.Duration) {
	if d <= 0 {
		panic("non-positive interval for Ticker.Reset")
	}
	b.feed.reset(func() bool { b.t.Reset(d); return false })
}

// hold takes a new note if none is held. Call with no lock held.
func (t *afterFuncTimer) hold() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.release == nil {
		t.release = t.note()
	}
}

// drop releases the held note, if any. The release funcs are
// once-guarded, but clearing the field also stops a later Stop from
// double-counting a callback that already fired.
func (t *afterFuncTimer) drop() {
	t.mu.Lock()
	r := t.release
	t.release = nil
	t.mu.Unlock()
	if r != nil {
		r()
	}
}

// Stop cancels the timer like time.Timer.Stop: a true return means the
// callback will never run, so its wait note is released.
func (t *afterFuncTimer) Stop() bool {
	if !t.Timer.Stop() {
		return false
	}
	t.drop()
	return true
}

// Reset reschedules the timer like time.Timer.Reset: a callback the
// timer may now fire must hold a wait note again.
func (t *afterFuncTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.hold()
	return active
}

// hostType is a TypeDef whose zero is a host value: `var m T` and `T{}`
// produce a boxed *newT() so selectMember sees the real method set.
func hostType(name string, new func() any) *runtime.TypeDef {
	return &runtime.TypeDef{Name: name, Kind: runtime.KindStruct, HostNew: new}
}

// hostSplitFunc binds one of bufio's stock split funcs: a script call
// returns the (advance, token, err) triple, and Target hands the host
// func itself to Scanner.Split.
func hostSplitFunc(name string, f bufio.SplitFunc) *runtime.BuiltinFunc {
	// the Target is the unnamed func type: gc's stock split funcs are
	// declared as plain funcs (%T spells the signature), and an unnamed
	// func still assigns to a SplitFunc parameter.
	var target func([]byte, bool) (int, []byte, error) = f
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		atEOF, _ := runtime.Unwrap(args[1]).(bool)
		adv, tok, err := f(byteSlice(goNative(args[0])), atEOF)
		return &runtime.Tuple{Elems: []runtime.Value{int64(adv), scriptVal(tok), errVal(err)}}, nil
	}}
}

func (s *scriptLocker) Lock() { s.call(s.lock) }

func (s *scriptLocker) Unlock() { s.call(s.unlock) }

func (s *scriptLocker) call(fn runtime.Value) {
	if _, err := s.vc.Call(fn, nil); err != nil {
		panic(err)
	}
}

func (b *atomicBox) Load() (old any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

func (b *atomicBox) Store(x any) {
	b.mu.Lock()
	b.v = x
	b.mu.Unlock()
}

func (b *atomicBox) Swap(x any) (old any) {
	b.mu.Lock()
	old, b.v = b.v, x
	b.mu.Unlock()
	return old
}

func (b *atomicBox) CompareAndSwap(old, new any) (swapped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !atomicCellEq(b.v, old) {
		return false
	}
	b.v = new
	return true
}

// hostHelpers builds BuiltinFuncs whose Fn marshals arguments to Go natives
// and results back to runtime values.
type hostHelpers struct {
	e *Engine // for the configured output writer
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

// out returns the engine's output writer (io.Discard when unset).
func (h *hostHelpers) out() io.Writer {
	if h.e == nil || h.e.out == nil {
		return io.Discard
	}
	return h.e.out
}

func (h *hostHelpers) fn(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.fnvc(name, func(_ runtime.VMCaller, a []any) (any, error) { return f(a) }, target...)
}

// fnvc is fn for bindings that need the caller (e.g. deadlock-wait
// bookkeeping through the channel a host call hands to the script).
func (h *hostHelpers) fnvc(name string, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = goNative(v)
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
// error keeps its identity through the host (goNative would format it).
func (h *hostHelpers) fnvcRaw(name string, arity int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != arity {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, arity, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = goNative(v)
		}
		r, err := f(vc, a, args)
		if err != nil {
			return nil, err
		}
		return scriptVal(r), nil
	}}
}

// fn1/fn2/fn3 are arity-checked variants.
func (h *hostHelpers) fn1(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 1, f, target...)
}

func (h *hostHelpers) fn2(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 2, f, target...)
}

func (h *hostHelpers) fn3(name string, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return h.arity(name, 3, f, target...)
}

func (h *hostHelpers) arity(name string, n int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < n {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, n, len(args))
		}
		a := make([]any, len(args))
		for i, v := range args {
			a[i] = goNative(v)
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
func (h *hostHelpers) scanFn(name string, heads int, headFn func(runtime.VMCaller, []runtime.Value) ([]any, error), target any, call func(head, ptrs []any) (int, error)) *runtime.BuiltinFunc {
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
			head = append(head, goNative(args[i]))
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
			t := reflect.TypeOf(goNative(cur))
			if t == nil {
				return retErr(0, fmt.Errorf("%s: cannot infer scan target type: argument %d", name, i+heads+1))
			}
			rv := reflect.New(t)
			// Seed with the current pointee: gc leaves args the scan
			// never reached (or failed to store) untouched.
			rv.Elem().Set(reflect.ValueOf(goNative(cur)))
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
func (h *hostHelpers) scanReader(v runtime.VMCaller, args []runtime.Value) ([]any, error) {
	r, err := h.asReaderVM(v, args[0])
	if err != nil {
		return nil, err
	}
	return []any{r}, nil
}

// bytealgMaxLen mirrors the host build's arch-tuned init (internal/bytealg
// index_<arch>.go). amd64 is 63 with AVX2 else 31; without reading CPUID we
// take the conservative 31 — the bound only gates when IndexString may be
// used, and a smaller bound just sends longer needles to the generic path.
// arm64/ppc64x 32, s390x/loong64 64.
func bytealgMaxLen() int {
	switch goruntime.GOARCH {
	case "amd64":
		return 31
	case "arm64", "ppc64", "ppc64le":
		return 32
	case "s390x", "loong64":
		return 64
	}
	return 0
}

// bytealgMaxBruteForce mirrors the host build's arch-tuned constant
// (internal/bytealg/index_<arch>.go): amd64 and s390x brute-force up to
// 64, arm64/ppc64x/loong64 up to 16, generic 0.
func bytealgMaxBruteForce() int {
	switch goruntime.GOARCH {
	case "amd64", "s390x":
		return 64
	case "arm64", "ppc64", "ppc64le", "loong64":
		return 16
	}
	return 0
}

// sortInPlace sorts a *runtime.Slice's elements directly — going through
// goNative would sort a fresh copy and leave the script's slice untouched.
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
		return argFloat(n)
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
	if ifc, ok := goNative(args[0]).(sort.Interface); ok {
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

func (s *scriptSortable) Len() int {
	r, err := s.vc.Call(s.lenFn, nil)
	if err != nil {
		panic(err)
	}
	return int(int64Of(r))
}

func (s *scriptSortable) Less(i, j int) bool {
	r, err := s.vc.Call(s.lessFn, []runtime.Value{int64(i), int64(j)})
	if err != nil {
		panic(err)
	}
	b, _ := r.(bool)
	return b
}

func (s *scriptSortable) Swap(i, j int) {
	if _, err := s.vc.Call(s.swapFn, []runtime.Value{int64(i), int64(j)}); err != nil {
		panic(err)
	}
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
		r := callOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
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
			r := callOrPanic(v, cmp, []runtime.Value{a, b})
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
	return &runtime.Tuple{Elems: []runtime.Value{int64(n), errVal(err)}}, nil
}

func retErr2[T any](v T, err error) (any, error) {
	return &runtime.Tuple{Elems: []runtime.Value{scriptVal(v), errVal(err)}}, nil
}

func errVal(err error) runtime.Value {
	if err == nil {
		return runtime.NIL
	}
	if sb, ok := err.(runtime.ScriptBoxed); ok {
		// a script error that went through host code (a %w cause, a
		// cancel cause, a callback's result) reads back as itself.
		return sb.ScriptValue()
	}
	return &runtime.GoValue{V: err} // boxed: method calls (Error(), Unwrap()) dispatch via reflection
}

// namedSized boxes a host-sized int with its declared typedef so %T
// spells int8/int16/int32/int64/uintN like Go (rune is int32).
func namedSized(x any, v int64) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(fmt.Sprintf("%T", x)), v)
}

// scriptVal converts a Go-native result back to a runtime value. Concrete
// runtime types pass through; anything else (errors, host structs) is boxed
// as a GoValue — Value is `any`, so it cannot be a type-switch case itself.
func scriptVal(v any) runtime.Value {
	switch x := v.(type) {
	case nil:
		return runtime.NIL
	case bool, string, float64:
		return x
	case int64:
		// int64 is a named type in Go — distinct from int at assert time —
		// so host int64 results ride in the box like the other sized ints.
		return namedSized(x, x)
	case int:
		return int64(x)
	case int8, int16, int32:
		return namedSized(x, reflect.ValueOf(x).Int())
	case uint, uint8, uint16, uint32, uint64, uintptr:
		// int64(x) keeps the full bit pattern — a >=2^63 uint64 like
		// math.Float64bits(-0.0) reinterprets losslessly.
		return namedSized(x, int64(reflect.ValueOf(x).Uint()))
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(runtime.BasicTypedef("float32"), float64(x))
	case []byte:
		if x == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("byte")}
		}
		// a []byte result unmarshals to byte-tagged elements so
		// `string(b)` and indexing behave like Go source suggests, and
		// elements compare like a []byte{...} literal's.
		el := make([]runtime.Value, len(x))
		for i, b := range x {
			el[i] = runtime.ByteValue(b)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []string:
		return strsSlice(x)
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return x
	case []any:
		if x == nil {
			return &runtime.TypedNil{Typ: anonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(x))
		for i, e := range x {
			el[i] = scriptVal(e)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("any")}
	case map[any]any:
		if x == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := runtime.NewMap(nil, true)
		for k, vv := range x {
			m.Insert(scriptVal(k), scriptVal(vv))
		}
		return m
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.Named, *runtime.TypedNil:
		return x
	default:
		// a nil *T of a named host type (os.Open's file on error), or a
		// nil chan/func of a predeclared spelling, is a typed nil, as
		// goValueOf reads it: equal to nil, typed once stored in an
		// interface. A box would make `f == nil` false.
		if rv := reflect.ValueOf(x); (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Chan || rv.Kind() == reflect.Func || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
			if tn := vm.HostNil(rv.Type()); tn != nil {
				return tn
			}
		}
		return &runtime.GoValue{V: x}
	}
}

// goNative converts a runtime value to its Go-native counterpart for host
// calls: cells unwrap, slices/maps become []any / map[any]any, structs get a
// Stringer view so fmt prints them sensibly.
// float32Tag reports whether a Named tag's underlying type is float32 —
// the one builtin where a float64 box and the declared width disagree in
// formatting. `type F32 float32` tags count too (Anon names the builtin).
func float32Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "float32"
}

// complex64Tag mirrors float32Tag for complex64 — the payload rides the
// float64/complex128 domain but formats at complex64 width per part.
func complex64Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "complex64"
}

// complex64Value reads a payload as complex64 — either a complex128 box
// or the constant domain's complex literal.
func complex64Value(v runtime.Value) (complex64, bool) {
	switch pv := v.(type) {
	case complex128:
		return complex64(pv), true
	case *runtime.UConst:
		if nv, err := uconstNative(pv); err == nil {
			if cv, ok := nv.(complex128); ok {
				return complex64(cv), true
			}
		}
	}
	return 0, false
}

// uconstNative materializes an untyped constant to its host default —
// the same rule the VM applies when the constant crosses a value
// boundary. An overflowing constant reports like Go's compile error.
func uconstNative(u *runtime.UConst) (any, error) {
	return runtime.UConstNative(u)
}

// mustNativeConst is uconstNative for a builtin argument position that
// cannot report the error: it panics with Go's compile-error wording.
func mustNativeConst(u *runtime.UConst) any {
	nv, err := uconstNative(u)
	if err != nil {
		panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: err.Error()}})
	}
	return nv
}

func goNative(v runtime.Value) any {
	return goNativeWalk(v, runtime.NewWalk())
}

// goNativeWalk is goNative's worker. The Slice/Map arms run through
// the shared runtime.Walk — which, besides the dedupe, gives the walk
// cycle folding (a container holding itself through an any slot used
// to recurse until the stack overflowed), the virtual-slice length
// (a make([]struct{}, n) used to arrive empty), and comparable keys
// (a script array key used to panic "hash of unhashable type").
func goNativeWalk(v runtime.Value, w *runtime.Walk) any {
	if n, ok := v.(*runtime.Named); ok {
		// fast path for the common int-tagged element (every byte of a
		// []byte argument): only a host-scalar tag changes an int64
		// payload — the float32/complex64 narrowing never applies.
		if iv, ok := n.V.(int64); ok && (n.Typ == nil || n.Typ.HostScalar == nil) {
			return iv
		}
	}
	if out, ok := w.Container(v, func(e runtime.Value) any { return goNativeWalk(e, w) }, runtime.UnwrapKey); ok {
		return out
	}
	switch x := v.(type) {
	case runtime.Nil:
		return nil
	case *runtime.IfaceNil:
		// a nil interface value crosses to the host as nil (a typed
		// nil stays a TypedNil — the host has no box for it).
		return nil
	case *runtime.UConst:
		return mustNativeConst(x)
	case *runtime.Named:
		// a float32-tagged value crosses to fmt as a real float32 so
		// %v applies float32 formatting (0.1, not 0.10000000149011612);
		// %T is rewritten to scriptTypeString before this runs.
		if float32Tag(x.Typ) {
			if fv, ok := x.V.(float64); ok {
				return float32(fv)
			}
			// the payload may still ride the constant domain — a
			// `float32(c)` conversion keeps UConst so the value stays
			// arbitrary-precision; read it at float32 width the same
			// way the materialized float64 path above does.
			if uc, ok := x.V.(*runtime.UConst); ok {
				if nv, err := uconstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return float32(fv)
					}
				}
			}
		}
		// a complex64-tagged value narrows each part to float32 — the
		// constant domain's widened digits must not reach the host.
		if complex64Tag(x.Typ) {
			if cv, ok := complex64Value(x.V); ok {
				return cv
			}
		}
		// a bound host scalar crossing to a host `any` parameter
		// materializes to the host type — a host call sees a real
		// time.Month, not the int64 or UConst the script shape keeps.
		if x.Typ != nil && x.Typ.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(x.Typ, x.V); ok {
				return hv
			}
		}
		return goNativeWalk(x.V, w)
	case *runtime.Cell:
		return goNativeWalk(x.Elem, w)
	case *runtime.Struct:
		return fmt.Sprintf("%s%+v", x.Def.Name, x.Fields)
	case *runtime.GoValue:
		return x.V
	default:
		return v
	}
}

func str(v any) string {
	if n, ok := v.(*runtime.Named); ok {
		return str(n.V)
	}
	if u, ok := v.(*runtime.UConst); ok {
		// a builtin argument position materializes an untyped
		// constant at its default type — the same fold intOf does.
		return str(mustNativeConst(u))
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intOf(v any) int {
	switch x := v.(type) {
	case *runtime.Named:
		return intOf(x.V)
	case *runtime.UConst:
		// a named constant stays a UConst through declaration storage;
		// a builtin argument position materializes it at its default
		// type — Go folds these calls at compile time.
		return intOf(mustNativeConst(x))
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	case uint64:
		return int(x)
	case uint, uint8, uint16, uint32, uintptr:
		return int(reflect.ValueOf(x).Uint())
	case time.Duration:
		return int(x)
	case *runtime.GoValue:
		return intOf(x.V)
	}
	// a defined numeric type the switch above missed (e.g. fs.FileMode
	// from a host call's result) still carries its integer value — Go
	// would fold passing it to an int-typed parameter at compile time.
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(rv.Uint())
	}
	return 0
}

func int64Of(v any) int64 { return int64(intOf(v)) }

func durOf(v any) time.Duration { return time.Duration(int64Of(v)) }

// borrowBytes marshals a script value to []byte for a host call that
// writes into the buffer: the returned commit writes the bytes back
// into the script slice's elements. It must see the arg before goNative
// flattens it — an []any has lost the script slice it came from. Values
// already sharing their backing (a host []byte inside a GoValue) need
// no commit; read-only consumers keep using byteSlice.
func borrowBytes(v runtime.Value) ([]byte, func()) {
	u := v
	for {
		if n, ok := u.(*runtime.Named); ok {
			u = n.V
			continue
		}
		if dv, ok := runtime.Deref(u); ok {
			u = dv
			continue
		}
		break
	}
	switch x := u.(type) {
	case *runtime.Slice:
		bs := byteSlice(x)
		return bs, func() {
			for i := range bs {
				x.Elems[i] = runtime.ByteValue(bs[i])
			}
		}
	case *runtime.GoValue:
		if bs, ok := x.V.([]byte); ok {
			// shared backing — host writes are already visible
			return bs, func() {}
		}
	}
	return byteSlice(v), func() {}
}

// unsafeElems resolves unsafe.String/Slice's (ptr, len) to the n
// elements starting at an &s[i] ref, sharing the slice's backing.
// sliceTypFromPtr maps a pointer/array/slice typedef to the unnamed
// []E typedef unsafe.Slice's result carries — never the named slice or
// the [N]E array the pointer was taken from.
func sliceTypFromPtr(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	switch x := typedefAst(td).(type) {
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.X}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: x.Elt}, Elem: td.Elem, Pkg: td.Pkg, File: td.File}
	}
	return td
}

// byteSlice unmarshals a script value to []byte for os.WriteFile & co:
// accepts strings, []byte natives and int64 element slices.
func byteSlice(v any) []byte {
	if n, ok := v.(*runtime.Named); ok {
		return byteSlice(n.V)
	}
	switch x := v.(type) {
	case string:
		return []byte(x)
	case []byte:
		return x
	case []any:
		out := make([]byte, len(x))
		for i, e := range x {
			out[i] = byte(int64Of(e))
		}
		return out
	case *runtime.Slice:
		out := make([]byte, len(x.Elems))
		for i, e := range x.Elems {
			// byte elements are Named{byte, int64} or raw int64 — read
			// them directly instead of a goNative walk per element.
			if n, ok := e.(*runtime.Named); ok && (n.Typ == nil || n.Typ.HostScalar == nil) {
				e = n.V
			}
			if iv, ok := e.(int64); ok {
				out[i] = byte(iv)
				continue
			}
			out[i] = byte(int64Of(goNative(e)))
		}
		return out
	}
	return nil
}

func strSlice(v any) []string {
	if n, ok := v.(*runtime.Named); ok {
		return strSlice(n.V)
	}
	if s, ok := v.(*runtime.Slice); ok {
		out := make([]string, len(s.Elems))
		for i, e := range s.Elems {
			out[i] = str(goNative(e))
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

// sliceOf unwraps a script slice (cell/pointer derefed, Named-peeled,
// or bare); typed nils and non-slices report not-ok so callers can give
// their own error.
func sliceOf(v runtime.Value) (*runtime.Slice, bool) {
	if d, ok := runtime.Deref(v); ok {
		v = d
	}
	s, ok := runtime.Unwrap(v).(*runtime.Slice)
	return s, ok
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
func seqElems(vc runtime.VMCaller, v runtime.Value) ([]runtime.Value, error) {
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
func scriptSliceArg(vc runtime.VMCaller, v runtime.Value) ([]runtime.Value, *runtime.TypeDef, bool) {
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
func sliceIndexScript(vc runtime.VMCaller, name string, args []runtime.Value) (int, error) {
	if len(args) != 2 {
		return 0, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
	}
	el, et, ok := scriptSliceArg(vc, args[0])
	if !ok {
		return slices.Index(anySlice(goNative(args[0])), goNative(args[1])), nil
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
			out[i] = goNative(e)
		}
		return out
	}
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func strsSlice(ss []string) runtime.Value {
	if ss == nil {
		// a nil []string result stays nil — SplitN(s, sep, 0) == nil.
		return &runtime.TypedNil{Typ: anonSliceTyp("string")}
	}
	el := make([]runtime.Value, len(ss))
	for i, s := range ss {
		el[i] = s
	}
	return &runtime.Slice{Elems: el, Typ: anonSliceTyp("string")}
}

// anonSliceTyp builds the anonymous []name typedef used to tag slices
// unboxed from host values (no package context — the name is a builtin).
func anonSliceTyp(name string) *runtime.TypeDef {
	return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent(name)}}
}

// runeSlice lifts a []rune result into a script slice of rune-tagged
// int64s — the same element representation a []rune{...} literal makes.
func runeSlice(rs []rune) any {
	if rs == nil {
		return &runtime.TypedNil{Typ: anonSliceTyp("rune")}
	}
	out := make([]runtime.Value, len(rs))
	for i, r := range rs {
		out[i] = runtime.Tag(runtime.BasicTypedef("rune"), int64(r))
	}
	return &runtime.Slice{Elems: out, Typ: anonSliceTyp("rune")}
}

func floatOf(v any) float64 {
	switch x := v.(type) {
	case *runtime.Named:
		return floatOf(x.V)
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
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
func callOrPanic(v runtime.VMCaller, fn runtime.Value, args []runtime.Value) runtime.Value {
	r, err := v.Call(fn, args)
	if err != nil {
		panic(err)
	}
	return r
}

// walkCallback runs a filepath.Walk/WalkDir script callback for host
// path p: the walk ran from ap, the engine's resolution of the script's
// root, so p maps back onto root as written (gc hands the root over
// verbatim — "./a", "a/" — and joins descendants onto it), entry is the
// boxed fs.DirEntry/fs.FileInfo (NIL when the host passed none), and
// the result goes back through hostErrOf so the host walker compares
// fs.SkipDir/SkipAll by identity and a script error keeps its identity
// on the way out (errVal).
func walkCallback(v runtime.VMCaller, cb runtime.Value, root, ap, p string, entry runtime.Value, werr error) error {
	sp := scriptPathOf(root, ap, p)
	r := callOrPanic(v, cb, []runtime.Value{sp, entry, errVal(scriptPathErr(werr, root, ap))})
	return hostErrOf(v, r)
}

// hostSpelling anchors a script path at the virtual cwd WITHOUT
// cleaning it, so a host lstat names the result like gc's lstat of the
// spelled path (basename "." for ".", ".." for "a/b/..").
func (e *Engine) hostSpelling(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return e.cwd + string(filepath.Separator) + p
}

// hostBoxOf unwraps a host struct value (`var sb strings.Builder`,
// `&strings.Builder{}`), which rides as Named{HostNew typedef, GoValue},
// to its GoValue box, so io helpers hand the real host value to the host
// call instead of adapting it through the script-callback proxy — the
// proxy converts every chunk to a script []byte and back.
func hostBoxOf(orig, v any) any {
	// a pointer converted to a script-defined type, (*MyB)(&sb), keeps
	// that type's method set: its Write must not be bypassed.
	if t, ok := orig.(*runtime.Named); ok && (t.Typ == nil || t.Typ.HostNew == nil) {
		return v
	}
	if n, ok := v.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostNew != nil {
		if g, ok := n.V.(*runtime.GoValue); ok {
			return g
		}
	}
	return v
}

func (s *scriptReader) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := scriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	if err != nil {
		// a panic inside the script Read propagates as a panic like
		// callOrPanic — io.Copy's caller must not see "panic: boom"
		// as an error return.
		panic(err)
	}
	n, err2 := readResult(s.v, r)
	copyBackBytes(p, pv, n)
	return n, err2
}

func (s *scriptReaderWriterTo) Read(p []byte) (int, error) {
	m, ok := s.v.Member(s.recv, "Read")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	pv := scriptVal(p)
	r, err := s.v.Call(m, []runtime.Value{pv})
	if err != nil {
		panic(err)
	}
	n, err2 := readResult(s.v, r)
	copyBackBytes(p, pv, n)
	return n, err2
}

func (s *scriptReaderWriterTo) WriteTo(w io.Writer) (int64, error) {
	m, ok := s.v.Member(s.recv, "WriteTo")
	if !ok || m == nil || m == runtime.NIL {
		return 0, fmt.Errorf("runtime error: invalid memory address or nil pointer dereference")
	}
	r, err := s.v.Call(m, []runtime.Value{&runtime.GoValue{V: w}})
	if err != nil {
		panic(err)
	}
	return readResult64(s.v, r)
}

func (s *scriptWriter) Write(p []byte) (int, error) {
	r, err := s.v.Call(s.write, []runtime.Value{scriptVal(p)})
	if err != nil {
		panic(err)
	}
	switch t := r.(type) {
	case *runtime.Tuple:
		var n int
		if len(t.Elems) > 0 {
			if i, ok := runtime.Unwrap(t.Elems[0]).(int64); ok {
				n = int(i)
			}
		}
		if len(t.Elems) > 1 {
			return n, hostErrOf(s.v, t.Elems[1])
		}
		return n, nil
	default:
		if i, ok := runtime.Unwrap(r).(int64); ok {
			return int(i), nil
		}
		return 0, nil
	}
}

func (j jsonCycle) MarshalJSON() ([]byte, error) {
	return nil, &json.UnsupportedValueError{Str: jsonCyclePrefix + j.via}
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range o.keys {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(o.vals[i])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
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

func (u jsonUnsupported) MarshalJSON() ([]byte, error) { return nil, u.e }

func (e *unsupportedFunc) Error() string { return "json: unsupported type: " + e.name }

func (e *jsonScriptErr) Error() string { return "json: script marshaler error" }

func (e *jsonScriptErr) MarshalJSON() ([]byte, error) {
	return nil, e
}

// ScriptMethod routes Encode around the reflect call (runtime.ScriptMethoder).
func (e *jsonEncoder) ScriptMethod(name string) (func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), bool) {
	if name != "Encode" {
		return nil, false
	}
	return func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("json.Encoder.Encode needs 1 arg, got %d", len(args))
		}
		return errVal(e.encode(v, args[0])), nil
	}, true
}

func (e *jsonEncoder) encode(c runtime.VMCaller, v runtime.Value) error {
	err := e.enc.Encode(goJSON(c, v))
	err = jsonUnsupportedOf(err)
	var se *jsonScriptErr
	if errors.As(err, &se) {
		return jsonMarshalerErr(c, se)
	}
	return err
}

// Encode serves host callers holding the boxed encoder (a bound host
// func taking `interface{ Encode(any) error }`); script calls take
// ScriptMethod's raw-arg path. A script value reaches here already
// projected to its host shape, so jsonHostArg recovers the original
// before goJSON walks it.
func (e *jsonEncoder) Encode(v any) error {
	return e.encode(e.c, jsonHostArg(v))
}

func (e *jsonEncoder) SetIndent(prefix, indent string) {
	e.enc.SetIndent(prefix, indent)
}

func (e *jsonEncoder) SetEscapeHTML(on bool) {
	e.enc.SetEscapeHTML(on)
}

func (cleanupHandle) Stop() {}

func (e *jsonMarshalTextErr) Error() string {
	return "json: error calling MarshalText for type " + e.me.Type.String() + ": " + e.me.Err.Error()
}

func (e *jsonMarshalTextErr) Unwrap() error { return e.me }

func (t scriptReflectType) String() string { return t.name }

// fail records the first type-mismatch error the way saveError does:
// later failures are ignored while decoding continues.
func (x *jsonErrCtx) fail(value string, td *runtime.TypeDef, cause error) {
	err := &jsonUnmarshalTypeError{
		value: value,
		strct: x.strct,
		field: strings.Join(x.path, "."),
		typ:   runtime.DisplayName(td),
		cause: cause,
	}
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
	if x.err == nil {
		x.err = err
	}
}

// failErr records a ready-made error verbatim — for failures whose
// text is not an UnmarshalTypeError (e.g. the inner-literal syntax
// error of a `,string` decode into an interface field).
func (x *jsonErrCtx) failErr(err error) {
	x.errs = append(x.errs, jsonErrAt{err: err, path: append([]string{}, x.path...)})
	if x.err == nil {
		x.err = err
	}
}

func (e *jsonUnmarshalTypeError) Error() string {
	var s string
	// gc names a struct field only when the failure sits INSIDE one —
	// a root-level mismatch reports "into Go value" even when the
	// target is a struct.
	if e.field != "" {
		intoWhat := "Go struct field "
		i := strings.LastIndexByte(e.field, '.') + 1
		if len(e.field[i:]) > 0 && strings.TrimRight(e.field[i:], "0123456789") == "" {
			intoWhat = "" // likely a Go slice or array
		}
		s = "json: cannot unmarshal " + e.value + " into " + intoWhat + e.strct + "." + e.field + " of type " + e.typ
	} else {
		s = "json: cannot unmarshal " + e.value + " into Go value of type " + e.typ
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
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
func sliceExtreme(vc runtime.VMCaller, name string, args []runtime.Value, better func(best, x runtime.Value) bool) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
	}
	el, _, ok := scriptSliceArg(vc, args[0])
	if !ok {
		el = scriptElems(goNative(args[0]))
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
func elemOperands(vc runtime.VMCaller, name string, typ *runtime.TypeDef, vals []runtime.Value) ([]runtime.Value, error) {
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

// callBool runs a script predicate callback for its bool result.
func callBool(vc runtime.VMCaller, fn runtime.Value, args ...runtime.Value) bool {
	b, _ := runtime.Unwrap(callOrPanic(vc, fn, args)).(bool)
	return b
}

// callInt runs a script cmp-style callback for its int result.
func callInt(vc runtime.VMCaller, fn runtime.Value, args ...runtime.Value) int64 {
	return int64Of(callOrPanic(vc, fn, args))
}

// ScriptValue is the script-visible error the box carries: a typed nil
// (`var err error = (*E)(nil)`) reads back boxed in its interface so it
// stays non-nil.
func (e *scriptError) ScriptValue() runtime.Value {
	if tn, ok := e.v.(*runtime.TypedNil); ok {
		return &runtime.IfaceNil{Typ: tn.Typ}
	}
	return e.v
}

func (e *scriptError) Error() string {
	s, ok := runtime.IfaceCallString(e.c, e.v, "Error")
	if !ok {
		return fmt.Sprintf("%v", goNative(e.v))
	}
	return s
}

// Unwrap lets a script-declared `Unwrap() error` method join the host
// errors chain — errors.Unwrap/Is/As walk through it like Go's. A
// script `Unwrap() []error` reports no single cause — like
// errors.Join, the type has no Unwrap() error.
func (e *scriptError) Unwrap() error {
	kids := e.errKids()
	if len(kids) != 1 {
		return nil
	}
	return kids[0]
}

// Is lets a script-declared `Is(error) bool` method join host
// errors.Is — the error's own equality override like Go's custom-Is
// rule. The host error target maps back to its script value the way
// hostErrOf boxed it, so the script side compares what it sees.
func (e *scriptError) Is(target error) bool {
	m, ok := runtime.IfaceMember(e.c, e.v, "Is")
	if !ok {
		return false
	}
	r, err := e.c.Call(m, []runtime.Value{errVal(target)})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// As lets a script-declared `As(any) bool` method join host
// errors.As/AsType — the error's own As assigns the target like Go's
// custom-As rule. A non-pointer target has nothing to write to and
// simply fails the script side's assert.
func (e *scriptError) As(target any) bool {
	m, ok := runtime.IfaceMember(e.c, e.v, "As")
	if !ok {
		return false
	}
	r, err := e.c.Call(m, []runtime.Value{target})
	if err != nil {
		return false
	}
	b, _ := r.(bool)
	return b
}

// Timeout lets a script-declared `Timeout() bool` method answer host
// checks like os.IsTimeout; without one the error is not a timeout.
func (e *scriptError) Timeout() bool {
	m, ok := runtime.IfaceMember(e.c, e.v, "Timeout")
	if !ok {
		return false
	}
	r, err := e.c.Call(m, nil)
	if err != nil {
		return false
	}
	b, _ := runtime.Unwrap(r).(bool)
	return b
}

func (e *wrapError) Error() string { return e.msg }

func (e *wrapError) Unwrap() error { return e.err }

func (e *wrapErrors) Error() string { return e.msg }

func (e *wrapErrors) Unwrap() []error { return e.errs }

// structField reads a script struct's field by name (NIL when absent).
func structField(s *runtime.Struct, name string) runtime.Value {
	for i, f := range s.Def.Fields {
		if f == name && i < len(s.Fields) {
			return s.Fields[i]
		}
	}
	return runtime.NIL
}

// cellElemTyp reads the declared type held by a cell (the target of
// errors.As's &x): its TypedNil/IfaceNil/Struct tag.
func cellElemTyp(v runtime.Value) *runtime.TypeDef {
	c, ok := v.(*runtime.Cell)
	if !ok {
		return nil
	}
	switch e := c.Elem.(type) {
	case *runtime.Cell:
		// the variable's VALUE is itself a pointer (a cell): `&p` where
		// p is declared `*T` — the assignable target type is T.
		if e.Typ != nil {
			return e.Typ
		}
		if s, ok := e.Elem.(*runtime.Struct); ok {
			return s.Def
		}
		return nil
	case *runtime.Slice, *runtime.Map, *runtime.Chan:
		// a container-typed target carries no usable element typedef for
		// As — nil keeps the matcher permissive (a nil want accepts
		// anything).
		return nil
	}
	return valueDeclTyp(c.Elem)
}

func (s *fmtValue) Format(f fmt.State, verb rune) {
	io.WriteString(f, s.render(verb, f))
}

func (zeroState) Write(b []byte) (int, error) { return len(b), nil }

func (zeroState) Width() (int, bool) { return 0, false }

func (zeroState) Precision() (int, bool) { return 0, false }

func (zeroState) Flag(int) bool { return false }

func (s *captureState) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *captureState) Width() (int, bool) { return s.f.Width() }

func (s *captureState) Precision() (int, bool) { return s.f.Precision() }

func (s *captureState) Flag(ch int) bool { return s.f.Flag(ch) }

func (s *fmtValue) render(verb rune, f fmt.State) string {
	// fmt consults Formatter before the Stringer family, for every verb
	// but %T/%p — a script Formatter writes to the live state itself, so
	// a successful call already emitted its output. zeroState is the
	// flag-less stand-in for nested renderings (bad-verb markers, %p)
	// where Go invokes no Formatter; nothing real writes to it.
	if s.c != nil && verb != 'T' && verb != 'p' {
		if cs, isCap := f.(*captureState); isCap {
			wrote, pan := callFormatReport(s.c, s.x, cs, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return cs.buf.String()
			}
		} else if _, isZero := f.(zeroState); !isZero {
			wrote, pan := callFormatReport(s.c, s.x, f, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return ""
			}
		}
	}
	// Stringer family first, like fmt does.
	if s.c != nil {
		switch verb {
		case 'v':
			if f.Flag('#') {
				// %#v consults GoString, not String/Error.
				if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "GoString"); pan != nil {
					return fmtPanicMarker(verb, "GoString", pan)
				} else if ok {
					return str
				}
				break
			}
			// Go's fmt consults error before Stringer — a value
			// implementing both prints its Error().
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 's':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 'q':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return strconv.Quote(str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return strconv.Quote(str)
			}
		case 'x', 'X':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
		}
	}
	return s.renderValue(s.x, verb, f)
}

func (s *fmtValue) renderValue(x runtime.Value, verb rune, f fmt.State) string {
	// Go's fmt has no render-depth cap — a deeply nested composite
	// prints every level (issue29264), and a cyclic one overflows the
	// host stack just as it does under go run.
	switch v := x.(type) {
	case *runtime.Named:
		// %T keeps the declared name; other verbs render through.
		if verb == 'T' {
			if runtime.IfaceTaggedNil(v.V) != nil {
				return "<nil>" // a nil interface has no dynamic type
			}
			return typedefSpelling(v.Typ)
		}
		// a bound host scalar (time.Month/time.Duration) whose payload is
		// still script-shaped — a stored int64 or a const-domain UConst —
		// renders through the host scalar so String() and every verb
		// apply like gc (%v → "January", %d → 1).
		if td := v.Typ; td != nil && td.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(td, v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), hv)
			}
		}
		// an int64 carrying a uint64/uintptr tag must format its bits as
		// unsigned — host fmt would read the int64 as signed otherwise.
		if unsignedIntTyp(v.Typ) {
			if iv, ok := v.V.(int64); ok {
				return fmt.Sprintf(formatOf(f, verb), uint64(iv))
			}
		}
		// a float64 carrying a float32 tag formats in float32 — the
		// rounded value, not the wider box's digits. A payload still
		// riding the constant domain reads at float32 width the same
		// way (`float32(c)` conversions keep UConst).
		if float32Tag(v.Typ) {
			if fv, ok := v.V.(float64); ok {
				return fmt.Sprintf(formatOf(f, verb), float32(fv))
			}
			if uc, ok := v.V.(*runtime.UConst); ok {
				if nv, err := uconstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return fmt.Sprintf(formatOf(f, verb), float32(fv))
					}
				}
			}
		}
		// a complex128 carrying a complex64 tag rounds each half to
		// float32 — same gap as float32, one level wider.
		if complex64Tag(v.Typ) {
			if cv, ok := complex64Value(v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), cv)
			}
		}
		return (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: v.V, et: v.Typ, depth: s.depth + 1}).render(verb, f)
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(v)
		if !ok {
			return "<nil>"
		}
		// Go's printPtr descends one level only when the pointee is a
		// composite — struct, array, slice, or map — spelling `&[...]`/
		// `&{...}`/`&map[...]`; every other pointee (scalars, other
		// pointers, chans, funcs) reads as the address. The descent only
		// happens at the top level: a pointer nested inside a composite
		// prints 0x... — []*S{p} renders [0xADDR], and under %#v
		// [(*S)(0xADDR)].
		composite := func(x runtime.Value) bool {
			switch t := x.(type) {
			case *runtime.Struct, *runtime.Slice, *runtime.Map:
				return true
			case *runtime.TypedNil:
				// &ns for a nil slice/map spells &[]/&map[]
				return t.Typ != nil &&
					(t.Typ.Kind == runtime.KindSlice || t.Typ.Kind == runtime.KindMap)
			}
			return false
		}
		if s.depth == 0 && composite(dv) {
			return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: dv, depth: s.depth + 1}).render(verb, nested(f))
		}
		if s.depth == 0 {
			if n, isNamed := dv.(*runtime.Named); isNamed && composite(n.V) {
				return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: n.V, depth: s.depth + 1}).render(verb, nested(f))
			}
		}
		// scalar pointer: Go prints the address — a host pointer repr
		// is the closest readable stand-in; %#v wraps it as (*T)(0xADDR).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf("(*%s)(%p)", scriptTypeString(dv), v)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Struct:
		switch verb {
		case 'T':
			return typedefSpelling(v.Def)
		case 'q':
			return strconv.Quote(s.renderValue(x, 'v', f))
		}
		parts := make([]string, len(v.Fields))
		for i, e := range v.Fields {
			fv := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: fieldTypOf(v.Def, i), depth: s.depth + 1}).render(elemVerb(verb), nested(f))
			if f.Flag('+') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			if f.Flag('#') && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			parts[i] = fv
		}
		sep := " "
		if f.Flag('#') {
			sep = ", "
		}
		body := strings.Join(parts, sep)
		if f.Flag('#') {
			return typedefSpelling(v.Def) + "{" + body + "}"
		}
		return "{" + body + "}"
	case *runtime.Slice:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'p':
			if isArrayTyp(v.Typ) {
				return badVerb(verb, typedefSpelling(v.Typ), s.renderList(v, 'v', f))
			}
			return fmt.Sprintf("%p", v)
		case 's', 'q', 'x', 'X':
			// a byte-wise slice formats as text like Go's fmt does
			if bs, ok := sliceBytes(v); ok {
				switch verb {
				case 's':
					return string(bs)
				case 'q':
					return strconv.Quote(string(bs))
				default:
					return fmt.Sprintf(formatOf(f, verb), bs)
				}
			}
		case 'v':
			// %#v renders a byte slice as []byte{0xNN, ...} — the byte
			// spelling applies only at the top level (reflect cannot
			// distinguish byte from uint8 below it).
			if f.Flag('#') && byteSliceTyp(v.Typ) {
				if bs, ok := sliceBytes(v); ok {
					spell := typedefSpelling(v.Typ)
					if v.Typ.Name == "" && s.depth == 0 && !isArrayTyp(v.Typ) {
						spell = "[]byte"
					}
					parts := make([]string, len(bs))
					for i, b := range bs {
						parts[i] = fmt.Sprintf("0x%x", b)
					}
					return spell + "{" + strings.Join(parts, ", ") + "}"
				}
			}
		}
		return s.renderList(v, verb, f)
	case *runtime.Map:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		// Go's fmt prints maps in sorted-key order (fmtsort), not
		// insertion order — sort a copy of Order the same way.
		order := append([]runtime.Value{}, v.Order...)
		sort.SliceStable(order, func(i, j int) bool {
			a, b := order[i], order[j]
			if lessScript(a, b) {
				return true
			}
			if lessScript(b, a) {
				return false
			}
			// unordered kinds (bool, composites, mixed): order by the
			// rendered key like fmtsort's fallback.
			ka := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: a, depth: s.depth + 1}).render('v', nested(f))
			kb := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: b, depth: s.depth + 1}).render('v', nested(f))
			return ka < kb
		})
		kt, vt := mapElemTyps(v.Typ)
		ev := elemVerb(verb)
		parts := make([]string, 0, len(order))
		for _, k := range order {
			e, _ := v.Get(k)
			kr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: k, et: kt, depth: s.depth + 1}).render(ev, nested(f))
			vr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: vt, depth: s.depth + 1}).render(ev, nested(f))
			parts = append(parts, kr+":"+vr)
		}
		if f.Flag('#') {
			return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
		}
		return "map[" + strings.Join(parts, " ") + "]"
	case *runtime.Tuple:
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, depth: s.depth + 1}).render(verb, nested(f))
		}
		return strings.Join(parts, " ")
	case *runtime.TypedNil:
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		if rt := vm.HostNilType(v.Typ); rt != nil {
			// a nil named host slice (net.IP) renders as the host nil
			// would — its String method on the nil receiver
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case *runtime.IfaceNil:
		if v.Typ != nil && v.Typ.Kind == runtime.KindInterface {
			// a nil interface value prints like a bare nil — <nil> at
			// top level, the slot's static type inside %#v composites.
			if verb == 'v' && f.Flag('#') && s.et != nil {
				return nilGoSyntax(s.et)
			}
			if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
				return "<nil>"
			}
			return badVerb(verb, "", "<nil>")
		}
		if verb == 'T' {
			return typedefSpelling(v.Typ)
		}
		if rt := vm.HostNilType(v.Typ); rt != nil {
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case runtime.Nil:
		// %#v spells a nil sitting in an interface-typed field or
		// element with the slot's static type — error(nil),
		// interface {}(nil) — the way reflect sees the declared type.
		// Every other verb (and an untyped top-level nil) keeps <nil>.
		if verb == 'v' && f.Flag('#') && s.et != nil {
			return nilGoSyntax(s.et)
		}
		// a nil interface element inside a composite renders <nil>
		// under every verb; top-level non-%v verbs get the marker.
		if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
			return "<nil>"
		}
		return badVerb(verb, "", "<nil>")
	case *runtime.Chan:
		switch verb {
		case 'T':
			return typedefSpelling(v.Typ)
		case 'v':
			// %#v spells pointer-shaped values as (T)(0xADDR).
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", chanTypSpelling(v.Typ), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, typedefSpelling(v.Typ), fmt.Sprintf("%p", v))
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch verb {
		case 'T':
			return funcSigSpelling(v)
		case 'v':
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", funcSigSpelling(v), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, funcSigSpelling(v), fmt.Sprintf("%p", v))
	case *runtime.GoValue:
		if verb == 'T' {
			// a proxy box spells its API type, not the box's name.
			if rt := proxyHostTypeOf(v.V); rt != nil {
				return rt.String()
			}
		}
		return fmt.Sprintf(formatOf(f, verb), v.V)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return typedefSpelling(v)
	}
	return s.leaf(x, verb, f)
}

// renderList renders a slice's bracketed element list — every verb
// descends elementwise like Go's fmt ([]int under %q quotes each element
// as a char, under %s each becomes a %!s marker).
func (s *fmtValue) renderList(v *runtime.Slice, verb rune, f fmt.State) string {
	et := sliceElemTyp(v.Typ)
	ev := elemVerb(verb)
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: et, depth: s.depth + 1}).render(ev, nested(f))
	}
	if f.Flag('#') && verb == 'v' {
		return typedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// leaf formats a scalar value: compatible verbs go to host fmt after
// normalizing to the script-canonical Go type (script ints are int64 but
// spell int), incompatible verbs produce Go's %!verb(type=value) marker
// spelled with the declared element type when the leaf has one.
func (s *fmtValue) leaf(x runtime.Value, verb rune, f fmt.State) string {
	if u, ok := x.(*runtime.UConst); ok {
		x = mustNativeConst(u)
	}
	if leafBadVerb(x, verb) {
		name := ""
		// an interface-typed element (incl. any) names the value's
		// dynamic type, not the interface — []any{1} gives %!v(int=1)
		if s.et != nil && s.et.Name != "any" {
			if _, isIface := typedefAst(s.et).(*ast.InterfaceType); !isIface {
				name = typedefSpelling(s.et)
			}
		}
		if name == "" {
			name = scriptScalarName(x)
		}
		return badVerb(verb, name, (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: x, depth: s.depth + 1}).render('v', nested(f)))
	}
	if i, ok := x.(int64); ok {
		// script ints store int64 but spell int — including inside
		// composites, where %#v elements stay decimal (the 0x-hex int
		// spelling is a top-level-only form).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf(formatOfNoHash(f, verb), int(i))
		}
		return fmt.Sprintf(formatOf(f, verb), int(i))
	}
	return fmt.Sprintf(formatOf(f, verb), x)
}

// nilTyp renders a typed nil like Go's fmt: a nil slice or map prints its
// empty composite ([] or map[]) under every verb — a nil []byte renders
// like the empty string — while a nil pointer, chan or func prints
// <nil>/0x0/0 like the nil address it is. %#v uses the Go-syntax
// conversion form T(nil): []string(nil), map[string]int(nil), (*int)(nil).
func (s *fmtValue) nilTyp(verb rune, f fmt.State, td *runtime.TypeDef) string {
	if verb == 'v' && f.Flag('#') {
		return nilGoSyntax(td)
	}
	if verb == 'v' && s.nilSyntax && td != nil &&
		(td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) {
		return nilGoSyntax(td)
	}
	if td == nil {
		return "<nil>"
	}
	switch td.Kind {
	case runtime.KindSlice:
		if byteSliceTyp(td) {
			switch verb {
			case 's', 'x', 'X':
				return ""
			case 'q':
				return strconv.Quote("")
			}
		}
		if verb == 'p' {
			return "0x0"
		}
		return "[]"
	case runtime.KindMap:
		if verb == 'p' {
			return "0x0"
		}
		return "map[]"
	case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
		switch verb {
		case 'v':
			return "<nil>"
		case 'p':
			return "0x0"
		case 'd', 'o', 'b', 'x', 'X', 'U', 'c':
			// a nil address formats as 0 under the numeric verbs
			return fmt.Sprintf(formatOf(f, verb), 0)
		}
		return badVerb(verb, typedefSpelling(td), "<nil>")
	}
	return "<nil>"
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

// Next implements (*runtime.Frames).Next — the bool reports whether a
// further call would yield another frame, so the last real frame
// returns false and `for { f, more := frames.Next(); ...; if !more {
// break } }` does not process an extra empty frame.
func (cf *callerFrames) Next() (callerFrame, bool) {
	if cf.i >= len(cf.sites) {
		return callerFrame{}, false
	}
	s := cf.sites[cf.i]
	cf.i++
	return callerFrame{
		PC:       uintptr(cf.i),
		Func:     &callerFunc{name: s.Name},
		Function: s.Name,
		File:     s.File,
		Line:     s.Line,
	}, cf.i < len(cf.sites)
}

// Name implements (*runtime.Func).Name.
func (f *callerFunc) Name() string { return f.name }

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
		a, b = deepTwinOperands(a, b)
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
	return reflect.DeepEqual(goNative(a), goNative(b))
}

// deepNilTyp is the dynamic type of a nil v: a typed nil's own type or
// the type boxed in an interface nil; nil for the nil interface.
func deepNilTyp(v runtime.Value) *runtime.TypeDef {
	if tn, ok := v.(*runtime.TypedNil); ok {
		return tn.Typ
	}
	return runtime.BoxedNilTyp(v)
}

// deepTwinOperands lets a boxed host named basic meet a Named of its
// source twin (a host FileMode against io/fs's ModeDir): the host one
// reads as that Named. Like the VM's twinOperands, it folds only when
// the Named is the host type's source twin or bound typedef
// ([runtime.HostTwinOf]) — never a look-alike with the same payload.
func deepTwinOperands(a, b runtime.Value) (runtime.Value, runtime.Value) {
	if n, ok := b.(*runtime.Named); ok {
		if p, rt, ok := runtime.HostNamedScalar(a); ok && runtime.HostTwinOf(rt, n.Typ) {
			return runtime.Tag(n.Typ, p), b
		}
	}
	if n, ok := a.(*runtime.Named); ok {
		if p, rt, ok := runtime.HostNamedScalar(b); ok && runtime.HostTwinOf(rt, n.Typ) {
			return a, runtime.Tag(n.Typ, p)
		}
	}
	return a, b
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

func (s *godebugSetting) Value() string {
	// a name may repeat in GODEBUG — gc's runtime parse keeps the LAST
	// entry, which is also what makes testenv.SetGODEBUG's appended
	// override win over an earlier setting.
	var val string
	for _, kv := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if n, v, ok := strings.Cut(kv, "="); ok && n == s.Name() {
			val = v
		}
	}
	return val
}

func (s *godebugSetting) Name() string {
	if s.name != "" && s.name[0] == '#' {
		return s.name[1:]
	}
	return s.name
}

func (s *godebugSetting) Undocumented() bool { return s.name != "" && s.name[0] == '#' }

func (s *godebugSetting) String() string { return s.Name() + "=" + s.Value() }

func (s *godebugSetting) IncNonDefault() {}

// installStdlib binds the intrinsic packages onto the engine's import-path
// table; Bound packages win over source resolution (loadPath checks pkgs).
func (e *Engine) installStdlib() {
	h := &hostHelpers{e: e}
	e.installFmt(h)
	e.installErrors(h)
	e.installStrings(h)
	e.installStrconv(h)
	e.installBytes(h)
	e.installUnicode(h)
	e.installUnicodeUtf8(h)
	e.installMath(h)
	e.installRegexp(h)
	e.installEncodingBase64(h)
	e.installEncodingHex(h)
	e.installEncodingJson(h)
	e.installInternalGodebug(h)
	e.installInternalTestenv(h)
	e.installSyscall(h)
	e.installInternalBytealg(h)
	e.installInternalRace(h)
	e.installInternalStringslite(h)
	e.installHtml(h)
	e.installPath(h)
	e.installReflect()
	e.installSort(h)
	e.installSlices(h)
	e.installMaps(h)
	e.installOs(h)
	e.installPathFilepath(h)
	e.installOsExec(h)
	e.installHost(h)
	e.installInspect()
	e.installUnsafe(h)
	e.installRuntime(h)
	e.installTime(h)
	e.installSync(h)
	e.installSyncAtomic(h)
	e.installContext(h)
	e.installNet(h)
	e.installNetNetip(h)
	e.installIo(h)
	e.installCryptoRand(h)
	e.installCryptoSha256(h)
	e.installEncodingCsv(h)
	e.installBufio(h)
	e.installTextTemplate(h)
	e.installHtmlTemplate(h)
}
