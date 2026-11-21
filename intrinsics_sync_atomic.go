package minigo

import (
	"fmt"
	"go/constant"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installSyncAtomic(h *hostHelpers) {
	// sync/atomic: the source implementation reinterprets values through
	// unsafe.Pointer, so the package is bound instead. The scalar types
	// are real host values; Value and Pointer[T] share a mutex-guarded
	// box holding script values verbatim (deepHost already flattens
	// containers at the call boundary, everything else stays raw), and
	// the pointer-taking functions operate on script cells through
	// Deref/SetRef — atomic in intent, since a script &x has no host
	// address to hand the real runtime.
	e.Bind("sync/atomic", map[string]runtime.Value{
		"Bool":    hostType("sync/atomic.Bool", func() any { return &atomic.Bool{} }),
		"Int32":   hostType("sync/atomic.Int32", func() any { return &atomic.Int32{} }),
		"Int64":   hostType("sync/atomic.Int64", func() any { return &atomic.Int64{} }),
		"Uint32":  hostType("sync/atomic.Uint32", func() any { return &atomic.Uint32{} }),
		"Uint64":  hostType("sync/atomic.Uint64", func() any { return &atomic.Uint64{} }),
		"Uintptr": hostType("sync/atomic.Uintptr", func() any { return &atomic.Uintptr{} }),
		"Value":   hostType("sync/atomic.Value", func() any { return &atomicBox{} }),
		// generic: a host reflect instantiation is impossible, so the
		// typedef declares T and HostNew boxes the same cell for every
		// instantiation — the stored script values carry the typing.
		"Pointer": &runtime.TypeDef{
			Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
			TParams: []string{"T"},
			HostNew: func() any { return &atomicBox{} },
		},
		"AddInt32":              atomicOp("atomic.AddInt32", atomicOpAdd),
		"AddInt64":              atomicOp("atomic.AddInt64", atomicOpAdd),
		"AddUint32":             atomicOp("atomic.AddUint32", atomicOpAdd),
		"AddUint64":             atomicOp("atomic.AddUint64", atomicOpAdd),
		"AddUintptr":            atomicOp("atomic.AddUintptr", atomicOpAdd),
		"AndInt32":              atomicOp("atomic.AndInt32", atomicOpAnd),
		"AndInt64":              atomicOp("atomic.AndInt64", atomicOpAnd),
		"AndUint32":             atomicOp("atomic.AndUint32", atomicOpAnd),
		"AndUint64":             atomicOp("atomic.AndUint64", atomicOpAnd),
		"AndUintptr":            atomicOp("atomic.AndUintptr", atomicOpAnd),
		"OrInt32":               atomicOp("atomic.OrInt32", atomicOpOr),
		"OrInt64":               atomicOp("atomic.OrInt64", atomicOpOr),
		"OrUint32":              atomicOp("atomic.OrUint32", atomicOpOr),
		"OrUint64":              atomicOp("atomic.OrUint64", atomicOpOr),
		"OrUintptr":             atomicOp("atomic.OrUintptr", atomicOpOr),
		"CompareAndSwapInt32":   atomicOp("atomic.CompareAndSwapInt32", atomicOpCAS),
		"CompareAndSwapInt64":   atomicOp("atomic.CompareAndSwapInt64", atomicOpCAS),
		"CompareAndSwapUint32":  atomicOp("atomic.CompareAndSwapUint32", atomicOpCAS),
		"CompareAndSwapUint64":  atomicOp("atomic.CompareAndSwapUint64", atomicOpCAS),
		"CompareAndSwapUintptr": atomicOp("atomic.CompareAndSwapUintptr", atomicOpCAS),
		"CompareAndSwapPointer": atomicOp("atomic.CompareAndSwapPointer", atomicOpCAS),
		"LoadInt32":             atomicOp("atomic.LoadInt32", atomicOpLoad),
		"LoadInt64":             atomicOp("atomic.LoadInt64", atomicOpLoad),
		"LoadUint32":            atomicOp("atomic.LoadUint32", atomicOpLoad),
		"LoadUint64":            atomicOp("atomic.LoadUint64", atomicOpLoad),
		"LoadUintptr":           atomicOp("atomic.LoadUintptr", atomicOpLoad),
		"LoadPointer":           atomicOp("atomic.LoadPointer", atomicOpLoad),
		"StoreInt32":            atomicOp("atomic.StoreInt32", atomicOpStore),
		"StoreInt64":            atomicOp("atomic.StoreInt64", atomicOpStore),
		"StoreUint32":           atomicOp("atomic.StoreUint32", atomicOpStore),
		"StoreUint64":           atomicOp("atomic.StoreUint64", atomicOpStore),
		"StoreUintptr":          atomicOp("atomic.StoreUintptr", atomicOpStore),
		"StorePointer":          atomicOp("atomic.StorePointer", atomicOpStore),
		"SwapInt32":             atomicOp("atomic.SwapInt32", atomicOpSwap),
		"SwapInt64":             atomicOp("atomic.SwapInt64", atomicOpSwap),
		"SwapUint32":            atomicOp("atomic.SwapUint32", atomicOpSwap),
		"SwapUint64":            atomicOp("atomic.SwapUint64", atomicOpSwap),
		"SwapUintptr":           atomicOp("atomic.SwapUintptr", atomicOpSwap),
		"SwapPointer":           atomicOp("atomic.SwapPointer", atomicOpSwap),
	})
}

// atomicBox backs the bound sync/atomic Value and Pointer[T]: the cell
// holds script values verbatim under a mutex so Load hands back the same
// objects Store received (a host atomic.Value would serve identically —
// the boundary already decides what crosses — but the box keeps the
// semantics obvious and needs no per-instantiation host type).
type atomicBox struct {
	mu sync.Mutex
	v  any
}

// atomicCellEq compares a stored value against a CAS old operand: nils
// match nils, non-comparable payloads never match (real atomic.Value
// panics on them), identical references or scalars match.
func atomicCellEq(a, b any) bool {
	ua, ub := runtime.Unwrap(a), runtime.Unwrap(b)
	if ua == nil || ub == nil {
		return ua == nil && ub == nil
	}
	if ua == runtime.NIL || ub == runtime.NIL {
		return ua == ub
	}
	ta, tb := reflect.TypeOf(ua), reflect.TypeOf(ub)
	if ta != tb {
		// int-family payloads across a named re-wrap (CAS(0, 1) on a
		// uint32-tagged cell) compare in the int64 domain.
		ai, aok := ua.(int64)
		bi, bok := ub.(int64)
		return aok && bok && ai == bi
	}
	if !ta.Comparable() {
		return false
	}
	return ua == ub
}

// atomicOpKind selects which of the pointer-taking sync/atomic function
// families an atomicOp builds.
type atomicOpKind int

const (
	atomicOpLoad atomicOpKind = iota
	atomicOpStore
	atomicOpAdd
	atomicOpAnd
	atomicOpOr
	atomicOpSwap
	atomicOpCAS
)

// atomicMu serializes the read-modify-write of every atomicOp: a script
// pointer has no host address to run a hardware atomic on, so one lock
// makes each Load/Store/Add/And/Or/Swap/CompareAndSwap indivisible
// against all the others.
var atomicMu sync.Mutex

// atomicOp builds one of the bound sync/atomic *T-pointer functions.
// A script &x has no host address, so the builtins load and store the
// pointed-to cell through Deref/SetRef; arithmetic happens in the int64
// domain. Load* returns the cell content verbatim — a Named uint32
// keeps its tag, LoadPointer hands the stored pointer back untyped.
func atomicOp(name string, op atomicOpKind) *runtime.BuiltinFunc {
	// the operand type the function's name spells (AddInt32 works on
	// int32): untyped constant and non-constant-shift operands convert
	// to it, and results wrap at its width. Pointer ops keep values as is.
	var wordTd *runtime.TypeDef
	for _, w := range []string{"Uintptr", "Uint32", "Uint64", "Int32", "Int64"} {
		if strings.HasSuffix(name, w) {
			wordTd = runtime.BasicTypedef(strings.ToLower(w))
			break
		}
	}
	want := 1
	switch op {
	case atomicOpStore, atomicOpAdd, atomicOpAnd, atomicOpOr, atomicOpSwap:
		want = 2
	case atomicOpCAS:
		want = 3
	}
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != want {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, want, len(args))
		}
		if wordTd != nil {
			// every operand converts to the word type first: Go
			// assigns it to the int32/uint32/... parameter.
			args = append([]runtime.Value(nil), args...)
			for i := 1; i < len(args); i++ {
				w, err := atomicWord(vc, wordTd, args[i])
				if err != nil {
					return nil, err
				}
				args[i] = w
			}
		}
		// the load, the computation and the store are one atomic step:
		// script goroutines run on host goroutines, so a separate
		// Deref/SetRef pair would lose concurrent updates.
		atomicMu.Lock()
		defer atomicMu.Unlock()
		cur, ok := runtime.Deref(args[0])
		if !ok {
			return nil, fmt.Errorf("%s needs a pointer operand, got %T", name, args[0])
		}
		set := func(v runtime.Value) error {
			if !runtime.SetRef(args[0], v) {
				return fmt.Errorf("%s needs a pointer operand, got %T", name, args[0])
			}
			return nil
		}
		switch op {
		case atomicOpLoad:
			return cur, nil
		case atomicOpStore:
			return runtime.NIL, set(args[1])
		case atomicOpSwap:
			if err := set(args[1]); err != nil {
				return nil, err
			}
			return cur, nil
		case atomicOpCAS:
			if !atomicCellEq(cur, args[1]) {
				return false, nil
			}
			if err := set(args[2]); err != nil {
				return nil, err
			}
			return true, nil
		}
		n := int64Of(cur)
		var nv runtime.Value = n
		if op == atomicOpAdd {
			n += int64Of(args[1])
			nv = n
			if wordTd != nil {
				w, err := atomicWord(vc, wordTd, n)
				if err != nil {
					return nil, err
				}
				nv = w
			}
			if err := set(nv); err != nil {
				return nil, err
			}
			// Add returns the NEW value.
			return nv, nil
		}
		switch op {
		case atomicOpAnd:
			n &= int64Of(args[1])
		case atomicOpOr:
			n |= int64Of(args[1])
		}
		nv = n
		if wordTd != nil {
			w, err := atomicWord(vc, wordTd, n)
			if err != nil {
				return nil, err
			}
			nv = w
		}
		if err := set(nv); err != nil {
			return nil, err
		}
		// And/Or return the OLD value.
		return cur, nil
	}}
}

// atomicWord converts an atomic operand to the word type td (int32,
// uint32, ...) the way the call's parameter assignment does, and
// returns it as a concrete td value: a constant materializes at td
// (`AddInt32(&x, 1)` stores an int32, not an int), and an int64
// payload wraps at td's width (`1<<(n+24)+3` adds 3 to a uint32).
func atomicWord(vc runtime.VMCaller, td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	cv, err := vc.Convert(td, x)
	if err != nil {
		return nil, err
	}
	if n, ok := cv.(*runtime.Named); ok {
		if u, ok := n.V.(*runtime.UConst); ok {
			// a typed constant: read its bits and convert the value.
			if u.V.Kind() != constant.Int {
				return nil, fmt.Errorf("cannot use constant %s as %s", u.V, td.Name)
			}
			return vc.Convert(td, runtime.WrapInt64(u.V))
		}
	}
	return cv, nil
}
